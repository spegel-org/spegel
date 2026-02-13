package otelx

import (
	"context"
	"testing"

	"github.com/go-logr/logr/funcr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// ensureTestTracerProvider sets a global tracer provider that always samples.
func ensureTestTracerProvider(t *testing.T) {
	t.Helper()
	prevProvider := otel.GetTracerProvider()
	prevPropagator := otel.GetTextMapPropagator()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(prevProvider)
		otel.SetTextMapPropagator(prevPropagator)
		if err := tp.Shutdown(context.Background()); err != nil {
			t.Errorf("failed to shutdown tracer provider: %v", err)
		}
	})
}

//nolint:paralleltest // Mutates global OTEL provider/propagator.
func TestStartSpan_Otel(t *testing.T) {
	ensureTestTracerProvider(t)
	ctx := context.Background()
	newCtx, end := StartSpan(ctx, "test-span")
	assert.NotNil(t, end)
	assert.NotEqual(t, ctx, newCtx)
	end()
}

//nolint:paralleltest // Mutates global OTEL provider/propagator.
func TestWithEnrichedLogger_AddsTraceFields(t *testing.T) {
	ensureTestTracerProvider(t)
	ctx, end := StartSpan(context.Background(), "log-span")
	defer end()

	var captured string
	logger := funcr.New(func(prefix, args string) {
		captured = args
	}, funcr.Options{Verbosity: 0})

	WithEnrichedLogger(ctx, logger).Info("x")
	assert.Contains(t, captured, "\"trace_id\"=")
	assert.Contains(t, captured, "\"span_id\"=")
}

//nolint:paralleltest // Mutates global OTEL provider/propagator.
func TestSetup_UsesExistingTracerProvider(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	prevProvider := otel.GetTracerProvider()
	prevPropagator := otel.GetTextMapPropagator()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(prevProvider)
		otel.SetTextMapPropagator(prevPropagator)
	})

	shutdown, err := Setup(context.Background(), Config{})
	require.NoError(t, err)
	assert.Nil(t, shutdown)
	assert.Same(t, tp, otel.GetTracerProvider())
}

//nolint:paralleltest // Mutates global OTEL provider/propagator.
func TestSetup_RespectsExistingPropagator(t *testing.T) {
	prevProvider := otel.GetTracerProvider()
	prevPropagator := otel.GetTextMapPropagator()
	customProp := propagation.NewCompositeTextMapPropagator(propagation.Baggage{})
	otel.SetTracerProvider(noop.NewTracerProvider())
	otel.SetTextMapPropagator(customProp)
	t.Cleanup(func() {
		otel.SetTracerProvider(prevProvider)
		otel.SetTextMapPropagator(prevPropagator)
	})

	shutdown, err := Setup(context.Background(), Config{
		Endpoint: "http://127.0.0.1:4318",
	})
	require.NoError(t, err)
	require.NotNil(t, shutdown)
	assert.Equal(t, customProp, otel.GetTextMapPropagator())
}

//nolint:paralleltest // Mutates global OTEL provider/propagator.
func TestSetup_OverridesExistingProviderWhenConfigured(t *testing.T) {
	prevProvider := otel.GetTracerProvider()
	prevPropagator := otel.GetTextMapPropagator()
	existing := sdktrace.NewTracerProvider()
	otel.SetTracerProvider(existing)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(prevProvider)
		otel.SetTextMapPropagator(prevPropagator)
	})

	shutdown, err := Setup(context.Background(), Config{
		Endpoint: "http://127.0.0.1:4318",
		Sampler:  "always_on",
		Insecure: true,
	})
	require.NoError(t, err)
	require.NotNil(t, shutdown)
	assert.NotSame(t, existing, otel.GetTracerProvider())
}

func TestSetup_UsesEnvSampler(t *testing.T) {
	prevProvider := otel.GetTracerProvider()
	prevPropagator := otel.GetTextMapPropagator()
	otel.SetTracerProvider(noop.NewTracerProvider())
	otel.SetTextMapPropagator(propagation.TraceContext{})

	t.Setenv("OTEL_TRACES_SAMPLER", "always_on")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4318")
	t.Cleanup(func() {
		otel.SetTracerProvider(prevProvider)
		otel.SetTextMapPropagator(prevPropagator)
	})

	shutdown, err := Setup(context.Background(), Config{})
	require.NoError(t, err)
	require.NotNil(t, shutdown)

	_, span := otel.Tracer("spegel-test").Start(context.Background(), "probe")
	assert.True(t, span.IsRecording())
	span.End()
}

//nolint:paralleltest // Mutates global OTEL provider/propagator.
func TestSetup_InsecureEndpoint(t *testing.T) {
	prevProvider := otel.GetTracerProvider()
	prevPropagator := otel.GetTextMapPropagator()
	otel.SetTracerProvider(noop.NewTracerProvider())
	t.Cleanup(func() {
		otel.SetTracerProvider(prevProvider)
		otel.SetTextMapPropagator(prevPropagator)
	})

	shutdown, err := Setup(context.Background(), Config{
		Endpoint: "127.0.0.1:4318",
		Insecure: true,
	})
	require.NoError(t, err)
	require.NotNil(t, shutdown)
}

//nolint:paralleltest // Mutates global OTEL provider/propagator.
func TestNewSampler(t *testing.T) {
	tests := []struct {
		name     string
		sampler  string
		expected sdktrace.SamplingDecision
	}{
		{name: "always_on", sampler: "always_on", expected: sdktrace.RecordAndSample},
		{name: "always_off", sampler: "always_off", expected: sdktrace.Drop},
		{name: "ratio", sampler: "0.0", expected: sdktrace.Drop},
		{name: "unknown", sampler: "bogus", expected: sdktrace.Drop},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			res := newSampler(tt.sampler).ShouldSample(sdktrace.SamplingParameters{
				ParentContext: context.Background(),
				Name:          "test",
			})
			assert.Equal(t, tt.expected, res.Decision)
		})
	}
}
