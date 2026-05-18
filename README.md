# go-ddd-adapters

Concrete adapter implementations for the [`go-ddd-core`][core] port
interfaces. The core ships interfaces only; this repository hosts the
infra-bound implementations so downstream services can wire a real stack
without re-inventing the plumbing.

[core]: https://github.com/slam0504/go-ddd-core

## Status

In development. `main` is aligned with `go-ddd-core v0.3.0`. v0.3.0
introduced contract changes inside core (Inbox key, Outbox event ID,
Unit-of-Work bridge), but those ports are not yet implemented in this
repo — only event bus, logger, and observability adapters ship today —
so the bump landed without migration work in adapter code. The adapter
surface remains a starter set covering the three cross-cutting concerns
most DDD services need before anything else.

| Adapter | Port | Backing tech |
| --- | --- | --- |
| `eventbus/kafka` | `eventbus.Publisher`, `eventbus.Subscriber`, `eventbus.Codec` | [watermill-kafka v3][wmk] (Sarama) |
| `eventbus/inbox` | `eventbus.Inbox` | in-process map (`Memory`) with optional `WithMaxSize` and `WithTTL` eviction |
| `logger/slogger` | `logger.Logger` | `log/slog` (stdlib) |
| `observability/otel` | `observability.Provider` | OpenTelemetry SDK v1.32 |

[wmk]: https://github.com/ThreeDotsLabs/watermill-kafka

## Compatibility matrix

| `go-ddd-adapters` | `go-ddd-core` | Go |
| --- | --- | --- |
| `main` (untagged, post `v0.2.0`) | `v0.3.x` | `>= 1.24` |
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
