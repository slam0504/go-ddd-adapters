# examples/orders pgx outbox demo — Design

- **Status**: DRAFT, awaiting user review before writing-plans hand-off
- **Date**: 2026-05-21 (Asia/Taipei)
- **Branch**: `feat/examples-orders-pgx-outbox`
- **Brainstorm session**: this document
- **Implementation plan**: TBD, produced by `writing-plans` skill after this spec is approved

---

## 1. Goal

Upgrade `examples/orders` from its current "save then publish" path to a
transactional outbox demo built on the v0.4.0 `eventbus/outbox/pgx` +
`ports/database/pgx` adapters. The demo must teach **one** thing clearly:
aggregate write and outbox row commit in the same Postgres transaction;
a separate process publishes from the outbox to Kafka.

## 2. In scope

- `cmd/api` writes through `pgxrepo` and stages `OrderPlaced` via
  `outbox.Stage` inside an `application.UnitOfWork`.
- `cmd/worker` consumes `orders.placed` from Kafka, dispatches
  `ShipOrderCommand`, and the handler writes through `pgxrepo` + stages
  `OrderShipped` via `outbox.Stage` inside the same UoW pattern.
- New `cmd/relay` binary drains `outbox_records` and publishes to Kafka.
  Wiring-only; no new generic relay adapter.
- `docker-compose.yml` gains a Postgres service, two `migrate/migrate`
  init services (one per migration source), and a `orders-relay`
  service.
- Four targeted integration tests: PlaceOrder happy / rollback, ShipOrder
  happy / rollback.
- README rewrite: remove the "No outbox" and "Per-process in-memory
  repos" shortcuts; add a "Remaining intentional shortcuts" section
  acknowledging reader projection / inbox dedup / exactly-once as
  separate follow-ups.

## 3. Non-goals (locked)

- No pgx read projection — `cmd/reader` keeps its in-memory projection.
- No durable Inbox / consumer-side dedup. Worker stays at at-least-once
  from broker.
- No exactly-once claim anywhere; the pgx outbox contract is explicitly
  at-least-once.
- `cmd/relay` is **not** a new generic adapter package; it only composes
  `eventbus/outbox.Relay` + `eventbus/outbox/pgx.Store` + `kafka.Publisher`
  + `kafka.RestoreCoreHeaders`.
- `orders.version` column is reserved but not used for optimistic
  locking. The behaviour is last-write-wins.
- `OrderPlaced` event payload does NOT gain an `Items` field. Worker
  uses `repo.FindByID` to read the aggregate, not event-hydration.
- `cmd/worker` transaction must cover **only** aggregate mutation +
  outbox.Stage — Kafka consumer ack/nack must NOT enter the DB tx.

## 4. Architecture

### 4.1 Topology

```
                      ┌──────────────────────────────────────────────┐
                      │            Postgres (port 5432)               │
                      │   orders / outbox_records /                   │
                      │   outbox_dead_letters / outbox_schema_migrations / │
                      │   orders_schema_migrations                    │
                      └──────────────────────────────────────────────┘
                              ▲              ▲
                              │              │ FOR UPDATE SKIP LOCKED
                              │ Tx           │ + claim_token
       ┌─────────────────┐    │              │
HTTP ──▶ cmd/api         │────┘              │
       │ - pgxpool       │                   │
       │ - UoW(pgxdb)    │                   │
       │ - pgxOrderRepo  │                   │
       │ - pgxoutbox.Store│                  │
       └─────────────────┘                   │
                                             │
       ┌─────────────────┐                   │
       │ cmd/worker      │ ──── consume ──── │
       │  (Kafka sub)    │                   │
       │ - pgxpool       │ ◀─────────────────┤
       │ - UoW(pgxdb)    │                   │
       │ - pgxOrderRepo  │                   │
       │ - pgxoutbox.Store│                  │
       └─────────────────┘                   │
                ▲                            │
                │ orders.placed              │
                │                            │
       ┌────────┴────────┐         ┌─────────┴─────────┐
       │     Kafka       │ ◀─ publish ─│  cmd/relay        │
       │   (redpanda)    │             │ - pgxpool         │
       └────────┬────────┘             │ - pgxoutbox.Store │
                │ orders.placed        │ - outbox.Relay    │
                │ orders.shipped       │ - kafka.Publisher │
                ▼                      │ - RestoreCoreHdrs │
       ┌─────────────────┐             └───────────────────┘
       │ cmd/reader      │ (memrepo projection, unchanged)
       │  HTTP GET       │
       └─────────────────┘
```

