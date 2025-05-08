package mux

import (
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
)

type HandlerFunc func(rw ResponseWriter, req *http.Request)

type ServeMux struct {
	mux *http.ServeMux
	log logr.Logger
}

func NewServeMux(log logr.Logger) *ServeMux {
	return &ServeMux{
		mux: http.NewServeMux(),
		log: log,
	}
}

func (s *ServeMux) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	h, pattern := s.mux.Handler(req)
	if pattern == "" {
		kvs := []any{
			"path", req.URL.Path,
			"status", http.StatusNotFound,
			"method", req.Method,
			"ip", GetClientIP(req),
		}
		s.log.Error(errors.New("page not found"), "", kvs...)
		rw.WriteHeader(http.StatusNotFound)
		return
	}
	h.ServeHTTP(rw, req)
}

func (s *ServeMux) Handle(pattern string, handler HandlerFunc) {
	metricsPath := metricsFriendlyPath(pattern)
	s.mux.HandleFunc(pattern, func(w http.ResponseWriter, req *http.Request) {
		start := time.Now()
		rw := &response{ResponseWriter: w}
		defer func() {
			latency := time.Since(start)
			statusCode := strconv.FormatInt(int64(rw.Status()), 10)

			HttpRequestsInflight.WithLabelValues(metricsPath).Add(-1)
			HttpRequestDurHistogram.WithLabelValues(metricsPath, req.Method, statusCode).Observe(latency.Seconds())
			HttpResponseSizeHistogram.WithLabelValues(metricsPath, req.Method, statusCode).Observe(float64(rw.Size()))

			// Ignore logging requests to healthz to reduce log noise
			if req.URL.Path == "/healthz" {
				return
			}

			kvs := []any{
				"path", req.URL.Path,
				"status", rw.Status(),
				"method", req.Method,
				"latency", latency.String(),
				"ip", GetClientIP(req),
				"handler", rw.handler,
			}
			if rw.Status() >= 200 && rw.Status() < 400 {
				s.log.Info("", kvs...)
				return
			}
			s.log.Error(rw.Error(), "", kvs...)
		}()
		HttpRequestsInflight.WithLabelValues(metricsPath).Add(1)
		handler(rw, req)
	})
}

func GetClientIP(req *http.Request) string {
	forwardedFor := req.Header.Get("X-Forwarded-For")
	if forwardedFor != "" {
		comps := strings.Split(forwardedFor, ",")
		if len(comps) > 1 {
			return comps[0]
		}
		return forwardedFor
	}
	h, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		return ""
	}
	return h
}

func metricsFriendlyPath(pattern string) string {
	_, path, _ := strings.Cut(pattern, "/")
	path = "/" + path
	if strings.HasSuffix(path, "/") {
		return path + "*"
	}
	return path
}
