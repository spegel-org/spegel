package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/netip"
	"net/url"
	"time"

	"github.com/go-logr/logr"

	"github.com/spegel-org/spegel/internal/option"
	"github.com/spegel-org/spegel/pkg/httpx"
	"github.com/spegel-org/spegel/pkg/oci"
	"github.com/spegel-org/spegel/pkg/registry"
	"github.com/spegel-org/spegel/pkg/routing"
)

//go:embed templates/*
var templatesFS embed.FS

type WebConfig struct {
	OCIClient *oci.Client
	Filters   []oci.Filter
}

type WebOption = option.Option[WebConfig]

func WithOCIClient(ociClient *oci.Client) WebOption {
	return func(cfg *WebConfig) error {
		cfg.OCIClient = ociClient
		return nil
	}
}

func WithRegistryFilters(filters []oci.Filter) WebOption {
	return func(cfg *WebConfig) error {
		cfg.Filters = filters
		return nil
	}
}

type Web struct {
	mirror    *url.URL
	router    *routing.P2PRouter
	ociClient *oci.Client
	ociStore  oci.Store
	tmpls     *template.Template
	reg       *registry.Registry
	filters   []oci.Filter
}

func NewWeb(router *routing.P2PRouter, ociStore oci.Store, reg *registry.Registry, mirror *url.URL, opts ...WebOption) (*Web, error) {
	cfg := WebConfig{}
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

	funcs := template.FuncMap{
		"join":           joinStrings,
		"formatBytes":    formatBytes,
		"formatDuration": formatDuration,
	}
	tmpls, err := template.New("").Funcs(funcs).ParseFS(templatesFS, "templates/*")
	if err != nil {
		return nil, err
	}
	return &Web{
		router:    router,
		ociClient: cfg.OCIClient,
		ociStore:  ociStore,
		filters:   cfg.Filters,
		tmpls:     tmpls,
		reg:       reg,
		mirror:    mirror,
	}, nil
}

func (w *Web) Handler(log logr.Logger) http.Handler {
	m := httpx.NewServeMux(log)
	m.Handle("GET /debug/web/", w.indexHandler)
	m.Handle("GET /debug/web/metadata", w.metaDataHandler)
	m.Handle("GET /debug/web/stats", w.statsHandler)
	m.Handle("GET /debug/web/measure", w.measureHandler)
	return m
}

func (w *Web) indexHandler(rw httpx.ResponseWriter, req *http.Request) {
	httpx.RenderTemplate(rw, w.tmpls.Lookup("index.html"), nil)
}

type LibP2P struct {
	ID string `json:"id"`
}

type Metadata struct {
	LibP2P LibP2P `json:"libp2p"`
}

func (w *Web) metaDataHandler(rw httpx.ResponseWriter, req *http.Request) {
	data := Metadata{
		LibP2P{
			ID: w.router.Host().ID().String(),
		},
	}
	rw.Header().Set(httpx.HeaderContentType, httpx.ContentTypeJSON)
	err := json.NewEncoder(rw).Encode(data)
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}
}

type statsData struct {
	LocalAddresses    []netip.Addr
	Images            []oci.Image
	Peers             []routing.Peer
	MirrorLastSuccess time.Duration
}

func (w *Web) statsHandler(rw httpx.ResponseWriter, req *http.Request) {
	data := statsData{}

	images, err := w.ociStore.ListImages(req.Context())
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}
	for _, img := range images {
		if oci.MatchesFilter(img.Reference, w.filters) {
			continue
		}
		data.Images = append(data.Images, img)
	}

	stats := w.reg.Stats()
	mirrorLastSuccess := stats.MirrorLastSuccess.Load()
	if mirrorLastSuccess > 0 {
		data.MirrorLastSuccess = time.Since(time.Unix(mirrorLastSuccess, 0))
	}

	localAddrs, err := w.router.LocalAddresses()
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}
	data.LocalAddresses = localAddrs
	peers, err := w.router.ListPeers()
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}
	data.Peers = peers

	httpx.RenderTemplate(rw, w.tmpls.Lookup("stats.html"), data)
}

type pullResult struct {
	Identifier string
	Type       string
	Size       int64
	Duration   time.Duration
}

type measureResult struct {
	LookupResults []routing.LookupResult
	PullResults   []pullResult
	PeerDuration  time.Duration
	PullDuration  time.Duration
	PullSize      int64
}

func (w *Web) measureHandler(rw httpx.ResponseWriter, req *http.Request) {
	// Parse image name.
	imgName := req.URL.Query().Get("image")
	if imgName == "" {
		rw.WriteError(http.StatusBadRequest, NewHTMLResponseError(errors.New("image name cannot be empty")))
		return
	}
	img, err := oci.ParseImage(imgName, oci.AllowDefaults(), oci.AllowTagOnly())
	if err != nil {
		rw.WriteError(http.StatusBadRequest, NewHTMLResponseError(err))
		return
	}

	res := measureResult{}
	lookupCtx, lookupCancel := context.WithTimeout(req.Context(), 3*time.Second)
	defer lookupCancel()
	lookupRes, err := w.router.Measure(lookupCtx, img.Identifier())
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, NewHTMLResponseError(err))
	}
	res.LookupResults = lookupRes

	if len(res.LookupResults) > 0 {
		// Pull the image and measure performance.
		pullMetrics, err := w.ociClient.Pull(req.Context(), img, oci.WithPullMirror(w.mirror))
		if err != nil {
			rw.WriteError(http.StatusInternalServerError, NewHTMLResponseError(err))
			return
		}
		for _, metric := range pullMetrics {
			res.PullDuration += metric.Duration
			res.PullSize += metric.ContentLength
			res.PullResults = append(res.PullResults, pullResult{
				Identifier: metric.Digest.String(),
				Type:       metric.ContentType,
				Size:       metric.ContentLength,
				Duration:   metric.Duration,
			})
		}
	}

	httpx.RenderTemplate(rw, w.tmpls.Lookup("measure.html"), res)
}
