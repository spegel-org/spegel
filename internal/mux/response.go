package mux

import (
	"bufio"
	"io"
	"net"
	"net/http"
)

type ResponseWriter interface {
	http.ResponseWriter
	WriteError(statusCode int, err error)
	Error() error
	Status() int
	Size() int64
}

var (
	_ http.ResponseWriter = &response{}
	_ http.Flusher        = &response{}
	_ http.Hijacker       = &response{}
	_ io.ReaderFrom       = &response{}
)

type response struct {
	http.ResponseWriter
	error         error
	status        int
	size          int64
	writtenHeader bool
}

func (r *response) WriteHeader(statusCode int) {
	if !r.writtenHeader {
		r.writtenHeader = true
		r.status = statusCode
	}
	r.ResponseWriter.WriteHeader(statusCode)
}

func (r *response) Write(b []byte) (int, error) {
	r.writtenHeader = true
	n, err := r.ResponseWriter.Write(b)
	r.size += int64(n)
	return n, err
}

func (r *response) WriteError(statusCode int, err error) {
	r.error = err
	r.WriteHeader(statusCode)
}

func (r *response) Flush() {
	r.writtenHeader = true
	//nolint: errcheck // No method to throw the error.
	flusher := r.ResponseWriter.(http.Flusher)
	flusher.Flush()
}

func (r *response) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	//nolint: errcheck // No method to throw the error.
	hijacker := r.ResponseWriter.(http.Hijacker)
	return hijacker.Hijack()
}

func (r *response) ReadFrom(rd io.Reader) (int64, error) {
	n, err := io.Copy(r.ResponseWriter, rd)
	r.size += n
	return n, err
}

func (r *response) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

func (r *response) Status() int {
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}

func (r *response) Error() error {
	return r.error
}

func (r *response) Size() int64 {
	return r.size
}
