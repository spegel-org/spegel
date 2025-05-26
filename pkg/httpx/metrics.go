package httpx

import "github.com/prometheus/client_golang/prometheus"

var (
	HttpRequestDurHistogram = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Subsystem: "http",
		Name:      "request_duration_seconds",
		Help:      "The latency of the HTTP requests.",
	}, []string{"handler", "method", "code"})
	HttpResponseSizeHistogram = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Subsystem: "http",
		Name:      "response_size_bytes",
		Help:      "The size of the HTTP responses.",
		// 1kB up to 2GB
		Buckets: prometheus.ExponentialBuckets(1024, 5, 10),
	}, []string{"handler", "method", "code"})
	HttpRequestsInflight = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Subsystem: "http",
		Name:      "requests_inflight",
		Help:      "The number of inflight requests being handled at the same time.",
	}, []string{"handler"})
)

func RegisterMetrics(registerer prometheus.Registerer) {
	if registerer == nil {
		registerer = prometheus.DefaultRegisterer
	}
	registerer.MustRegister(HttpRequestDurHistogram)
	registerer.MustRegister(HttpResponseSizeHistogram)
	registerer.MustRegister(HttpRequestsInflight)
}
