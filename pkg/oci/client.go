package oci

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/containerd/containerd/v2/core/images"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/spegel-org/spegel/pkg/httpx"
)

const (
	HeaderDockerDigest = "Docker-Content-Digest"
)

type FetchConfig struct {
	Mirror *url.URL
	Header http.Header
}

func (cfg *FetchConfig) Apply(opts ...FetchOption) error {
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(cfg); err != nil {
			return err
		}
	}
	return nil
}

type FetchOption func(cfg *FetchConfig) error

func WithFetchMirror(mirror *url.URL) FetchOption {
	return func(cfg *FetchConfig) error {
		cfg.Mirror = mirror
		return nil
	}
}

func WithFetchHeader(header http.Header) FetchOption {
	return func(cfg *FetchConfig) error {
		cfg.Header = header
		return nil
	}
}

type Client struct {
	hc *http.Client
	tc sync.Map
}

func NewClient() *Client {
	transport := httpx.BaseTransport()
	transport.MaxIdleConnsPerHost = transport.MaxIdleConns
	hc := &http.Client{
		Transport: transport,
		Timeout:   0,
	}
	return &Client{
		hc: hc,
		tc: sync.Map{},
	}
}

type PullMetric struct {
	Digest        digest.Digest
	ContentType   string
	ContentLength int64
	Duration      time.Duration
}

