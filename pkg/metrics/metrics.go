package metrics

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/spegel-org/spegel/pkg/httpx"
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
	}, []string{"registry", "cache"})
	MirrorLastSuccessTimestamp = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "spegel_mirror_last_success_timestamp_seconds",
		Help: "The timestamp of the last successful mirror request.",
	})
	ResolveDurHistogram = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "spegel_resolve_duration_seconds",
		Help: "The duration for router to resolve a peer.",
	}, []string{"router"})
	AdvertisedKeys = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "spegel_advertised_keys",
		Help: "Number of image digests advertised to be available.",
	})
)

func Register() {
	DefaultRegisterer.MustRegister(MirrorRequestsTotal)
	DefaultRegisterer.MustRegister(MirrorLastSuccessTimestamp)
	DefaultRegisterer.MustRegister(ResolveDurHistogram)
	DefaultRegisterer.MustRegister(AdvertisedKeys)
	httpx.RegisterMetrics(DefaultRegisterer)
}
