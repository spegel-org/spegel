package oci

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/platforms"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/kvick-org/pkg/option"

	"github.com/spegel-org/spegel/internal/resilient"
	"github.com/spegel-org/spegel/pkg/httpx"
)

const (
	HeaderDockerDigest = "Docker-Content-Digest"
	HeaderNamespace    = "OCI-Namespace"
)

type ClientConfig struct {
	TLSClientConfig     *tls.Config
	CredentialProviders []CredentialProvider
	DisableKeepAlives   bool
}

type ClientOption = option.Option[ClientConfig]

func WithTLS(rootCAs *x509.CertPool, certificates []tls.Certificate) ClientOption {
	return func(cfg *ClientConfig) error {
		cfg.TLSClientConfig = &tls.Config{
			RootCAs:      rootCAs,
			Certificates: certificates,
		}
		return nil
	}
}

func WithDisableKeepAlives(v bool) ClientOption {
	return func(cfg *ClientConfig) error {
		cfg.DisableKeepAlives = v
		return nil
	}
}

func WithCredentialProviders(cps ...CredentialProvider) ClientOption {
	return func(cfg *ClientConfig) error {
		cfg.CredentialProviders = cps
		return nil
	}
}

type Client struct {
	httpClient *http.Client
	auth       *Authenticator
}

func NewClient(opts ...ClientOption) (*Client, error) {
	cfg := ClientConfig{}
	err := option.Apply(&cfg, opts...)
	if err != nil {
		return nil, err
	}

	httpClient := &http.Client{
		Timeout: 0,
	}
	transport := httpx.BaseTransport()
	transport.TLSClientConfig = cfg.TLSClientConfig
	transport.MaxIdleConns = 100
	transport.MaxConnsPerHost = 100
	transport.MaxIdleConnsPerHost = 100
	transport.DisableKeepAlives = cfg.DisableKeepAlives
	httpClient.Transport = transport

	ociClient := &Client{
		httpClient: httpClient,
		auth:       NewAuthenticator(httpClient, cfg.CredentialProviders),
	}
	return ociClient, nil
}

type CommonConfig struct {
	Mirror *url.URL
	Header http.Header
}

type PullConfig struct {
	CommonConfig
	Platform ocispec.Platform
}

type PullOption = option.Option[PullConfig]

func WithPullMirror(mirror *url.URL) PullOption {
	return func(cfg *PullConfig) error {
		cfg.Mirror = mirror
		return nil
	}
}

func WithPullHeader(header http.Header) PullOption {
	return func(cfg *PullConfig) error {
		cfg.Header = header
		return nil
	}
}

func WithPullPlatform(platform ocispec.Platform) PullOption {
	return func(cfg *PullConfig) error {
		cfg.Platform = platform
		return nil
	}
}

type FetchOption = option.Option[CommonConfig]

func WithFetchMirror(mirror *url.URL) FetchOption {
	return func(cfg *CommonConfig) error {
		cfg.Mirror = mirror
		return nil
	}
}

func WithFetchHeader(k, v string) FetchOption {
	return func(cfg *CommonConfig) error {
		if cfg.Header == nil {
			cfg.Header = http.Header{}
		}
		cfg.Header.Set(k, v)
		return nil
	}
}

type ImageWriter interface {
	Root(ctx context.Context, img Image, desc ocispec.Descriptor) error
	Write(ctx context.Context, desc ocispec.Descriptor, r io.Reader) error
}

type DiscardWriter struct{}

func (DiscardWriter) Root(ctx context.Context, img Image, desc ocispec.Descriptor) error {
	return nil
}

func (DiscardWriter) Write(ctx context.Context, desc ocispec.Descriptor, r io.Reader) error {
	_, err := io.Copy(io.Discard, r)
	if err != nil {
		return err
	}
	return nil
}

