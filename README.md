# go-ddd-adapters

Concrete adapter implementations for the [`go-ddd-core`][core] port
interfaces. The core ships interfaces only; this repository hosts the
infra-bound implementations so downstream services can wire a real stack
without re-inventing the plumbing.

[core]: https://github.com/slam0504/go-ddd-core

## Status

`v0.3.0` is the first tagged release on the v0.3.x line and aligns
this repo with `go-ddd-core v0.3.0`. v0.3.0 brings the in-process
`Memory` Inbox (relocated from core) and the new in-process Outbox +
Relay (with adapter-private DLQ and a kafka header-restorer bridge),
plus the unchanged Kafka publisher/subscriber/codec, slog logger, and
OpenTelemetry provider that already shipped in `v0.2.0`. The `Memory`
Outbox is explicitly a non-transactional test/dev adapter — production
users need the forthcoming SQL/pgx successor.

| Adapter | Port | Backing tech |
| --- | --- | --- |
| `eventbus/kafka` | `eventbus.Publisher`, `eventbus.Subscriber`, `eventbus.Codec` | [watermill-kafka v3][wmk] (Sarama) |
| `eventbus/inbox` | `eventbus.Inbox` | in-process map (`Memory`) with optional `WithMaxSize` and `WithTTL` eviction |
| `eventbus/outbox` | `eventbus.Outbox`, `eventbus.OutboxStore`, `eventbus.Relay` | in-process `Memory` + polling `Relay` — **non-transactional test/dev adapter, not for production** |
| `logger/slogger` | `logger.Logger` | `log/slog` (stdlib) |
| `observability/otel` | `observability.Provider` | OpenTelemetry SDK v1.32 |

[wmk]: https://github.com/ThreeDotsLabs/watermill-kafka

## Compatibility matrix

| `go-ddd-adapters` | `go-ddd-core` | Go |
| --- | --- | --- |
| `main` (post-`v0.3.0`) | `v0.3.x` | `>= 1.24` |
| `v0.3.0` | `v0.3.0` | `>= 1.24` |
| `v0.2.x` | `v0.2.x` | `>= 1.24` |

## Install

```sh
go get github.com/slam0504/go-ddd-adapters@latest
```

You only pay for what you import — each adapter lives under its own
package, so e.g. importing `logger/slog` does not pull Kafka or OTel SDK
dependencies into your binary.

## Usage sketches

### Kafka publisher

```go
codec := kafka.NewJSONCodec()
codec.Register("game.submitted.v1", func() domain.DomainEvent { return &GameSubmitted{} })

pub, err := kafka.NewPublisher(kafka.PublisherConfig{
    Brokers:              []string{"kafka:9092"},
    Codec:                codec,
    PartitionByAggregate: true,
})
if err != nil { /* handle */ }
defer pub.Close()

_ = pub.Publish(ctx, "game-events", evt)
```

### In-process Inbox

```go
in := inbox.NewMemory(
    inbox.WithMaxSize(10_000),       // bound the dedup map
    inbox.WithTTL(24*time.Hour),     // and/or time-based expiry
)

k := eventbus.InboxKey{Consumer: "projector", EventID: evt.EventID()}
seen, _ := in.Seen(ctx, k)
if seen {
    return nil // already processed
}
if err := handler(ctx, evt); err != nil {
    return err
}
return in.Record(ctx, k)
```

The `Inbox` interface itself lives in
`github.com/slam0504/go-ddd-core/eventbus`; this adapter ships the
in-process default that previously lived in `go-ddd-core` v0.2.0–v0.3.0.

### In-process Outbox + Relay

> **Warning — non-transactional.** This `Memory` outbox implements the
> `eventbus.Outbox` interface but does **not** participate in the
> caller's database transaction. It only provides in-process mutex
> safety inside its own store. If aggregate persistence succeeds but
> `Stage` is not called, or the process crashes between domain `Save`
> and `Stage`, the corresponding event is lost. Use this adapter for
> tests, single-instance examples, and local development — not for
> production at-least-once delivery.

```go
codec := kafka.NewJSONCodec()
codec.Register("order.placed.v1", func() domain.DomainEvent { return &OrderPlaced{} })

// Producer side — Stage runs inside (or alongside) the aggregate save.
ob, _ := outbox.NewMemory(outbox.MemoryConfig{Codec: codec},
    outbox.WithMaxSize(10_000), // ErrOutboxFull when backlog exceeds limit
)
_ = ob.Stage(ctx, "orders", evt)

// Relay side — drains the store and publishes via Kafka.
pub, _ := kafka.NewPublisher(kafka.PublisherConfig{Brokers: []string{"kafka:9092"}, Codec: codec})
relay, _ := outbox.NewRelay(outbox.RelayConfig{
    Store: ob, Publisher: pub, Codec: codec, Logger: log,
},
    outbox.WithMaxAttempts(10),
    // Re-promote stored trace_id / causation_id / correlation_id back
    // into the publish ctx so the kafka codec re-emits them.
    outbox.WithHeaderRestorer(kafka.RestoreCoreHeaders),
)

// Wire into bootstrap so Stop drains the relay before shutdown.
app.Use(kafka.PublisherModule(pub))
app.Use(outbox.RelayModule(relay, log))
```

Records that exceed `WithMaxAttempts` are moved to an adapter-private
dead-letter quarantine; inspect via `ob.DeadLettered()`.

### Slog logger

```go
log := slogger.New(slogger.Config{Level: slog.LevelInfo})
log.Log(ctx, logger.LevelInfo, "service started", logger.Attr{Key: "addr", Value: ":8080"})
```

### OTel provider

```go
prov, err := otelad.New(ctx, otelad.Config{
    ServiceName:    "billing",
    ServiceVersion: "1.4.2",
    SpanExporter:   exp, // your own otlptracegrpc / stdout / jaeger exporter
})
if err != nil { /* handle */ }
defer prov.Shutdown(ctx)

tr := prov.Tracer("billing")
```

## License

MIT — see [LICENSE](LICENSE).