### 4.2 Invariants (will be stated in spec, README, and `.agent/decisions.md`)

1. `cmd/api` has **no Kafka dependency** — no publisher, no subscriber.
2. `cmd/worker` has Kafka subscriber dependency only; **no Kafka
   publisher dependency**.
3. **All outbound domain events flow through `eventbus.Outbox.Stage`.**
   `cmd/api` and `cmd/worker` never call `Publisher.Publish` directly.
4. **Only `cmd/relay` owns Kafka publishing** and `MarkSent` /
   `MarkFailed` / `Terminate`.
5. Transaction boundary frames only `repo.Save(...)` +
   `outbox.Stage(...)`. Domain logic, codec marshal, command bus
   dispatch, and Kafka consumer ack/nack are all outside the tx.
6. Header propagation is automatic via the existing codec
   (`kafka.WithTraceID` / `WithCausationID` / `WithCorrelationID`
   helpers populate ctx; `codec.Marshal` writes them into
   `OutboxRecord.Headers`; relay's `kafka.RestoreCoreHeaders` puts
   them back into ctx; publisher writes them as Kafka headers).
7. `cmd/relay` is demo runtime wiring, not a new adapter. No new
   generic package under `eventbus/`.

## 5. Schema and migrations

### 5.1 Business schema

`examples/orders/migrations/001_create_orders.up.sql`:

```sql
CREATE TABLE orders (
    id          TEXT PRIMARY KEY,
    customer_id TEXT NOT NULL,
    status      TEXT NOT NULL,
    version     BIGINT NOT NULL,
    total_cents BIGINT NOT NULL,
    items       JSONB NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

`examples/orders/migrations/001_create_orders.down.sql`:

```sql
DROP TABLE IF EXISTS orders;
```

`version` is BIGINT (not INTEGER) to match `Order.Version() int64`.

Rationale for JSONB items: outbox demo is the topic; normalized
`order_items` table would shift focus to aggregate-children
persistence patterns.

### 5.2 Migration sources and state tables

Two independent migration sources, each tracking state in its own
table via golang-migrate's `x-migrations-table` parameter:

| Source | Path | State table |
| --- | --- | --- |
| Adapter outbox | `eventbus/outbox/pgx/migrations/` (already exists, files `001_*` `002_*`) | `outbox_schema_migrations` |
| Demo business | `examples/orders/migrations/` (new, file `001_*`) | `orders_schema_migrations` |

Rationale: adapter migrations and example business migrations are
different owners. Sharing a single `schema_migrations` table would
risk version-number collision when the adapter ships a future
`003_*.sql` migration. `x-migrations-table` is golang-migrate's
official multi-source escape hatch.

Both sources can start at `001` because they are tracked
independently.

### 5.3 Migration runtime

**docker-compose**: two init services using the official
`migrate/migrate:v4.19.1` image, mounting each migration source
read-only. Chain `orders-migrate` after `outbox-migrate` via
`service_completed_successfully`. All app services depend on
`orders-migrate`. App binaries never import golang-migrate.

**Integration tests**: identical conceptually but go through the
golang-migrate **library** with the pgx5 source/driver, applying
each source's `embed.FS` against the matching `x-migrations-table`.

A new `examples/orders/migrations/migrations.go`:

```go
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
```

mirrors the v0.4.0 adapter pattern (`eventbus/outbox/pgx/migrations.FS`).

### 5.4 YAML hazard

In docker-compose `command:` lists, the DSN contains `?` and `&`.
Use the list-of-args syntax (each item is a quoted scalar):

```yaml
command:
  - "-path=/migrations"
  - "-database=postgres://orders:orders@postgres:5432/orders?sslmode=disable&x-migrations-table=outbox_schema_migrations"
  - "up"
```

This avoids YAML interpreting `&x-migrations-table` as an anchor
declaration. The compose CLI hands the string to `migrate` as-is.

Note: compose CLI uses the default `postgres://` scheme (lib/pq
driver compiled into the migrate image). Integration tests rewrite
to `pgx5://...` because the library binds to whichever scheme the
caller imports. Both drivers apply the same SQL successfully —
migrations use only standard Postgres syntax (BIGINT, JSONB,
TIMESTAMPTZ, DEFAULT now()).

## 6. Handler refactor and pgxrepo

### 6.1 Domain changes

