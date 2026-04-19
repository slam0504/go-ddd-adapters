// Package otelexp builds an OTLP/gRPC SpanExporter aimed at a Jaeger
// collector. The observability/otel adapter is exporter-agnostic so the
// example wires its own exporter here, in one place, and the three cmd
// binaries reuse it.
package otelexp

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// New dials the OTLP/gRPC endpoint (e.g. "jaeger:4317") and returns a
// configured SpanExporter. The example uses WithInsecure because the
// containerised collector has no TLS; production code would not.
func New(ctx context.Context, endpoint string) (sdktrace.SpanExporter, error) {
	exp, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("otelexp: dial %s: %w", endpoint, err)
	}
	return exp, nil
}
