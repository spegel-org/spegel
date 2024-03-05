package registry

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-logr/logr"
	"github.com/opencontainers/go-digest"
	pkggin "github.com/xenitab/pkg/gin"

	"github.com/xenitab/spegel/pkg/metrics"
	"github.com/xenitab/spegel/pkg/oci"
	"github.com/xenitab/spegel/pkg/routing"
	"github.com/xenitab/spegel/pkg/throttle"
)

const (
	MirroredHeaderKey = "X-Spegel-Mirrored"
)

type Option func(*Registry)

func WithResolveRetries(resolveRetries int) Option {
	return func(r *Registry) {
		r.resolveRetries = resolveRetries
	}
}

func WithResolveLatestTag(resolveLatestTag bool) Option {
	return func(r *Registry) {
		r.resolveLatestTag = resolveLatestTag
	}
}

func WithBlobCopyBuffer(blobCopyBuffer int) Option {
	return func(r *Registry) {
		r.blobCopyBuffer = blobCopyBuffer
	}
}

func WithResolveTimeout(resolveTimeout time.Duration) Option {
	return func(r *Registry) {
		r.resolveTimeout = resolveTimeout
	}
}

func WithTransport(transport http.RoundTripper) Option {
	return func(r *Registry) {
		r.transport = transport
	}
}

func WithLocalAddress(localAddr string) Option {
	return func(r *Registry) {
		r.localAddr = localAddr
	}
}

func WithBlobSpeed(blobSpeed throttle.Byterate) Option {
	return func(r *Registry) {
		r.throttler = throttle.NewThrottler(blobSpeed)
	}
}

type Registry struct {
	throttler        *throttle.Throttler
	ociClient        oci.Client
	router           routing.Router
	transport        http.RoundTripper
	localAddr        string
	resolveRetries   int
	resolveTimeout   time.Duration
	resolveLatestTag bool
	blobCopyBuffer   int
}

