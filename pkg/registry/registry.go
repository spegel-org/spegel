package registry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"sync"
	"time"

	"github.com/go-logr/logr"

	"github.com/spegel-org/spegel/internal/otelx"
	"github.com/spegel-org/spegel/internal/option"
	"github.com/spegel-org/spegel/pkg/httpx"
	"github.com/spegel-org/spegel/pkg/metrics"
	"github.com/spegel-org/spegel/pkg/oci"
	"github.com/spegel-org/spegel/pkg/routing"
)

const (
	HeaderSpegelMirrored = "X-Spegel-Mirrored"
	HandlerAttrKey       = "handler"
	RegistryAttrKey      = "registry"
)

type RegistryConfig struct {
	Transport      http.RoundTripper
	Username       string
	Password       string
	Filters        []oci.Filter
	ResolveTimeout time.Duration
	ResolveRetries int
}

type RegistryOption = option.Option[RegistryConfig]

func WithResolveRetries(resolveRetries int) RegistryOption {
	return func(cfg *RegistryConfig) error {
		cfg.ResolveRetries = resolveRetries
		return nil
	}
}

func WithRegistryFilters(filters []oci.Filter) RegistryOption {
	return func(cfg *RegistryConfig) error {
		cfg.Filters = filters
		return nil
	}
}

func WithResolveTimeout(resolveTimeout time.Duration) RegistryOption {
	return func(cfg *RegistryConfig) error {
		cfg.ResolveTimeout = resolveTimeout
		return nil
	}
}

func WithTransport(transport http.RoundTripper) RegistryOption {
	return func(cfg *RegistryConfig) error {
		cfg.Transport = transport
		return nil
	}
}

func WithBasicAuth(username, password string) RegistryOption {
	return func(cfg *RegistryConfig) error {
		cfg.Username = username
		cfg.Password = password
		return nil
	}
}

type Registry struct {
	bufferPool     *sync.Pool
	ociStore       oci.Store
	ociClient      *oci.Client
	router         routing.Router
	username       string
	password       string
	filters        []oci.Filter
	resolveTimeout time.Duration
	resolveRetries int
}

func NewRegistry(ociStore oci.Store, router routing.Router, opts ...RegistryOption) (*Registry, error) {
	cfg := RegistryConfig{
		ResolveRetries: 3,
		ResolveTimeout: 20 * time.Millisecond,
	}
	err := option.Apply(&cfg, opts...)
	if err != nil {
		return nil, err
	}

	httpClient := &http.Client{}
	if cfg.Transport != nil {
		httpClient.Transport = cfg.Transport
	} else {
		transport := httpx.BaseTransport()
		transport.MaxIdleConns = 100
		transport.MaxConnsPerHost = 100
		transport.MaxIdleConnsPerHost = 100
		httpClient.Transport = transport
	}
	ociClient := oci.NewClient(httpClient)

	bufferPool := &sync.Pool{
		New: func() any {
			buf := make([]byte, 32*1024)
			return &buf
		},
	}

	r := &Registry{
		ociStore:       ociStore,
		router:         router,
		ociClient:      ociClient,
		resolveRetries: cfg.ResolveRetries,
		filters:        cfg.Filters,
		resolveTimeout: cfg.ResolveTimeout,
		username:       cfg.Username,
		password:       cfg.Password,
		bufferPool:     bufferPool,
	}
	return r, nil
}

func (r *Registry) Handler(log logr.Logger) *httpx.ServeMux {
	m := httpx.NewServeMux(log)
	m.Handle("GET /readyz", r.readyHandler)
	m.Handle("GET /livez", r.livenessHandler)
	m.Handle("GET /v2/", r.registryHandler)
	m.Handle("HEAD /v2/", r.registryHandler)
	return m
}

func (r *Registry) readyHandler(rw httpx.ResponseWriter, req *http.Request) {
	rw.SetAttrs(HandlerAttrKey, "readyz")

	ok, err := r.router.Ready(req.Context())
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, fmt.Errorf("could not determine router readiness: %w", err))
		return
	}
	if !ok {
		rw.WriteHeader(http.StatusInternalServerError)
		return
	}
	rw.WriteHeader(http.StatusOK)
}

func (r *Registry) livenessHandler(rw httpx.ResponseWriter, req *http.Request) {
	rw.SetAttrs(HandlerAttrKey, "livez")

	rw.WriteHeader(http.StatusOK)
}

