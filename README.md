# go-ddd-adapters

Concrete adapter implementations for the [`go-ddd-core`][core] port
interfaces. The core ships interfaces only; this repository hosts the
infra-bound implementations so downstream services can wire a real stack
without re-inventing the plumbing.

[core]: https://github.com/slam0504/go-ddd-core

## Status

In development. Next release will be `v0.2.0` to align with `go-ddd-core`'s
breaking API changes (Inbox key, Outbox event ID, Unit-of-Work bridge). The
adapters surface itself remains a starter set covering the three
cross-cutting concerns most DDD services need before anything else.

| Adapter | Port | Backing tech |
| --- | --- | --- |
| `eventbus/kafka` | `eventbus.Publisher`, `eventbus.Subscriber`, `eventbus.Codec` | [watermill-kafka v3][wmk] (Sarama) |
| `logger/slogger` | `logger.Logger` | `log/slog` (stdlib) |
| `observability/otel` | `observability.Provider` | OpenTelemetry SDK v1.32 |

[wmk]: https://github.com/ThreeDotsLabs/watermill-kafka

## Compatibility matrix

| `go-ddd-adapters` | `go-ddd-core` | Go |
| --- | --- | --- |
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
