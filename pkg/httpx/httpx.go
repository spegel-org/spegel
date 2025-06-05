package httpx

import (
	"net"
	"net/http"
	"time"
)

const (
	HeaderContentType     = "Content-Type"
	HeaderContentLength   = "Content-Length"
	HeaderContentRange    = "Content-Range"
	HeaderRange           = "Range"
	HeaderAcceptRanges    = "Accept-Ranges"
	HeaderUserAgent       = "User-Agent"
	HeaderAccept          = "Accept"
	HeaderAuthorization   = "Authorization"
	HeaderWWWAuthenticate = "WWW-Authenticate"
	HeaderXForwardedFor   = "X-Forwarded-For"
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
