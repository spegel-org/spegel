package web

import (
	"embed"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/netip"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/common/expfmt"

	"github.com/spegel-org/spegel/pkg/mux"
	"github.com/spegel-org/spegel/pkg/oci"
	"github.com/spegel-org/spegel/pkg/routing"
)

//go:embed templates/*
var templatesFS embed.FS

type Web struct {
	router routing.Router
	client *oci.Client
	tmpls  *template.Template
}

func NewWeb(router routing.Router) (*Web, error) {
	tmpls, err := template.New("").ParseFS(templatesFS, "templates/*")
	if err != nil {
		return nil, err
	}
	return &Web{
		router: router,
		client: oci.NewClient(),
		tmpls:  tmpls,
	}, nil
}

func (w *Web) Handler(log logr.Logger) http.Handler {
	m := mux.NewServeMux(log)
	m.Handle("GET /debug/web/", w.indexHandler)
	m.Handle("GET /debug/web/stats", w.statsHandler)
	m.Handle("GET /debug/web/measure", w.measureHandler)
	return m
}

func (w *Web) indexHandler(rw mux.ResponseWriter, req *http.Request) {
	err := w.tmpls.ExecuteTemplate(rw, "index.html", nil)
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}
}

func (w *Web) statsHandler(rw mux.ResponseWriter, req *http.Request) {
	//nolint: errcheck // Ignore error.
	srvAddr := req.Context().Value(http.LocalAddrContextKey).(net.Addr)
	resp, err := http.Get(fmt.Sprintf("http://%s/metrics", srvAddr.String()))
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}
	defer resp.Body.Close()
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
	PeerResults []peerResult
	PullResults []pullResult
}

type peerResult struct {
	Peer     netip.AddrPort
	Duration time.Duration
}

type pullResult struct {
	Identifier    string
	ContentType   string
	ContentLength string
	Duration      time.Duration
}

func (w *Web) measureHandler(rw mux.ResponseWriter, req *http.Request) {
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
		res.PeerResults = append(res.PeerResults, peerResult{
			Peer:     peer,
			Duration: time.Since(resolveStart),
		})
	}

	if len(res.PeerResults) > 0 {
		// Pull the image and measure performance.
		pullMetrics, err := w.client.Pull(req.Context(), img, "http://localhost:5000")
		if err != nil {
			rw.WriteError(http.StatusInternalServerError, err)
			return
		}
		pullResults := []pullResult{}
		for _, metric := range pullMetrics {
			pullResults = append(pullResults, pullResult{
				Identifier:    metric.Digest.String(),
				ContentType:   metric.ContentType,
				ContentLength: formatByteSize(metric.ContentLength),
				Duration:      metric.Duration,
			})
		}
		res.PullResults = pullResults
	}

	err = w.tmpls.ExecuteTemplate(rw, "measure.html", res)
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}
}

func formatByteSize(size int64) string {
	const unit = 1000
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(size)/float64(div), "kMGTPE"[exp])
}
