package observ

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/trace"
)

func TestSetupTracingEnabledExportsSpans(t *testing.T) {
	var mutex sync.Mutex
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || !strings.HasSuffix(request.URL.Path, "/v1/traces") {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.Copy(io.Discard, request.Body)
		writer.WriteHeader(http.StatusOK)
		mutex.Lock()
		requests++
		mutex.Unlock()
	}))
	defer server.Close()
	shutdown, err := SetupTracing(context.Background(), true,
		otlptracehttp.WithEndpoint(strings.TrimPrefix(server.URL, "http://")),
		otlptracehttp.WithInsecure())
	if err != nil {
		t.Fatal(err)
	}
	_, span := otel.Tracer("event-radar").Start(context.Background(), "observ.test")
	span.End()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	mutex.Lock()
	defer mutex.Unlock()
	if requests < 1 {
		t.Fatal("fake OTLP receiver received no export request")
	}
}

func TestSetupTracingDisabledIsNoop(t *testing.T) {
	shutdown, err := SetupTracing(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if shutdown == nil {
		t.Fatal("shutdown func is nil")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("disabled shutdown returned error: %v", err)
	}
}

type capturingHandler struct {
	records []slog.Record
}

func (c *capturingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (c *capturingHandler) Handle(_ context.Context, record slog.Record) error {
	c.records = append(c.records, record)
	return nil
}

func (c *capturingHandler) WithAttrs([]slog.Attr) slog.Handler { return c }

func (c *capturingHandler) WithGroup(string) slog.Handler { return c }

func recordAttrs(record slog.Record) map[string]string {
	attrs := map[string]string{}
	record.Attrs(func(attr slog.Attr) bool {
		attrs[attr.Key] = attr.Value.String()
		return true
	})
	return attrs
}

func TestTraceHandlerAddsSpanIDs(t *testing.T) {
	spanContext := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    [16]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10},
		SpanID:     [8]byte{0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x11, 0x12},
		TraceFlags: trace.FlagsSampled,
	})
	capture := &capturingHandler{}
	logger := slog.New(NewTraceHandler(capture))
	logger.InfoContext(trace.ContextWithSpanContext(context.Background(), spanContext), "correlated")
	logger.InfoContext(context.Background(), "uncorrelated")
	if len(capture.records) != 2 {
		t.Fatalf("records = %d, want 2", len(capture.records))
	}
	correlated := recordAttrs(capture.records[0])
	if correlated["trace_id"] != spanContext.TraceID().String() || correlated["span_id"] != spanContext.SpanID().String() {
		t.Fatalf("correlated attrs = %#v, want trace_id %s span_id %s", correlated, spanContext.TraceID().String(), spanContext.SpanID().String())
	}
	plain := recordAttrs(capture.records[1])
	if _, ok := plain["trace_id"]; ok {
		t.Fatalf("plain context leaked trace_id: %#v", plain)
	}
	if _, ok := plain["span_id"]; ok {
		t.Fatalf("plain context leaked span_id: %#v", plain)
	}
}

func TestTraceHandlerNoSpanPlainContext(t *testing.T) {
	capture := &capturingHandler{}
	slog.New(NewTraceHandler(capture)).InfoContext(context.Background(), "no span")
	if len(capture.records) != 1 {
		t.Fatalf("records = %d, want 1", len(capture.records))
	}
	for key := range recordAttrs(capture.records[0]) {
		if key == "trace_id" || key == "span_id" {
			t.Fatalf("unexpected %q attr without span context", key)
		}
	}
}

func TestTraceHandlerJSONOutputCarriesTraceID(t *testing.T) {
	spanContext := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    [16]byte{0xde, 0xad, 0xbe, 0xef, 0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb},
		SpanID:     [8]byte{0xc0, 0xff, 0xee, 0x00, 0x01, 0x02, 0x03, 0x04},
		TraceFlags: trace.FlagsSampled,
	})
	var buffer bytes.Buffer
	logger := slog.New(NewTraceHandler(slog.NewJSONHandler(&buffer, nil)))
	logger.InfoContext(trace.ContextWithSpanContext(context.Background(), spanContext), "loki correlation")
	output := buffer.String()
	if !strings.Contains(output, `"trace_id":"`+spanContext.TraceID().String()+`"`) {
		t.Fatalf("JSON output missing trace_id: %s", output)
	}
	if !strings.Contains(output, `"span_id":"`+spanContext.SpanID().String()+`"`) {
		t.Fatalf("JSON output missing span_id: %s", output)
	}
}
