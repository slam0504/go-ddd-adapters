# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [v0.3.0] - 2026-05-19

First tagged release on the v0.3.x line. Aligns adapters with
`go-ddd-core v0.3.0` and lands the inbox + outbox adapter packages
plus the kafka outbox-relay header bridge. No breaking changes to
adapters that existed in `v0.2.0` (Kafka publisher / subscriber /
codec, slog logger, OpenTelemetry provider) — all v0.3.0 additions
are new packages or new exported symbols.

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
- `RestoreCoreHeaders` — an `outbox.HeaderRestorer` callback that
  re-injects the three core well-known propagation headers
  (`trace_id` / `causation_id` / `correlation_id`) from a stored
  `OutboxRecord.Headers` map back into ctx via this package's
  `WithTraceID` / `WithCausationID` / `WithCorrelationID` helpers.

#### Inbox eventbus (`eventbus/inbox`)
- New package providing the in-process `Memory` Inbox previously
  shipped at `go-ddd-core/eventbus/inbox/memory.go` (v0.2.0 / v0.3.0).
  Downstream services migrate by changing the import path from
  `go-ddd-core/eventbus/inbox` to
  `go-ddd-adapters/eventbus/inbox`; call sites
  (`inbox.NewMemory(...)`) stay identical.
- `WithMaxSize(limit int)` bounds the dedup map by evicting the
  oldest half whenever the map reaches `limit`. Default is
  unbounded.
- `WithTTL(d time.Duration)` adds time-based dedup expiry. Lazy
  filtering at read time (no write-side contention); `Record`
  overwrites an expired entry in place; expired entries are
  reclaimed only when `WithMaxSize`-triggered eviction fires.
  Strict-`>` boundary: `age == ttl` is still fresh,
  `age == ttl + 1ns` is expired.
- `WithClock(now func() time.Time)` for deterministic tests.
- Core retains its copy of `eventbus/inbox/memory.go` for one more
  release cycle; the planned removal there is tracked as an open
  follow-up (see `.agent/state.md`).

#### Outbox eventbus (`eventbus/outbox`)
- New package providing in-process implementations of core's outbox
  contracts. **`Memory` is a non-transactional test/dev adapter only.**
  It implements `eventbus.Outbox` for shape compatibility but does NOT
  participate in the caller's database transaction (only in-process
  mutex safety inside its own store). Use it for tests, single-instance
  examples, and exercising Relay/backoff/DLQ behaviour — not for
  production. The forthcoming SQL/pgx successor will deliver a real
  transactional Outbox.
- `Memory` satisfies `eventbus.Outbox`, `eventbus.OutboxStore`, and
  the local `DeadLetterRecorder` extension. Options:
  `WithMaxSize` (returns `ErrOutboxFull` on overflow; never evicts
  unsent records), `WithClock`, `WithIDGenerator`. Stage is
  all-or-nothing: marshal failures abort the batch; canonical
  `EventName / AggregateID / AggregateType` come from the
  `domain.DomainEvent` interface methods, not from codec metadata.
- `Relay` is a driver-agnostic polling drainer. Options:
  `WithPollInterval`, `WithBatchSize`, `WithBackoff` (default
  exponential with jitter, capped at 60s), `WithMaxAttempts` (default
  10; 0 = unlimited), `WithRelayClock`, `WithHeaderRestorer`. Per-
  record `defer recover` routes panics through the same `fail()` path
  that handles publish errors, so MaxAttempts / DLQ still apply.
  `ErrRelayAlreadyRunning` guards same-`*Relay` reentry; multi-Relay
  against one Memory is the user's responsibility.
- `DeadLetterRecord` carries `Attempts` (terminal count =
  `Record.Attempts + 1`), `Reason`, and `FailedAt`. `Memory.DeadLettered()`
  returns a defensive-copy snapshot for inspection.
- `RelayModule(*Relay, logger.Logger)` wraps Relay into a
  `bootstrap.ModuleFunc` with the kafka-consumer-style detach-then-
  cancel lifecycle: Start spawns one goroutine; Stop cancels and
  waits bounded by `stopCtx`; `context.Canceled` is a silent clean
  shutdown.

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

[Unreleased]: https://github.com/slam0504/go-ddd-adapters/compare/v0.3.0...HEAD
[v0.3.0]: https://github.com/slam0504/go-ddd-adapters/compare/v0.2.0...v0.3.0
