package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

var (
	// DefaultRegisterer and DefaultGatherer are the implementations of the
	// prometheus Registerer and Gatherer interfaces that all metrics operations
	// will use. They are variables so that packages that embed this library can
	// replace them at runtime, instead of having to pass around specific
	// registries.
	DefaultRegisterer = prometheus.DefaultRegisterer
	DefaultGatherer   = prometheus.DefaultGatherer
)

var (
	MirrorRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "spegel_mirror_requests_total",
		Help: "Total number of mirror requests.",
	}, []string{"registry", "cache", "source"})
	ResolveDurHistogram = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "spegel_resolve_duration_seconds",
		Help: "The duration for router to resolve a peer.",
	}, []string{"router"})
	AdvertisedImages = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "spegel_advertised_images",
		Help: "Number of images advertised to be available.",
	}, []string{"registry"})
	AdvertisedImageTags = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "spegel_advertised_image_tags",
		Help: "Number of image tags advertised to be available.",
	}, []string{"registry"})
	AdvertisedImageDigests = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "spegel_advertised_image_digests",
		Help: "Number of image digests advertised to be available.",
	}, []string{"registry"})
	AdvertisedKeys = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "spegel_advertised_keys",
		Help: "Number of keys advertised to be available.",
	}, []string{"registry"})
	HttpRequestDurHistogram = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Subsystem: "http",
		Name:      "request_duration_seconds",
		Help:      "The latency of the HTTP requests.",
	}, []string{"handler", "method", "code"})
	HttpResponseSizeHistogram = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Subsystem: "http",
		Name:      "response_size_bytes",
		Help:      "The size of the HTTP responses.",
	}, []string{"handler", "method", "code"})
	HttpRequestsInflight = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Subsystem: "http",
		Name:      "requests_inflight",
		Help:      "The number of inflight requests being handled at the same time.",
	}, []string{"handler"})
)

func Register() {
	DefaultRegisterer.MustRegister(MirrorRequestsTotal)
	DefaultRegisterer.MustRegister(ResolveDurHistogram)
	DefaultRegisterer.MustRegister(AdvertisedImages)
	DefaultRegisterer.MustRegister(AdvertisedImageTags)
	DefaultRegisterer.MustRegister(AdvertisedImageDigests)
	DefaultRegisterer.MustRegister(AdvertisedKeys)
	DefaultRegisterer.MustRegister(HttpRequestDurHistogram)
	DefaultRegisterer.MustRegister(HttpResponseSizeHistogram)
	DefaultRegisterer.MustRegister(HttpRequestsInflight)
}
