package mux

import "net/http"

type Handler func(rw ResponseWriter, req *http.Request)

type ServeMux struct {
	h Handler
}

func NewServeMux(handler Handler) *ServeMux {
	return &ServeMux{h: handler}
}

func (s *ServeMux) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	s.h(&response{ResponseWriter: rw}, req)
}
