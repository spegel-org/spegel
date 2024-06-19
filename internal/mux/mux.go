package mux

import (
	"errors"
	"net/http"
)

type Handler func(rw ResponseWriter, req *http.Request)

type ServeMux struct {
	h Handler
}

func NewServeMux(h Handler) (*ServeMux, error) {
	if h == nil {
		return nil, errors.New("handler cannot be nil")
	}
	return &ServeMux{h: h}, nil
}

func (s *ServeMux) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	s.h(&response{ResponseWriter: rw}, req)
}
