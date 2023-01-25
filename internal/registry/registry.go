package registry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
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

	"github.com/xenitab/spegel/internal/store"
)

type Registry struct {
	srv *http.Server
}

func NewRegistry(ctx context.Context, addr string, containerdClient *containerd.Client, store store.Store) (*Registry, error) {
	_, registryPort, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	log := logr.FromContextOrDiscard(ctx)

	router := pkggin.Default(log, "registry")
	registryHandler := &RegistryHandler{
		log:              log,
		registryPort:     registryPort,
		containerdClient: containerdClient,
		store:            store,
	}
	router.GET("/healthz", registryHandler.readyHandler)
	router.GET("/debug", registryHandler.debugHandler)
	router.Any("/v2/*params", registryHandler.registryHandler)
	srv := &http.Server{
		Addr:    addr,
		Handler: router,
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
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return r.srv.Shutdown(shutdownCtx)
}

type RegistryHandler struct {
	log              logr.Logger
	registryPort     string
	containerdClient *containerd.Client
	store            store.Store
}

func (r *RegistryHandler) readyHandler(c *gin.Context) {
	c.Status(http.StatusOK)
}

func (r *RegistryHandler) debugHandler(c *gin.Context) {
	data, err := r.store.Dump(c)
	if err != nil {
		c.AbortWithError(http.StatusInternalServerError, err)
	}
	c.JSON(http.StatusOK, data)
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
		c.AbortWithError(http.StatusNotFound, err)
		return
	}

	// Requests coming from localhost are meant to be mirrored.
	if isMirrorRequest(c.Request.Header) {
		r.handleMirror(c, remoteRegistry, r.registryPort)
		return
	}

	// Serve registry endpoints.
	r.log.Info("serving registry request", "path", c.Request.URL.Path)
	ref, ok, err := ManifestReference(remoteRegistry, c.Request.URL.Path)
	if err != nil {
		c.AbortWithError(http.StatusNotFound, err)
		return
	}
	if ok {
		r.handleManifest(c, ref)
		return
	}
	ref, ok, err = BlobReference(remoteRegistry, c.Request.URL.Path)
	if err != nil {
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
	// Disable mirroring so we dont end with an infinite loop
	c.Set(MirrorHeader, "false")

	ref, ok, err := AnyReference(remoteRegistry, c.Request.URL.Path)
	if err != nil {
		c.AbortWithError(http.StatusNotFound, err)
		return
	}
	if !ok {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	key := ref.Digest().String()
	if key == "" {
		key = ref.String()
	}
	ips, err := r.store.Get(logr.NewContext(c, r.log), key)
	if err != nil {
		c.AbortWithError(http.StatusNotFound, err)
		return
	}
	if len(ips) == 0 {
		r.log.Info("could not find node to forward", "ref", ref.String())
		c.Status(http.StatusNotFound)
		return
	}
	idx := rand.Intn(len(ips))
	url, err := url.Parse(fmt.Sprintf("http://%s:%s", ips[idx], registryPort))
	if err != nil {
		c.AbortWithError(http.StatusNotFound, err)
		return
	}
	r.log.Info("forwarding request", "path", c.Request.URL.Path, "url", url.String())
	proxy := httputil.NewSingleHostReverseProxy(url)
	proxy.ServeHTTP(c.Writer, c.Request)
}

func (r *RegistryHandler) handleManifest(c *gin.Context, ref reference.Spec) {
	dgst := ref.Digest()
	// Reference is tag so need to resolve digest
	if dgst == "" {
		image, err := r.containerdClient.ImageService().Get(c, ref.String())
		if err != nil {
			c.AbortWithError(http.StatusNotFound, err)
			return
		}
		dgst = image.Target.Digest
	}
	info, err := r.containerdClient.ContentStore().Info(c, dgst)
	if err != nil {
		c.AbortWithError(http.StatusNotFound, err)
		return
	}
	b, err := content.ReadBlob(c, r.containerdClient.ContentStore(), ocispec.Descriptor{Digest: info.Digest})
	if err != nil {
		c.AbortWithError(http.StatusNotFound, err)
		return
	}
	mediaType := fastjson.GetString(b, "mediaType")
	if mediaType == "" {
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
		c.AbortWithError(http.StatusNotFound, err)
		return
	}
	c.Status(http.StatusOK)
}

func (r *RegistryHandler) handleBlob(c *gin.Context, ref reference.Spec) {
	info, err := r.containerdClient.ContentStore().Info(c, ref.Digest())
	if err != nil {
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
		c.AbortWithError(http.StatusNotFound, err)
		return
	}
	defer ra.Close()
	_, err = io.Copy(c.Writer, content.NewReader(ra))
	if err != nil {
		c.AbortWithError(http.StatusNotFound, err)
		return
	}
	c.Status(http.StatusOK)
}
