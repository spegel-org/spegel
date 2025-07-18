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

	"github.com/spegel-org/spegel/pkg/httpx"
	"github.com/spegel-org/spegel/pkg/metrics"
	"github.com/spegel-org/spegel/pkg/oci"
	"github.com/spegel-org/spegel/pkg/routing"
)

const (
	HeaderSpegelMirrored = "X-Spegel-Mirrored"
)

type RegistryConfig struct {
	Transport        http.RoundTripper
	Log              logr.Logger
	Username         string
	Password         string
	ResolveRetries   int
	ResolveLatestTag bool
	ResolveTimeout   time.Duration
}

func (cfg *RegistryConfig) Apply(opts ...RegistryOption) error {
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

type RegistryOption func(cfg *RegistryConfig) error

func WithResolveRetries(resolveRetries int) RegistryOption {
	return func(cfg *RegistryConfig) error {
		cfg.ResolveRetries = resolveRetries
		return nil
	}
}

func WithResolveLatestTag(resolveLatestTag bool) RegistryOption {
	return func(cfg *RegistryConfig) error {
		cfg.ResolveLatestTag = resolveLatestTag
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

func WithLogger(log logr.Logger) RegistryOption {
	return func(cfg *RegistryConfig) error {
		cfg.Log = log
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
	log              logr.Logger
	ociStore         oci.Store
	ociClient        *oci.Client
	router           routing.Router
	username         string
	password         string
	resolveRetries   int
	resolveTimeout   time.Duration
	resolveLatestTag bool
}

func NewRegistry(ociStore oci.Store, router routing.Router, opts ...RegistryOption) (*Registry, error) {
	cfg := RegistryConfig{
		Log:              logr.Discard(),
		ResolveRetries:   3,
		ResolveLatestTag: true,
		ResolveTimeout:   20 * time.Millisecond,
	}
	err := cfg.Apply(opts...)
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
		log:              cfg.Log,
		resolveRetries:   cfg.ResolveRetries,
		resolveLatestTag: cfg.ResolveLatestTag,
		resolveTimeout:   cfg.ResolveTimeout,
		username:         cfg.Username,
		password:         cfg.Password,
		bufferPool:       bufferPool,
	}
	return r, nil
}

func (r *Registry) Handler() *httpx.ServeMux {
	m := httpx.NewServeMux(r.log)
	m.Handle("GET /healthz", r.readyHandler)
	m.Handle("GET /v2/", r.registryHandler)
	m.Handle("HEAD /v2/", r.registryHandler)
	return m
}

func (r *Registry) Server(addr string) (*http.Server, error) {
	srv := &http.Server{
		Addr:    addr,
		Handler: r.Handler(),
	}
	return srv, nil
}

func (r *Registry) readyHandler(rw httpx.ResponseWriter, req *http.Request) {
	rw.SetHandler("ready")
	ok, err := r.router.Ready(req.Context())
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, fmt.Errorf("could not determine router readiness: %w", err))
		return
	}
	if !ok {
		rw.WriteHeader(http.StatusInternalServerError)
		return
	}
}

func (r *Registry) registryHandler(rw httpx.ResponseWriter, req *http.Request) {
	rw.SetHandler("registry")

	// Check basic authentication
	if r.username != "" || r.password != "" {
		username, password, _ := req.BasicAuth()
		if r.username != username || r.password != password {
			rw.WriteError(http.StatusUnauthorized, errors.New("invalid basic authentication"))
			return
		}
	}

	// Quickly return 200 for /v2 to indicate that registry supports v2.
	if path.Clean(req.URL.Path) == "/v2" {
		rw.SetHandler("v2")
		rw.WriteHeader(http.StatusOK)
		return
	}

	// Parse out path components from request.
	dist, err := oci.ParseDistributionPath(req.URL)
	if err != nil {
		rw.WriteError(http.StatusNotFound, fmt.Errorf("could not parse path according to OCI distribution spec: %w", err))
		return
	}

	// Request with mirror header are proxied.
	if req.Header.Get(HeaderSpegelMirrored) != "true" {
		// If content is present locally we should skip the mirroring and just serve it.
		var ociErr error
		if dist.Digest == "" {
			_, ociErr = r.ociStore.Resolve(req.Context(), dist.Reference())
		} else {
			_, ociErr = r.ociStore.Size(req.Context(), dist.Digest)
		}
		if ociErr != nil {
			rw.SetHandler("mirror")
			r.handleMirror(rw, req, dist)
			return
		}
	}

	// Serve registry endpoints.
	switch dist.Kind {
	case oci.DistributionKindManifest:
		rw.SetHandler("manifest")
		r.handleManifest(rw, req, dist)
		return
	case oci.DistributionKindBlob:
		rw.SetHandler("blob")
		r.handleBlob(rw, req, dist)
		return
	default:
		rw.WriteError(http.StatusNotFound, fmt.Errorf("unknown distribution path kind %s", dist.Kind))
		return
	}
}

func (r *Registry) handleMirror(rw httpx.ResponseWriter, req *http.Request, dist oci.DistributionPath) {
	log := r.log.WithValues("ref", dist.Reference(), "path", req.URL.Path)

	defer func() {
		cacheType := "hit"
		if rw.Status() != http.StatusOK {
			cacheType = "miss"
		}
		metrics.MirrorRequestsTotal.WithLabelValues(dist.Registry, cacheType).Inc()
	}()

	if !r.resolveLatestTag && dist.IsLatestTag() {
		r.log.V(4).Info("skipping mirror request for image with latest tag", "image", dist.Reference())
		rw.WriteHeader(http.StatusNotFound)
		return
	}

	// Resolve mirror with the requested reference
	resolveCtx, resolveCancel := context.WithTimeout(req.Context(), r.resolveTimeout)
	defer resolveCancel()
	resolveCtx = logr.NewContext(resolveCtx, log)
	peerCh, err := r.router.Resolve(resolveCtx, dist.Reference(), r.resolveRetries)
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, fmt.Errorf("error occurred when attempting to resolve mirrors: %w", err))
		return
	}

	var mirrorAttempts = 0
	var rng *httpx.Range
	for {
		select {
		case <-req.Context().Done():
			// Request has been closed by server or client. No use continuing.
			rw.WriteError(http.StatusNotFound, fmt.Errorf("mirroring for image component %s has been cancelled: %w", dist.Reference(), resolveCtx.Err()))
			return
		case peer, ok := <-peerCh:
			// Channel closed means no more mirrors will be received and max retries has been reached.
			if !ok {
				err = fmt.Errorf("mirror with image component %s could not be found", dist.Reference())
				if mirrorAttempts > 0 {
					err = errors.Join(err, fmt.Errorf("requests to %d mirrors failed, all attempts have been exhausted or timeout has been reached", mirrorAttempts))
				}
				rw.WriteError(http.StatusNotFound, err)
				return
			}

			mirrorAttempts++

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
				log.Error(err, "request to mirror failed", "attempt", mirrorAttempts, "path", req.URL.Path, "mirror", peer)
				continue
			}

			log.V(4).Info("mirrored request", "path", req.URL.Path, "mirror", peer)
			return
		}
	}
}

