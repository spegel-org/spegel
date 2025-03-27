package web

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"runtime"
	"strconv"
	"time"

	"github.com/containerd/containerd/v2/core/images"
	"github.com/go-logr/logr"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/prometheus/common/expfmt"

	"github.com/spegel-org/spegel/pkg/oci"
	"github.com/spegel-org/spegel/pkg/routing"
)

//go:embed templates/*
var templatesFS embed.FS

type Web struct {
	router routing.Router
	client *http.Client
	tmpls  *template.Template
}

func NewWeb(router routing.Router) (*Web, error) {
	tmpls, err := template.New("").ParseFS(templatesFS, "templates/*")
	if err != nil {
		return nil, err
	}
	return &Web{
		router: router,
		client: &http.Client{},
		tmpls:  tmpls,
	}, nil
}

func (w *Web) Handler(log logr.Logger) http.Handler {
	log = log.WithName("web")
	handlers := map[string]func(*http.Request) (string, any, error){
		"/debug/web/": func(r *http.Request) (string, any, error) {
			return "index", nil, nil
		},
		"/debug/web/stats":   w.stats,
		"/debug/web/measure": w.measure,
	}
	return http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		h, ok := handlers[req.URL.Path]
		if !ok {
			rw.WriteHeader(http.StatusNotFound)
			return
		}
		t, data, err := h(req)
		if err != nil {
			log.Error(err, "error when running handler", "path", req.URL.Path)
			rw.WriteHeader(http.StatusInternalServerError)
			return
		}
		err = w.tmpls.ExecuteTemplate(rw, t+".html", data)
		if err != nil {
			log.Error(err, "error rendering page", "path", req.URL.Path)
			rw.WriteHeader(http.StatusInternalServerError)
			return
		}
	})
}

func (w *Web) stats(req *http.Request) (string, any, error) {
	//nolint: errcheck // Ignore error.
	srvAddr := req.Context().Value(http.LocalAddrContextKey).(net.Addr)
	resp, err := http.Get(fmt.Sprintf("http://%s/metrics", srvAddr.String()))
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	parser := expfmt.TextParser{}
	metricFamilies, err := parser.TextToMetricFamilies(resp.Body)
	if err != nil {
		return "", nil, err
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
	return "stats", data, nil
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

func (w *Web) measure(req *http.Request) (string, any, error) {
	// Parse image name.
	imgName := req.URL.Query().Get("image")
	if imgName == "" {
		return "", nil, errors.New("image name cannot be empty")
	}
	img, err := oci.ParseImage(imgName)
	if err != nil {
		return "", nil, err
	}

	res := measureResult{}

	// Resolve peers for the given image.
	resolveStart := time.Now()
	peerCh, err := w.router.Resolve(req.Context(), imgName, false, 0)
	if err != nil {
		return "", nil, err
	}
	for peer := range peerCh {
		res.PeerResults = append(res.PeerResults, peerResult{
			Peer:     peer,
			Duration: time.Since(resolveStart),
		})
	}
	if len(res.PeerResults) == 0 {
		return "measure", res, nil
	}

	// Pull the image and measure performance.
	pullResults, err := measureImagePull(w.client, "http://localhost:5000", img)
	if err != nil {
		return "", nil, err
	}
	res.PullResults = pullResults

	return "measure", res, nil
}

func measureImagePull(client *http.Client, regURL string, img oci.Image) ([]pullResult, error) {
	pullResults := []pullResult{}
	queue := []oci.DistributionPath{
		{
			Kind:     oci.DistributionKindManifest,
			Name:     img.Repository,
			Digest:   img.Digest,
			Tag:      img.Tag,
			Registry: img.Registry,
		},
	}
	for {
		//nolint: staticcheck // Ignore until we have proper tests.
		if len(queue) == 0 {
			break
		}
		pr, dists, err := fetchDistributionPath(client, regURL, queue[0])
		if err != nil {
			return nil, err
		}
		queue = queue[1:]
		queue = append(queue, dists...)
		pullResults = append(pullResults, pr)
	}
	return pullResults, nil
}

func fetchDistributionPath(client *http.Client, regURL string, dist oci.DistributionPath) (pullResult, []oci.DistributionPath, error) {
	regU, err := url.Parse(regURL)
	if err != nil {
		return pullResult{}, nil, err
	}
	u := dist.URL()
	u.Scheme = regU.Scheme
	u.Host = regU.Host

	pullStart := time.Now()
	pullReq, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return pullResult{}, nil, err
	}
	pullResp, err := client.Do(pullReq)
	if err != nil {
		return pullResult{}, nil, err
	}
	defer pullResp.Body.Close()
	if pullResp.StatusCode != http.StatusOK {
		_, err = io.Copy(io.Discard, pullResp.Body)
		if err != nil {
			return pullResult{}, nil, err
		}
		return pullResult{}, nil, fmt.Errorf("request returned unexpected status code %s", pullResp.Status)
	}

	queue := []oci.DistributionPath{}
	ct := pullResp.Header.Get("Content-Type")
	switch dist.Kind {
	case oci.DistributionKindBlob:
		_, err = io.Copy(io.Discard, pullResp.Body)
		if err != nil {
			return pullResult{}, nil, err
		}
		ct = "Layer"
	case oci.DistributionKindManifest:
		b, err := io.ReadAll(pullResp.Body)
		if err != nil {
			return pullResult{}, nil, err
		}
		switch ct {
		case images.MediaTypeDockerSchema2ManifestList, ocispec.MediaTypeImageIndex:
			var idx ocispec.Index
			if err := json.Unmarshal(b, &idx); err != nil {
				return pullResult{}, nil, err
			}
			for _, m := range idx.Manifests {
				//nolint: staticcheck // Simplify in the future.
				if !(m.Platform.OS == runtime.GOOS && m.Platform.Architecture == runtime.GOARCH) {
					continue
				}
				queue = append(queue, oci.DistributionPath{
					Kind:     oci.DistributionKindManifest,
					Name:     dist.Name,
					Digest:   m.Digest,
					Registry: dist.Registry,
				})
			}
			ct = "Index"
		case images.MediaTypeDockerSchema2Manifest, ocispec.MediaTypeImageManifest:
			var manifest ocispec.Manifest
			err := json.Unmarshal(b, &manifest)
			if err != nil {
				return pullResult{}, nil, err
			}
			queue = append(queue, oci.DistributionPath{
				Kind:     oci.DistributionKindManifest,
				Name:     dist.Name,
				Digest:   manifest.Config.Digest,
				Registry: dist.Registry,
			})
			for _, layer := range manifest.Layers {
				queue = append(queue, oci.DistributionPath{
					Kind:     oci.DistributionKindBlob,
					Name:     dist.Name,
					Digest:   layer.Digest,
					Registry: dist.Registry,
				})
			}
			ct = "Manifest"
		case ocispec.MediaTypeImageConfig:
			ct = "Config"
		}
	}
	pullResp.Body.Close()

	i, err := strconv.ParseInt(pullResp.Header.Get("Content-Length"), 10, 0)
	if err != nil {
		return pullResult{}, nil, err
	}
	return pullResult{
		Identifier:    dist.Reference(),
		ContentType:   ct,
		ContentLength: formatByteSize(i),
		Duration:      time.Since(pullStart),
	}, queue, nil
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