`orderdom.Hydrate` gains an `items []Item` parameter:

```go
// before
func Hydrate(id ID, customerID string, status Status, version int64, totalCents int64) *Order

// after
func Hydrate(id ID, customerID string, status Status, version int64, totalCents int64, items []Item) *Order
```

Inside `Hydrate`, items are defensively copied:
`o.items = append([]Item(nil), items...)`. All existing callers update
to the new signature. After the worker refactor (§6.3), the only
caller is `pgxrepo.FindByID`.

`orderdom.Repository` interface is unchanged:

```go
type Repository interface {
    FindByID(ctx context.Context, id ID) (*Order, error)
    Save(ctx context.Context, o *Order) error
    Delete(ctx context.Context, id ID) error
}
```

### 6.2 pgxrepo (`examples/orders/infra/pgxrepo/order_repo.go`)

```go
type OrderRepository struct {
    pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *OrderRepository { ... }

// Save uses pgxdb.Executor(ctx, pool) — returns ctx-bound tx when
// running inside UoW, falls through to pool otherwise.
//
// updated_at is explicitly set on UPDATE; created_at retains its
// DEFAULT now() and is only written by INSERT.
func (r *OrderRepository) Save(ctx context.Context, o *orderdom.Order) error {
    exec := pgxdb.Executor(ctx, r.pool)
    itemsJSON, err := json.Marshal(o.Items())
    if err != nil { return err }
    _, err = exec.Exec(ctx, `
        INSERT INTO orders (id, customer_id, status, version, total_cents, items)
        VALUES ($1, $2, $3, $4, $5, $6)
        ON CONFLICT (id) DO UPDATE SET
            customer_id = EXCLUDED.customer_id,
            status      = EXCLUDED.status,
            version     = EXCLUDED.version,
            total_cents = EXCLUDED.total_cents,
            items       = EXCLUDED.items,
            updated_at  = now()
    `, string(o.ID()), o.CustomerID(), string(o.Status()),
       o.Version(), o.TotalCents(), itemsJSON)
    return err
}
```

`FindByID` selects the same columns, unmarshals `items` JSONB, and
returns `orderdom.Hydrate(...)`.

### 6.3 Handler refactor

**`application/order/place_order.go`** signature:

```go
type PlaceOrderHandler struct {
    uow    application.UnitOfWork
    repo   orderdom.Repository
    outbox eventbus.Outbox  // replaces publisher
    topic  string
    newID  func() string
}

func (h *PlaceOrderHandler) Handle(ctx context.Context, cmd PlaceOrderCommand) (PlaceOrderResult, error) {
    o := orderdom.New(orderdom.ID(cmd.OrderID), cmd.CustomerID)
    if err := o.Place(cmd.Items, h.newID()); err != nil {
        return PlaceOrderResult{}, err
    }

    err := h.uow.Do(ctx, func(ctx context.Context) error {
        if err := h.repo.Save(ctx, o); err != nil {
            return err
        }
        return h.outbox.Stage(ctx, h.topic, o.DomainEvents()...)
    })
    if err != nil {
        return PlaceOrderResult{}, err
    }

    o.ClearEvents()  // only after uow.Do returns nil
    return PlaceOrderResult{OrderID: cmd.OrderID, TotalCents: o.TotalCents()}, nil
}
```

**`application/order/ship_order.go`** mirrors the same shape:
`FindByID → mutate → uow.Do { Save + Stage }`.

### 6.4 Worker refactor — drop hydrate-from-event

The current `handleOrderPlaced` is:

```go
o := orderdom.Hydrate(ID(placed.AggregateID()), placed.CustomerID,
    StatusPlaced, placed.Version(), placed.TotalCents)
repo.Save(ctx, o)
cmdBus.Dispatch(ctx, ShipOrderCommand{OrderID: ..., Carrier: ...})
```

Becomes:

```go
cmdBus.Dispatch(ctx, ShipOrderCommand{OrderID: placed.AggregateID(), Carrier: "demo-carrier"})
```

`ShipOrderHandler` itself calls `repo.FindByID(ctx, id)` from the
shared Postgres — the api's tx has already committed the order by the
time the relay publishes the event, so FindByID is guaranteed to
find the row.

This eliminates the README's "per-process in-mem workaround" shortcut.
`OrderPlaced` event payload stays unchanged (no `Items` field).

### 6.5 Wiring (`cmd/api/main.go` and `cmd/worker/main.go`)

