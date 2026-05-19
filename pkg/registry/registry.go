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
	hedger         *resilient.Hedger
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
		hedger:         resilient.NewHedger([]float64{80, 85, 90}, 50*time.Millisecond),
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

func (r *Registry) mirrorHandler(ctx context.Context, dist oci.DistributionPath, rw httpx.ResponseWriter) {
	rw.SetAttrs(HandlerAttrKey, "mirror")

	log := logr.FromContextOrDiscard(ctx).WithValues("ref", dist.Identifier(), "path", dist.URL().Path)
	ctx = logr.NewContext(ctx, log)

	defer func() {
		if rw.Error() == nil {
			metrics.MirrorRequestsTotal.WithLabelValues(dist.Registry, "hit").Inc()
			metrics.MirrorLastSuccessTimestamp.SetToCurrentTime()
			r.stats.MirrorLastSuccess.Store(time.Now().Unix())
		} else {
			metrics.MirrorRequestsTotal.WithLabelValues(dist.Registry, "miss").Inc()
		}
	}()

	// Set max duration for non blob requests.
	if dist.Method == http.MethodHead || dist.Kind == oci.DistributionKindManifest {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
	}

	// Lookup peers for the given key.
	iter, err := r.router.Lookup(ctx, dist.Identifier(), r.resolveRetries)
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}

	// Retry requests until success or timeout.
	for {
		done := func() bool {
			res, err := r.raceFetch(ctx, iter, dist)
			if err != nil {
				rw.WriteError(http.StatusNotFound, err)
				return true
			}
			defer httpx.DrainAndClose(res.rc)

			if !rw.HeadersWritten() {
				oci.WriteDescriptorToHeader(res.desc, rw.Header())

				switch dist.Kind {
				case oci.DistributionKindManifest:
					rw.WriteHeader(http.StatusOK)
				case oci.DistributionKindBlob:
					rw.Header().Set(httpx.HeaderAcceptRanges, httpx.RangeUnit)
					if dist.Range == nil {
						rw.WriteHeader(http.StatusOK)
					} else {
						crng, err := httpx.ContentRangeFromRange(*dist.Range, res.desc.Size)
						if err != nil {
							rw.WriteError(http.StatusRequestedRangeNotSatisfiable, err)
							return true
						}
						rw.Header().Set(httpx.HeaderContentType, httpx.ContentTypeBinary)
						rw.Header().Set(httpx.HeaderContentRange, crng.String())
						rw.Header().Set(httpx.HeaderContentLength, strconv.FormatInt(crng.Length(), 10))
						rw.WriteHeader(http.StatusPartialContent)
					}
				}
			}
			if dist.Method == http.MethodHead {
				return true
			}

			// Copy the data to the response writer.
			//nolint: errcheck // Ignore
			buf := r.bufferPool.Get().(*[]byte)
			defer r.bufferPool.Put(buf)
			n, err := io.CopyBuffer(rw, res.rc, *buf)
			if err != nil {
				switch dist.Kind {
				case oci.DistributionKindManifest:
					log.Error(err, "copying of manifest data failed")
					return true
				case oci.DistributionKindBlob:
					dist = dist.Clone()
					if dist.Range == nil {
						dist.Range = &httpx.Range{
							Start: ptr.To(int64(0)),
							End:   ptr.To(res.desc.Size - 1),
						}
					}
					dist.Range.Start = ptr.To(*dist.Range.Start + n)
					log.Error(err, "copying of blob data failed")
					return false
				}
			}
			return true
		}()
		if done {
			return
		}
	}
}

type mirrorErrorDetails struct {
	Attempts int `json:"attempts"`
}

type fetchResponse struct {
	rc   io.ReadCloser
	desc ocispec.Descriptor
	peer routing.Peer
}

type fetchFailure struct {
	err  error
	peer routing.Peer
}

