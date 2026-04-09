package registry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/spegel-org/spegel/internal/option"
	"github.com/spegel-org/spegel/internal/ptr"
	"github.com/spegel-org/spegel/internal/resilient"
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
	OCIClient      *oci.Client
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

func WithOCIClient(ociClient *oci.Client) RegistryOption {
	return func(cfg *RegistryConfig) error {
		cfg.OCIClient = ociClient
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

type Statistics struct {
	MirrorLastSuccess atomic.Int64
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
	stats          Statistics
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
	if cfg.OCIClient == nil {
		ociClient, err := oci.NewClient()
		if err != nil {
			return nil, err
		}
		cfg.OCIClient = ociClient
	}

	bufferPool := &sync.Pool{
		New: func() any {
			buf := make([]byte, 32*1024)
			return &buf
		},
	}

	r := &Registry{
		ociStore:       ociStore,
		router:         router,
		ociClient:      cfg.OCIClient,
		resolveRetries: cfg.ResolveRetries,
		filters:        cfg.Filters,
		resolveTimeout: cfg.ResolveTimeout,
		username:       cfg.Username,
		password:       cfg.Password,
		bufferPool:     bufferPool,
		stats:          Statistics{},
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

func (r *Registry) Stats() *Statistics {
	return &r.stats
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
	dist, err := oci.ParseDistributionPath(req)
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
			r.mirrorHandler(req.Context(), dist, rw)
			return
		}
	}

	// Serve registry endpoints.
	switch dist.Kind {
	case oci.DistributionKindManifest:
		r.manifestHandler(req.Context(), dist, rw)
		return
	case oci.DistributionKindBlob:
		r.blobHandler(req.Context(), dist, rw)
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

func (r *Registry) mirrorHandler(ctx context.Context, dist oci.DistributionPath, rw httpx.ResponseWriter) {
	rw.SetAttrs(HandlerAttrKey, "mirror")

	log := logr.FromContextOrDiscard(ctx).WithValues("ref", dist.Identifier(), "path", dist.URL().Path)

	defer func() {
		if rw.Error() == nil {
			metrics.MirrorRequestsTotal.WithLabelValues(dist.Registry, "hit").Inc()
			metrics.MirrorLastSuccessTimestamp.SetToCurrentTime()
			r.stats.MirrorLastSuccess.Store(time.Now().Unix())
		} else {
			metrics.MirrorRequestsTotal.WithLabelValues(dist.Registry, "miss").Inc()
		}
	}()

	mirrorDetails := MirrorErrorDetails{
		Attempts: 0,
	}
	errCode := map[oci.DistributionKind]oci.DistributionErrorCode{
		oci.DistributionKindBlob:     oci.ErrCodeBlobUnknown,
		oci.DistributionKindManifest: oci.ErrCodeManifestUnknown,
	}[dist.Kind]

	lookupCtx, lookupCancel := context.WithTimeout(ctx, r.resolveTimeout)
	defer lookupCancel()
	balancer, err := r.router.Lookup(lookupCtx, dist.Identifier(), r.resolveRetries)
	if err != nil {
		respErr := oci.NewDistributionError(errCode, fmt.Sprintf("lookup failed for %s", dist.Identifier()), mirrorDetails)
		rw.WriteError(http.StatusNotFound, errors.Join(respErr, err))
		return
	}

	// Set timeout for non blob data requests.
	fetchCtx := ctx
	if dist.Method == http.MethodHead || dist.Kind == oci.DistributionKindManifest {
		var reqCancel context.CancelFunc
		fetchCtx, reqCancel = context.WithTimeout(ctx, 3*time.Second)
		defer reqCancel()
	}

	retryOpts := []resilient.RetryOption{
		resilient.WithOnRetry(func(attempt int, err error) {
			log.Error(err, "retrying mirror request", "attempt", attempt)
		}),
	}
	err = resilient.Retry(fetchCtx, r.resolveRetries, resilient.FixedDelay(0), func(ctx context.Context) error {
		peer, err := balancer.Next()
		if err != nil {
			return resilient.Unrecoverable(err)
		}

		mirrorDetails.Attempts += 1

		type fetchResult struct {
			rc   io.ReadCloser
			desc ocispec.Descriptor
		}
		res, err := httpx.HappyEyeballs(fetchCtx, peer.Addresses, func(ctx context.Context, ipAddr netip.Addr) (fetchResult, error) {
			mirror := &url.URL{
				Scheme: dist.Scheme,
				Host:   netip.AddrPortFrom(peer.Addresses[0], peer.Metadata.RegistryPort).String(),
			}
			fetchOpts := []oci.FetchOption{
				oci.WithFetchHeader(HeaderSpegelMirrored, "true"),
				oci.WithFetchMirror(mirror),
				oci.WithFetchBasicAuth(r.username, r.password),
			}
			rc, desc, err := r.ociClient.Fetch(fetchCtx, dist, fetchOpts...)
			if err != nil {
				return fetchResult{}, err
			}
			res := fetchResult{
				desc: desc,
				rc:   rc,
			}
			return res, nil
		})
		if err != nil {
			balancer.Remove(peer)
			return fmt.Errorf("request to mirror failed: %w", err)
		}
		desc := res.desc
		rc := res.rc
		defer httpx.DrainAndClose(rc)

		if !rw.HeadersWritten() {
			oci.WriteDescriptorToHeader(desc, rw.Header())

			switch dist.Kind {
			case oci.DistributionKindManifest:
				rw.WriteHeader(http.StatusOK)
			case oci.DistributionKindBlob:
				rw.Header().Set(httpx.HeaderAcceptRanges, httpx.RangeUnit)
				if dist.Range == nil {
					rw.WriteHeader(http.StatusOK)
				} else {
					crng, err := httpx.ContentRangeFromRange(*dist.Range, desc.Size)
					if err != nil {
						rw.WriteError(http.StatusBadRequest, err)
						return resilient.Unrecoverable(err)
					}
					rw.Header().Set(httpx.HeaderContentType, httpx.ContentTypeBinary)
					rw.Header().Set(httpx.HeaderContentRange, crng.String())
					rw.Header().Set(httpx.HeaderContentLength, strconv.FormatInt(crng.Length(), 10))
					rw.WriteHeader(http.StatusPartialContent)
				}
			}
		}
		if dist.Method == http.MethodHead {
			return nil
		}

		//nolint: errcheck // Ignore
		buf := r.bufferPool.Get().(*[]byte)
		defer r.bufferPool.Put(buf)
		n, err := io.CopyBuffer(rw, rc, *buf)
		if err != nil {
			switch dist.Kind {
			case oci.DistributionKindManifest:
				return resilient.Unrecoverable(fmt.Errorf("copying of manifest data failed: %w", err))
			case oci.DistributionKindBlob:
				// TODO: Avoid modifying a pointer.
				if dist.Range == nil {
					dist.Range = &httpx.Range{
						Start: ptr.To(int64(0)),
						End:   ptr.To(desc.Size - 1),
					}
				}
				dist.Range.Start = ptr.To(*dist.Range.Start + n)
				balancer.Remove(peer)
				return fmt.Errorf("copying of blob data failed: %w", err)
			}
		}
		return nil
	}, retryOpts...)
	if err != nil {
		if !rw.HeadersWritten() {
			respErr := oci.NewDistributionError(errCode, fmt.Sprintf("all request retries exhausted for %s", dist.Identifier()), mirrorDetails)
			if mirrorDetails.Attempts == 0 {
				respErr = oci.NewDistributionError(errCode, fmt.Sprintf("could not find peer for %s", dist.Identifier()), mirrorDetails)
			}
			rw.WriteError(http.StatusNotFound, errors.Join(respErr, err))
		} else {
			log.Error(err, "failure after headers written")
		}
		return
	}
}

func (r *Registry) manifestHandler(ctx context.Context, dist oci.DistributionPath, rw httpx.ResponseWriter) {
	rw.SetAttrs(HandlerAttrKey, "manifest")

	if dist.Digest == "" {
		dgst, err := r.ociStore.Resolve(ctx, dist.Identifier())
		if err != nil {
			respErr := oci.NewDistributionError(oci.ErrCodeManifestUnknown, fmt.Sprintf("could not get digest for image tag %s", dist.Identifier()), nil)
			rw.WriteError(http.StatusNotFound, errors.Join(respErr, err))
			return
		}
		dist.Digest = dgst
	}
	desc, err := r.ociStore.Descriptor(ctx, dist.Digest)
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
	rw.Header().Set(oci.HeaderNamespace, dist.Registry)
	if dist.Method == http.MethodHead {
		rw.WriteHeader(http.StatusOK)
		return
	}

	rc, err := r.ociStore.Open(ctx, dist.Digest)
	if err != nil {
		respErr := oci.NewDistributionError(oci.ErrCodeManifestUnknown, fmt.Sprintf("could not get manifest %s", dist.Digest), nil)
		rw.WriteError(http.StatusNotFound, errors.Join(respErr, err))
		return
	}
	defer rc.Close()
	rw.WriteHeader(http.StatusOK)
	_, err = io.Copy(rw, rc)
	if err != nil {
		logr.FromContextOrDiscard(ctx).Error(err, "error occurred when writing manifest")
		return
	}
}

func (r *Registry) blobHandler(ctx context.Context, dist oci.DistributionPath, rw httpx.ResponseWriter) {
	rw.SetAttrs(HandlerAttrKey, "blob")

	desc, err := r.ociStore.Descriptor(ctx, dist.Digest)
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

	crng, err := func() (*httpx.ContentRange, error) {
		if dist.Range == nil {
			return nil, nil
		}
		crng, err := httpx.ContentRangeFromRange(*dist.Range, desc.Size)
		if err != nil {
			return nil, err
		}
		return &crng, nil
	}()
	if err != nil {
		rw.WriteError(http.StatusBadRequest, err)
		return
	}

	rw.Header().Set(oci.HeaderDockerDigest, dist.Digest.String())
	rw.Header().Set(oci.HeaderNamespace, dist.Registry)
	rw.Header().Set(httpx.HeaderAcceptRanges, httpx.RangeUnit)
	var status int
	if crng == nil {
		status = http.StatusOK
		rw.Header().Set(httpx.HeaderContentType, desc.MediaType)
		rw.Header().Set(httpx.HeaderContentLength, strconv.FormatInt(desc.Size, 10))
	} else {
		status = http.StatusPartialContent
		rw.Header().Set(httpx.HeaderContentType, httpx.ContentTypeBinary)
		rw.Header().Set(httpx.HeaderContentLength, strconv.FormatInt(crng.Length(), 10))
		rw.Header().Set(httpx.HeaderContentRange, crng.String())
	}
	if dist.Method == http.MethodHead {
		rw.WriteHeader(status)
		return
	}

	rc, err := r.ociStore.Open(ctx, dist.Digest)
	if err != nil {
		respErr := oci.NewDistributionError(oci.ErrCodeBlobUnknown, fmt.Sprintf("could not get reader for blob %s", dist.Digest), nil)
		rw.WriteError(http.StatusNotFound, errors.Join(respErr, err))
		return
	}
	defer rc.Close()
	var src io.Reader = rc
	if crng != nil {
		_, err := rc.Seek(crng.Start, io.SeekStart)
		if err != nil {
			rw.WriteError(http.StatusInternalServerError, err)
			return
		}
		src = io.LimitReader(rc, crng.Length())
	}
	rw.WriteHeader(status)
	_, err = io.Copy(rw, src)
	if err != nil {
		logr.FromContextOrDiscard(ctx).Error(err, "failed to write blob")
		return
	}
}
