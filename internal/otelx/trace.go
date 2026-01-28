package otelx

import (
	"context"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

// WithEnrichedLogger adds trace correlation fields and returns a derived logger.
func WithEnrichedLogger(ctx context.Context, log logr.Logger) logr.Logger {
	span := trace.SpanFromContext(ctx)
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
func StartSpan(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, func()) {
	tracer := otel.Tracer("github.com/spegel-org/spegel")
	ctx, span := tracer.Start(ctx, name, opts...)
	return ctx, func() { span.End() }
}