```go
dbURL, err := runtime.RequiredEnv("DATABASE_URL")
if err != nil { return err }

pool, err := pgxpool.New(ctx, dbURL)
if err != nil { return err }
defer pool.Close()  // hook into binary lifecycle

txMgr := pgxdb.NewTxManager(pool)
uow   := application.UnitOfWorkFromTxManager(txMgr)
repo  := pgxrepo.New(pool)

codec := eventcodec.New()
store, err := pgxoutbox.NewStore(pgxoutbox.Config{Pool: pool, Codec: codec})
if err != nil { return err }

cmdBus := registerCommands(uow, repo, store, topicPlaced)
// (cmd/api): no publisher anywhere
// (cmd/worker): subscriber stays; publisher gone
```

`registerCommands` signature drops `pub` parameter, adds `uow` /
`outbox` parameters.

`runtime.RequiredEnv("DATABASE_URL")` is the existing helper — no new
`Must*` panic-style helpers.

## 7. cmd/relay binary

### 7.1 Structure

```go
// examples/orders/cmd/relay/main.go — wiring only
func run() error {
    ctx := context.Background()
    log := slogger.New(slogger.Config{Level: slog.LevelInfo}).
        With(logger.F("service", "orders-relay"))

    brokers, err := runtime.BrokersFromEnv()
    if err != nil { return err }

    dbURL, err := runtime.RequiredEnv("DATABASE_URL")
    if err != nil { return err }

    prov, err := runtime.OTelProvider(ctx, "orders-relay", os.Getenv("OTEL_OTLP_ENDPOINT"))
    if err != nil { return err }

    pool, err := pgxpool.New(ctx, dbURL)
    if err != nil { return err }
    defer pool.Close()

    codec := eventcodec.New()

    store, err := pgxoutbox.NewStore(pgxoutbox.Config{Pool: pool, Codec: codec})
    if err != nil { return err }

    pub, err := kafka.NewPublisher(kafka.PublisherConfig{
        Brokers: brokers, Codec: codec, PartitionByAggregate: true,
    })
    if err != nil { return err }

    relay, err := outbox.NewRelay(
        outbox.RelayConfig{Store: store, Publisher: pub, Codec: codec, Logger: log},
        outbox.WithHeaderRestorer(kafka.RestoreCoreHeaders),
    )
    if err != nil { return err }

    app := bootstrap.New(bootstrap.Options{Logger: log})
    app.Use(
        otelad.Module(prov),
        kafka.PublisherModule(pub),
        outbox.RelayModule(relay, log),
    )
    return app.Run(ctx)
}
```

Notes:

- `DeadLetterRecorder` is auto-discovered from `Store` by `NewRelay`
  when `MaxAttempts > 0`. We do **not** wire it explicitly.
- `HeaderRestorer` is an option (`outbox.WithHeaderRestorer(...)`),
  not a field in `RelayConfig`.
- `PartitionByAggregate: true` mirrors api/worker so the relay's
  publish path preserves per-aggregate ordering.
- Relay defaults — `MaxAttempts=10`, `PollInterval=1s`,
  `BatchSize=100`, exponential-with-jitter backoff, `ClaimLease=5s`
  — are used unchanged. No env knobs added in this PR.

### 7.2 Dockerfile

Add a build target and copy:

```dockerfile
RUN go build -o /orders-relay ./examples/orders/cmd/relay
# ... runtime stage ...
COPY --from=builder /orders-relay /orders-relay
```

## 8. docker-compose changes

### 8.1 New services

```yaml
postgres:
  image: postgres:16-alpine
  environment:
    POSTGRES_DB: orders
    POSTGRES_USER: orders
    POSTGRES_PASSWORD: orders
  healthcheck:
    test: ["CMD-SHELL", "pg_isready -U orders -d orders"]
    interval: 2s
    timeout: 2s
    retries: 30

outbox-migrate:
  image: migrate/migrate:v4.19.1
  volumes:
    - ../../eventbus/outbox/pgx/migrations:/migrations:ro
  command:
    - "-path=/migrations"
    - "-database=postgres://orders:orders@postgres:5432/orders?sslmode=disable&x-migrations-table=outbox_schema_migrations"
    - "up"
  depends_on:
    postgres:
      condition: service_healthy

orders-migrate:
  image: migrate/migrate:v4.19.1
  volumes:
    - ./migrations:/migrations:ro
  command:
    - "-path=/migrations"
    - "-database=postgres://orders:orders@postgres:5432/orders?sslmode=disable&x-migrations-table=orders_schema_migrations"
    - "up"
  depends_on:
    outbox-migrate:
      condition: service_completed_successfully

orders-relay:
  build:
    context: ../..
    dockerfile: examples/orders/Dockerfile
  command: ["/orders-relay"]
  environment:
    DATABASE_URL: postgres://orders:orders@postgres:5432/orders?sslmode=disable
    KAFKA_BROKERS: redpanda:9092
    OTEL_OTLP_ENDPOINT: jaeger:4317
  depends_on:
    orders-migrate: { condition: service_completed_successfully }
    redpanda:       { condition: service_healthy }
    jaeger:         { condition: service_started }
```