func (c *Client) Pull(ctx context.Context, img Image, w ImageWriter, opts ...PullOption) error {
	cfg := PullConfig{
		Platform: platforms.DefaultSpec(),
	}
	err := option.Apply(&cfg, opts...)
	if err != nil {
		return err
	}
	fetchOpt := func(commonCfg *CommonConfig) error {
		commonCfg.Mirror = cfg.Mirror
		commonCfg.Header = cfg.Header
		return nil
	}

	imgDist, err := img.DistributionPath("http", http.MethodGet)
	if err != nil {
		return err
	}
	queue := []DistributionPath{
		imgDist,
	}
	for len(queue) > 0 {
		dist := queue[0]
		queue = queue[1:]

		err := func() error {
			rc, desc, err := c.Fetch(ctx, dist, fetchOpt)
			if err != nil {
				return err
			}
			defer httpx.DrainAndClose(rc)

			switch dist.Kind {
			case DistributionKindBlob:
				writErr := w.Write(ctx, desc, rc)
				closeErr := rc.Close()
				err := errors.Join(writErr, closeErr)
				if err != nil {
					return err
				}
			case DistributionKindManifest:
				b, readErr := io.ReadAll(rc)
				closeErr := rc.Close()
				err = errors.Join(readErr, closeErr)
				if err != nil {
					return err
				}
				err := w.Write(ctx, desc, bytes.NewBuffer(b))
				if err != nil {
					return err
				}
				switch desc.MediaType {
				case images.MediaTypeDockerSchema2ManifestList, ocispec.MediaTypeImageIndex:
					var idx ocispec.Index
					if err := json.Unmarshal(b, &idx); err != nil {
						return err
					}
					for _, m := range idx.Manifests {
						if !platforms.Only(cfg.Platform).Match(*m.Platform) {
							continue
						}
						nextRef := Reference{
							Registry:   dist.Registry,
							Repository: dist.Repository,
							Digest:     m.Digest,
						}
						nextDist, err := NewDistributionPath(nextRef, DistributionKindManifest, dist.Scheme, http.MethodGet, nil)
						if err != nil {
							return err
						}
						queue = append(queue, nextDist)
					}
				case images.MediaTypeDockerSchema2Manifest, ocispec.MediaTypeImageManifest:
					var manifest ocispec.Manifest
					err := json.Unmarshal(b, &manifest)
					if err != nil {
						return err
					}
					nextRef := Reference{
						Registry:   dist.Registry,
						Repository: dist.Repository,
						Digest:     manifest.Config.Digest,
					}
					nextDist, err := NewDistributionPath(nextRef, DistributionKindBlob, dist.Scheme, http.MethodGet, nil)
					if err != nil {
						return err
					}
					queue = append(queue, nextDist)
					for _, layer := range manifest.Layers {
						nextRef := Reference{
							Registry:   dist.Registry,
							Repository: dist.Repository,
							Digest:     layer.Digest,
						}
						nextDist, err := NewDistributionPath(nextRef, DistributionKindBlob, dist.Scheme, http.MethodGet, nil)
						if err != nil {
							return err
						}
						queue = append(queue, nextDist)
					}
				}
				if imgDist == dist {
					err = w.Root(ctx, img, desc)
					if err != nil {
						return err
					}
				}
			}
			return nil
		}()
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) Fetch(ctx context.Context, dist DistributionPath, opts ...FetchOption) (io.ReadCloser, ocispec.Descriptor, error) {
	if err := dist.Validate(); err != nil {
		return nil, ocispec.Descriptor{}, err
	}

	cfg := CommonConfig{}
	err := option.Apply(&cfg, opts...)
	if err != nil {
		return nil, ocispec.Descriptor{}, err
	}

	u := dist.URL()
	if cfg.Mirror != nil {
		u.Scheme = cfg.Mirror.Scheme
		u.Host = cfg.Mirror.Host
		u.Path = path.Join(cfg.Mirror.Path, u.Path)
	}
	if u.Host == "docker.io" {
		u.Host = "registry-1.docker.io"
	}

	var body io.ReadCloser
	var desc ocispec.Descriptor
	err = resilient.Retry(ctx, 2, resilient.NoDelay(), func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, dist.Method, u.String(), nil)
		if err != nil {
			return resilient.Unrecoverable(err)
		}
		httpx.CopyHeader(req.Header, cfg.Header)
		req.Header.Set(httpx.HeaderUserAgent, "spegel")
		req.Header.Add(httpx.HeaderAccept, ocispec.MediaTypeImageManifest)
		req.Header.Add(httpx.HeaderAccept, images.MediaTypeDockerSchema2Manifest)
		req.Header.Add(httpx.HeaderAccept, ocispec.MediaTypeImageIndex)
		req.Header.Add(httpx.HeaderAccept, images.MediaTypeDockerSchema2ManifestList)
		if dist.Range != nil {
			req.Header.Add(httpx.HeaderRange, dist.Range.String())
		}
		c.auth.Authorize(req, u.Host, dist.Repository)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return resilient.Unrecoverable(err)
		}
		statusErr := httpx.CheckResponseStatus(resp, http.StatusOK, http.StatusPartialContent)
		if statusErr != nil {
			httpx.DrainAndClose(resp.Body)
			if resp.StatusCode == http.StatusUnauthorized {
				wwwAuth := resp.Header.Values(httpx.HeaderWWWAuthenticate)
				err := c.auth.HandleChallenge(ctx, u.Host, dist.Repository, req.Header.Get(httpx.HeaderAuthorization), wwwAuth)
				if err != nil {
					return resilient.Unrecoverable(err)
				}
				return statusErr
			}
			return resilient.Unrecoverable(statusErr)
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

		desc, err = DescriptorFromHeader(header)
		if err != nil {
			httpx.DrainAndClose(resp.Body)
			return resilient.Unrecoverable(err)
		}
		body = resp.Body
		return nil
	})
	if err != nil {
		return nil, ocispec.Descriptor{}, err
	}
	return body, desc, nil
}

