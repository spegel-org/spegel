package web

import (
	"embed"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/common/expfmt"

	"github.com/spegel-org/spegel/pkg/httpx"
	"github.com/spegel-org/spegel/pkg/oci"
	"github.com/spegel-org/spegel/pkg/routing"
)

//go:embed templates/*
var templatesFS embed.FS

type Web struct {
	router     routing.Router
	log        logr.Logger
	ociClient  *oci.Client
	httpClient *http.Client
	transport  http.RoundTripper
	tmpls      *template.Template
	addr       string
}

type WebOption func(*Web) error

func WithLogger(log logr.Logger) WebOption {
	return func(w *Web) error {
		w.log = log
		return nil
	}
}

func WithTransport(transport http.RoundTripper) WebOption {
	return func(w *Web) error {
		w.transport = transport
		return nil
	}
}

func WithAddress(addr string) WebOption {
	return func(w *Web) error {
		w.addr = addr
		return nil
	}
}

func NewWeb(router routing.Router, opts ...WebOption) (*Web, error) {
	funcs := template.FuncMap{
		"formatBytes":    formatBytes,
		"formatDuration": formatDuration,
	}
	tmpls, err := template.New("").Funcs(funcs).ParseFS(templatesFS, "templates/*")
	if err != nil {
		return nil, err
	}
	w := &Web{
		router: router,
		log:    logr.Discard(),
		tmpls:  tmpls,
	}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(w); err != nil {
			return nil, err
		}
	}

	httpClient := &http.Client{}
	if w.transport != nil {
		httpClient.Transport = w.transport
	} else {
		httpClient.Transport = httpx.BaseTransport()
	}

	w.httpClient = httpClient
	w.ociClient = oci.NewClient(httpClient)

	return w, nil
}

func (w *Web) Handler() http.Handler {
	m := httpx.NewServeMux(w.log)
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
	req, err := http.NewRequestWithContext(req.Context(), http.MethodGet, w.baseURL(req)+"/metrics", nil)
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}
	resp, err := w.httpClient.Do(req)
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}
	defer httpx.DrainAndClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		rw.WriteError(http.StatusInternalServerError, fmt.Errorf("invalid metrics response status %s", resp.Status))
		return
	}
	parser := expfmt.TextParser{}
	metricFamilies, err := parser.TextToMetricFamilies(resp.Body)
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}

	data := struct {
		ImageCount int64
		LayerCount int64
	}{}
	if mf, ok := metricFamilies["spegel_advertised_images"]; ok {
		for _, metric := range mf.Metric {
			data.ImageCount += int64(*metric.Gauge.Value)
		}
	}
	if mf, ok := metricFamilies["spegel_advertised_keys"]; ok {
		for _, metric := range mf.Metric {
			data.LayerCount += int64(*metric.Gauge.Value)
		}
	}
	err = w.tmpls.ExecuteTemplate(rw, "stats.html", data)
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}
}

type measureResult struct {
	PeerResults  []peerResult
	PullResults  []pullResult
	PeerDuration time.Duration
	PullDuration time.Duration
	PullSize     int64
}

type peerResult struct {
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
	mirror, err := url.Parse(w.baseURL(req))
	if err != nil {
		rw.WriteError(http.StatusBadRequest, err)
		return
	}

	// Parse image name.
	imgName := req.URL.Query().Get("image")
	if imgName == "" {
		rw.WriteError(http.StatusBadRequest, errors.New("image name cannot be empty"))
		return
	}
	img, err := oci.ParseImage(imgName)
	if err != nil {
		rw.WriteError(http.StatusBadRequest, err)
		return
	}

	res := measureResult{}

	// Resolve peers for the given image.
	resolveStart := time.Now()
	peerCh, err := w.router.Resolve(req.Context(), imgName, 0)
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}
	for peer := range peerCh {
		d := time.Since(resolveStart)
		res.PeerDuration += d
		res.PeerResults = append(res.PeerResults, peerResult{
			Peer:     peer,
			Duration: d,
		})
	}

	if len(res.PeerResults) > 0 {
		// Pull the image and measure performance.
		pullMetrics, err := w.ociClient.Pull(req.Context(), img, oci.WithFetchMirror(mirror))
		if err != nil {
			rw.WriteError(http.StatusInternalServerError, err)
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
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}
}

func (w *Web) baseURL(req *http.Request) string {
	addr := w.addr
	if addr == "" {
		//nolint: errcheck // Ignore error.
		srvAddr := req.Context().Value(http.LocalAddrContextKey).(net.Addr)
		addr = srvAddr.String()
	}
	if req.TLS != nil {
		return "https://" + addr
	}
	return "http://" + addr
}

func formatBytes(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(size)/float64(div), "KMGTPE"[exp])
}

func formatDuration(d time.Duration) string {
	if d < time.Millisecond {
		return "<1ms"
	}

	totalMs := int64(d / time.Millisecond)
	minutes := totalMs / 60000
	seconds := (totalMs % 60000) / 1000
	milliseconds := totalMs % 1000

	out := ""
	if minutes > 0 {
		out += fmt.Sprintf("%dm", minutes)
	}
	if seconds > 0 {
		out += fmt.Sprintf("%ds", seconds)
	}
	if milliseconds > 0 {
		out += fmt.Sprintf("%dms", milliseconds)
	}
	return out
}
