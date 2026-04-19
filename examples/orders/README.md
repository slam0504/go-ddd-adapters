# orders — realistic example service

A CQRS-shaped sample service that exercises every adapter shipped from
this repo end-to-end:

- `eventbus/kafka` — JSON codec + publisher + subscriber, with
  `PartitionByAggregate` enabled so per-aggregate ordering is preserved.
- `logger/slogger` — slog-backed structured logging.
- `observability/otel` — OTel SDK provider exporting via OTLP/gRPC to
  Jaeger.

The example is a separate Go module (`go.mod` here) so its dev-only deps
(testcontainers, otlptracegrpc, gRPC) do not pollute the adapter root
module that downstream library users import.

## Topology

```
HTTP POST /orders          orders.placed                   orders.shipped
   │                         │                                │
   ▼                         ▼                                ▼
[ api ] ──publish──▶ Kafka ──▶ [ worker ] ──publish──▶ Kafka ──▶ [ reader ]
                                                                   │
                                                                   ▼
                                                             HTTP GET /orders/{id}
```

| Service | Role | Reads | Writes | Listens |
| --- | --- | --- | --- | --- |
| `cmd/api` | Command side | HTTP | publishes `orders.placed` | `:8080` |
| `cmd/worker` | Saga / event handler | subscribes `orders.placed` | publishes `orders.shipped` | — |
| `cmd/reader` | Query side | subscribes both topics → projection | none | `:8081` |

The example uses an in-memory aggregate repository in each cmd. Replace
`infra/memrepo` with a real Postgres/DynamoDB adapter and an outbox to
turn this into something production-shaped — that work belongs in a later
step.

## Running locally with docker-compose

```sh
docker compose up --build
```

Then in another terminal:

```sh
ORDER_ID=$(uuidgen)
curl -X POST http://localhost:8080/orders \
     -H 'content-type: application/json' \
     -d "{\"order_id\":\"$ORDER_ID\",\"customer_id\":\"alice\",\"items\":[{\"sku\":\"A\",\"quantity\":2,\"price_cents\":499}]}"

# wait a moment for worker to ship + reader to project
sleep 1
curl "http://localhost:8081/orders/$ORDER_ID"
```

Open http://localhost:16686 in a browser and search by service
`orders-api` to see the trace flow through publisher → consumer.

## Running integration tests

`integration/` contains `//go:build integration` tests that spin up a
single redpanda container via `testcontainers-go` and exercise the
publisher / subscriber against it. They are excluded from the default
`go test ./...` run.

```sh
go test -tags=integration -race ./integration/...
```

A working Docker daemon is required. First run pulls the redpanda image
(~3-5s of network); subsequent runs reuse it.

## Known shortcuts (intentional, for the example)

- **Per-process in-memory repos**: api, worker, and reader each keep
  their own in-mem `*Order` map. The worker uses
  `orderdom.Hydrate(...)` to build a local copy from the
  `OrderPlaced` event so the ship handler can find it. With a shared
  database this hack disappears entirely.
- **No outbox**: api saves the aggregate then publishes — a crash between
  the two would lose the event. A real service uses an outbox table and
  a relay process.
- **No authn/authz**: the HTTP endpoints are wide open.

These are documented here rather than fixed because the goal of the
example is to show the adapter wiring, not to build a production
template.