### 8.2 Dependency matrix (final)

| service | depends_on |
| --- | --- |
| `postgres` | — |
| `redpanda` | — |
| `jaeger` | — |
| `outbox-migrate` | `postgres: service_healthy` |
| `orders-migrate` | `outbox-migrate: service_completed_successfully` |
| `orders-api` | `orders-migrate: service_completed_successfully`, `jaeger: service_started` |
| `orders-worker` | `orders-migrate: service_completed_successfully`, `redpanda: service_healthy`, `jaeger: service_started` |
| `orders-relay` | `orders-migrate: service_completed_successfully`, `redpanda: service_healthy`, `jaeger: service_started` |
| `orders-reader` | `redpanda: service_healthy`, `jaeger: service_started` |

`orders-api` deliberately **does not** depend on `redpanda` —
removing the false dependency teaches the right topology
(write side is decoupled from broker via outbox).

`orders-reader` does **not** gain `DATABASE_URL` — it stays
Kafka-only with in-memory projection.

### 8.3 Env additions

- `orders-api`, `orders-worker`, `orders-relay` get
  `DATABASE_URL: postgres://orders:orders@postgres:5432/orders?sslmode=disable`.
- `orders-reader` unchanged.

## 9. Integration tests

### 9.1 Layout

```
examples/orders/integration/
  main_test.go           # extend: Postgres + dual-source migrate
  round_trip_test.go     # unchanged content; refactor TestMain to run(m) int
  place_order_test.go    # new: PlaceOrder happy + rollback
  ship_order_test.go     # new: ShipOrder happy + rollback
```

### 9.2 TestMain (run(m) int wrapper)

The existing `os.Exit(m.Run())` skips deferred cleanup. Refactor:

```go
func TestMain(m *testing.M) {
    os.Exit(run(m))
}

func run(m *testing.M) int {
    ctx := context.Background()

    // Postgres
    pgC, err := tcpostgres.Run(ctx, "postgres:16-alpine",
        tcpostgres.WithDatabase("orders"),
        tcpostgres.WithUsername("orders"),
        tcpostgres.WithPassword("orders"),
        tcpostgres.BasicWaitStrategies(),
    )
    if err != nil { log.Printf("postgres: %v", err); return 1 }
    defer func() { _ = pgC.Terminate(ctx) }()

    dsn, _ := pgC.ConnectionString(ctx, "sslmode=disable")
    pool, err := pgxpool.New(ctx, dsn)
    if err != nil { log.Printf("pool: %v", err); return 1 }
    defer pool.Close()
    sharedPool = pool

    if err := applyMigrations(dsn,
        "outbox_schema_migrations", pgxoutboxmigs.FS); err != nil {
        log.Printf("outbox migrate: %v", err); return 1
    }
    if err := applyMigrations(dsn,
        "orders_schema_migrations", ordersmigs.FS); err != nil {
        log.Printf("orders migrate: %v", err); return 1
    }

    // Redpanda (existing)
    rpC, err := redpanda.Run(ctx, redpandaImage, redpanda.WithAutoCreateTopics())
    if err != nil { log.Printf("redpanda: %v", err); return 1 }
    defer func() { _ = rpC.Terminate(ctx) }()

    broker, _ := rpC.KafkaSeedBroker(ctx)
    sharedBrokers = []string{broker}

    return m.Run()
}

func applyMigrations(pgDSN, table string, srcFS embed.FS) error {
    // construct pgx5:// DSN with x-migrations-table query param
    // run iofs source from srcFS, up()
    // (helper inside main_test.go to keep DSN rewrite logic in one place)
}

func truncate(t *testing.T) {
    t.Helper()
    _, err := sharedPool.Exec(context.Background(),
        "TRUNCATE orders, outbox_records, outbox_dead_letters")
    if err != nil { t.Fatalf("truncate: %v", err) }
}
```