func (c *Client) Pull(ctx context.Context, img Image, opts ...FetchOption) ([]PullMetric, error) {
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
		desc, err := func() (ocispec.Descriptor, error) {
			rc, desc, err := c.Get(ctx, dist, nil, opts...)
			if err != nil {
				return ocispec.Descriptor{}, err
			}
			defer httpx.DrainAndClose(rc)

			switch dist.Kind {
			case DistributionKindBlob:
				// Right now we are just discarding the contents because we do not have a writable store.
				_, copyErr := io.Copy(io.Discard, rc)
				closeErr := rc.Close()
				err := errors.Join(copyErr, closeErr)
				if err != nil {
					return ocispec.Descriptor{}, err
				}
			case DistributionKindManifest:
				b, readErr := io.ReadAll(rc)
				closeErr := rc.Close()
				err = errors.Join(readErr, closeErr)
				if err != nil {
					return ocispec.Descriptor{}, err
				}
				switch desc.MediaType {
				case images.MediaTypeDockerSchema2ManifestList, ocispec.MediaTypeImageIndex:
					var idx ocispec.Index
					if err := json.Unmarshal(b, &idx); err != nil {
						return ocispec.Descriptor{}, err
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
						return ocispec.Descriptor{}, err
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
			return desc, nil
		}()
		if err != nil {
			return nil, err
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

func (c *Client) Head(ctx context.Context, dist DistributionPath, opts ...FetchOption) (ocispec.Descriptor, error) {
	rc, desc, err := c.fetch(ctx, http.MethodHead, dist, nil, opts...)
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	defer httpx.DrainAndClose(rc)
	return desc, nil
}

func (c *Client) Get(ctx context.Context, dist DistributionPath, brr []httpx.ByteRange, opts ...FetchOption) (io.ReadCloser, ocispec.Descriptor, error) {
	rc, desc, err := c.fetch(ctx, http.MethodGet, dist, brr, opts...)
	if err != nil {
		return nil, ocispec.Descriptor{}, err
	}
	return rc, desc, nil
}

func (c *Client) fetch(ctx context.Context, method string, dist DistributionPath, brr []httpx.ByteRange, opts ...FetchOption) (io.ReadCloser, ocispec.Descriptor, error) {
	cfg := FetchConfig{}
	err := cfg.Apply(opts...)
	if err != nil {
		return nil, ocispec.Descriptor{}, err
	}

	tcKey := dist.Registry + dist.Name

	u := dist.URL()
	if cfg.Mirror != nil {
		u.Scheme = cfg.Mirror.Scheme
		u.Host = cfg.Mirror.Host
		u.Path = path.Join(cfg.Mirror.Path, u.Path)
	}
	if u.Host == "docker.io" {
		u.Host = "registry-1.docker.io"
	}

	for range 2 {
		req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
		if err != nil {
			return nil, ocispec.Descriptor{}, err
		}
		httpx.CopyHeader(req.Header, cfg.Header)
		req.Header.Set(httpx.HeaderUserAgent, "spegel")
		req.Header.Add(httpx.HeaderAccept, "application/vnd.oci.image.manifest.v1+json")
		req.Header.Add(httpx.HeaderAccept, "application/vnd.docker.distribution.manifest.v2+json")
		req.Header.Add(httpx.HeaderAccept, "application/vnd.oci.image.index.v1+json")
		req.Header.Add(httpx.HeaderAccept, "application/vnd.docker.distribution.manifest.list.v2+json")
		if len(brr) > 0 {
			req.Header.Add(httpx.HeaderRange, httpx.FormatMultipartRangeHeader(brr))
		}
		token, ok := c.tc.Load(tcKey)
		if ok {
			//nolint: errcheck // We know it will be a string.
			req.Header.Set(httpx.HeaderAuthorization, "Bearer "+token.(string))
		}
		resp, err := c.hc.Do(req)
		if err != nil {
			return nil, ocispec.Descriptor{}, err
		}
		if resp.StatusCode == http.StatusUnauthorized {
			c.tc.Delete(tcKey)
			wwwAuth := resp.Header.Get(httpx.HeaderWWWAuthenticate)
			token, err = getBearerToken(ctx, wwwAuth, c.hc)
			if err != nil {
				return nil, ocispec.Descriptor{}, err
			}
			c.tc.Store(tcKey, token)
			continue
		}
		err = httpx.CheckResponseStatus(resp, http.StatusOK, http.StatusPartialContent)
		if err != nil {
			httpx.DrainAndClose(resp.Body)
			return nil, ocispec.Descriptor{}, err
		}

		// Handle optional headers for blobs.
		header := resp.Header.Clone()
		if dist.Kind == DistributionKindBlob {
			if header.Get(httpx.HeaderContentType) == "" {
				header.Set(httpx.HeaderContentType, httpx.ContentTypeBinary)
			}
			if header.Get(HeaderDockerDigest) == "" {
				header.Set(HeaderDockerDigest, dist.Digest.String())
			}
		}

		desc, err := DescriptorFromHeader(header)
		if err != nil {
			httpx.DrainAndClose(resp.Body)
			return nil, ocispec.Descriptor{}, err
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
	defer httpx.DrainAndClose(resp.Body)
	err = httpx.CheckResponseStatus(resp, http.StatusOK)
	if err != nil {
		return "", err
	}
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

func DescriptorFromHeader(header http.Header) (ocispec.Descriptor, error) {
	mediaType := header.Get(httpx.HeaderContentType)
	if mediaType == "" {
		return ocispec.Descriptor{}, errors.New("content type cannot be empty")
	}
	contentLength := header.Get(httpx.HeaderContentLength)
	if contentLength == "" {
		return ocispec.Descriptor{}, errors.New("content length cannot be empty")
	}
	size, err := strconv.ParseInt(contentLength, 10, 64)
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	dgst, err := digest.Parse(header.Get(HeaderDockerDigest))
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	desc := ocispec.Descriptor{
		MediaType: mediaType,
		Size:      size,
		Digest:    dgst,
	}
	return desc, nil
}

func WriteDescriptorToHeader(desc ocispec.Descriptor, header http.Header) {
	header.Set(httpx.HeaderContentType, desc.MediaType)
	header.Set(httpx.HeaderContentLength, strconv.FormatInt(desc.Size, 10))
	header.Set(HeaderDockerDigest, desc.Digest.String())
}