func (r *Registry) registryHandler(rw httpx.ResponseWriter, req *http.Request) {
	rw.SetAttrs(HandlerAttrKey, "registry")

	// Check basic authentication
	if r.username != "" || r.password != "" {
		username, password, _ := req.BasicAuth()
		if r.username != username || r.password != password {
			respErr := oci.NewDistributionError(oci.ErrCodeUnauthorized, "invalid credentials", nil)
			rw.WriteError(http.StatusUnauthorized, respErr)
			return
		}
	}

	// Quickly return 200 for /v2 to indicate that registry supports v2.
	if path.Clean(req.URL.Path) == "/v2" {
		rw.SetAttrs(HandlerAttrKey, "v2")
		rw.WriteHeader(http.StatusOK)
		return
	}

	// Parse out path components from request.
	dist, err := oci.ParseDistributionPath(req.URL)
	if err != nil {
		rw.WriteError(http.StatusNotFound, fmt.Errorf("could not parse path according to OCI distribution spec: %w", err))
		return
	}
	if dist.Registry != "" {
		rw.SetAttrs(RegistryAttrKey, dist.Registry)
	}

	if oci.MatchesFilter(dist.Reference, r.filters) {
		rw.WriteError(http.StatusNotFound, fmt.Errorf("request %s is filtered out by registry filters", dist.String()))
		return
	}

	// Request with mirror header are proxied.
	if req.Header.Get(HeaderSpegelMirrored) != "true" {
		// If content is present locally we should skip the mirroring and just serve it.
		var ociErr error
		if dist.Digest == "" {
			_, ociErr = r.ociStore.Resolve(req.Context(), dist.Identifier())
		} else {
			_, ociErr = r.ociStore.Descriptor(req.Context(), dist.Digest)
		}
		if ociErr != nil {
			r.mirrorHandler(rw, req, dist)
			return
		}
	}

	// Serve registry endpoints.
	switch dist.Kind {
	case oci.DistributionKindManifest:
		r.manifestHandler(rw, req, dist)
		return
	case oci.DistributionKindBlob:
		r.blobHandler(rw, req, dist)
		return
	default:
		// This should never happen as it would be caught when parsing the path.
		rw.WriteError(http.StatusNotFound, fmt.Errorf("unknown distribution path kind %s", dist.Kind))
		return
	}
}

type MirrorErrorDetails struct {
	Attempts int `json:"attempts"`
}

