# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

#### Kafka eventbus (`eventbus/kafka`)
- `JSONCodec` with type registry — Marshal populates the six identity
  headers (`event_id`, `event_name`, `aggregate_id`, `aggregate_type`,
  `occurred_at` as RFC3339Nano, `event_version`) and pass-through reads
  `trace_id` / `causation_id` / `correlation_id` from context if set via
  `WithTraceID` / `WithCausationID` / `WithCorrelationID`.
- `Publisher` wrapping `watermill-kafka` v3 with optional
  `PartitionByAggregate` for per-aggregate ordering.
- `Subscriber` wrapping `watermill-kafka` v3, decoding into
  `eventbus.Envelope` and forwarding raw `*message.Message` so callers
  retain ack/nack control.
- Public context-key helpers (`WithTraceID`, `TraceIDFrom`, …) for
  trace/correlation pass-through.

#### Slog logger (`logger/slogger`)
- `Logger` implementing `logger.Logger` over `log/slog`.
- Config-driven `New` (writer, level, format `json`/`text`, source
  attribution) plus `NewWithHandler` escape hatch.

#### OpenTelemetry provider (`observability/otel`)
- `Provider` implementing `observability.Provider`. Caller supplies
  `SpanExporter` and `MetricReader`; the adapter does not depend on any
  exporter package, leaving exporter choice (OTLP, stdout, Jaeger…) up
  to the consumer.
- Default propagator is the W3C composite of TraceContext + Baggage.
- `Shutdown` flushes both providers and reports the first error
  encountered.

### Notes

- Single Go module. Per-adapter `go.mod` splitting is deferred until
  dependency footprint becomes a real concern.
- Adapters are constructors only; no global registration. A bootstrap
  registry will arrive in a later release alongside the realistic
  example service.

[Unreleased]: https://github.com/slam0504/go-ddd-adapters/compare/HEAD...main