### 9.3 Happy-path test pattern (PlaceOrder shown; ShipOrder mirrors)

```go
func TestPlaceOrder_TransactionalOutbox(t *testing.T) {
    truncate(t)
    ctx, cancel := context.WithCancel(context.Background())
    t.Cleanup(cancel)

    codec := eventcodec.New()
    uow   := application.UnitOfWorkFromTxManager(pgxdb.NewTxManager(sharedPool))
    repo  := pgxrepo.New(sharedPool)
    store, _ := pgxoutbox.NewStore(pgxoutbox.Config{Pool: sharedPool, Codec: codec})

    topic := "orders.placed.testPlaceOrder"
    handler := apporder.NewPlaceOrderHandler(uow, repo, store, topic, uuid.NewString)

    pubCtx := kafka.WithTraceID(ctx, "trace-pop")
    pubCtx  = kafka.WithCausationID(pubCtx, "cause-pop")
    pubCtx  = kafka.WithCorrelationID(pubCtx, "correlation-pop")

    _, err := handler.Handle(pubCtx, apporder.PlaceOrderCommand{
        OrderID: "ord-1", CustomerID: "alice",
        Items: []orderdom.Item{{SKU: "A", Quantity: 2, PriceCents: 499}},
    })
    if err != nil { t.Fatalf("handle: %v", err) }

    // (i) orders row exists
    var customer, status string
    var version int64
    err = sharedPool.QueryRow(ctx,
        `SELECT customer_id, status, version FROM orders WHERE id=$1`, "ord-1").
        Scan(&customer, &status, &version)
    if err != nil { t.Fatalf("orders row: %v", err) }
    if customer != "alice" { t.Errorf("customer: want alice, got %q", customer) }
    if status != string(orderdom.StatusPlaced) { t.Errorf("status: %q", status) }

    // (ii) outbox_records row exists with expected headers
    var topicGot, eventName string
    var headersJSON []byte
    err = sharedPool.QueryRow(ctx,
        `SELECT topic, event_name, headers FROM outbox_records WHERE aggregate_id=$1`,
        "ord-1").Scan(&topicGot, &eventName, &headersJSON)
    if err != nil { t.Fatalf("outbox row: %v", err) }
    var hdrs map[string]string
    _ = json.Unmarshal(headersJSON, &hdrs)
    if hdrs[eventbus.HeaderTraceID] != "trace-pop" {
        t.Errorf("outbox row trace header: %q", hdrs[eventbus.HeaderTraceID])
    }
    // ... causation, correlation, event_id, occurred_at ...

    // (iii) Subscribe + Relay, assert published envelope keeps headers
    sub, err := kafka.NewSubscriber(kafka.SubscriberConfig{
        Brokers: sharedBrokers, ConsumerGroup: "test-" + uuid.NewString(), Codec: codec,
    })
    if err != nil { t.Fatalf("subscriber: %v", err) }
    t.Cleanup(func() { _ = sub.Close() })

    envelopes, err := sub.Subscribe(ctx, topic)
    if err != nil { t.Fatalf("subscribe: %v", err) }

    pub, err := kafka.NewPublisher(kafka.PublisherConfig{Brokers: sharedBrokers, Codec: codec})
    if err != nil { t.Fatalf("publisher: %v", err) }
    t.Cleanup(func() { _ = pub.Close() })

    relay, err := outbox.NewRelay(
        outbox.RelayConfig{Store: store, Publisher: pub, Codec: codec, Logger: testLog(t)},
        outbox.WithHeaderRestorer(kafka.RestoreCoreHeaders),
        outbox.WithPollInterval(50*time.Millisecond),
    )
    if err != nil { t.Fatalf("new relay: %v", err) }

    relayCtx, relayCancel := context.WithCancel(ctx)
    relayDone := make(chan struct{})
    go func() { defer close(relayDone); _ = relay.Run(relayCtx) }()

    // Wait for relay's publish to arrive at the subscriber
    select {
    case env := <-envelopes:
        if env.Raw.Metadata.Get(eventbus.HeaderTraceID) != "trace-pop" {
            t.Errorf("trace lost across outbox→relay→kafka: %q",
                env.Raw.Metadata.Get(eventbus.HeaderTraceID))
        }
        // ... causation, correlation, event_id, occurred_at ...
        env.Ack()
    case <-time.After(15 * time.Second):
        t.Fatal("timeout waiting for relay publish")
    }

    // Bounded relay shutdown
    relayCancel()
    select {
    case <-relayDone:
    case <-time.After(5 * time.Second):
        t.Fatal("relay did not stop within 5s after cancel")
    }
}
```

