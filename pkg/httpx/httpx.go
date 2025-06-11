package httpx

import (
	"errors"
	"io"
	"net"
	"net/http"
	"time"
)

// BaseClient returns a http client with reasonable defaults set.
func BaseClient() *http.Client {
	return &http.Client{
		Transport: BaseTransport(),
		Timeout:   10 * time.Second,
	}
}

// BaseTransport returns a http transport with reasonable defaults set.
func BaseTransport() *http.Transport {
	return &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

const (
	// MaxReadBytes is the maximum amount of bytes read when draining a response or reading error message.
	MaxReadBytes = 512 * 1024
)

// DrainAndCloses empties the body buffer before closing the body.
func DrainAndClose(rc io.ReadCloser) error {
	errs := []error{}
	n, err := io.Copy(io.Discard, io.LimitReader(rc, MaxReadBytes+1))
	if err != nil {
		errs = append(errs, err)
	}
	if n > MaxReadBytes {
		errs = append(errs, errors.New("reader has more data than max read bytes"))
	}
	err = rc.Close()
	if err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