func (r *Registry) mirrorHandler(rw httpx.ResponseWriter, req *http.Request, dist oci.DistributionPath) {
	rw.SetAttrs(HandlerAttrKey, "mirror")

	log := logr.FromContextOrDiscard(req.Context()).WithValues("ref", dist.Identifier(), "path", req.URL.Path)

	defer func() {
		cacheType := "hit"
		if rw.Status() != http.StatusOK {
			cacheType = "miss"
		}
		metrics.MirrorRequestsTotal.WithLabelValues(dist.Registry, cacheType).Inc()
		metrics.MirrorLastSuccessTimestamp.SetToCurrentTime()
	}()

	mirrorDetails := MirrorErrorDetails{
		Attempts: 0,
	}
	errCode := map[oci.DistributionKind]oci.DistributionErrorCode{
		oci.DistributionKindBlob:     oci.ErrCodeBlobUnknown,
		oci.DistributionKindManifest: oci.ErrCodeManifestUnknown,
	}[dist.Kind]

	// Resume range for when blobs fail midway through copying.
	var resumeRng *httpx.Range

	lookupCtx, lookupCancel := context.WithTimeout(req.Context(), r.resolveTimeout)
	defer lookupCancel()
	balancer, err := r.router.Lookup(lookupCtx, dist.Identifier(), r.resolveRetries)
	if err != nil {
		respErr := oci.NewDistributionError(errCode, fmt.Sprintf("lookup failed for %s", dist.Identifier()), mirrorDetails)
		rw.WriteError(http.StatusNotFound, errors.Join(respErr, err))
		return
	}
	for range r.resolveRetries {
		peer, err := balancer.Next()
		if err != nil {
			respErr := oci.NewDistributionError(errCode, fmt.Sprintf("could not find peer for %s", dist.Identifier()), mirrorDetails)
			rw.WriteError(http.StatusNotFound, errors.Join(respErr, lookupCtx.Err()))
			return
		}

		mirrorDetails.Attempts += 1

		mirror := &url.URL{
			Scheme: "http",
			Host:   peer.String(),
		}
		if req.TLS != nil {
			mirror.Scheme = "https"
		}
		fetchOpts := []oci.FetchOption{
			oci.WithFetchHeader(HeaderSpegelMirrored, "true"),
			oci.WithFetchMirror(mirror),
			oci.WithFetchBasicAuth(r.username, r.password),
		}
		// Override range header with resume range if set.
		if resumeRng != nil {
			fetchOpts = append(fetchOpts, oci.WithFetchRange(*resumeRng))
		} else if h := req.Header.Get(httpx.HeaderRange); h != "" {
			fetchOpts = append(fetchOpts, oci.WithFetchHeader(httpx.HeaderRange, h))
		}

		done := func() bool {
			log := log.WithValues("attempt", mirrorDetails.Attempts, "path", req.URL.Path, "mirror", peer)

			fetchCtx := req.Context()
			if req.Method == http.MethodHead {
				var reqCancel context.CancelFunc
				fetchCtx, reqCancel = context.WithTimeout(req.Context(), 1*time.Second)
				defer reqCancel()
			} else if req.Method == http.MethodGet && dist.Kind == oci.DistributionKindManifest {
				var reqCancel context.CancelFunc
				fetchCtx, reqCancel = context.WithTimeout(req.Context(), 2*time.Second)
				defer reqCancel()
			}

			rc, desc, err := r.ociClient.Fetch(fetchCtx, req.Method, dist, fetchOpts...)
			if err != nil {
				log.Error(err, "request to mirror failed, retrying with next")
				balancer.Remove(peer)
				return false
			}
			defer httpx.DrainAndClose(rc)

			if !rw.HeadersWritten() {
				oci.WriteDescriptorToHeader(desc, rw.Header())

				switch dist.Kind {
				case oci.DistributionKindManifest:
					rw.WriteHeader(http.StatusOK)
				case oci.DistributionKindBlob:
					rng, err := httpx.ParseRangeHeader(req.Header, desc.Size)
					if err != nil {
						rw.WriteError(http.StatusBadRequest, err)
						return true
					}
					resumeRng = rng

					rw.Header().Set(httpx.HeaderAcceptRanges, httpx.RangeUnit)
					if rng == nil {
						rw.WriteHeader(http.StatusOK)
					} else {
						rw.Header().Set(httpx.HeaderContentType, httpx.ContentTypeBinary)
						rw.Header().Set(httpx.HeaderContentRange, httpx.ContentRangeFromRange(*rng, desc.Size).String())
						rw.Header().Set(httpx.HeaderContentLength, strconv.FormatInt(rng.Size(), 10))
						rw.WriteHeader(http.StatusPartialContent)
					}
				}
			}
			if req.Method == http.MethodHead {
				return true
			}

			//nolint: errcheck // Ignore
			buf := r.bufferPool.Get().(*[]byte)
			defer r.bufferPool.Put(buf)
			n, err := io.CopyBuffer(rw, rc, *buf)
			if err != nil {
				switch dist.Kind {
				case oci.DistributionKindManifest:
					log.Error(err, "copying of manifest data failed")
					return true
				case oci.DistributionKindBlob:
					if resumeRng == nil {
						resumeRng = &httpx.Range{
							End: desc.Size - 1,
						}
					}
					resumeRng.Start += n
					log.Error(err, "copying of blob data failed, retrying with offset")
					return false
				}
			}
			return true
		}()
		if done {
			return
		}
	}

	respErr := oci.NewDistributionError(errCode, fmt.Sprintf("all request retries exhausted for %s", dist.Identifier()), mirrorDetails)
	rw.WriteError(http.StatusNotFound, errors.Join(respErr, err))
}

