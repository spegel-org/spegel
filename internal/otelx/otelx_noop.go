//go:build !otel

package otelx

import (
	"context"
	"net/http"

	"github.com/go-logr/logr"
)

// Shutdown is a function that cleans up tracing resources.
type Shutdown func(context.Context) error

// Config holds OTEL configuration (unused in noop).
type Config struct {
	ServiceName string
	Endpoint    string
	Sampler     string
	Insecure    bool
}

// Setup is a no-op tracing initializer used when OTEL is not built in.
func Setup(ctx context.Context, cfg Config) (Shutdown, error) {
	return nil, nil
}

// SetupWithDefaults is a convenience function that accepts a service name.
func SetupWithDefaults(ctx context.Context, serviceName string) (Shutdown, error) {
	return nil, nil
}

// WrapHandler returns the original handler when tracing is disabled.
func WrapHandler(name string, h http.Handler) http.Handler {
	return h
}

// WrapTransport returns the original transport when tracing is disabled.
func WrapTransport(name string, rt http.RoundTripper) http.RoundTripper {
	return rt
}

// WithEnrichedLogger returns the original logger without correlation fields.
func WithEnrichedLogger(ctx context.Context, log logr.Logger) logr.Logger {
	return log
}

// StartSpan returns the original context and a no-op end function.
func StartSpan(ctx context.Context, name string) (context.Context, func()) {
	return ctx, func() {}
}
