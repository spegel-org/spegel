package otelx

import (
	"context"
	"net/http"

	"github.com/go-logr/logr"
)

// Shutdown is a function that cleans up tracing resources.
type Shutdown func(context.Context) error

// Setup is a no-op tracing initializer used when OTEL is not built in.
func Setup(ctx context.Context, serviceName string) (Shutdown, error) {
	return nil, nil
}

// WrapHandler returns the original handler when tracing is disabled.
func WrapHandler(h http.Handler, name string) http.Handler {
	return h
}

// WrapTransport returns the original transport when tracing is disabled.
func WrapTransport(rt http.RoundTripper) http.RoundTripper {
	return rt
}

// EnrichLogger returns the original logger without correlation fields.
func EnrichLogger(ctx context.Context, log logr.Logger) logr.Logger {
	return log
}
