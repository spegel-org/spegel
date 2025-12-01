package web

import (
	"context"
	"embed"
	"errors"
	"html/template"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
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
		"join":           strings.Join,
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
	m.Handle("GET /debug/web/stats", w.statsHandler)
	m.Handle("GET /debug/web/measure", w.measureHandler)
	return m
}

func (w *Web) indexHandler(rw httpx.ResponseWriter, req *http.Request) {
	err := w.tmpls.ExecuteTemplate(rw, "index.html", nil)
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}
}

func (w *Web) statsHandler(rw httpx.ResponseWriter, req *http.Request) {
	data := struct {
		LocalAddresses    []string
		Images            []oci.Image
		Peers             []routing.Peer
		MirrorLastSuccess time.Duration
	}{}

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

	data.LocalAddresses = w.router.LocalAddresses()
	peers, err := w.router.ListPeers()
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}
	data.Peers = peers

	err = w.tmpls.ExecuteTemplate(rw, "stats.html", data)
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}
}

type measureResult struct {
	LookupResults []lookupResult
	PullResults   []pullResult
	PeerDuration  time.Duration
	PullDuration  time.Duration
	PullSize      int64
}

type lookupResult struct {
	Peer     netip.AddrPort
	Duration time.Duration
}

type pullResult struct {
	Identifier string
	Type       string
	Size       int64
	Duration   time.Duration
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

	// Lookup peers for the given image.
	lookupStart := time.Now()
	lookupCtx, lookupCancel := context.WithTimeout(req.Context(), 1*time.Second)
	defer lookupCancel()
	rr, err := w.router.Lookup(lookupCtx, img.Identifier(), 0)
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, NewHTMLResponseError(err))
		return
	}
	for {
		peer, err := rr.Next()
		if err != nil {
			break
		}

		// TODO(phillebaba): This isnt a great solution as removing the peers will affect caching.
		rr.Remove(peer)

		d := time.Since(lookupStart)
		res.PeerDuration += d
		res.LookupResults = append(res.LookupResults, lookupResult{
			Peer:     peer,
			Duration: d,
		})
	}

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

	err = w.tmpls.ExecuteTemplate(rw, "measure.html", res)
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, NewHTMLResponseError(err))
		return
	}
}
