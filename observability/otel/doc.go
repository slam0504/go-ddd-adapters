// Package otel provides an observability.Provider backed by the OpenTelemetry
// Go SDK. The adapter is exporter-agnostic: the caller injects a SpanExporter
// and/or MetricReader, which keeps gRPC, HTTP, OTLP, and Prometheus
// dependencies out of consumers that do not need them.
//
// The provider does not register itself as the OTel global. To use the
// global API surface (otel.Tracer, otel.GetTextMapPropagator, ...), wire it
// explicitly:
//
//	prov, _ := otel.New(ctx, otel.Config{ServiceName: "billing"})
//	otelglobal.SetTracerProvider(prov.tracerProvider())
//	otelglobal.SetTextMapPropagator(prov.TextMapPropagator())
//
// (the unexported accessor is intentional — most apps should resolve the
// provider through dependency injection rather than the global singleton.)
package otel