func DescriptorFromHeader(header http.Header) (ocispec.Descriptor, error) {
	desc := ocispec.Descriptor{}

	mediaType := header.Get(httpx.HeaderContentType)
	if mediaType == "" {
		return ocispec.Descriptor{}, errors.New("content type cannot be empty")
	}
	desc.MediaType = mediaType

	if contentRange := header.Get(httpx.HeaderContentRange); contentRange != "" {
		if !strings.HasPrefix(contentRange, httpx.RangeUnit) {
			return ocispec.Descriptor{}, fmt.Errorf("unsupported content range unit %s", contentRange)
		}
		_, after, ok := strings.Cut(contentRange, "/")
		if !ok {
			return ocispec.Descriptor{}, fmt.Errorf("unexpected content range format %s", contentRange)
		}
		if after == "*" {
			return ocispec.Descriptor{}, fmt.Errorf("content range expected to specify size %s", contentRange)
		}
		size, err := strconv.ParseInt(after, 10, 64)
		if err != nil {
			return ocispec.Descriptor{}, err
		}
		desc.Size = size
	} else {
		contentLength := header.Get(httpx.HeaderContentLength)
		if contentLength == "" {
			return ocispec.Descriptor{}, errors.New("content length cannot be empty")
		}
		size, err := strconv.ParseInt(contentLength, 10, 64)
		if err != nil {
			return ocispec.Descriptor{}, err
		}
		desc.Size = size
	}

	dgst, err := digest.Parse(header.Get(HeaderDockerDigest))
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	desc.Digest = dgst

	return desc, nil
}

func WriteDescriptorToHeader(desc ocispec.Descriptor, header http.Header) {
	header.Set(httpx.HeaderContentType, desc.MediaType)
	header.Set(httpx.HeaderContentLength, strconv.FormatInt(desc.Size, 10))
	header.Set(HeaderDockerDigest, desc.Digest.String())
}
