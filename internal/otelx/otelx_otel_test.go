//go:build otel

package otelx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-logr/logr/funcr"
	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// ensureTestTracerProvider sets a global tracer provider that always samples.
func ensureTestTracerProvider(t *testing.T) {
	t.Helper()
	tp := trace.NewTracerProvider(trace.WithSampler(trace.AlwaysSample()))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
}

func TestStartSpan_Otel(t *testing.T) {
	t.Parallel()
	ensureTestTracerProvider(t)
	ctx := context.Background()
	newCtx, end := StartSpan(ctx, "test-span")
	assert.NotNil(t, end)
	assert.NotEqual(t, ctx, newCtx)
	end()
}

func TestWithEnrichedLogger_AddsTraceFields(t *testing.T) {
	t.Parallel()
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

func TestWrapHandler_SetsActiveSpan(t *testing.T) {
	t.Parallel()
	ensureTestTracerProvider(t)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify wrapper installed an active span in the request context
		s := oteltrace.SpanFromContext(r.Context())
		if !s.SpanContext().IsValid() || !s.IsRecording() {
			t.Fatalf("expected valid recording span in context")
		}
		// Start and end a child span to exercise parent linkage
		_, span := otel.Tracer("test").Start(r.Context(), "inner")
		span.End()
		w.WriteHeader(http.StatusOK)
	})
	wrapped := WrapHandler("test-handler", h)
	rr := httptest.NewRecorder()
	// Provide an incoming parent context via traceparent header to ensure activation
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	parentCtx, parentSpan := otel.Tracer("test").Start(context.Background(), "parent")
	otel.GetTextMapPropagator().Inject(parentCtx, propagation.HeaderCarrier(req.Header))
	wrapped.ServeHTTP(rr, req)
	parentSpan.End()
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestWrapTransport_InjectsTraceparent(t *testing.T) {
	t.Parallel()
	ensureTestTracerProvider(t)

	gotHeader := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader <- r.Header.Get("traceparent")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	client := &http.Client{Transport: WrapTransport("test-transport", http.DefaultTransport)}
	// Use a context with an active span so transport will inject traceparent
	ctx, span := otel.Tracer("test").Start(context.Background(), "client-parent")
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	res, err := client.Do(req)
	span.End()
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, res.StatusCode)
	assert.NotEmpty(t, <-gotHeader)
}
