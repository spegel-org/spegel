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
	"strconv"
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

type Client struct {
	httpClient *http.Client
}

func NewClient() *Client {
	return &Client{
		httpClient: &http.Client{},
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
					Kind:     DistributionKindManifest,
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
	u := dist.URL()
	if mirror != "" {
		mirrorUrl, err := url.Parse(mirror)
		if err != nil {
			return nil, ocispec.Descriptor{}, err
		}
		u.Scheme = mirrorUrl.Scheme
		u.Host = mirrorUrl.Host
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
	if err != nil {
		return nil, ocispec.Descriptor{}, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, ocispec.Descriptor{}, err
	}
	if resp.StatusCode != http.StatusOK {
		statusErr := fmt.Errorf("unexpected status code  %s", resp.Status)
		_, err := io.Copy(io.Discard, resp.Body)
		if err != nil {
			return nil, ocispec.Descriptor{}, errors.Join(statusErr, err)
		}
		err = resp.Body.Close()
		if err != nil {
			return nil, ocispec.Descriptor{}, errors.Join(statusErr, err)
		}
		return nil, ocispec.Descriptor{}, statusErr
	}
	// TODO: Defer empty response body and close it on error.
	dgstStr := resp.Header.Get(DigestHeader)
	if dgstStr == "" {
		return nil, ocispec.Descriptor{}, errors.New("digest header cannot be empty")
	}
	dgst, err := digest.Parse(dgstStr)
	if err != nil {
		return nil, ocispec.Descriptor{}, err
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
