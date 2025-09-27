package registry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/go-logr/logr"

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
	Transport        http.RoundTripper
	Username         string
	Password         string
	Filters          []*regexp.Regexp
	ResolveLatestTag bool
	ResolveTimeout   time.Duration
	ResolveRetries   int
}

type RegistryOption = option.Option[RegistryConfig]

func WithResolveRetries(resolveRetries int) RegistryOption {
	return func(cfg *RegistryConfig) error {
		cfg.ResolveRetries = resolveRetries
		return nil
	}
}

// Deprecated: Resolve latest tag is replaced by registry filter which offers more customizable behavior. Use the filter `:latest$` to achieve the same behavior.
func WithResolveLatestTag(resolveLatestTag bool) RegistryOption {
	return func(cfg *RegistryConfig) error {
		cfg.ResolveLatestTag = resolveLatestTag
		return nil
	}
}

func WithRegistryFilters(filters []*regexp.Regexp) RegistryOption {
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
	bufferPool       *sync.Pool
	ociStore         oci.Store
	ociClient        *oci.Client
	router           routing.Router
	username         string
	password         string
	filters          []*regexp.Regexp
	resolveTimeout   time.Duration
	resolveRetries   int
	resolveLatestTag bool
}

func NewRegistry(ociStore oci.Store, router routing.Router, opts ...RegistryOption) (*Registry, error) {
	cfg := RegistryConfig{
		ResolveRetries:   3,
		ResolveLatestTag: true,
		ResolveTimeout:   20 * time.Millisecond,
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
		ociStore:         ociStore,
		router:           router,
		ociClient:        ociClient,
		resolveRetries:   cfg.ResolveRetries,
		resolveLatestTag: cfg.ResolveLatestTag,
		filters:          cfg.Filters,
		resolveTimeout:   cfg.ResolveTimeout,
		username:         cfg.Username,
		password:         cfg.Password,
		bufferPool:       bufferPool,
	}
	return r, nil
}