### 9.4 Rollback test pattern

```go
func TestPlaceOrder_TxRollback(t *testing.T) {
    truncate(t)
    ctx := context.Background()

    codec := eventcodec.New()
    realUoW := application.UnitOfWorkFromTxManager(pgxdb.NewTxManager(sharedPool))
    repo    := pgxrepo.New(sharedPool)
    store, _ := pgxoutbox.NewStore(pgxoutbox.Config{Pool: sharedPool, Codec: codec})

    forceErr := errors.New("forced rollback")
    testUoW := application.UnitOfWorkFunc(func(ctx context.Context, fn func(context.Context) error) error {
        return realUoW.Do(ctx, func(ctx context.Context) error {
            if err := fn(ctx); err != nil { return err }
            return forceErr  // propagate to handler; do not swallow
        })
    })

    handler := apporder.NewPlaceOrderHandler(
        testUoW, repo, store, "orders.placed.rollback", uuid.NewString)

    _, err := handler.Handle(ctx, apporder.PlaceOrderCommand{
        OrderID: "ord-roll", CustomerID: "bob",
        Items: []orderdom.Item{{SKU: "B", Quantity: 1, PriceCents: 99}},
    })

    if !errors.Is(err, forceErr) {
        t.Fatalf("handle: want forceErr, got %v", err)
    }

    var orderCount, outboxCount int
    sharedPool.QueryRow(ctx, "SELECT COUNT(*) FROM orders").Scan(&orderCount)
    sharedPool.QueryRow(ctx, "SELECT COUNT(*) FROM outbox_records").Scan(&outboxCount)
    if orderCount != 0 { t.Errorf("orders rows: want 0, got %d", orderCount) }
    if outboxCount != 0 { t.Errorf("outbox rows: want 0, got %d", outboxCount) }
}
```

### 9.5 ShipOrder seeding constraint

`TestShipOrder_TransactionalOutbox` seeds a `placed` order **directly
via `repo.Save` outside any UoW** (or raw SQL INSERT) — explicitly NOT
through `PlaceOrderHandler`. The reason: ShipOrder tests must produce
exactly one outbox row (`OrderShipped`); seeding via `PlaceOrderHandler`
would leave an `OrderPlaced` row in `outbox_records` that pollutes
the count assertion.

The seed helper:

```go
func seedPlacedOrder(t *testing.T, id, customer string, items []orderdom.Item) {
    t.Helper()
    o := orderdom.Hydrate(orderdom.ID(id), customer, orderdom.StatusPlaced,
        1 /* version */, totalCents(items), items)
    if err := pgxrepo.New(sharedPool).Save(context.Background(), o); err != nil {
        t.Fatalf("seed: %v", err)
    }
}
```

`repo.Save` outside `uow.Do` falls through to `pool` via
`pgxdb.Executor` — no outbox row is staged.

## 10. README rewrite

### 10.1 Remove

- The `**No outbox**` bullet from "Known shortcuts (intentional, for the example)".
- The `**Per-process in-memory repos**` bullet from the same section.

### 10.2 Replace

A new section `## Postgres + outbox flow` describing:

- `cmd/api` and `cmd/worker` share Postgres for write model
  (no per-process memrepo for those two).
- Aggregate save + outbox stage happen in one Postgres transaction.
- `cmd/relay` is a separate process that drains the outbox table and
  publishes to Kafka.
- Headers (trace/causation/correlation) survive the outbox→relay→kafka
  round trip via existing codec + `kafka.RestoreCoreHeaders`.

### 10.3 Update topology diagram

The ASCII diagram in README gains a Postgres node and a relay node;
reader stays where it is.

### 10.4 Add "Remaining intentional shortcuts"

