package registry

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"sync"
	"time"

	"github.com/go-logr/logr"

	"github.com/spegel-org/spegel/internal/ocifs"
	"github.com/spegel-org/spegel/pkg/httpx"
	"github.com/spegel-org/spegel/pkg/metrics"
	"github.com/spegel-org/spegel/pkg/oci"
	"github.com/spegel-org/spegel/pkg/routing"
)

const (
	HeaderSpegelMirrored = "X-Spegel-Mirrored"
)

type RegistryConfig struct {
	Client           *http.Client
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
		if cfg.Client == nil {
			cfg.Client = &http.Client{}
		}
		cfg.Client.Transport = transport
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
	client           *http.Client
	bufferPool       *sync.Pool
	log              logr.Logger
	ociStore         oci.Store
	router           routing.Router
	username         string
	password         string
	resolveRetries   int
	resolveTimeout   time.Duration
	resolveLatestTag bool
}

func NewRegistry(ociStore oci.Store, router routing.Router, opts ...RegistryOption) (*Registry, error) {
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("default transporn is not of type http.Transport")
	}
	cfg := RegistryConfig{
		Client: &http.Client{
			Transport: transport.Clone(),
		},
		Log:              logr.Discard(),
		ResolveRetries:   3,
		ResolveLatestTag: true,
		ResolveTimeout:   20 * time.Millisecond,
	}
	err := cfg.Apply(opts...)
	if err != nil {
		return nil, err
	}

	bufferPool := &sync.Pool{
		New: func() any {
			buf := make([]byte, 32*1024)
			return &buf
		},
	}
	r := &Registry{
		ociStore:         ociStore,
		router:           router,
		client:           cfg.Client,
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

func (r *Registry) Server(addr string) (*http.Server, error) {
	m := httpx.NewServeMux(r.log)
	m.Handle("GET /healthz", r.readyHandler)
	m.Handle("GET /v2/", r.registryHandler)
	m.Handle("HEAD /v2/", r.registryHandler)
	srv := &http.Server{
		Addr:    addr,
		Handler: m,
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
		// Set mirrored header in request to stop infinite loops
		req.Header.Set(HeaderSpegelMirrored, "true")

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

	forwardScheme := "http"
	if req.TLS != nil {
		forwardScheme = "https"
	}
	f, err := ocifs.NewRoutedFile(logr.NewContext(req.Context(), r.log), r.router, dist, req.Method, forwardScheme, r.resolveTimeout, r.resolveRetries)
	if err != nil {
		rw.WriteError(http.StatusNotFound, err)
		return
	}
	defer f.Close()

	desc, err := f.Descriptor()
	if err != nil {
		rw.WriteError(http.StatusNotFound, err)
		return
	}
	oci.WriteDescriptorToHeader(desc, rw.Header())

	//nolint: errcheck // Ignore
	buf := r.bufferPool.Get().(*[]byte)
	defer r.bufferPool.Put(buf)
	_, err = io.CopyBuffer(rw, f, *buf)
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, err)
		return
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
