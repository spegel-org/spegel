package httpx

import (
	"net"
	"net/http"
	"slices"
	"strings"
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
	mws []MiddlewareFunc
}

func NewServeMux(middlewares ...MiddlewareFunc) *ServeMux {
	return &ServeMux{
		mux: http.NewServeMux(),
		mws: []MiddlewareFunc{},
	}
}

func (s *ServeMux) Use(middleware ...MiddlewareFunc) {
	for _, mw := range middleware {
		s.mws = append(s.mws, mw)
	}
}

func (s *ServeMux) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	rw := &response{
		ResponseWriter: w,
		method:         req.Method,
	}
	s.mux.ServeHTTP(rw, req)
}

func (s *ServeMux) Handle(pattern string, handler Handler) {
	for _, mw := range slices.Backward(s.mws) {
		handler = mw(handler)
	}
	s.mux.Handle(pattern, http.HandlerFunc(func(w http.ResponseWriter, rew *http.Request) {
		handler.ServeHTTP(w.(ResponseWriter), rew)
	}))
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
