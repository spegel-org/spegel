//go:build !otel

package otelx

import (
	"context"
	"net/http"
	"reflect"
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
	// Compare underlying function entry pointers to avoid func equality
	hp := reflect.ValueOf(handler).Pointer()
	wp := reflect.ValueOf(wrapped).Pointer()
	assert.Equal(t, hp, wp, "noop must return original handler")
}

func TestWrapTransport_NoOp(t *testing.T) {
	t.Parallel()

	transport := http.DefaultTransport
	wrapped := WrapTransport("test-transport", transport)
	assert.Equal(t, transport, wrapped, "no-op should return original transport")
}

func TestWithEnrichedLogger_NoOp(t *testing.T) {
	t.Parallel()

	log := logr.Discard()
	ctx := context.Background()

	enriched := WithEnrichedLogger(ctx, log)
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