```
## Remaining intentional shortcuts

- **Reader uses in-memory projection** — cmd/reader builds an
  in-process projection from Kafka events. A real service uses a
  durable read store (Postgres / DynamoDB / Elasticsearch) with
  idempotent upserts. The pgx projection variant is tracked as a
  separate follow-up.
- **No durable Inbox / consumer-side dedup** — at-least-once
  redelivery from the broker can cause duplicate Ship dispatches.
  A real service uses eventbus/inbox keyed on OutboxRecord.EventID.
  The eventbus/inbox/pgx adapter is tracked as a separate follow-up.
- **No exactly-once claim** — at-least-once is the explicit contract
  from the pgx outbox adapter (WithClaimLease(5s) reduces the
  duplicate window but does not eliminate it).
- **No authn/authz** — HTTP endpoints are wide open. Demo only.
```

### 10.5 Update running-locally section

`docker compose up --build` text stays; just note the new services
(Postgres, two migrate init containers, relay) and that the curl
example flow is unchanged from a user perspective.

## 11. Commit decomposition

| # | commit | scope |
| --- | --- | --- |
| 0 | `docs(spec): brainstorm design for examples/orders pgx outbox demo` | this file |
| 1 | `feat(examples/orders): add Postgres order repository + migrations` | `migrations/001_create_orders.{up,down}.sql`, `migrations/migrations.go` (embed.FS), `infra/pgxrepo/order_repo.go`, `Order.Hydrate(...items)` signature change with defensive copy, any domain test updates |
| 2 | `refactor(examples/orders): stage OrderPlaced through outbox` | `application/order/place_order.go` handler signature, `cmd/api/main.go` wiring (pgxpool, UoW, outbox.Stage, defer pool.Close), TestMain refactor to `run(m) int` pattern, `integration/place_order_test.go` happy + rollback |
| 3 | `refactor(examples/orders): stage OrderShipped through outbox` | `application/order/ship_order.go` handler, `cmd/worker/main.go` wiring (pgxpool, UoW, outbox.Stage, defer pool.Close, drop hydrate-from-event), `integration/ship_order_test.go` happy + rollback |
| 4 | `feat(examples/orders): add outbox relay binary` | `cmd/relay/main.go`, `Dockerfile` target for `/orders-relay` |
| 5 | `chore(examples/orders): wire Postgres, migrations, and relay in compose` | `docker-compose.yml` — new services, dependency matrix update, env additions |
| 6 | `docs(examples/orders): document pgx outbox demo and remaining shortcuts` | `examples/orders/README.md` rewrite, `CHANGELOG.md` `[Unreleased]` entry |
| 7 | `chore(agent-memory): record examples orders pgx outbox cycle` | `.agent/state.md`, `.agent/decisions.md`, possibly `.agent/review-log.md` |

Branch: `feat/examples-orders-pgx-outbox`.

Single PR. Multi-commit decomposition stays in PR for atomic review;
reviewer can walk commit-by-commit.

## 12. Implementation notes (small precision points)

- `pgx5://` scheme rewrite for migrate library DSN in integration
  tests; compose CLI uses `postgres://` and the migrate image's
  default lib/pq driver. Both apply the same SQL.
- YAML `command:` list-of-args form to avoid `&` being interpreted as
  anchor; section 5.4.
- `pool.Close()` via `defer` in every binary's `run()` — no new
  generic bootstrap module for DB lifecycle this PR.
- `DeadLetterRecorder` is auto-discovered from `Store` by `outbox.NewRelay`
  when `MaxAttempts > 0`. Do not wire it explicitly.
- Relay knobs all stay at defaults; no env exposure this PR.
- `runtime.RequiredEnv("DATABASE_URL")` reused — no new `Must*`
  panic helper.

## 13. Open questions (to be settled at implementation time, not gating brainstorm)

- Exact `applyMigrations` helper shape — likely takes
  `(pgDSN, table string, srcFS embed.FS) error` and rewrites scheme +
  appends `x-migrations-table` query param.
- Exact log line text for relay publish / fail / terminate paths —
  inherits from `outbox.Relay` defaults; demo does not customise.

`outbox.RelayModule` signature is verified at brainstorm time as
`func RelayModule(r *Relay, log logger.Logger) bootstrap.ModuleFunc`
(see `eventbus/outbox/module.go:25`). No implementation-time check
required.

## 14. Out of brainstorm scope (future cycles, each its own spec)

- `eventbus/inbox/pgx` adapter (consumer-side dedup).
- pgx projection variant for `cmd/reader`.
- `LISTEN/NOTIFY` push-based delivery for relay.
- `claim_id` worker attribution.
- `orders.version` optimistic-locking behaviour activation.
- Full end-to-end docker-compose test (HTTP → DB → relay → Kafka →
  worker → Kafka → reader → projection).
