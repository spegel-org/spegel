package oci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/containerd/containerd/v2/core/images"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	DigestHeader        = "Docker-Content-Digest"
	ContentTypeHeader   = "Content-Type"
	ContentLengthHeader = "Content-Length"
)

type StatusError struct {
	Content    string
	StatusCode int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("unexpected status code %d with body %s", e.StatusCode, e.Content)
}

func CheckResponseStatus(resp *http.Response, expected ...int) error {
	if slices.Contains(expected, resp.StatusCode) {
		return nil
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return &StatusError{
		StatusCode: resp.StatusCode,
		Content:    string(b),
	}
}

type Client struct {
	hc *http.Client
	tc sync.Map
}

func NewClient() *Client {
	return &Client{
		hc: &http.Client{},
		tc: sync.Map{},
	}
}

type PullMetric struct {
	Digest        digest.Digest
	ContentType   string
	ContentLength int64
	Duration      time.Duration
}

func (c *Client) Pull(ctx context.Context, img Image, mirror string) ([]PullMetric, error) {
	pullMetrics := []PullMetric{}

	queue := []DistributionPath{
		{
			Kind:     DistributionKindManifest,
			Name:     img.Repository,
			Digest:   img.Digest,
			Tag:      img.Tag,
			Registry: img.Registry,
		},
	}
	for len(queue) > 0 {
		dist := queue[0]
		queue = queue[1:]

		start := time.Now()
		rc, desc, err := c.Get(ctx, dist, mirror)
		if err != nil {
			return nil, err
		}

		switch dist.Kind {
		case DistributionKindBlob:
			_, copyErr := io.Copy(io.Discard, rc)
			closeErr := rc.Close()
			err := errors.Join(copyErr, closeErr)
			if err != nil {
				return nil, err
			}
		case DistributionKindManifest:
			b, readErr := io.ReadAll(rc)
			closeErr := rc.Close()
			err = errors.Join(readErr, closeErr)
			if err != nil {
				return nil, err
			}
			switch desc.MediaType {
			case images.MediaTypeDockerSchema2ManifestList, ocispec.MediaTypeImageIndex:
				var idx ocispec.Index
				if err := json.Unmarshal(b, &idx); err != nil {
					return nil, err
				}
				for _, m := range idx.Manifests {
					// TODO: Add platform option.
					//nolint: staticcheck // Simplify in the future.
					if !(m.Platform.OS == runtime.GOOS && m.Platform.Architecture == runtime.GOARCH) {
						continue
					}
					queue = append(queue, DistributionPath{
						Kind:     DistributionKindManifest,
						Name:     dist.Name,
						Digest:   m.Digest,
						Registry: dist.Registry,
					})
				}
			case images.MediaTypeDockerSchema2Manifest, ocispec.MediaTypeImageManifest:
				var manifest ocispec.Manifest
				err := json.Unmarshal(b, &manifest)
				if err != nil {
					return nil, err
				}
				queue = append(queue, DistributionPath{
					Kind:     DistributionKindBlob,
					Name:     dist.Name,
					Digest:   manifest.Config.Digest,
					Registry: dist.Registry,
				})
				for _, layer := range manifest.Layers {
					queue = append(queue, DistributionPath{
						Kind:     DistributionKindBlob,
						Name:     dist.Name,
						Digest:   layer.Digest,
						Registry: dist.Registry,
					})
				}
			}
		}

		metric := PullMetric{
			Digest:        desc.Digest,
			Duration:      time.Since(start),
			ContentType:   desc.MediaType,
			ContentLength: desc.Size,
		}
		pullMetrics = append(pullMetrics, metric)
	}

	return pullMetrics, nil
}

func (c *Client) Head(ctx context.Context, dist DistributionPath, mirror string) (ocispec.Descriptor, error) {
	rc, desc, err := c.fetch(ctx, http.MethodHead, dist, mirror)
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	defer rc.Close()
	_, err = io.Copy(io.Discard, rc)
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	return desc, nil
}

func (c *Client) Get(ctx context.Context, dist DistributionPath, mirror string) (io.ReadCloser, ocispec.Descriptor, error) {
	rc, desc, err := c.fetch(ctx, http.MethodGet, dist, mirror)
	if err != nil {
		return nil, ocispec.Descriptor{}, err
	}
	return rc, desc, nil
}

func (c *Client) fetch(ctx context.Context, method string, dist DistributionPath, mirror string) (io.ReadCloser, ocispec.Descriptor, error) {
	tcKey := dist.Registry + dist.Name

	u := dist.URL()
	if mirror != "" {
		mirrorUrl, err := url.Parse(mirror)
		if err != nil {
			return nil, ocispec.Descriptor{}, err
		}
		u.Scheme = mirrorUrl.Scheme
		u.Host = mirrorUrl.Host
	}
	if u.Host == "docker.io" {
		u.Host = "registry-1.docker.io"
	}

	for range 2 {
		req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
		if err != nil {
			return nil, ocispec.Descriptor{}, err
		}
		req.Header.Set("User-Agent", "spegel")
		req.Header.Add("Accept", "application/vnd.oci.image.manifest.v1+json")
		req.Header.Add("Accept", "application/vnd.docker.distribution.manifest.v2+json")
		req.Header.Add("Accept", "application/vnd.oci.image.index.v1+json")
		req.Header.Add("Accept", "application/vnd.docker.distribution.manifest.list.v2+json")
		token, ok := c.tc.Load(tcKey)
		if ok {
			//nolint: errcheck // We know it will be a string.
			req.Header.Set("Authorization", "Bearer "+token.(string))
		}
		resp, err := c.hc.Do(req)
		if err != nil {
			return nil, ocispec.Descriptor{}, err
		}
		if resp.StatusCode == http.StatusUnauthorized {
			c.tc.Delete(tcKey)
			wwwAuth := resp.Header.Get("WWW-Authenticate")
			token, err = getBearerToken(ctx, wwwAuth, c.hc)
			if err != nil {
				return nil, ocispec.Descriptor{}, err
			}
			c.tc.Store(tcKey, token)
			continue
		}
		err = CheckResponseStatus(resp, http.StatusOK)
		if err != nil {
			return nil, ocispec.Descriptor{}, err
		}

		dgst := dist.Digest
		dgstStr := resp.Header.Get(DigestHeader)
		if dgstStr != "" {
			dgst, err = digest.Parse(dgstStr)
			if err != nil {
				return nil, ocispec.Descriptor{}, err
			}
		}
		if dgst == "" {
			return nil, ocispec.Descriptor{}, errors.New("digest cannot be empty")
		}
		mt := resp.Header.Get(ContentTypeHeader)
		if mt == "" {
			return nil, ocispec.Descriptor{}, errors.New("content type header cannot be empty")
		}
		cl := resp.Header.Get(ContentLengthHeader)
		if cl == "" {
			return nil, ocispec.Descriptor{}, errors.New("content length header cannot be empty")
		}
		size, err := strconv.ParseInt(cl, 10, 64)
		if err != nil {
			return nil, ocispec.Descriptor{}, err
		}
		desc := ocispec.Descriptor{
			Digest:    dgst,
			MediaType: mt,
			Size:      size,
		}
		return resp.Body, desc, nil
	}
	return nil, ocispec.Descriptor{}, errors.New("could not perform request")
}

func getBearerToken(ctx context.Context, wwwAuth string, client *http.Client) (string, error) {
	if !strings.HasPrefix(wwwAuth, "Bearer ") {
		return "", errors.New("unsupported auth scheme")
	}

	params := map[string]string{}
	for _, part := range strings.Split(wwwAuth[len("Bearer "):], ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) == 2 {
			params[kv[0]] = strings.Trim(kv[1], `"`)
		}
	}
	authURL, err := url.Parse(params["realm"])
	if err != nil {
		return "", err
	}
	q := authURL.Query()
	if service, ok := params["service"]; ok {
		q.Set("service", service)
	}
	if scope, ok := params["scope"]; ok {
		q.Set("scope", scope)
	}
	authURL.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, authURL.String(), nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	err = CheckResponseStatus(resp, http.StatusOK)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	tokenResp := struct {
		Token string `json:"token"`
	}{}
	err = json.Unmarshal(b, &tokenResp)
	if err != nil {
		return "", err
	}
	return tokenResp.Token, nil
}
