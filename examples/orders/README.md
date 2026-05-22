# orders — realistic example service

A CQRS-shaped sample service exercising every production-shaped adapter
shipped from this repo end-to-end:

- `eventbus/outbox/pgx` — transactional Outbox over Postgres + pgx/v5.
- `ports/database/pgx` — `TxManager` adapter that bridges to
  `application.UnitOfWork`.
- `eventbus/outbox` — driver-agnostic polling `Relay` (and
  `RelayModule` bootstrap helper).
- `eventbus/kafka` — JSON codec + publisher + subscriber, with
  `PartitionByAggregate` so per-aggregate ordering is preserved
  through the outbox → relay → Kafka path.
- `logger/slogger` — slog-backed structured logging.
- `observability/otel` — OTel SDK provider exporting via OTLP/gRPC
  to Jaeger.

The example is a separate Go module (`go.mod` here) so its dev-only
deps (testcontainers, otlptracegrpc, gRPC, golang-migrate) do not
pollute the adapter root module that downstream library users import.
Both modules pin `go 1.25.0`.

## Topology

```
HTTP POST /orders         orders.placed                   orders.shipped
   │                         │                                │
   ▼                         ▼                                ▼
[ api ] ── tx Stage ──▶ [ outbox_records (Postgres) ] ──▶ [ relay ] ──▶ Kafka
                                  ▲                                    │
                                  │ tx Stage                           │
                                  │                                    ▼
                              [ worker ] ◀── consume orders.placed ──[ kafka ]
                                  │                                    │
                                  │                                    ▼
                                  │                                [ reader ]
                                  │                                    │
                                  │                                    ▼
                                  │                             HTTP GET /orders/{id}
                                  ▼
                          shared orders row in Postgres
```

| Service | Role | Reads | Writes | Listens |
| --- | --- | --- | --- | --- |
| `cmd/api` | Command side | HTTP | `orders` row + `outbox_records` row in one tx | `:8080` |
| `cmd/worker` | Saga / event handler | subscribes `orders.placed`, FindByID from Postgres | `orders` row + `outbox_records` row in one tx | — |
| `cmd/relay` | Outbox drain | `outbox_records` via `FOR UPDATE SKIP LOCKED` | publishes to Kafka; `MarkSent` / `MarkFailed` / `Terminate` | — |
| `cmd/reader` | Query side | subscribes both topics, builds in-memory projection | none | `:8081` |

The example uses a shared Postgres for the write model (`cmd/api` +
`cmd/worker`). Aggregate state and outbox row commit in the **same
transaction** — a crash between `repo.Save` and `outbox.Stage` is no
longer possible. A separate process (`cmd/relay`) drains the outbox
to Kafka under `FOR UPDATE SKIP LOCKED`, so multiple relay instances
are safe to run (no overlap; at-least-once contract).

## Postgres + outbox flow

The transactional outbox is the central invariant of this example:

- **`cmd/api` and `cmd/worker` never publish to Kafka.** All outbound
  events go through `eventbus.Outbox.Stage` and reach Kafka only via
  `cmd/relay`. `cmd/worker` still **consumes** from Kafka (it
  subscribes to `orders.placed`); `cmd/api` is broker-free at runtime.
  Both api and worker still import the `kafka` adapter package for
  ctx-bound header helpers (`kafka.WithTraceID` / `WithCausationID` /
  `WithCorrelationID`) used by the shared codec — that is a Go import
  edge, not a broker connection.
- Inside a UoW transaction, the handler writes the aggregate state to
  the `orders` table AND inserts the domain event row into
  `outbox_records`. If anything fails before commit, neither write
  persists.
- The codec automatically reads `trace_id` / `causation_id` /
  `correlation_id` from ctx and writes them into the outbox row's
  `headers` JSONB column.
- The worker restores those headers from the inbound Kafka envelope
  before dispatching `ShipOrderCommand`, then sets `causation_id` to
  the consumed `OrderPlaced` event id. The OrderShipped outbox row
  thus inherits the inbound trace + correlation and reports the
  consumed event as its cause.
- `cmd/relay` polls `outbox_records` (`FOR UPDATE SKIP LOCKED` +
  `claim_token` UUID lease), restores headers into ctx via
  `kafka.RestoreCoreHeaders`, publishes via the Kafka publisher,
  and stamps `MarkSent`. Failures route through backoff +
  `MarkFailed`; after `MaxAttempts` (default 10), the row terminates
  into `outbox_dead_letters`.
- Per-aggregate ordering: `PartitionByAggregate: true` is enabled on
  the relay's publisher so two events for the same aggregate id land
  in the same Kafka partition in the order their outbox rows were
  fetched.

Distributed tracing survives the round-trip: any `trace_id` carried
on the inbound HTTP request reaches the Kafka envelope via the
outbox row's headers — the relay process boundary does not break the
trace.

## Running locally with docker-compose

```sh
docker compose up --build
```

Compose boots in lifecycle order:

1. `postgres` (healthcheck on `pg_isready`).
2. `outbox-migrate` — applies `eventbus/outbox/pgx/migrations/`
   (state table `outbox_schema_migrations`), exits 0.
3. `orders-migrate` — applies `examples/orders/migrations/`
   (state table `orders_schema_migrations`), exits 0.
4. `orders-api`, `orders-worker`, `orders-relay`, `orders-reader`
   start (each depending on the migrate chain as appropriate).

Then in another terminal:

```sh
ORDER_ID=$(uuidgen)
curl -X POST http://localhost:8080/orders \
     -H 'content-type: application/json' \
     -d "{\"order_id\":\"$ORDER_ID\",\"customer_id\":\"alice\",\"items\":[{\"sku\":\"A\",\"quantity\":2,\"price_cents\":499}]}"

# Wait briefly for worker to ship + relay to publish + reader to project.
sleep 1
curl "http://localhost:8081/orders/$ORDER_ID"
```

Open http://localhost:16686 in a browser and search by service
`orders-api` or `orders-relay` to see the trace flow span the
process boundary.

## Running integration tests

`integration/` contains `//go:build integration` tests that spin up
Postgres + Redpanda containers via `testcontainers-go` and exercise
the outbox flow against real services. They are excluded from the
default `go test ./...` run.

```sh
go test -tags=integration -race ./integration/...
```

A working Docker daemon is required. First run pulls the Postgres +
Redpanda images (~5–10s of network); subsequent runs reuse them.

## Remaining intentional shortcuts

These are deliberately out of scope for this example. Each will be
tracked as its own follow-up.

- **Reader uses in-memory projection.** `cmd/reader` builds an
  in-process projection from Kafka events. A real service uses a
  durable read store (Postgres / DynamoDB / Elasticsearch) with
  idempotent upserts. The pgx projection variant is a separate
  follow-up.
- **No durable Inbox / consumer-side dedup.** At-least-once
  redelivery from the broker can cause duplicate Ship dispatches
  (the handler treats already-shipped as a graceful ack to absorb
  this, but that's a workaround, not a real Inbox). A real service
  uses `eventbus/inbox` keyed on `OutboxRecord.EventID`. The
  `eventbus/inbox/pgx` adapter is a separate follow-up.
- **No exactly-once claim.** At-least-once is the explicit contract
  of the pgx outbox adapter. `WithClaimLease(5s)` reduces the
  duplicate window but does not eliminate it.
- **No authn/authz.** HTTP endpoints are wide open. Demo only.
- **`orders.version` column is reserved for future optimistic
  locking** but the behaviour is last-write-wins today.

These are documented here rather than fixed because the goal of this
example is to show the production-shaped outbox wiring, not to ship
a complete production reference service.
