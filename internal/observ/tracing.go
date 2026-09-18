// Package observ wires optional OpenTelemetry tracing into the process.
package observ

import (
	"context"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// SetupTracing initializes the global OTel tracer provider when tracing is enabled.
// It returns a shutdown func to flush/export pending spans. When disabled it does
// nothing (no otel globals touched) and returns a no-op shutdown.
func SetupTracing(ctx context.Context, enabled bool, options ...otlptracehttp.Option) (func(context.Context) error, error) {
	if !enabled {
		return func(context.Context) error { return nil }, nil
	}
	exporter, err := otlptracehttp.New(ctx, options...)
	if err != nil {
		return nil, err
	}
	var resourceOptions []resource.Option
	if os.Getenv("OTEL_SERVICE_NAME") != "" {
		resourceOptions = []resource.Option{resource.WithFromEnv(), resource.WithTelemetrySDK()}
	} else {
		resourceOptions = []resource.Option{resource.WithTelemetrySDK(), resource.WithAttributes(attribute.String("service.name", "event-radar"))}
	}
	res, err := resource.New(ctx, resourceOptions...)
	if err != nil {
		return nil, err
	}
	provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(res))
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	return provider.Shutdown, nil
}