func (r *Registry) handleManifest(rw httpx.ResponseWriter, req *http.Request, dist oci.DistributionPath) {
	if dist.Digest == "" {
		dgst, err := r.ociStore.Resolve(req.Context(), dist.Reference())
		if err != nil {
			rw.WriteError(http.StatusNotFound, fmt.Errorf("could not get digest for image %s: %w", dist.Reference(), err))
			return
		}
		dist.Digest = dgst
	}
	b, mediaType, err := r.ociStore.GetManifest(req.Context(), dist.Digest)
	if err != nil {
		rw.WriteError(http.StatusNotFound, fmt.Errorf("could not get manifest content for digest %s: %w", dist.Digest.String(), err))
		return
	}
	rw.Header().Set(httpx.HeaderContentType, mediaType)
	rw.Header().Set(httpx.HeaderContentLength, strconv.FormatInt(int64(len(b)), 10))
	rw.Header().Set(oci.HeaderDockerDigest, dist.Digest.String())
	if req.Method == http.MethodHead {
		return
	}
	_, err = rw.Write(b)
	if err != nil {
		r.log.Error(err, "error occurred when writing manifest")
		return
	}
}

func (r *Registry) handleBlob(rw httpx.ResponseWriter, req *http.Request, dist oci.DistributionPath) {
	size, err := r.ociStore.Size(req.Context(), dist.Digest)
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, fmt.Errorf("could not determine size of blob with digest %s: %w", dist.Digest.String(), err))
		return
	}
	rw.Header().Set(httpx.HeaderAcceptRanges, "bytes")
	rw.Header().Set(httpx.HeaderContentType, "application/octet-stream")
	rw.Header().Set(httpx.HeaderContentLength, strconv.FormatInt(size, 10))
	rw.Header().Set(oci.HeaderDockerDigest, dist.Digest.String())
	if req.Method == http.MethodHead {
		return
	}

	rc, err := r.ociStore.GetBlob(req.Context(), dist.Digest)
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, fmt.Errorf("could not get reader for blob with digest %s: %w", dist.Digest.String(), err))
		return
	}
	defer rc.Close()

	http.ServeContent(rw, req, "", time.Time{}, rc)
}
