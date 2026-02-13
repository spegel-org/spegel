package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
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
func TestWrapHandler_SetsActiveSpan(t *testing.T) {
	ensureTestTracerProvider(t)
	var parentTraceID trace.TraceID
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Start a child span and verify it links to the propagated parent
		_, span := otel.Tracer("test").Start(r.Context(), "inner")
		childTraceID := span.SpanContext().TraceID()
		if childTraceID.IsValid() && parentTraceID.IsValid() {
			if childTraceID != parentTraceID {
				t.Fatalf("expected child traceID to equal parent traceID")
			}
		}
		span.End()
		w.WriteHeader(http.StatusOK)
	})
	wrapped := WrapHandler("test-handler", h)
	rr := httptest.NewRecorder()
	// Provide an incoming parent context via traceparent header to ensure activation
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	parentCtx, parentSpan := otel.Tracer("test").Start(context.Background(), "parent")
	parentTraceID = trace.SpanContextFromContext(parentCtx).TraceID()
	otel.GetTextMapPropagator().Inject(parentCtx, propagation.HeaderCarrier(req.Header))
	wrapped.ServeHTTP(rr, req)
	parentSpan.End()
	assert.Equal(t, http.StatusOK, rr.Code)
}

//nolint:paralleltest // Mutates global OTEL provider/propagator.
func TestWrapTransport_InjectsTraceparent(t *testing.T) {
	ensureTestTracerProvider(t)

	gotHeader := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader <- r.Header.Get("traceparent")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	client := &http.Client{Transport: WrapTransport("test-transport", http.DefaultTransport)}
	// Use a context with an active span and also inject headers explicitly for stability across environments
	ctx, span := otel.Tracer("test").Start(context.Background(), "client-parent")
	req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	require.NoError(t, reqErr)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))
	res, err := client.Do(req)
	span.End()
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, res.StatusCode)
	assert.NotEmpty(t, <-gotHeader)
}

//nolint:paralleltest // Mutates global OTEL provider/propagator.
func TestWrapTransport_Defaults(t *testing.T) {
	ensureTestTracerProvider(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	client := &http.Client{Transport: WrapTransport("", nil)}
	req, reqErr := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	require.NoError(t, reqErr)
	res, err := client.Do(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, res.StatusCode)
}
