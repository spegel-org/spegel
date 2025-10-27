package otelx

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetup_NoOp(t *testing.T) {
	t.Parallel()

	ctx := logr.NewContext(context.Background(), logr.Discard())
	shutdown, err := Setup(ctx, Config{
		ServiceName: "test-service",
		Endpoint:    "http://localhost:4318",
		Sampler:     "always_off",
	})

	require.NoError(t, err)
	// No-op shutdown can be nil, that's fine
	if shutdown != nil {
		err = shutdown(ctx)
		assert.NoError(t, err)
	}
}

func TestWrapHandler_NoOp(t *testing.T) {
	t.Parallel()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	wrapped := WrapHandler("test-handler", handler)
	assert.NotNil(t, wrapped, "wrapped handler should not be nil")
	// In no-op mode, wrapped == handler, so both should be non-nil
	assert.NotNil(t, handler, "original handler should not be nil")
}

func TestWrapTransport_NoOp(t *testing.T) {
	t.Parallel()

	transport := http.DefaultTransport
	wrapped := WrapTransport("test-transport", transport)
	assert.Equal(t, transport, wrapped, "no-op should return original transport")
}

func TestEnrichLogger_NoOp(t *testing.T) {
	t.Parallel()

	log := logr.Discard()
	ctx := context.Background()

	enriched := EnrichLogger(ctx, log)
	assert.Equal(t, log, enriched, "no-op should return original logger")
}

func TestStartSpan_NoOp(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	newCtx, end := StartSpan(ctx, "test-span")

	assert.Equal(t, ctx, newCtx, "no-op should return original context")
	assert.NotNil(t, end)

	// Calling end should not panic
	end()
}

func TestWithEnrichedLogger_NoOp(t *testing.T) {
	t.Parallel()

	log := logr.Discard()
	ctx := context.Background()

	enriched := WithEnrichedLogger(ctx, log)
	assert.Equal(t, log, enriched, "no-op should return original logger")
}

func TestSetupWithDefaults_NoOp(t *testing.T) {
	t.Parallel()

	ctx := logr.NewContext(context.Background(), logr.Discard())
	shutdown, err := SetupWithDefaults(ctx, "test-service")

	require.NoError(t, err)
	// No-op shutdown can be nil, that's fine
	if shutdown != nil {
		err = shutdown(ctx)
		assert.NoError(t, err)
	}
}

// Integration test: verify WrapHandler doesn't break HTTP handlers
func TestWrapHandler_Integration(t *testing.T) {
	t.Parallel()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	wrapped := WrapHandler("integration-test", handler)

	req := http.Request{
		Method: http.MethodGet,
		Header: make(http.Header),
	}
	rr := &testResponseWriter{
		header: make(http.Header),
		status: 0,
		body:   &mockWriter{},
	}

	wrapped.ServeHTTP(rr, &req)

	assert.Equal(t, http.StatusOK, rr.status)
}

// Helper types for testing
type testResponseWriter struct {
	header http.Header
	status int
	body   io.Writer
}

func (tw *testResponseWriter) Header() http.Header       { return tw.header }
func (tw *testResponseWriter) Write(b []byte) (int, error) { return tw.body.Write(b) }
func (tw *testResponseWriter) WriteHeader(code int)        { tw.status = code }

type mockWriter struct{}

func (m *mockWriter) Write(b []byte) (int, error) { return len(b), nil }