func NewRegistry(ociClient oci.Client, router routing.Router, opts ...Option) *Registry {
	r := &Registry{
		ociClient:        ociClient,
		router:           router,
		resolveRetries:   3,
		resolveTimeout:   1 * time.Second,
		resolveLatestTag: true,
		blobCopyBuffer:   32768,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

func (r *Registry) Server(addr string, log logr.Logger) *http.Server {
	cfg := pkggin.Config{
		LogConfig: pkggin.LogConfig{
			Logger:          log,
			PathFilter:      regexp.MustCompile("/healthz"),
			IncludeLatency:  true,
			IncludeClientIP: true,
			IncludeKeys:     []string{"handler"},
		},
		MetricsConfig: pkggin.MetricsConfig{
			HandlerID: "registry",
		},
	}
	engine := pkggin.NewEngine(cfg)
	engine.GET("/healthz", r.readyHandler)
	engine.Any("/v2/*params", r.metricsHandler, r.registryHandler)
	srv := &http.Server{
		Addr:    addr,
		Handler: engine,
	}
	return srv
}

func (r *Registry) readyHandler(c *gin.Context) {
	ok, err := r.router.Ready()
	if err != nil {
		//nolint:errcheck // ignore
		c.AbortWithError(http.StatusInternalServerError, err)
		return
	}
	if !ok {
		c.Status(http.StatusInternalServerError)
		return

	}
	c.Status(http.StatusOK)
}

func (r *Registry) registryHandler(c *gin.Context) {
	// Only deal with GET and HEAD requests.
	if !(c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead) {
		c.Status(http.StatusNotFound)
		return
	}
	// Quickly return 200 for /v2/ to indicate that registry supports v2.
	if path.Clean(c.Request.URL.Path) == "/v2" {
		if c.Request.Method != http.MethodGet {
			c.Status(http.StatusNotFound)
			return
		}
		c.Status(http.StatusOK)
		return
	}

	// Parse out path components from request.
	ref, dgst, refType, err := parsePathComponents(c.Query("ns"), c.Request.URL.Path)
	if err != nil {
		//nolint:errcheck // ignore
		c.AbortWithError(http.StatusNotFound, err)
		return
	}

	if !r.resolveLatestTag && ref != "" {
		_, tag, _ := strings.Cut(ref, ":")
		if tag == "latest" {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
	}

	// Request with mirror header are proxied.
	if c.Request.Header.Get(MirroredHeaderKey) != "true" {
		// Set mirrored header in request to stop infinite loops
		c.Request.Header.Set(MirroredHeaderKey, "true")

		key := dgst.String()
		if key == "" {
			key = ref
		}
		r.handleMirror(c, key)
		return
	}

	// Serve registry endpoints.
	if dgst == "" {
		dgst, err = r.ociClient.Resolve(c, ref)
		if err != nil {
			//nolint:errcheck // ignore
			c.AbortWithError(http.StatusNotFound, err)
			return
		}
	}
	switch refType {
	case referenceTypeManifest:
		r.handleManifest(c, dgst)
		return
	case referenceTypeBlob:
		r.handleBlob(c, dgst)
		return
	}

	// If nothing matches return 404.
	c.Status(http.StatusNotFound)
}

func (r *Registry) handleMirror(c *gin.Context, key string) {
	c.Set("handler", "mirror")

	log := pkggin.FromContextOrDiscard(c).WithValues("key", key, "path", c.Request.URL.Path, "ip", c.RemoteIP())

	// Resolve mirror with the requested key
	resolveCtx, cancel := context.WithTimeout(c, r.resolveTimeout)
	defer cancel()
	resolveCtx = logr.NewContext(resolveCtx, log)
	isExternal := r.isExternalRequest(c)
	if isExternal {
		log.Info("handling mirror request from external node")
	}
	peerCh, err := r.router.Resolve(resolveCtx, key, isExternal, r.resolveRetries)
	if err != nil {
		//nolint:errcheck // ignore
		c.AbortWithError(http.StatusInternalServerError, err)
		return
	}
	// TODO: Refactor context cancel and mirror channel closing
	for {
		select {
		case <-resolveCtx.Done():
			// Request has been closed by server or client. No use continuing.
			//nolint:errcheck // ignore
			c.AbortWithError(http.StatusNotFound, fmt.Errorf("request closed for key: %s", key))
			return
		case ipAddr, ok := <-peerCh:
			// Channel closed means no more mirrors will be received and max retries has been reached.
			if !ok {
				// TODO: Change to a 404 instead
				//nolint:errcheck // ignore
				c.AbortWithError(http.StatusInternalServerError, fmt.Errorf("mirror resolve retries exhausted for key: %s", key))
				return
			}

			// Modify response returns and error on non 200 status code and NOP error handler skips response writing.
			// If proxy fails no response is written and it is tried again against a different mirror.
			// If the response writer has been written to it means that the request was properly proxied.
			succeeded := false
			scheme := "http"
			if c.Request.TLS != nil {
				scheme = "https"
			}
			u := &url.URL{
				Scheme: scheme,
				Host:   ipAddr.String(),
			}
			proxy := httputil.NewSingleHostReverseProxy(u)
			proxy.Transport = r.transport
			proxy.ErrorHandler = func(_ http.ResponseWriter, _ *http.Request, err error) {
				log.Error(err, "proxy failed attempting next")
			}
			proxy.ModifyResponse = func(resp *http.Response) error {
				if resp.StatusCode != http.StatusOK {
					err := fmt.Errorf("expected mirror to respond with 200 OK but received: %s", resp.Status)
					log.Error(err, "mirror failed attempting next")
					return err
				}
				succeeded = true
				return nil
			}
			proxy.ServeHTTP(c.Writer, c.Request)
			if !succeeded {
				break
			}
			log.V(5).Info("mirrored request", "path", c.Request.URL.Path, "url", u.String())
			return
		}
	}
}

func (r *Registry) handleManifest(c *gin.Context, dgst digest.Digest) {
	c.Set("handler", "manifest")
	b, mediaType, err := r.ociClient.GetManifest(c, dgst)
	if err != nil {
		//nolint:errcheck // ignore
		c.AbortWithError(http.StatusNotFound, err)
		return
	}
	c.Header("Content-Type", mediaType)
	c.Header("Content-Length", strconv.FormatInt(int64(len(b)), 10))
	c.Header("Docker-Content-Digest", dgst.String())
	if c.Request.Method == http.MethodHead {
		return
	}
	_, err = c.Writer.Write(b)
	if err != nil {
		//nolint:errcheck // ignore
		c.AbortWithError(http.StatusNotFound, err)
		return
	}
}

func (r *Registry) handleBlob(c *gin.Context, dgst digest.Digest) {
	c.Set("handler", "blob")
	size, err := r.ociClient.Size(c, dgst)
	if err != nil {
		//nolint:errcheck // ignore
		c.AbortWithError(http.StatusInternalServerError, err)
		return
	}
	c.Header("Content-Length", strconv.FormatInt(size, 10))
	c.Header("Docker-Content-Digest", dgst.String())
	if c.Request.Method == http.MethodHead {
		return
	}
	var writer io.Writer = c.Writer
	if r.throttler != nil {
		writer = r.throttler.Writer(c.Writer)
	}
	err = r.ociClient.CopyLayer(c, dgst, writer, r.blobCopyBuffer)
	if err != nil {
		//nolint:errcheck // ignore
		c.AbortWithError(http.StatusInternalServerError, err)
		return
	}
}

func (r *Registry) metricsHandler(c *gin.Context) {
	c.Next()
	handler, ok := c.Get("handler")
	if !ok {
		return
	}
	if handler != "mirror" {
		return
	}
	sourceType := "internal"
	if r.isExternalRequest(c) {
		sourceType = "external"
	}
	cacheType := "hit"
	if c.Writer.Status() != http.StatusOK {
		cacheType = "miss"
	}
	metrics.MirrorRequestsTotal.WithLabelValues(c.Query("ns"), cacheType, sourceType).Inc()
}

func (r *Registry) isExternalRequest(c *gin.Context) bool {
	return c.Request.Host != r.localAddr
}