func (r *Registry) raceFetch(ctx context.Context, iterator *routing.Iterator, dist oci.DistributionPath) (fetchResponse, error) {
	log := logr.FromContextOrDiscard(ctx)

	errDetails := mirrorErrorDetails{
		Attempts: 0,
	}
	errCode := map[oci.DistributionKind]oci.DistributionErrorCode{
		oci.DistributionKindBlob:     oci.ErrCodeBlobUnknown,
		oci.DistributionKindManifest: oci.ErrCodeManifestUnknown,
	}[dist.Kind]

	fetchCh, immediateCh := fetchChannel(ctx, r.hedger, iterator)
	resCh := make(chan fetchResponse)
	failureCh := make(chan fetchFailure)

	fetchCtxs := map[string]context.Context{}
	fetchCancels := map[string]context.CancelFunc{}
	defer func() {
		for _, cancel := range fetchCancels {
			cancel()
		}
	}()

	for {
		// We only want to return early when there are no inflight requests.
		var idleTimeoutCh <-chan time.Time
		var exhaustedCh <-chan any
		if len(fetchCtxs) == 0 {
			idleTimeoutCh = time.After(r.resolveTimeout)
			exhaustedCh = iterator.Exhausted()
		}

		select {
		case <-ctx.Done():
			return fetchResponse{}, ctx.Err()
		case <-exhaustedCh:
			if errDetails.Attempts == 0 {
				return fetchResponse{}, oci.NewDistributionError(errCode, fmt.Sprintf("could not find peer for %s", dist.Identifier()), errDetails)
			}
			return fetchResponse{}, oci.NewDistributionError(errCode, fmt.Sprintf("all request retries exhausted for %s", dist.Identifier()), errDetails)
		case <-idleTimeoutCh:
			return fetchResponse{}, oci.NewDistributionError(errCode, fmt.Sprintf("waited too long for new peer with no inflight fetches for %s", dist.Identifier()), errDetails)
		case <-fetchCh:
			peer, ok := iterator.Acquire()
			if !ok {
				immediateCh <- false
				continue
			}

			errDetails.Attempts += 1

			fetchCtx, fetchCancel := context.WithCancel(ctx)
			fetchCtxs[peer.Host] = fetchCtx
			fetchCancels[peer.Host] = fetchCancel

			go func() {
				start := time.Now()
				res, err := httpx.HappyEyeballs(fetchCtx, peer.Addresses, func(ctx context.Context, ipAddr netip.Addr) (fetchResponse, error) {
					mirror := &url.URL{
						Scheme: dist.Scheme,
						Host:   netip.AddrPortFrom(ipAddr, peer.Metadata.RegistryPort).String(),
					}
					fetchOpts := []oci.FetchOption{
						oci.WithFetchHeader(HeaderSpegelMirrored, "true"),
						oci.WithFetchMirror(mirror),
						oci.WithFetchBasicAuth(r.username, r.password),
					}
					rc, desc, err := r.ociClient.Fetch(ctx, dist, fetchOpts...)
					if err != nil {
						return fetchResponse{}, err
					}
					res := fetchResponse{
						peer: peer,
						desc: desc,
						rc:   rc,
					}
					return res, nil
				})
				if err != nil {
					if fetchCtx.Err() != nil {
						iterator.Release(peer)
						return
					}

					iterator.Remove(peer)

					failure := fetchFailure{
						peer: peer,
						err:  err,
					}
					select {
					case <-fetchCtx.Done():
					case failureCh <- failure:
					}
					return
				}

				iterator.Release(peer)

				err = r.hedger.Observe(time.Since(start))
				if err != nil {
					log.Error(err, "could not observe fetch duration for hedger")
				}

				select {
				case <-fetchCtx.Done():
					err = httpx.DrainAndClose(res.rc)
					if err != nil {
						log.Error(err, "could not drain and close")
					}
				case resCh <- res:
				}
			}()
		case failure := <-failureCh:
			// Remove context to indicate fetch is not inflight.
			log.Error(failure.err, "request to peer failed")
			delete(fetchCtxs, failure.peer.Host)
			delete(fetchCancels, failure.peer.Host)
			immediateCh <- true
		case res := <-resCh:
			// Remove context so successful request is not cancelled.
			delete(fetchCtxs, res.peer.Host)
			delete(fetchCancels, res.peer.Host)
			return res, nil
		}
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

func fetchChannel(ctx context.Context, hedger *resilient.Hedger, iterator *routing.Iterator) (<-chan any, chan<- bool) {
	fetchCh := make(chan any)
	immediateCh := make(chan bool, hedger.Size()+1)
	immediateCh <- false
	go func() {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		hedgeCount := 0
		hedgeCh := hedger.Channel(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-hedgeCh:
				hedgeCount += 1
			case count := <-immediateCh:
				if count {
					hedgeCount += 1
				}
			}

			select {
			case <-ctx.Done():
				return
			case <-iterator.Ready():
			}

			select {
			case <-ctx.Done():
				return
			case fetchCh <- nil:
			}

			// We dont want to trigger fetch more than want hedger would.
			if hedgeCount == hedger.Size() {
				return
			}
		}
	}()
	return fetchCh, immediateCh
}
