package observ

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// NewTraceHandler wraps h so log records emitted with a context carrying a
// valid span include trace_id/span_id string attributes. Records without a
// span are passed through unchanged.
func NewTraceHandler(h slog.Handler) slog.Handler {
	return traceHandler{inner: h}
}

type traceHandler struct {
	inner slog.Handler
}

func (t traceHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return t.inner.Enabled(ctx, level)
}

func (t traceHandler) Handle(ctx context.Context, record slog.Record) error {
	if spanContext := trace.SpanContextFromContext(ctx); spanContext.IsValid() {
		record.AddAttrs(
			slog.String("trace_id", spanContext.TraceID().String()),
			slog.String("span_id", spanContext.SpanID().String()),
		)
	}
	return t.inner.Handle(ctx, record)
}

func (t traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return NewTraceHandler(t.inner.WithAttrs(attrs))
}

func (t traceHandler) WithGroup(name string) slog.Handler {
	return NewTraceHandler(t.inner.WithGroup(name))
}