func (r *Registry) Handler(log logr.Logger) *httpx.ServeMux {
	m := httpx.NewServeMux(log)
	m.Handle("GET /readyz", r.readyHandler)
	m.Handle("GET /livez", r.livenesHandler)
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

func (r *Registry) livenesHandler(rw httpx.ResponseWriter, req *http.Request) {
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

	// Apply registry filters to determine if the request should be mirrored.
	for _, f := range r.filters {
		if f.MatchString(dist.Reference()) {
			rw.WriteError(http.StatusNotFound, fmt.Errorf("request %s is filtered out by registry filters", dist.Reference()))
			return
		}
	}

	// Request with mirror header are proxied.
	if req.Header.Get(HeaderSpegelMirrored) != "true" {
		// If content is present locally we should skip the mirroring and just serve it.
		var ociErr error
		if dist.Digest == "" {
			_, ociErr = r.ociStore.Resolve(req.Context(), dist.Reference())
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

	log := logr.FromContextOrDiscard(req.Context()).WithValues("ref", dist.Reference(), "path", req.URL.Path)

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

	if !r.resolveLatestTag && dist.IsLatestTag() {
		respErr := oci.NewDistributionError(errCode, "latest tag resolving is disabled", mirrorDetails)
		rw.WriteError(http.StatusNotFound, respErr)
		return
	}

	// Resolve mirror with the requested reference
	resolveCtx, resolveCancel := context.WithTimeout(req.Context(), r.resolveTimeout)
	defer resolveCancel()
	peerCh, err := r.router.Resolve(resolveCtx, dist.Reference(), r.resolveRetries)
	if err != nil {
		respErr := oci.NewDistributionError(errCode, "unable to resolve peers", mirrorDetails)
		rw.WriteError(http.StatusNotFound, respErr)
		return
	}

	var rng *httpx.Range
	for {
		select {
		case <-req.Context().Done():
			// Request has been closed by server or client, no use continuing.
			respErr := oci.NewDistributionError(errCode, "mirroring for image component has been cancelled", mirrorDetails)
			rw.WriteError(http.StatusNotFound, errors.Join(respErr, req.Context().Err()))
			return
		case peer, ok := <-peerCh:
			// Channel closed means no more mirrors will be received and max retries has been reached.
			if !ok {
				msg := fmt.Sprintf("mirror with image component %s could not be found", dist.Reference())
				if mirrorDetails.Attempts > 0 {
					msg = fmt.Sprintf("%s requests to %d mirrors failed, all attempts have been exhausted or timeout has been reached", msg, mirrorDetails.Attempts)
				}
				respErr := oci.NewDistributionError(errCode, msg, mirrorDetails)
				rw.WriteError(http.StatusNotFound, errors.Join(respErr, resolveCtx.Err()))
				return
			}

			mirrorDetails.Attempts++

			mirror := &url.URL{
				Scheme: "http",
				Host:   peer.String(),
			}
			if req.TLS != nil {
				mirror.Scheme = "https"
			}
			fetchOpts := []oci.FetchOption{
				oci.WithFetchHeader(http.Header{HeaderSpegelMirrored: []string{"true"}}),
				oci.WithFetchMirror(mirror),
				oci.WithBasicAuth(r.username, r.password),
			}

			err := func() error {
				if req.Method == http.MethodHead {
					headCtx, headCancel := context.WithTimeout(req.Context(), 1*time.Second)
					defer headCancel()
					desc, err := r.ociClient.Head(headCtx, dist, fetchOpts...)
					if err != nil {
						return err
					}
					if !rw.HeadersWritten() {
						oci.WriteDescriptorToHeader(desc, rw.Header())
						rw.WriteHeader(http.StatusOK)
					}
					return nil
				}

				if dist.Kind == oci.DistributionKindManifest {
					manifestCtx, manifestCancel := context.WithTimeout(req.Context(), 2*time.Second)
					defer manifestCancel()
					rc, desc, err := r.ociClient.Get(manifestCtx, dist, nil, fetchOpts...)
					if err != nil {
						return err
					}
					if !rw.HeadersWritten() {
						oci.WriteDescriptorToHeader(desc, rw.Header())
						rw.WriteHeader(http.StatusOK)
					}
					//nolint: errcheck // Ignore
					buf := r.bufferPool.Get().(*[]byte)
					defer r.bufferPool.Put(buf)
					_, err = io.CopyBuffer(rw, rc, *buf)
					if err != nil {
						return err
					}
					return nil
				}

				if !rw.HeadersWritten() {
					headCtx, headCancel := context.WithTimeout(req.Context(), 1*time.Second)
					defer headCancel()
					desc, err := r.ociClient.Head(headCtx, dist, fetchOpts...)
					if err != nil {
						return err
					}
					oci.WriteDescriptorToHeader(desc, rw.Header())

					status := http.StatusOK
					rangeHeader := req.Header.Get(httpx.HeaderRange)
					if rangeHeader != "" {
						parsedRng, err := httpx.ParseRangeHeader(rangeHeader, desc.Size)
						if err != nil {
							return err
						}
						rng = &parsedRng
						crng := httpx.ContentRangeFromRange(*rng, desc.Size)
						rw.Header().Set(httpx.HeaderContentRange, crng.String())
						rw.Header().Set(httpx.HeaderContentLength, strconv.FormatInt(rng.Size(), 10))
						status = http.StatusPartialContent
					}
					rw.WriteHeader(status)
				}

				rc, _, err := r.ociClient.Get(req.Context(), dist, rng, fetchOpts...)
				if err != nil {
					return err
				}
				//nolint: errcheck // Ignore
				buf := r.bufferPool.Get().(*[]byte)
				defer r.bufferPool.Put(buf)
				_, err = io.CopyBuffer(rw, rc, *buf)
				if err != nil {
					return err
				}
				return nil
			}()
			if err != nil {
				log.Error(err, "request to mirror failed", "attempt", mirrorDetails.Attempts, "path", req.URL.Path, "mirror", peer)
				continue
			}
			return
		}
	}
}

func (r *Registry) manifestHandler(rw httpx.ResponseWriter, req *http.Request, dist oci.DistributionPath) {
	rw.SetAttrs(HandlerAttrKey, "manifest")

	if dist.Digest == "" {
		dgst, err := r.ociStore.Resolve(req.Context(), dist.Reference())
		if err != nil {
			respErr := oci.NewDistributionError(oci.ErrCodeManifestUnknown, fmt.Sprintf("could not get digest for image tag %s", dist.Reference()), nil)
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
	rw.WriteHeader(http.StatusOK)
	if req.Method == http.MethodHead {
		return
	}

	rc, err := r.ociStore.Open(req.Context(), dist.Digest)
	if err != nil {
		respErr := oci.NewDistributionError(oci.ErrCodeManifestUnknown, fmt.Sprintf("could not get manifest %s", dist.Digest), nil)
		rw.WriteError(http.StatusNotFound, errors.Join(respErr, err))
		return
	}
	defer rc.Close()
	_, err = io.Copy(rw, rc)
	if err != nil {
		logr.FromContextOrDiscard(req.Context()).Error(err, "error occurred when writing manifest")
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

	rw.Header().Set(httpx.HeaderAcceptRanges, httpx.RangeUnit)
	rw.Header().Set(httpx.HeaderContentType, httpx.ContentTypeBinary)
	rw.Header().Set(httpx.HeaderContentLength, strconv.FormatInt(desc.Size, 10))
	rw.Header().Set(oci.HeaderDockerDigest, dist.Digest.String())
	if req.Method == http.MethodHead {
		rw.WriteHeader(http.StatusOK)
		return
	}

	rc, err := r.ociStore.Open(req.Context(), dist.Digest)
	if err != nil {
		respErr := oci.NewDistributionError(oci.ErrCodeBlobUnknown, fmt.Sprintf("could not get reader for blob %s", dist.Digest), nil)
		rw.WriteError(http.StatusNotFound, errors.Join(respErr, err))
		return
	}
	defer rc.Close()
	http.ServeContent(rw, req, "", time.Time{}, rc)
}
