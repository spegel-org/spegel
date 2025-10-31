package web

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html"
	"html/template"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"

	"github.com/spegel-org/spegel/pkg/httpx"
	"github.com/spegel-org/spegel/pkg/oci"
	"github.com/spegel-org/spegel/pkg/routing"
)

//go:embed templates/*
var templatesFS embed.FS

type Web struct {
	router          routing.Router
	ociClient       *oci.Client
	ociStore        oci.Store
	httpClient      *http.Client
	tmpls           *template.Template
	registryAddress string
}

func NewWeb(router routing.Router, ociClient *oci.Client, ociStore oci.Store, registryAddr string) (*Web, error) {
	funcs := template.FuncMap{
		"formatBytes":    formatBytes,
		"formatDuration": formatDuration,
	}
	tmpls, err := template.New("").Funcs(funcs).ParseFS(templatesFS, "templates/*")
	if err != nil {
		return nil, err
	}
	return &Web{
		router:          router,
		ociClient:       ociClient,
		ociStore:        ociStore,
		httpClient:      httpx.BaseClient(),
		tmpls:           tmpls,
		registryAddress: registryAddr,
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

	parser := expfmt.NewTextParser(model.UTF8Validation)
	metricFamilies, err := parser.TextToMetricFamilies(resp.Body)
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}

	data := struct {
		LocalAddress      string
		Images            []oci.Image
		PeerAddresses     []string
		MirrorLastSuccess time.Duration
		ImageCount        int64
		LayerCount        int64
		PeerCount         int
	}{}
	if family, ok := metricFamilies["spegel_advertised_images"]; ok {
		for _, metric := range family.Metric {
			data.ImageCount += int64(*metric.Gauge.Value)
		}
	}
	if family, ok := metricFamilies["spegel_advertised_keys"]; ok {
		for _, metric := range family.Metric {
			data.LayerCount += int64(*metric.Gauge.Value)
		}
	}
	mirrorLastSuccess := int64(*metricFamilies["spegel_mirror_last_success_timestamp_seconds"].Metric[0].Gauge.Value)
	if mirrorLastSuccess > 0 {
		data.MirrorLastSuccess = time.Since(time.Unix(mirrorLastSuccess, 0))
	}

	if p2pRouter, ok := w.router.(*routing.P2PRouter); ok {
		peerAddrs := p2pRouter.PeerAddresses()
		data.PeerCount = len(peerAddrs)
		data.PeerAddresses = peerAddrs
		data.LocalAddress = p2pRouter.LocalAddress()
	}

	if w.ociStore != nil {
		images, err := w.ociStore.ListImages(req.Context())
		if err == nil {
			data.Images = images
		}
	}

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
	mirror := &url.URL{
		Scheme: "http",
		Host:   w.registryAddress,
	}

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
		pullMetrics, err := w.ociClient.Pull(req.Context(), img, oci.WithPullMirror(mirror))
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

var _ httpx.ResponseError = &HTMLResponseError{}

type HTMLResponseError struct {
	error
}

func NewHTMLResponseError(err error) *HTMLResponseError {
	return &HTMLResponseError{err}
}

func (e *HTMLResponseError) ResponseBody() ([]byte, string, error) {
	if e.error == nil {
		return nil, "", errors.New("no error set")
	}
	return fmt.Appendf(nil, `<p class="error">%s</p>`, html.EscapeString(e.Error())), httpx.ContentTypeText, nil
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

	values := []int64{
		int64(d / (24 * time.Hour)),
		int64((d % (24 * time.Hour)) / time.Hour),
		int64((d % time.Hour) / time.Minute),
		int64((d % time.Minute) / time.Second),
		int64((d % time.Second) / time.Millisecond),
	}
	units := []string{
		"d",
		"h",
		"m",
		"s",
		"ms",
	}

	comps := []string{}
	for i, v := range values {
		if v == 0 {
			if len(comps) > 0 {
				break
			}
			continue
		}
		comps = append(comps, fmt.Sprintf("%d%s", v, units[i]))
		if len(comps) == 2 {
			break
		}
	}
	return strings.Join(comps, " ")
}
