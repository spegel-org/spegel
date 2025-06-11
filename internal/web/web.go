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
	ociClient  *oci.Client
	httpClient *http.Client
	tmpls      *template.Template
}

func NewWeb(router routing.Router, ociClient *oci.Client) (*Web, error) {
	funcs := template.FuncMap{
		"formatBytes":    formatBytes,
		"formatDuration": formatDuration,
	}
	tmpls, err := template.New("").Funcs(funcs).ParseFS(templatesFS, "templates/*")
	if err != nil {
		return nil, err
	}
	return &Web{
		router:     router,
		ociClient:  ociClient,
		httpClient: httpx.BaseClient(),
		tmpls:      tmpls,
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
	//nolint: errcheck // Ignore error.
	srvAddr := req.Context().Value(http.LocalAddrContextKey).(net.Addr)
	req, err := http.NewRequestWithContext(req.Context(), http.MethodGet, fmt.Sprintf("http://%s/metrics", srvAddr.String()), nil)
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
	for _, metric := range metricFamilies["spegel_advertised_images"].Metric {
		data.ImageCount += int64(*metric.Gauge.Value)
	}
	for _, metric := range metricFamilies["spegel_advertised_keys"].Metric {
		data.LayerCount += int64(*metric.Gauge.Value)
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
	mirror := &url.URL{
		Scheme: "http",
		Host:   "localhost:5000",
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
