package httpx

import (
	"bufio"
	"errors"
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
	SetHandler(handler string)
	HeadersWritten() bool
}

var (
	_ http.ResponseWriter = &response{}
	_ http.Flusher        = &response{}
	_ http.Hijacker       = &response{}
	_ io.ReaderFrom       = &response{}
	_ ResponseWriter      = &response{}
)

type response struct {
	http.ResponseWriter
	error       error
	handler     string
	method      string
	status      int
	size        int64
	wroteHeader bool
}

func (r *response) WriteHeader(statusCode int) {
	if !r.wroteHeader {
		r.wroteHeader = true
		r.status = statusCode
	}
	r.ResponseWriter.WriteHeader(statusCode)
}

func (r *response) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(b)
	r.size += int64(n)
	return n, err
}

func (r *response) WriteError(statusCode int, err error) {
	r.error = err
	r.WriteHeader(statusCode)

	if r.method == http.MethodHead {
		return
	}

	var respErr ResponseError
	if errors.As(err, &respErr) {
		b, rbErr := respErr.ResponseBody()
		if rbErr != nil {
			r.error = errors.Join(r.error, rbErr)
			return
		}
		_, wErr := r.Write(b)
		if wErr != nil {
			r.error = errors.Join(r.error, wErr)
			return
		}
	}
}

func (r *response) Flush() {
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
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	n, err := io.Copy(r.ResponseWriter, rd)
	r.size += n
	return n, err
}

func (r *response) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

func (r *response) Status() int {
	if !r.wroteHeader {
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

func (r *response) SetHandler(handler string) {
	r.handler = handler
}

func (r *response) HeadersWritten() bool {
	return r.wroteHeader
}
