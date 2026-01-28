package httpx

import (
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// WrapHandler wraps an HTTP handler with OpenTelemetry instrumentation.
func WrapHandler(name string, h http.Handler) http.Handler {
	return otelhttp.NewHandler(h, name)
}

// WrapTransport wraps an HTTP transport with OpenTelemetry instrumentation.
func WrapTransport(name string, rt http.RoundTripper) http.RoundTripper {
	if rt == nil {
		rt = http.DefaultTransport
	}
	if name == "" {
		return otelhttp.NewTransport(rt)
	}
	return otelhttp.NewTransport(rt, otelhttp.WithSpanNameFormatter(func(operation string, _ *http.Request) string {
		return name + " " + operation
	}))
}
