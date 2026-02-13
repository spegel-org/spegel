package otelx

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"time"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	"go.opentelemetry.io/otel/trace"
)

const (
	defaultServiceName = "spegel"
	defaultSampler     = "parentbased_always_off"
	defaultEndpoint    = "localhost:4318"
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

	explicitConfig := isExplicitConfig(cfg)
	if !explicitConfig && !isNoopTracerProvider(otel.GetTracerProvider()) {
		log.Info("skipping OTEL setup; tracer provider already configured")
		return nil, nil
	}
	if explicitConfig && !isNoopTracerProvider(otel.GetTracerProvider()) {
		log.Info("overriding existing OTEL tracer provider due to explicit config")
	}

	// Use cfg if provided, otherwise fall back to environment
	if cfg.Endpoint == "" {
		cfg.Endpoint = getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", defaultEndpoint)
	}
	if cfg.ServiceName == "" {
		cfg.ServiceName = getEnv("OTEL_SERVICE_NAME", defaultServiceName)
	}
	if cfg.Sampler == "" {
		cfg.Sampler = getEnv("OTEL_TRACES_SAMPLER", defaultSampler)
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

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(newSampler(cfg.Sampler)),
	)

	otel.SetTracerProvider(tp)

	defaultPropagator := propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)
	if reflect.DeepEqual(otel.GetTextMapPropagator(), defaultPropagator) {
		otel.SetTextMapPropagator(defaultPropagator)
	} else {
		log.Info("keeping existing OTEL propagator")
	}

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

func isNoopTracerProvider(tp trace.TracerProvider) bool {
	tracer := tp.Tracer("spegel-otel-probe")
	_, span := tracer.Start(context.Background(), "otel-probe")
	spanCtx := span.SpanContext()
	span.End()
	return !spanCtx.IsValid()
}

func isExplicitConfig(cfg Config) bool {
	if cfg.Endpoint != "" || cfg.Insecure {
		return true
	}
	if cfg.ServiceName != "" && cfg.ServiceName != defaultServiceName {
		return true
	}
	if cfg.Sampler != "" && cfg.Sampler != defaultSampler {
		return true
	}
	return false
}

// SetupWithDefaults is a convenience function that accepts a service name.
func SetupWithDefaults(ctx context.Context, serviceName string) (Shutdown, error) {
	return Setup(ctx, Config{ServiceName: serviceName})
}

// getEnv returns an environment variable value or a default.
func getEnv(key, defaultValue string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultValue
}

// newSampler creates a trace sampler based on the provided string.
func newSampler(samplerType string) sdktrace.Sampler {
	switch samplerType {
	case "always_on":
		return sdktrace.AlwaysSample()
	case "always_off":
		return sdktrace.NeverSample()
	case "parentbased_always_on":
		return sdktrace.ParentBased(sdktrace.AlwaysSample())
	case "parentbased_always_off":
		return sdktrace.ParentBased(sdktrace.NeverSample())
	default:
		// Try to parse as a ratio
		if ratio, err := strconv.ParseFloat(samplerType, 64); err == nil && ratio >= 0 && ratio <= 1 {
			return sdktrace.TraceIDRatioBased(ratio)
		}
		// Default to parentbased_always_off
		return sdktrace.ParentBased(sdktrace.NeverSample())
	}
}
