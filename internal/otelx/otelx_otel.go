//go:build otel

package otelx

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// Shutdown is a function type for shutting down the OTEL SDK.
type Shutdown func(context.Context) error

// Config holds OTEL configuration.
type Config struct {
	ServiceName string
	Endpoint    string
	Sampler     string
	Insecure    bool
}

// Setup initializes the OpenTelemetry SDK.
func Setup(ctx context.Context, cfg Config) (Shutdown, error) {
	log := logr.FromContextOrDiscard(ctx)

	// Use cfg if provided, otherwise fall back to environment
	if cfg.Endpoint == "" {
		cfg.Endpoint = getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4318")
	}
	if cfg.ServiceName == "" {
		cfg.ServiceName = "spegel"
	}
	if cfg.Sampler == "" {
		cfg.Sampler = getEnv("OTEL_TRACES_SAMPLER", "parentbased_always_off")
	}

	log.Info("initializing OTEL", "service", cfg.ServiceName, "endpoint", cfg.Endpoint, "sampler", cfg.Sampler)

	opts := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(cfg.Endpoint),
	}
	if cfg.Insecure {
		opts = append(opts, otlptracehttp.WithInsecure())
	}

	exporter, err := otlptracehttp.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create OTLP exporter: %w", err)
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}

	tp := trace.NewTracerProvider(
		trace.WithBatcher(exporter),
		trace.WithResource(res),
		trace.WithSampler(newSampler(cfg.Sampler)),
	)

	otel.SetTracerProvider(tp)

	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	shutdownFn := func(ctx context.Context) error {
		if tp != nil {
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			return tp.Shutdown(ctx)
		}
		return nil
	}

	return shutdownFn, nil
}

// SetupWithDefaults is a convenience function that accepts a service name.
func SetupWithDefaults(ctx context.Context, serviceName string) (Shutdown, error) {
	return Setup(ctx, Config{ServiceName: serviceName})
}

// WrapHandler wraps an HTTP handler with OpenTelemetry instrumentation.
func WrapHandler(name string, h http.Handler) http.Handler {
	return otelhttp.NewHandler(h, name)
}

// WrapTransport wraps an HTTP transport with OpenTelemetry instrumentation.
func WrapTransport(name string, rt http.RoundTripper) http.RoundTripper {
	return otelhttp.NewTransport(rt)
}

// WithEnrichedLogger adds trace correlation fields and returns a derived logger.
func WithEnrichedLogger(ctx context.Context, log logr.Logger) logr.Logger {
	span := oteltrace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return log
	}

	spanCtx := span.SpanContext()
	if !spanCtx.IsValid() {
		return log
	}

	traceID := spanCtx.TraceID().String()
	spanID := spanCtx.SpanID().String()

	return log.WithValues("trace_id", traceID, "span_id", spanID)
}

// StartSpan creates a new trace span.
func StartSpan(ctx context.Context, name string, opts ...oteltrace.SpanStartOption) (context.Context, func()) {
	tracer := otel.Tracer("github.com/spegel-org/spegel")
	ctx, span := tracer.Start(ctx, name, opts...)
	return ctx, func() { span.End() }
}

// getEnv returns an environment variable value or a default.
func getEnv(key, defaultValue string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultValue
}

// newSampler creates a trace sampler based on the provided string.
func newSampler(samplerType string) trace.Sampler {
	switch samplerType {
	case "always_on":
		return trace.AlwaysSample()
	case "always_off":
		return trace.NeverSample()
	case "parentbased_always_on":
		return trace.ParentBased(trace.AlwaysSample())
	case "parentbased_always_off":
		return trace.ParentBased(trace.NeverSample())
	default:
		// Try to parse as a ratio
		if ratio, err := strconv.ParseFloat(samplerType, 64); err == nil && ratio >= 0 && ratio <= 1 {
			return trace.TraceIDRatioBased(ratio)
		}
		// Default to parentbased_always_off
		return trace.ParentBased(trace.NeverSample())
	}
}
