package httpx

import (
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
)

type MiddlewareFunc func(next Handler) Handler

func LogrMiddleware(log logr.Logger, ignore []string) MiddlewareFunc {
	return func(next Handler) Handler {
		return HandlerFunc(func(rw ResponseWriter, req *http.Request) {
			latency := time.Duration(0)

			next.ServeHTTP(rw, req.WithContext(logr.NewContext(req.Context(), log)))

			// Ignore logging requests to readyz and livez to reduce log noise
			if slices.Contains(ignore, req.URL.Path) {
				return
			}

			if log.V(1).Enabled() {
				kvs := []any{
					"path", req.URL.Path,
					"status", rw.Status(),
					"method", req.Method,
					"latency", latency.String(),
					"ip", clientIP(req),
				}
				for k, v := range rw.Attrs() {
					kvs = append(kvs, k, v)
				}
				if rw.Status() >= 200 && rw.Status() < 400 {
					log.Info("", kvs...)
				} else {
					log.Error(rw.Error(), "", kvs...)
				}
			}
		})
	}
}

func PrometheusMiddleware(registerer prometheus.Registerer) MiddlewareFunc {
	httpRequestDurHistogram := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Subsystem: "http",
		Name:      "request_duration_seconds",
		Help:      "The latency of the HTTP requests.",
	}, []string{"handler", "method", "code"})
	registerer.MustRegister(httpRequestDurHistogram)
	httpResponseSizeHistogram := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Subsystem: "http",
		Name:      "response_size_bytes",
		Help:      "The size of the HTTP responses.",
		// 1kB up to 2GB
		Buckets: prometheus.ExponentialBuckets(1024, 5, 10),
	}, []string{"handler", "method", "code"})
	registerer.MustRegister(httpResponseSizeHistogram)
	httpRequestsInflight := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Subsystem: "http",
		Name:      "requests_inflight",
		Help:      "The number of inflight requests being handled at the same time.",
	}, []string{"handler"})
	registerer.MustRegister(httpRequestsInflight)

	return func(next Handler) Handler {
		return HandlerFunc(func(rw ResponseWriter, req *http.Request) {
			// TODO: check pattern empty.
			metricsPath := metricsFriendlyPath(req.Pattern)

			httpRequestsInflight.WithLabelValues(metricsPath).Add(1)

			next.ServeHTTP(rw, req)

			latency := time.Duration(0)

			// latency := time.Since(start)
			statusCode := strconv.FormatInt(int64(rw.Status()), 10)

			httpRequestsInflight.WithLabelValues(metricsPath).Add(-1)
			httpRequestDurHistogram.WithLabelValues(metricsPath, req.Method, statusCode).Observe(latency.Seconds())
			httpResponseSizeHistogram.WithLabelValues(metricsPath, req.Method, statusCode).Observe(float64(rw.Size()))
		})
	}
}
