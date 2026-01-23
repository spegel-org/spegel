package oci

import (
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
	"sync"
	"time"

	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/platforms"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/spegel-org/spegel/internal/option"
	"github.com/spegel-org/spegel/pkg/httpx"
)

const (
	HeaderDockerDigest = "Docker-Content-Digest"
	HeaderNamespace    = "OCI-Namespace"
)

type ClientConfig struct {
	TLSClientConfig *tls.Config
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

type Client struct {
	httpClient *http.Client
	tokenCache sync.Map
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
	httpClient.Transport = httpx.WrapTransport("oci", transport)

	ociClient := &Client{
		httpClient: httpClient,
		tokenCache: sync.Map{},
	}
	return ociClient, nil
}

type CommonConfig struct {
	Mirror   *url.URL
	Header   http.Header
	Username string
	Password string
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

func WithPullBasicAuth(username, password string) PullOption {
	return func(cfg *PullConfig) error {
		cfg.Username = username
		cfg.Password = password
		return nil
	}
}

func WithPullPlatform(platform ocispec.Platform) PullOption {
	return func(cfg *PullConfig) error {
		cfg.Platform = platform
		return nil
	}
}

type FetchConfig struct {
	Range *httpx.Range
	CommonConfig
}

type FetchOption = option.Option[FetchConfig]

func WithFetchMirror(mirror *url.URL) FetchOption {
	return func(cfg *FetchConfig) error {
		cfg.Mirror = mirror
		return nil
	}
}

func WithFetchHeader(k, v string) FetchOption {
	return func(cfg *FetchConfig) error {
		if cfg.Header == nil {
			cfg.Header = http.Header{}
		}
		cfg.Header.Set(k, v)
		return nil
	}
}

func WithFetchBasicAuth(username, password string) FetchOption {
	return func(cfg *FetchConfig) error {
		cfg.Username = username
		cfg.Password = password
		return nil
	}
}

func WithFetchRange(rng httpx.Range) FetchOption {
	return func(cfg *FetchConfig) error {
		cfg.Range = &rng
		return nil
	}
}

type PullMetric struct {
	Digest        digest.Digest
	ContentType   string
	ContentLength int64
	Duration      time.Duration
}

func (c *Client) Pull(ctx context.Context, img Image, opts ...PullOption) ([]PullMetric, error) {
	cfg := PullConfig{
		Platform: platforms.DefaultSpec(),
	}
	err := option.Apply(&cfg, opts...)
	if err != nil {
		return nil, err
	}
	fetchOpt := func(fetchCfg *FetchConfig) error {
		fetchCfg.CommonConfig = cfg.CommonConfig
		return nil
	}

	pullMetrics := []PullMetric{}
	queue := []DistributionPath{
		img.DistributionPath(),
	}
	for len(queue) > 0 {
		dist := queue[0]
		queue = queue[1:]

		start := time.Now()
		desc, err := func() (ocispec.Descriptor, error) {
			rc, desc, err := c.Get(ctx, dist, fetchOpt)
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
						if !platforms.Only(cfg.Platform).Match(*m.Platform) {
							continue
						}
						queue = append(queue, DistributionPath{
							Kind: DistributionKindManifest,
							Reference: Reference{
								Registry:   dist.Registry,
								Repository: dist.Repository,
								Digest:     m.Digest,
							},
						})
					}
				case images.MediaTypeDockerSchema2Manifest, ocispec.MediaTypeImageManifest:
					var manifest ocispec.Manifest
					err := json.Unmarshal(b, &manifest)
					if err != nil {
						return ocispec.Descriptor{}, err
					}
					queue = append(queue, DistributionPath{
						Kind: DistributionKindBlob,
						Reference: Reference{
							Registry:   dist.Registry,
							Repository: dist.Repository,
							Digest:     manifest.Config.Digest,
						},
					})
					for _, layer := range manifest.Layers {
						queue = append(queue, DistributionPath{
							Kind: DistributionKindBlob,
							Reference: Reference{
								Registry:   dist.Registry,
								Repository: dist.Repository,
								Digest:     layer.Digest,
							},
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
	rc, desc, err := c.Fetch(ctx, http.MethodHead, dist, opts...)
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	defer httpx.DrainAndClose(rc)
	return desc, nil
}

func (c *Client) Get(ctx context.Context, dist DistributionPath, opts ...FetchOption) (io.ReadCloser, ocispec.Descriptor, error) {
	rc, desc, err := c.Fetch(ctx, http.MethodGet, dist, opts...)
	if err != nil {
		return nil, ocispec.Descriptor{}, err
	}
	return rc, desc, nil
}

func (c *Client) Fetch(ctx context.Context, method string, dist DistributionPath, opts ...FetchOption) (io.ReadCloser, ocispec.Descriptor, error) {
	if method != http.MethodHead && method != http.MethodGet {
		return nil, ocispec.Descriptor{}, errors.New("fetch only supports HEAD and GET requests")
	}

	cfg := FetchConfig{}
	err := option.Apply(&cfg, opts...)
	if err != nil {
		return nil, ocispec.Descriptor{}, err
	}
	if dist.Kind == DistributionKindManifest && cfg.Range != nil {
		return nil, ocispec.Descriptor{}, errors.New("cannot make range requests for manifests")
	}

	tcKey := dist.Registry + dist.Repository

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
		req.SetBasicAuth(cfg.Username, cfg.Password)
		req.Header.Set(httpx.HeaderUserAgent, "spegel")
		req.Header.Add(httpx.HeaderAccept, ocispec.MediaTypeImageManifest)
		req.Header.Add(httpx.HeaderAccept, images.MediaTypeDockerSchema2Manifest)
		req.Header.Add(httpx.HeaderAccept, ocispec.MediaTypeImageIndex)
		req.Header.Add(httpx.HeaderAccept, images.MediaTypeDockerSchema2ManifestList)
		if cfg.Range != nil {
			req.Header.Add(httpx.HeaderRange, cfg.Range.String())
		}
		token, ok := c.tokenCache.Load(tcKey)
		if ok {
			//nolint: errcheck // We know it will be a string.
			req.Header.Set(httpx.HeaderAuthorization, "Bearer "+token.(string))
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, ocispec.Descriptor{}, err
		}
		if resp.StatusCode == http.StatusUnauthorized {
			c.tokenCache.Delete(tcKey)
			wwwAuth := resp.Header.Get(httpx.HeaderWWWAuthenticate)
			token, err = getBearerToken(ctx, c.httpClient, dist.Repository, wwwAuth)
			if err != nil {
				return nil, ocispec.Descriptor{}, err
			}
			c.tokenCache.Store(tcKey, token)
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

func getBearerToken(ctx context.Context, client *http.Client, repository, wwwAuth string) (string, error) {
	if !strings.HasPrefix(wwwAuth, "Bearer ") {
		return "", errors.New("unsupported auth scheme")
	}

	params := map[string]string{}
	for part := range strings.SplitSeq(wwwAuth[len("Bearer "):], ",") {
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
		if authURL.Host == "ghcr.io" {
			scope = strings.ReplaceAll(scope, "user/image", repository)
		}
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
