package httpx

import (
	"errors"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
)

type Handler interface {
	ServeHTTP(rw ResponseWriter, req *http.Request)
}

type HandlerFunc func(rw ResponseWriter, req *http.Request)

func (f HandlerFunc) ServeHTTP(rw ResponseWriter, req *http.Request) {
	f(rw, req)
}

func StdlibHandler(handler http.Handler) Handler {
	return HandlerFunc(func(rw ResponseWriter, req *http.Request) {
		handler.ServeHTTP(rw, req)
	})
}

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

func (s *ServeMux) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	rw := &response{
		ResponseWriter: w,
		method:         req.Method,
	}
	s.mux.ServeHTTP(rw, req)
	if req.Pattern == "" {
		kvs := []any{
			"path", req.URL.Path,
			"status", http.StatusNotFound,
			"method", req.Method,
			"ip", clientIP(req),
		}
		s.log.Error(errors.New("page not found"), "", kvs...)
		rw.WriteHeader(http.StatusNotFound)
		return
	}
}

func (s *ServeMux) Handle(pattern string, handler Handler) {
	metricsPath := metricsFriendlyPath(pattern)
	s.mux.HandleFunc(pattern, func(w http.ResponseWriter, req *http.Request) {
		start := time.Now()
		rw := &response{
			ResponseWriter: w,
			method:         req.Method,
		}
		defer func() {
			latency := time.Since(start)
			statusCode := strconv.FormatInt(int64(rw.Status()), 10)

			HttpRequestsInflight.WithLabelValues(metricsPath).Add(-1)
			HttpRequestDurHistogram.WithLabelValues(metricsPath, req.Method, statusCode).Observe(latency.Seconds())
			HttpResponseSizeHistogram.WithLabelValues(metricsPath, req.Method, statusCode).Observe(float64(rw.Size()))

			// Ignore logging requests to readyz and livez to reduce log noise
			if slices.Contains([]string{"/readyz", "/livez"}, req.URL.Path) {
				return
			}

			if s.log.V(1).Enabled() {
				kvs := []any{
					"path", req.URL.Path,
					"status", rw.Status(),
					"method", req.Method,
					"latency", latency.String(),
					"ip", clientIP(req),
				}
				for k, v := range rw.attrs {
					kvs = append(kvs, k, v)
				}
				if rw.Status() >= 200 && rw.Status() < 400 {
					s.log.Info("", kvs...)
				} else {
					s.log.Error(rw.Error(), "", kvs...)
				}
			}
		}()
		HttpRequestsInflight.WithLabelValues(metricsPath).Add(1)
		//nolint: errcheck // Cannot be any other type as it is injected in ServeHttp.
		handler.ServeHTTP(w.(ResponseWriter), req.WithContext(logr.NewContext(req.Context(), s.log)))
	})
}

func (s *ServeMux) HandleFunc(pattern string, handler func(rw ResponseWriter, req *http.Request)) {
	s.Handle(pattern, HandlerFunc(handler))
}

func clientIP(req *http.Request) string {
	forwardedFor := req.Header.Get(HeaderXForwardedFor)
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
