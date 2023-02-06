package registry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"time"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/reference"
	"github.com/gin-gonic/gin"
	"github.com/go-logr/logr"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/valyala/fastjson"
	pkggin "github.com/xenitab/pkg/gin"

	"github.com/xenitab/spegel/internal/routing"
)

type Registry struct {
	srv *http.Server
}

func NewRegistry(ctx context.Context, addr string, containerdClient *containerd.Client, router routing.Router) (*Registry, error) {
	_, registryPort, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	log := logr.FromContextOrDiscard(ctx)

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
	registryHandler := &RegistryHandler{
		log:              log,
		registryPort:     registryPort,
		containerdClient: containerdClient,
		router:           router,
	}
	engine.GET("/healthz", registryHandler.readyHandler)
	engine.Any("/v2/*params", registryHandler.registryHandler)
	srv := &http.Server{
		Addr:    addr,
		Handler: engine,
	}
	return &Registry{
		srv: srv,
	}, nil
}

func (r *Registry) ListenAndServe(ctx context.Context) error {
	if err := r.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (r *Registry) Shutdown() error {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return r.srv.Shutdown(shutdownCtx)
}

type RegistryHandler struct {
	log              logr.Logger
	registryPort     string
	containerdClient *containerd.Client
	router           routing.Router
}

func (r *RegistryHandler) readyHandler(c *gin.Context) {
	c.Status(http.StatusOK)
}

// TODO: Explore using leases to make sure resources are not deleted mid request.
// https://github.com/containerd/containerd/blob/main/docs/garbage-collection.md
func (r *RegistryHandler) registryHandler(c *gin.Context) {
	// Only deal with GET and HEAD requests.
	if !(c.Request.Method == "GET" || c.Request.Method == "HEAD") {
		c.Status(http.StatusNotFound)
		return
	}

	// Quickly return 200 for /v2/ to indicate that registry supports v2.
	if path.Clean(c.Request.URL.Path) == "/v2" {
		if c.Request.Method != "GET" {
			c.Status(http.StatusNotFound)
			return
		}
		c.Status(http.StatusOK)
		return
	}

	// Always expect remoteRegistry header to be passed in request.
	remoteRegistry, err := getRemoteRegistry(c.Request.Header)
	if err != nil {
		//nolint:errcheck // ignore
		c.AbortWithError(http.StatusNotFound, err)
		return
	}

	// Requests coming from localhost are meant to be mirrored.
	if isMirrorRequest(c.Request.Header) {
		r.handleMirror(c, remoteRegistry, r.registryPort)
		return
	}

	// Serve registry endpoints.
	ref, ok, err := ManifestReference(remoteRegistry, c.Request.URL.Path)
	if err != nil {
		//nolint:errcheck // ignore
		c.AbortWithError(http.StatusNotFound, err)
		return
	}
	if ok {
		r.handleManifest(c, ref)
		return
	}
	ref, ok, err = BlobReference(remoteRegistry, c.Request.URL.Path)
	if err != nil {
		//nolint:errcheck // ignore
		c.AbortWithError(http.StatusNotFound, err)
		return
	}
	if ok {
		r.handleBlob(c, ref)
		return
	}

	// If nothing matches return 404.
	c.Status(http.StatusNotFound)
}

// TODO: Retry multiple endoints
func (r *RegistryHandler) handleMirror(c *gin.Context, remoteRegistry, registryPort string) {
	c.Set("handler", "mirror")

	// Disable mirroring so we dont end with an infinite loop
	c.Request.Header[MirrorHeader] = []string{"false"}

	ref, ok, err := AnyReference(remoteRegistry, c.Request.URL.Path)
	if err != nil {
		//nolint:errcheck // ignore
		c.AbortWithError(http.StatusNotFound, err)
		return
	}
	if !ok {
		//nolint:errcheck // ignore
		c.AbortWithError(http.StatusNotFound, fmt.Errorf("could not parse reference"))
		return
	}

	key := ref.Digest().String()
	if key == "" {
		key = ref.String()
	}
	timeoutCtx, cancel := context.WithTimeout(c, 5*time.Second)
	defer cancel()
	ip, ok, err := r.router.Resolve(timeoutCtx, key)
	if err != nil {
		//nolint:errcheck // ignore
		c.AbortWithError(http.StatusNotFound, err)
		return
	}
	if !ok {
		//nolint:errcheck // ignore
		c.AbortWithError(http.StatusNotFound, fmt.Errorf("could not find node with ref: %s", ref.String()))
		return
	}
	url, err := url.Parse(fmt.Sprintf("http://%s:%s", ip, registryPort))
	if err != nil {
		//nolint:errcheck // ignore
		c.AbortWithError(http.StatusNotFound, err)
		return
	}
	r.log.V(5).Info("forwarding request", "path", c.Request.URL.Path, "url", url.String())
	proxy := httputil.NewSingleHostReverseProxy(url)
	proxy.ServeHTTP(c.Writer, c.Request)
}

func (r *RegistryHandler) handleManifest(c *gin.Context, ref reference.Spec) {
	c.Set("handler", "manifest")

	dgst := ref.Digest()
	// Reference is tag so need to resolve digest
	if dgst == "" {
		image, err := r.containerdClient.ImageService().Get(c, ref.String())
		if err != nil {
			//nolint:errcheck // ignore
			c.AbortWithError(http.StatusNotFound, err)
			return
		}
		dgst = image.Target.Digest
	}
	info, err := r.containerdClient.ContentStore().Info(c, dgst)
	if err != nil {
		//nolint:errcheck // ignore
		c.AbortWithError(http.StatusNotFound, err)
		return
	}
	b, err := content.ReadBlob(c, r.containerdClient.ContentStore(), ocispec.Descriptor{Digest: info.Digest})
	if err != nil {
		//nolint:errcheck // ignore
		c.AbortWithError(http.StatusNotFound, err)
		return
	}
	mediaType := fastjson.GetString(b, "mediaType")
	if mediaType == "" {
		//nolint:errcheck // ignore
		c.AbortWithError(http.StatusNotFound, fmt.Errorf("could not find media type in manifest %s", dgst))
		return
	}
	c.Header("Content-Type", mediaType)
	c.Header("Content-Length", strconv.FormatInt(info.Size, 10))
	c.Header("Docker-Content-Digest", info.Digest.String())
	if c.Request.Method == "HEAD" {
		c.Status(http.StatusOK)
		return
	}
	_, err = c.Writer.Write(b)
	if err != nil {
		//nolint:errcheck // ignore
		c.AbortWithError(http.StatusNotFound, err)
		return
	}
	c.Status(http.StatusOK)
}

func (r *RegistryHandler) handleBlob(c *gin.Context, ref reference.Spec) {
	c.Set("handler", "blob")

	info, err := r.containerdClient.ContentStore().Info(c, ref.Digest())
	if err != nil {
		//nolint:errcheck // ignore
		c.AbortWithError(http.StatusNotFound, err)
		return
	}
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Length", strconv.FormatInt(info.Size, 10))
	c.Header("Docker-Content-Digest", ref.Digest().String())
	if c.Request.Method == "HEAD" {
		c.Status(http.StatusOK)
		return
	}
	ra, err := r.containerdClient.ContentStore().ReaderAt(c, ocispec.Descriptor{Digest: info.Digest})
	if err != nil {
		//nolint:errcheck // ignore
		c.AbortWithError(http.StatusNotFound, err)
		return
	}
	defer ra.Close()
	_, err = io.Copy(c.Writer, content.NewReader(ra))
	if err != nil {
		//nolint:errcheck // ignore
		c.AbortWithError(http.StatusNotFound, err)
		return
	}
	c.Status(http.StatusOK)
}
