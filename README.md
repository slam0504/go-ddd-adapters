# go-ddd-adapters

Concrete adapter implementations for the [`go-ddd-core`][core] port
interfaces. The core ships interfaces only; this repository hosts the
infra-bound implementations so downstream services can wire a real stack
without re-inventing the plumbing.

[core]: https://github.com/slam0504/go-ddd-core

## Status

`v0.3.0` is the latest tagged release on the v0.3.x line and aligns
this repo with `go-ddd-core v0.3.0`. v0.3.0 brings the in-process
`Memory` Inbox (relocated from core) and the new in-process Outbox +
Relay (with adapter-private DLQ and a kafka header-restorer bridge),
plus the unchanged Kafka publisher/subscriber/codec, slog logger, and
OpenTelemetry provider that already shipped in `v0.2.0`. The `Memory`
Outbox is explicitly a non-transactional test/dev adapter.

`main` (heading into the v0.4.0 cycle) adds the production-ready pgx
successor: the `eventbus/outbox/pgx` package implements a
transactional Outbox + OutboxStore + DLQ backed by Postgres 12+ via
pgx/v5, paired with `ports/database/pgx` providing the
`database.TxManager` adapter that lets `Stage` participate in the
caller's database transaction.

| Adapter | Port | Backing tech |
| --- | --- | --- |
| `eventbus/kafka` | `eventbus.Publisher`, `eventbus.Subscriber`, `eventbus.Codec` | [watermill-kafka v3][wmk] (Sarama) |
| `eventbus/inbox` | `eventbus.Inbox` | in-process map (`Memory`) with optional `WithMaxSize` and `WithTTL` eviction |
| `eventbus/outbox` | `eventbus.Outbox`, `eventbus.OutboxStore`, `eventbus.Relay` | in-process `Memory` + polling `Relay` — **non-transactional test/dev adapter, not for production** |
| `eventbus/outbox/pgx` | `eventbus.Outbox`, `eventbus.OutboxStore`, `outbox.DeadLetterRecorder` | [pgx/v5][pgx] + Postgres 12+; lease-based claim with `FOR UPDATE SKIP LOCKED`, separate `outbox_dead_letters` table, safe for multiple Relay instances |
| `ports/database/pgx` | `database.TxManager` | [pgx/v5][pgx] pool + ctx-bound transaction handle (`pgxdb.WithTx` / `pgxdb.TxFromContext` / `pgxdb.Executor`) |
| `logger/slogger` | `logger.Logger` | `log/slog` (stdlib) |
| `observability/otel` | `observability.Provider` | OpenTelemetry SDK v1.32 |

[wmk]: https://github.com/ThreeDotsLabs/watermill-kafka
[pgx]: https://github.com/jackc/pgx

## Compatibility matrix

| `go-ddd-adapters` | `go-ddd-core` | Go |
| --- | --- | --- |
| `main` (post-`v0.3.0`) | `v0.3.x` | `>= 1.25` |
| `v0.3.0` | `v0.3.0` | `>= 1.24` |
| `v0.2.x` | `v0.2.x` | `>= 1.24` |

`main` bumped the Go floor from 1.24 to 1.25 when adding the
`eventbus/outbox/pgx` adapter — its dependency tree
(`pgx/v5 v5.9.2`, `testcontainers-go v0.42.0`,
`golang-migrate/v4 v4.19.1`, current OpenTelemetry releases) requires
`go 1.25.0`. Tagged `v0.3.x` and `v0.2.x` are unaffected.

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

### Postgres Outbox + Relay (`eventbus/outbox/pgx`)

The pgx adapter is the production-shaped Outbox: `Stage` runs inside
the caller's database transaction, so aggregate persistence and event
staging commit (or roll back) atomically. The same `outbox.Relay`
drains the active table.

```go
pool, err := pgxpool.New(ctx, "postgres://user:pass@host:5432/db")
if err != nil { /* handle */ }
defer pool.Close()

tm := pgxdb.NewTxManager(pool)

codec := kafka.NewJSONCodec()
codec.Register("order.placed.v1", func() domain.DomainEvent { return &OrderPlaced{} })

store, _ := pgxoutbox.NewStore(pgxoutbox.Config{Pool: pool, Codec: codec},
    pgxoutbox.WithClaimLease(10*time.Second),
)

// Producer side — aggregate save AND Stage commit together. Stage
// reads the *pgx.Tx out of ctx and refuses (ErrNoTx) if there is
// none. No silent autocommit.
err = tm.WithinTx(ctx, func(ctx context.Context) error {
    if err := repo.Save(ctx, agg); err != nil { return err }
    return store.Stage(ctx, "orders", agg.PullEvents()...)
})

// Relay side — drains the active table; identical wiring to the
// memory Relay (decision: Relay is driver-agnostic).
pub, _ := kafka.NewPublisher(kafka.PublisherConfig{Brokers: []string{"kafka:9092"}, Codec: codec})
relay, _ := outbox.NewRelay(outbox.RelayConfig{
    Store: store, Publisher: pub, Codec: codec, Logger: log,
},
    outbox.WithMaxAttempts(10),
    outbox.WithHeaderRestorer(kafka.RestoreCoreHeaders),
)
```

Operational properties:

- **Transactional Stage.** `Stage` requires a `pgx.Tx` in ctx (put
  there by `pgxdb.TxManager.WithinTx`); otherwise it returns
  `pgxoutbox.ErrNoTx`. Aggregate save + event stage are atomic.
- **Safe for multiple Relay instances.** `Fetch` uses `FOR UPDATE
  SKIP LOCKED`; concurrent Relays claim disjoint rows. There is no
  fairness guarantee — one Relay can claim more rows than another in
  any given drain pass — only the no-overlap invariant.
- **At-least-once delivery.** Lease expiry, slow `Publisher.Publish`,
  or a worker crash between Publish and MarkSent can produce
  duplicate publishes. Downstream consumers MUST deduplicate via
  `eventbus/inbox` (or equivalent) keyed on `OutboxRecord.EventID`.
  Tuning `WithClaimLease` reduces the duplicate window but does NOT
  eliminate it — never claim exactly-once.
- **Postgres baseline 12+.** Migration `001` includes
  `CREATE EXTENSION IF NOT EXISTS pgcrypto` so `gen_random_uuid()` is
  available on 12 (no-op on 13+).
- **Schema is yours.** The adapter ships the SQL files and an
  `embed.FS` for tests, but does NOT run migrations at runtime — see
  the [Migrations](#migrations) section.

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

## Migrations

The `eventbus/outbox/pgx` adapter ships its Postgres schema as
versioned `.up.sql` / `.down.sql` files under
`eventbus/outbox/pgx/migrations/`. The adapter does NOT run
migrations at runtime — schema management is the caller's
responsibility.

Canonical command using [golang-migrate][gm]:

```sh
migrate -path eventbus/outbox/pgx/migrations \
        -database "postgres://user:pass@host:5432/db?sslmode=disable" \
        up
```

The same SQL files work with [goose][goose], [atlas][atlas],
[flyway][flyway], or any tool that consumes numerically-prefixed
versioned SQL — pick whatever already exists in your stack.

For tests and example wiring, the adapter exposes the embedded files
via `eventbus/outbox/pgx/migrations.FS` (an `embed.FS`), usable with
golang-migrate's `iofs` source driver.

[gm]: https://github.com/golang-migrate/migrate
[goose]: https://github.com/pressly/goose
[atlas]: https://atlasgo.io
[flyway]: https://flywaydb.org

## License

MIT — see [LICENSE](LICENSE).