func (r *Registry) manifestHandler(rw httpx.ResponseWriter, req *http.Request, dist oci.DistributionPath) {
	rw.SetAttrs(HandlerAttrKey, "manifest")

	if dist.Digest == "" {
		dgst, err := r.ociStore.Resolve(req.Context(), dist.Identifier())
		if err != nil {
			respErr := oci.NewDistributionError(oci.ErrCodeManifestUnknown, fmt.Sprintf("could not get digest for image tag %s", dist.Identifier()), nil)
			rw.WriteError(http.StatusNotFound, errors.Join(respErr, err))
			return
		}
		dist.Digest = dgst
	}
	desc, err := r.ociStore.Descriptor(req.Context(), dist.Digest)
	if err != nil {
		respErr := oci.NewDistributionError(oci.ErrCodeManifestUnknown, fmt.Sprintf("could not get manifest %s", dist.Digest), nil)
		rw.WriteError(http.StatusNotFound, errors.Join(respErr, err))
		return
	}
	if !oci.IsManifestsMediatype(desc.MediaType) {
		respErr := oci.NewDistributionError(oci.ErrCodeManifestUnknown, fmt.Sprintf("could not get manifest %s", dist.Digest), nil)
		rw.WriteError(http.StatusNotFound, errors.Join(respErr, err))
		return
	}

	rw.Header().Set(httpx.HeaderContentType, desc.MediaType)
	rw.Header().Set(httpx.HeaderContentLength, strconv.FormatInt(desc.Size, 10))
	rw.Header().Set(oci.HeaderDockerDigest, desc.Digest.String())
	if req.Method == http.MethodHead {
		rw.WriteHeader(http.StatusOK)
		return
	}

	rc, err := r.ociStore.Open(req.Context(), dist.Digest)
	if err != nil {
		respErr := oci.NewDistributionError(oci.ErrCodeManifestUnknown, fmt.Sprintf("could not get manifest %s", dist.Digest), nil)
		rw.WriteError(http.StatusNotFound, errors.Join(respErr, err))
		return
	}
	defer rc.Close()
	rw.WriteHeader(http.StatusOK)
	_, err = io.Copy(rw, rc)
	if err != nil {
		log := otelx.EnrichLogger(req.Context(), logr.FromContextOrDiscard(req.Context()))
		log.Error(err, "error occurred when writing manifest")
		return
	}
}

func (r *Registry) blobHandler(rw httpx.ResponseWriter, req *http.Request, dist oci.DistributionPath) {
	rw.SetAttrs(HandlerAttrKey, "blob")

	desc, err := r.ociStore.Descriptor(req.Context(), dist.Digest)
	if err != nil {
		respErr := oci.NewDistributionError(oci.ErrCodeBlobUnknown, fmt.Sprintf("could not get blob %s", dist.Digest), nil)
		rw.WriteError(http.StatusNotFound, errors.Join(respErr, err))
		return
	}
	if oci.IsManifestsMediatype(desc.MediaType) {
		respErr := oci.NewDistributionError(oci.ErrCodeBlobUnknown, fmt.Sprintf("could not get blob %s", dist.Digest), nil)
		rw.WriteError(http.StatusNotFound, errors.Join(respErr, err))
		return
	}

	rng, err := httpx.ParseRangeHeader(req.Header, desc.Size)
	if err != nil {
		rw.WriteError(http.StatusBadRequest, err)
		return
	}
	rw.Header().Set(oci.HeaderDockerDigest, dist.Digest.String())
	rw.Header().Set(httpx.HeaderAcceptRanges, httpx.RangeUnit)
	var status int
	if rng == nil {
		status = http.StatusOK
		rw.Header().Set(httpx.HeaderContentType, desc.MediaType)
		rw.Header().Set(httpx.HeaderContentLength, strconv.FormatInt(desc.Size, 10))
	} else {
		status = http.StatusPartialContent
		rw.Header().Set(httpx.HeaderContentType, httpx.ContentTypeBinary)
		rw.Header().Set(httpx.HeaderContentLength, strconv.FormatInt(rng.Size(), 10))
		rw.Header().Set(httpx.HeaderContentRange, httpx.ContentRangeFromRange(*rng, desc.Size).String())
	}
	if req.Method == http.MethodHead {
		rw.WriteHeader(status)
		return
	}

	rc, err := r.ociStore.Open(req.Context(), dist.Digest)
	if err != nil {
		respErr := oci.NewDistributionError(oci.ErrCodeBlobUnknown, fmt.Sprintf("could not get reader for blob %s", dist.Digest), nil)
		rw.WriteError(http.StatusNotFound, errors.Join(respErr, err))
		return
	}
	defer rc.Close()
	var src io.Reader = rc
	if rng != nil {
		_, err := rc.Seek(rng.Start, io.SeekStart)
		if err != nil {
			rw.WriteError(http.StatusInternalServerError, err)
			return
		}
		src = io.LimitReader(rc, rng.Size())
	}
	rw.WriteHeader(status)
	_, err = io.Copy(rw, src)
	if err != nil {
		log := otelx.EnrichLogger(req.Context(), logr.FromContextOrDiscard(req.Context()))
		log.Error(err, "failed to write blob")
		return
	}
}
