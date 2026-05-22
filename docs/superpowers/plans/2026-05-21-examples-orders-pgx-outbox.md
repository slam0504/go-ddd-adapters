# examples/orders pgx outbox demo — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Upgrade `examples/orders` from "save then publish" to a transactional outbox demo built on the v0.4.0 `eventbus/outbox/pgx` + `ports/database/pgx` adapters; introduce a standalone `cmd/relay` binary that drains the outbox to Kafka.

**Architecture:** `cmd/api` and `cmd/worker` write aggregate state and stage domain events into the same Postgres transaction via `application.UnitOfWork` + `eventbus.Outbox.Stage`. The new `cmd/relay` binary uses `outbox.Relay` + `pgxoutbox.Store` to publish committed outbox rows to Kafka. Schema migrations live in two sources (adapter outbox / orders business) tracked independently via golang-migrate's `x-migrations-table`.

**Tech Stack:** Go 1.25.0, pgx/v5 v5.9.2, Postgres 16, golang-migrate v4.19.1, watermill-kafka v3, OpenTelemetry, testcontainers-go v0.42.0, redpanda v24.2.7.

**Design Spec:** `docs/superpowers/specs/2026-05-21-examples-orders-pgx-outbox-design.md` (commits `7b6bae6` + `5a3e77f` on this branch).

**Branch:** `feat/examples-orders-pgx-outbox`.

---

## File Structure

### Files to create

| Path | Responsibility |
| --- | --- |
| `examples/orders/migrations/001_create_orders.up.sql` | Business schema for the orders aggregate (single table + JSONB items). |
| `examples/orders/migrations/001_create_orders.down.sql` | Reverse of the above. |
| `examples/orders/migrations/embed.go` | Expose orders migrations as `embed.FS` for integration tests. |
| `examples/orders/infra/pgxrepo/order_repo.go` | pgx-backed `orderdom.Repository` impl. |
| `examples/orders/cmd/relay/main.go` | Wiring-only relay binary. |
| `examples/orders/integration/place_order_test.go` | PlaceOrder happy + rollback integration tests. |
| `examples/orders/integration/ship_order_test.go` | ShipOrder happy + rollback + not-found integration tests. |

### Files to modify

| Path | Responsibility |
| --- | --- |
| `examples/orders/domain/order/order.go` | `Item` JSON tags + `Hydrate(...items)` signature + defensive copy + docstring cleanup. |
| `examples/orders/application/order/place_order.go` | Handler signature (UoW + Outbox replace Publisher). |
| `examples/orders/application/order/ship_order.go` | Handler signature (UoW + Outbox replace Publisher); preserve ALREADY_SHIPPED idempotency. |
| `examples/orders/cmd/api/main.go` | Wire pgxpool + UoW + pgxrepo + pgxoutbox.Store; drop Publisher. |
| `examples/orders/cmd/worker/main.go` | Wire pgxpool + UoW + pgxrepo + pgxoutbox.Store; drop Publisher + hydrate-from-event block. |
| `examples/orders/Dockerfile` | Add `/orders-relay` build target + COPY into runtime stage. |
| `examples/orders/docker-compose.yml` | Add `postgres`, `outbox-migrate`, `orders-migrate`, `orders-relay`; update dependency matrix; add `DATABASE_URL` env. |
| `examples/orders/integration/main_test.go` | Refactor TestMain to `run(m) int` wrapper; start Postgres testcontainer; apply two-source migrations. |
| `examples/orders/README.md` | Rewrite topology + shortcuts. |
| `CHANGELOG.md` | `[Unreleased]/Changed` entry. |
| `.agent/state.md` | Cycle bookkeeping. |
| `.agent/decisions.md` | Add examples/orders pgx outbox demo decisions block. |

### Files unchanged

- `examples/orders/domain/order/events.go` — `OrderPlaced` / `OrderShipped` event payloads do NOT change (no `Items` field added to events).
- `examples/orders/domain/order/repository.go` — interface unchanged.
- `examples/orders/infra/memrepo/order_repo.go` — kept for `cmd/reader` projection (unchanged).
- `examples/orders/infra/eventcodec/codec.go` — unchanged; the codec already extracts trace/causation/correlation from ctx via `kafka.WithXxxID` helpers.
- `examples/orders/application/order/get_order.go` — read-side handler unchanged.
- `examples/orders/cmd/reader/main.go` — unchanged.
- `examples/orders/projection/order_view.go` — unchanged.
- `examples/orders/integration/round_trip_test.go` / `partition_order_test.go` — unchanged content; the TestMain refactor in Task 2 lifts them to the new pattern transparently.

---

## Common Verification Commands

When a step says "run", these are the canonical commands:

```bash
# Unit tests (root module)
cd /Users/eason_tseng/playground/project/go-ddd-adapters
go test ./...

# Unit tests (examples/orders module)
cd /Users/eason_tseng/playground/project/go-ddd-adapters/examples/orders
go test ./...

# Integration tests (examples/orders) — requires Docker
cd /Users/eason_tseng/playground/project/go-ddd-adapters/examples/orders
go test -tags=integration -race ./integration/...

# Build all example binaries
cd /Users/eason_tseng/playground/project/go-ddd-adapters/examples/orders
go build ./...

# Lint
cd /Users/eason_tseng/playground/project/go-ddd-adapters
golangci-lint run ./...
cd /Users/eason_tseng/playground/project/go-ddd-adapters/examples/orders
golangci-lint run ./...

# Static check with integration tag
cd /Users/eason_tseng/playground/project/go-ddd-adapters
golangci-lint run --build-tags=integration ./...
```

All file paths in this plan are absolute or relative to `/Users/eason_tseng/playground/project/go-ddd-adapters`.

---

## Task 1: Postgres order repository + migrations

**Goal:** Lay down the persistence foundation — orders table SQL, embed.FS, pgxrepo Save/FindByID/Delete, `Item` JSON tags, `Hydrate` signature with items. After this commit the project compiles but no caller uses pgxrepo yet.

**Files:**
- Create: `examples/orders/migrations/001_create_orders.up.sql`
- Create: `examples/orders/migrations/001_create_orders.down.sql`
- Create: `examples/orders/migrations/embed.go`
- Create: `examples/orders/infra/pgxrepo/order_repo.go`
- Modify: `examples/orders/domain/order/order.go`
- Modify: `examples/orders/cmd/worker/main.go` (single-line update to pass `nil` to `Hydrate` until Task 3 removes the call entirely)

---

- [ ] **Step 1.1: Create orders up-migration SQL**

Write `examples/orders/migrations/001_create_orders.up.sql`:

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

- [ ] **Step 1.2: Create orders down-migration SQL**

Write `examples/orders/migrations/001_create_orders.down.sql`:

```sql
DROP TABLE IF EXISTS orders;
```

- [ ] **Step 1.3: Create migrations embed.go**

Write `examples/orders/migrations/embed.go`:

```go
// Package migrations exposes the examples/orders SQL migration files as an
// embed.FS for use with golang-migrate's iofs source driver in integration
// tests. In docker-compose the same files are mounted into the
// migrate/migrate official image.
package migrations

import "embed"

// FS holds the orders migration .sql files in version order.
//
//go:embed *.sql
var FS embed.FS
```

- [ ] **Step 1.4: Add JSON tags to `Item`**

Modify `examples/orders/domain/order/order.go` lines 22-26. Replace:

```go
type Item struct {
	SKU        string
	Quantity   int
	PriceCents int64
}
```

with:

```go
type Item struct {
	SKU        string `json:"sku"`
	Quantity   int    `json:"quantity"`
	PriceCents int64  `json:"price_cents"`
}
```

- [ ] **Step 1.5: Update `Hydrate` signature with items + defensive copy + docstring**

Modify `examples/orders/domain/order/order.go` lines 44-54. Replace:

```go
// Hydrate reconstructs an Order at the supplied state without recording
// any events. The example worker uses this to seed its per-process repo
// from a received OrderPlaced (no shared DB across cmds). Real services
// load from a transactional store and never need this.
func Hydrate(id ID, customerID string, status Status, version int64, totalCents int64) *Order {
	o := New(id, customerID)
	o.status = status
	o.totalCents = totalCents
	o.SetVersion(version)
	return o
}
```

with:

```go
// Hydrate reconstructs an Order at the supplied state without recording
// any events. The pgx repository uses this in FindByID; integration test
// seed helpers use it to insert placed orders that skip the outbox path.
func Hydrate(id ID, customerID string, status Status, version int64, totalCents int64, items []Item) *Order {
	o := New(id, customerID)
	o.status = status
	o.totalCents = totalCents
	o.items = append([]Item(nil), items...)
	o.SetVersion(version)
	return o
}
```

- [ ] **Step 1.6: Update `cmd/worker/main.go` Hydrate caller to pass nil items**

Modify `examples/orders/cmd/worker/main.go` lines 136-142. Replace:

```go
	o := orderdom.Hydrate(
		orderdom.ID(placed.AggregateID()),
		placed.CustomerID,
		orderdom.StatusPlaced,
		placed.Version(),
		placed.TotalCents,
	)
```

with:

```go
	o := orderdom.Hydrate(
		orderdom.ID(placed.AggregateID()),
		placed.CustomerID,
		orderdom.StatusPlaced,
		placed.Version(),
		placed.TotalCents,
		nil, // items: filled by FindByID in Task 3; whole hydrate-from-event block is removed there
	)
```

This keeps the worker compiling between commits. Task 3 removes the entire block.

- [ ] **Step 1.7: Create pgxrepo package**

Write `examples/orders/infra/pgxrepo/order_repo.go`:

```go
// Package pgxrepo is a pgx/v5-backed implementation of the Order
// Repository. Save participates in the caller's transaction via
// pgxdb.Executor: when ctx carries a tx (UoW path), it uses the tx;
// otherwise it falls through to the pool. FindByID maps pgx.ErrNoRows
// to domain.ErrNotFound to mirror the memrepo contract — swapping repo
// implementations must not drift error semantics.
package pgxrepo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/slam0504/go-ddd-core/domain"

	pgxdb "github.com/slam0504/go-ddd-adapters/ports/database/pgx"

	orderdom "github.com/slam0504/go-ddd-adapters/examples/orders/domain/order"
)

type OrderRepository struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *OrderRepository {
	return &OrderRepository{pool: pool}
}

func (r *OrderRepository) FindByID(ctx context.Context, id orderdom.ID) (*orderdom.Order, error) {
	exec := pgxdb.Executor(ctx, r.pool)
	var (
		customer    string
		status      string
		version     int64
		totalCents  int64
		itemsJSON   []byte
	)
	err := exec.QueryRow(ctx, `
		SELECT customer_id, status, version, total_cents, items
		FROM orders
		WHERE id = $1
	`, string(id)).Scan(&customer, &status, &version, &totalCents, &itemsJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("pgxrepo: find order %s: %w", id, err)
	}

	var items []orderdom.Item
	if err := json.Unmarshal(itemsJSON, &items); err != nil {
		return nil, fmt.Errorf("pgxrepo: decode items for %s: %w", id, err)
	}

	return orderdom.Hydrate(id, customer, orderdom.Status(status), version, totalCents, items), nil
}

func (r *OrderRepository) Save(ctx context.Context, o *orderdom.Order) error {
	exec := pgxdb.Executor(ctx, r.pool)
	itemsJSON, err := json.Marshal(o.Items())
	if err != nil {
		return fmt.Errorf("pgxrepo: encode items for %s: %w", o.ID(), err)
	}
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
	`,
		string(o.ID()),
		o.CustomerID(),
		string(o.Status()),
		o.Version(),
		o.TotalCents(),
		itemsJSON,
	)
	if err != nil {
		return fmt.Errorf("pgxrepo: upsert order %s: %w", o.ID(), err)
	}
	return nil
}

func (r *OrderRepository) Delete(ctx context.Context, id orderdom.ID) error {
	exec := pgxdb.Executor(ctx, r.pool)
	_, err := exec.Exec(ctx, `DELETE FROM orders WHERE id = $1`, string(id))
	if err != nil {
		return fmt.Errorf("pgxrepo: delete order %s: %w", id, err)
	}
	return nil
}

var _ orderdom.Repository = (*OrderRepository)(nil)
```

- [ ] **Step 1.8: Build examples/orders to verify compile**

Run:
```bash
cd /Users/eason_tseng/playground/project/go-ddd-adapters/examples/orders
go build ./...
```

Expected: clean build (exit 0, no output). pgxrepo compiles; worker compiles with new `Hydrate(..., nil)` call.

- [ ] **Step 1.9: Lint examples/orders**

Run:
```bash
cd /Users/eason_tseng/playground/project/go-ddd-adapters/examples/orders
golangci-lint run ./...
```

Expected: `0 issues.`

- [ ] **Step 1.10: Commit Task 1**

```bash
cd /Users/eason_tseng/playground/project/go-ddd-adapters
git add examples/orders/migrations/ \
        examples/orders/infra/pgxrepo/ \
        examples/orders/domain/order/order.go \
        examples/orders/cmd/worker/main.go
git commit -m "$(cat <<'EOF'
feat(examples/orders): add Postgres order repository + migrations

Lays down the persistence layer for the upcoming transactional outbox
demo. No caller uses pgxrepo yet; subsequent commits wire it into the
api and worker.

- New examples/orders/migrations/{001_create_orders.up.sql,
  001_create_orders.down.sql,embed.go} with a single orders table:
  TEXT id PK, BIGINT version (matches Order.Version() int64), JSONB
  items column, DEFAULT now() for created_at, UPDATE-time updated_at.
- New examples/orders/infra/pgxrepo with FindByID / Save / Delete.
  Save uses pgxdb.Executor(ctx, pool) so it participates in any
  ctx-bound transaction (the UoW path the handler refactor will
  introduce) or falls through to the pool for test seeding.
  FindByID maps pgx.ErrNoRows to domain.ErrNotFound to mirror
  memrepo and keep swap-friendly error semantics.
- Item gains json:"sku" / json:"quantity" / json:"price_cents" tags.
  Go encoding/json is case-insensitive but does NOT normalise
  underscores; the existing HTTP curl flow (price_cents) would
  silently decode to 0 without tags, and JSONB column shape would
  diverge from the wire shape.
- Hydrate signature gains an items []Item parameter with defensive
  copy (append([]Item(nil), items...)). The docstring is updated to
  reflect the new caller set (pgxrepo.FindByID + integration test
  seed helpers). cmd/worker passes nil for items as an interim step;
  Task 3 removes the entire hydrate-from-event block.

Pre-commit: go build + golangci-lint clean.
EOF
)"
```

Expected: commit succeeds, single commit appended to branch.

---

## Task 2: Stage OrderPlaced through outbox

**Goal:** Refactor `PlaceOrderHandler` to use `application.UnitOfWork` + `eventbus.Outbox.Stage` (Publisher dependency removed). Wire `cmd/api` against pgxpool + pgxrepo + pgxoutbox.Store. Extend integration TestMain to start Postgres + apply dual-source migrations via the `run(m) int` wrapper. Add two integration tests: PlaceOrder happy path + PlaceOrder transactional rollback.

**Files:**
- Modify: `examples/orders/application/order/place_order.go`
- Modify: `examples/orders/cmd/api/main.go`
- Modify: `examples/orders/integration/main_test.go` (rewrite TestMain to `run(m) int`)
- Create: `examples/orders/integration/place_order_test.go`

---

- [ ] **Step 2.1: Refactor `place_order.go` handler**

Modify `examples/orders/application/order/place_order.go`. Replace the entire file with:

```go
// Package order contains the application-layer command and query handlers
// for the Order aggregate. Handlers are kept transport-agnostic; the cmd
// wiring layer plugs them into HTTP, Kafka subscribers, etc.
package order

import (
	"context"

	"github.com/slam0504/go-ddd-core/application"
	"github.com/slam0504/go-ddd-core/eventbus"

	orderdom "github.com/slam0504/go-ddd-adapters/examples/orders/domain/order"
)

// PlaceOrderCommand creates a new order and transitions it to placed.
type PlaceOrderCommand struct {
	OrderID    string
	CustomerID string
	Items      []orderdom.Item
}

func (PlaceOrderCommand) CommandName() string { return "order.place" }

type PlaceOrderResult struct {
	OrderID    string
	TotalCents int64
}

// PlaceOrderHandler stages OrderPlaced into the outbox in the same
// transaction as the aggregate save. The UnitOfWork bridges to the
// underlying pgx TxManager; the handler itself stays driver-agnostic.
type PlaceOrderHandler struct {
	uow    application.UnitOfWork
	repo   orderdom.Repository
	outbox eventbus.Outbox
	topic  string
	newID  func() string
}

func NewPlaceOrderHandler(
	uow application.UnitOfWork,
	repo orderdom.Repository,
	outbox eventbus.Outbox,
	topic string,
	newID func() string,
) *PlaceOrderHandler {
	return &PlaceOrderHandler{uow: uow, repo: repo, outbox: outbox, topic: topic, newID: newID}
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

	o.ClearEvents()
	return PlaceOrderResult{OrderID: cmd.OrderID, TotalCents: o.TotalCents()}, nil
}
```

- [ ] **Step 2.2: Refactor `cmd/api/main.go` wiring**

Modify `examples/orders/cmd/api/main.go`. Replace the entire file with:

```go
// Command api is the command-side HTTP service for the orders example.
// POST /orders accepts a place-order request, dispatches the command,
// persists to Postgres, and stages OrderPlaced into the outbox in the
// same transaction. A separate cmd/relay drains the outbox to Kafka.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/slam0504/go-ddd-core/application"
	"github.com/slam0504/go-ddd-core/application/command"
	"github.com/slam0504/go-ddd-core/bootstrap"
	"github.com/slam0504/go-ddd-core/eventbus"
	"github.com/slam0504/go-ddd-core/ports/logger"

	"github.com/slam0504/go-ddd-adapters/eventbus/kafka"
	pgxoutbox "github.com/slam0504/go-ddd-adapters/eventbus/outbox/pgx"
	apporder "github.com/slam0504/go-ddd-adapters/examples/orders/application/order"
	"github.com/slam0504/go-ddd-adapters/examples/orders/cmd/internal/runtime"
	orderdom "github.com/slam0504/go-ddd-adapters/examples/orders/domain/order"
	"github.com/slam0504/go-ddd-adapters/examples/orders/infra/eventcodec"
	"github.com/slam0504/go-ddd-adapters/examples/orders/infra/pgxrepo"
	slogger "github.com/slam0504/go-ddd-adapters/logger/slogger"
	otelad "github.com/slam0504/go-ddd-adapters/observability/otel"
	pgxdb "github.com/slam0504/go-ddd-adapters/ports/database/pgx"
)

const (
	defaultHTTPAddr = ":8080"
	topicPlaced     = "orders.placed"
	serviceName     = "orders-api"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()
	log := slogger.New(slogger.Config{Level: slog.LevelInfo}).With(logger.F("service", serviceName))

	dbURL, err := runtime.RequiredEnv("DATABASE_URL")
	if err != nil {
		return err
	}

	prov, err := runtime.OTelProvider(ctx, serviceName, os.Getenv("OTEL_OTLP_ENDPOINT"))
	if err != nil {
		return err
	}

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("pgxpool: %w", err)
	}
	defer pool.Close()

	codec := eventcodec.New()
	store, err := pgxoutbox.NewStore(pgxoutbox.Config{Pool: pool, Codec: codec})
	if err != nil {
		return fmt.Errorf("pgxoutbox store: %w", err)
	}

	txMgr := pgxdb.NewTxManager(pool)
	uow := application.UnitOfWorkFromTxManager(txMgr)
	repo := pgxrepo.New(pool)

	cmdBus := registerCommands(uow, repo, store)

	app := bootstrap.New(bootstrap.Options{Logger: log})
	app.Use(
		otelad.Module(prov),
		runtime.HTTPModule(runtime.EnvOr("HTTP_ADDR", defaultHTTPAddr), routes(cmdBus, log), log),
	)
	return app.Run(ctx)
}

func registerCommands(uow application.UnitOfWork, repo orderdom.Repository, outbox eventbus.Outbox) *command.InMemoryBus {
	bus := command.NewInMemoryBus()
	command.Register[apporder.PlaceOrderCommand, apporder.PlaceOrderResult](
		bus,
		apporder.NewPlaceOrderHandler(uow, repo, outbox, topicPlaced, uuid.NewString),
	)
	return bus
}

func routes(cmdBus *command.InMemoryBus, log logger.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /orders", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			OrderID    string          `json:"order_id"`
			CustomerID string          `json:"customer_id"`
			Items      []orderdom.Item `json:"items"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if body.OrderID == "" {
			body.OrderID = uuid.NewString()
		}
		ctx := kafka.WithCorrelationID(r.Context(), body.OrderID)
		res, err := cmdBus.Dispatch(ctx, apporder.PlaceOrderCommand{
			OrderID:    body.OrderID,
			CustomerID: body.CustomerID,
			Items:      body.Items,
		})
		if err != nil {
			log.Log(r.Context(), logger.LevelWarn, "place order failed", logger.F("err", err.Error()))
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, res)
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
```

Notes for the implementer:
- The `kafka.PublisherModule(pub)` line from the old wiring is gone. So is the `newPublisher` helper. `cmd/api` no longer talks to Kafka.
- `kafka.WithCorrelationID(...)` import stays only because the route handler still annotates ctx with the order id; the codec picks it up at outbox.Stage time.

- [ ] **Step 2.3: Refactor `integration/main_test.go` to run(m) int + Postgres + dual-source migrate**

Modify `examples/orders/integration/main_test.go`. Replace the entire file with:

```go
//go:build integration

// Package integration runs the Kafka + Postgres adapters against real
// services started via testcontainers. Tests are gated behind the
// `integration` build tag so `go test ./...` stays fast for everyday
// work.
package integration

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/modules/redpanda"

	pgxoutboxmigs "github.com/slam0504/go-ddd-adapters/eventbus/outbox/pgx/migrations"

	ordersmigs "github.com/slam0504/go-ddd-adapters/examples/orders/migrations"
)

const (
	redpandaImage = "docker.redpanda.com/redpandadata/redpanda:v24.2.7"
	postgresImage = "postgres:16-alpine"
)

// sharedBrokers is populated by run() with the broker address of a
// single redpanda container shared across all tests in this package.
var sharedBrokers []string

// sharedPool is the pgx pool against the shared Postgres container,
// populated after migrations apply. Tests TRUNCATE between cases to
// keep one container fast.
var sharedPool *pgxpool.Pool

// TestMain wraps run so deferred cleanups (container Terminate, pool
// Close) fire before the process exits. os.Exit(m.Run()) would skip
// every defer above it.
func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	ctx := context.Background()

	// Postgres
	pgC, err := postgres.Run(ctx, postgresImage,
		postgres.WithDatabase("orders"),
		postgres.WithUsername("orders"),
		postgres.WithPassword("orders"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		log.Printf("integration: start postgres: %v", err)
		return 1
	}
	defer func() {
		if terr := pgC.Terminate(ctx); terr != nil {
			log.Printf("integration: terminate postgres: %v", terr)
		}
	}()

	pgDSN, err := pgC.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		log.Printf("integration: postgres dsn: %v", err)
		return 1
	}

	pool, err := pgxpool.New(ctx, pgDSN)
	if err != nil {
		log.Printf("integration: pgxpool: %v", err)
		return 1
	}
	defer pool.Close()
	sharedPool = pool

	if err := applyMigrations(pgDSN, "outbox_schema_migrations", pgxoutboxmigs.FS); err != nil {
		log.Printf("integration: outbox migrations: %v", err)
		return 1
	}
	if err := applyMigrations(pgDSN, "orders_schema_migrations", ordersmigs.FS); err != nil {
		log.Printf("integration: orders migrations: %v", err)
		return 1
	}

	// Redpanda
	rpC, err := redpanda.Run(ctx, redpandaImage, redpanda.WithAutoCreateTopics())
	if err != nil {
		log.Printf("integration: start redpanda: %v", err)
		return 1
	}
	defer func() {
		if terr := rpC.Terminate(ctx); terr != nil {
			log.Printf("integration: terminate redpanda: %v", terr)
		}
	}()

	broker, err := rpC.KafkaSeedBroker(ctx)
	if err != nil {
		log.Printf("integration: kafka broker: %v", err)
		return 1
	}
	sharedBrokers = []string{broker}

	fmt.Fprintf(os.Stderr, "integration: postgres ready at %s\n", pgDSN)
	fmt.Fprintf(os.Stderr, "integration: redpanda ready at %s\n", broker)

	return m.Run()
}

// applyMigrations runs `up` for srcFS against pgDSN with the given
// migrations-state table. The pgx5 driver requires the URL scheme
// pgx5://; the testcontainer DSN starts with postgres://, so we rewrite.
func applyMigrations(pgDSN, table string, srcFS embed.FS) error {
	src, err := iofs.New(srcFS, ".")
	if err != nil {
		return fmt.Errorf("iofs: %w", err)
	}

	pgx5DSN := strings.Replace(pgDSN, "postgres://", "pgx5://", 1)
	if strings.Contains(pgx5DSN, "?") {
		pgx5DSN += "&x-migrations-table=" + table
	} else {
		pgx5DSN += "?x-migrations-table=" + table
	}

	m, err := migrate.NewWithSourceInstance("iofs", src, pgx5DSN)
	if err != nil {
		return fmt.Errorf("new migrate (%s): %w", table, err)
	}
	defer func() {
		srcErr, dbErr := m.Close()
		if srcErr != nil {
			log.Printf("integration: migrate close src (%s): %v", table, srcErr)
		}
		if dbErr != nil {
			log.Printf("integration: migrate close db (%s): %v", table, dbErr)
		}
	}()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("up (%s): %w", table, err)
	}
	return nil
}

// truncate clears all three tables. Called by each test that needs a
// clean slate. The two outbox tables and orders are TRUNCATEd together
// in one statement so foreign keys (none today) wouldn't trip ordering.
func truncate(t *testing.T) {
	t.Helper()
	_, err := sharedPool.Exec(context.Background(),
		`TRUNCATE orders, outbox_records, outbox_dead_letters RESTART IDENTITY`)
	if err != nil {
		t.Fatalf("truncate: %v", err)
	}
}
```

Notes for the implementer:
- The `golang-migrate/v4` library is already a transitive dependency via the adapter root module's outbox integration tests; if go.mod surfaces a need to add it explicitly to the `examples/orders` go.mod, `go mod tidy` after this step will handle it.
- The `pgx5` driver alias is `github.com/golang-migrate/migrate/v4/database/pgx5` per v4.19.x. If the import fails, check the upstream release notes and use the correct package.
- `RESTART IDENTITY` is defensive; orders table has no IDENTITY column today, but outbox_records does.

- [ ] **Step 2.4: Add `golang-migrate` to examples/orders go.mod if needed**

After step 2.3 edits, run:
```bash
cd /Users/eason_tseng/playground/project/go-ddd-adapters/examples/orders
go mod tidy
```

Expected: `go.mod` and `go.sum` gain `github.com/golang-migrate/migrate/v4` if not already pulled in. No errors.

- [ ] **Step 2.5: Write PlaceOrder happy-path integration test**

Create `examples/orders/integration/place_order_test.go`:

```go
//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/slam0504/go-ddd-core/application"
	"github.com/slam0504/go-ddd-core/eventbus"
	"github.com/slam0504/go-ddd-core/ports/logger"

	"github.com/slam0504/go-ddd-adapters/eventbus/kafka"
	"github.com/slam0504/go-ddd-adapters/eventbus/outbox"
	pgxoutbox "github.com/slam0504/go-ddd-adapters/eventbus/outbox/pgx"
	apporder "github.com/slam0504/go-ddd-adapters/examples/orders/application/order"
	orderdom "github.com/slam0504/go-ddd-adapters/examples/orders/domain/order"
	"github.com/slam0504/go-ddd-adapters/examples/orders/infra/eventcodec"
	"github.com/slam0504/go-ddd-adapters/examples/orders/infra/pgxrepo"
	slogger "github.com/slam0504/go-ddd-adapters/logger/slogger"
	pgxdb "github.com/slam0504/go-ddd-adapters/ports/database/pgx"
)

func testLogger() logger.Logger {
	return slogger.New(slogger.Config{Level: slog.LevelWarn})
}

// TestPlaceOrder_TransactionalOutbox asserts: (1) orders row exists,
// (2) outbox_records row exists with headers from ctx, (3) the Relay
// publishes the row and preserves trace/causation/correlation headers
// through to the Kafka envelope.
func TestPlaceOrder_TransactionalOutbox(t *testing.T) {
	truncate(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	codec := eventcodec.New()
	uow := application.UnitOfWorkFromTxManager(pgxdb.NewTxManager(sharedPool))
	repo := pgxrepo.New(sharedPool)
	store, err := pgxoutbox.NewStore(pgxoutbox.Config{Pool: sharedPool, Codec: codec})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	topic := "orders.placed.happy.test-" + uuid.NewString()
	handler := apporder.NewPlaceOrderHandler(uow, repo, store, topic, uuid.NewString)

	const (
		wantTrace       = "trace-pop"
		wantCausation   = "cause-pop"
		wantCorrelation = "correlation-pop"
		orderID         = "ord-happy"
	)

	pubCtx := kafka.WithTraceID(ctx, wantTrace)
	pubCtx = kafka.WithCausationID(pubCtx, wantCausation)
	pubCtx = kafka.WithCorrelationID(pubCtx, wantCorrelation)

	res, err := handler.Handle(pubCtx, apporder.PlaceOrderCommand{
		OrderID:    orderID,
		CustomerID: "alice",
		Items:      []orderdom.Item{{SKU: "A", Quantity: 2, PriceCents: 499}},
	})
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if res.TotalCents != 998 {
		t.Errorf("result total_cents: want 998, got %d", res.TotalCents)
	}

	// (i) orders row exists with the right shape
	var (
		customer   string
		status     string
		version    int64
		totalCents int64
	)
	if err := sharedPool.QueryRow(ctx,
		`SELECT customer_id, status, version, total_cents FROM orders WHERE id=$1`,
		orderID,
	).Scan(&customer, &status, &version, &totalCents); err != nil {
		t.Fatalf("orders row: %v", err)
	}
	if customer != "alice" {
		t.Errorf("customer: want alice, got %q", customer)
	}
	if status != string(orderdom.StatusPlaced) {
		t.Errorf("status: want %q, got %q", orderdom.StatusPlaced, status)
	}
	if totalCents != 998 {
		t.Errorf("total_cents: want 998, got %d", totalCents)
	}
	if version <= 0 {
		t.Errorf("version: want > 0, got %d", version)
	}

	// (ii) outbox_records row exists with the headers we set on ctx
	var (
		topicGot    string
		eventName   string
		aggregateID string
		headersJSON []byte
	)
	if err := sharedPool.QueryRow(ctx,
		`SELECT topic, event_name, aggregate_id, headers FROM outbox_records WHERE aggregate_id=$1`,
		orderID,
	).Scan(&topicGot, &eventName, &aggregateID, &headersJSON); err != nil {
		t.Fatalf("outbox row: %v", err)
	}
	if topicGot != topic {
		t.Errorf("outbox topic: want %q, got %q", topic, topicGot)
	}
	if eventName != orderdom.EventNamePlaced {
		t.Errorf("outbox event_name: want %q, got %q", orderdom.EventNamePlaced, eventName)
	}
	var hdrs map[string]string
	if err := json.Unmarshal(headersJSON, &hdrs); err != nil {
		t.Fatalf("headers json: %v", err)
	}
	if hdrs[eventbus.HeaderTraceID] != wantTrace {
		t.Errorf("outbox trace header: want %q, got %q", wantTrace, hdrs[eventbus.HeaderTraceID])
	}
	if hdrs[eventbus.HeaderCausationID] != wantCausation {
		t.Errorf("outbox causation header: want %q, got %q", wantCausation, hdrs[eventbus.HeaderCausationID])
	}
	if hdrs[eventbus.HeaderCorrelationID] != wantCorrelation {
		t.Errorf("outbox correlation header: want %q, got %q", wantCorrelation, hdrs[eventbus.HeaderCorrelationID])
	}

	// (iii) Subscribe + Relay; assert the envelope carries the headers
	// across the outbox -> relay -> kafka boundary.
	sub, err := kafka.NewSubscriber(kafka.SubscriberConfig{
		Brokers:       sharedBrokers,
		ConsumerGroup: "test-place-happy-" + uuid.NewString(),
		Codec:         codec,
	})
	if err != nil {
		t.Fatalf("new subscriber: %v", err)
	}
	t.Cleanup(func() { _ = sub.Close() })

	envelopes, err := sub.Subscribe(ctx, topic)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	pub, err := kafka.NewPublisher(kafka.PublisherConfig{
		Brokers:              sharedBrokers,
		Codec:                codec,
		PartitionByAggregate: true,
	})
	if err != nil {
		t.Fatalf("new publisher: %v", err)
	}
	t.Cleanup(func() { _ = pub.Close() })

	relay, err := outbox.NewRelay(
		outbox.RelayConfig{Store: store, Publisher: pub, Codec: codec, Logger: testLogger()},
		outbox.WithHeaderRestorer(kafka.RestoreCoreHeaders),
		outbox.WithPollInterval(50*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("new relay: %v", err)
	}

	relayCtx, relayCancel := context.WithCancel(ctx)
	relayDone := make(chan struct{})
	go func() {
		defer close(relayDone)
		if rerr := relay.Run(relayCtx); rerr != nil && !errors.Is(rerr, context.Canceled) {
			t.Errorf("relay.Run: %v", rerr)
		}
	}()

	select {
	case env := <-envelopes:
		meta := env.Raw.Metadata
		if got := meta.Get(eventbus.HeaderTraceID); got != wantTrace {
			t.Errorf("kafka trace header: want %q, got %q", wantTrace, got)
		}
		if got := meta.Get(eventbus.HeaderCausationID); got != wantCausation {
			t.Errorf("kafka causation header: want %q, got %q", wantCausation, got)
		}
		if got := meta.Get(eventbus.HeaderCorrelationID); got != wantCorrelation {
			t.Errorf("kafka correlation header: want %q, got %q", wantCorrelation, got)
		}
		if meta.Get(eventbus.HeaderEventName) != orderdom.EventNamePlaced {
			t.Errorf("kafka event_name header: want %q, got %q", orderdom.EventNamePlaced, meta.Get(eventbus.HeaderEventName))
		}
		env.Ack()
	case <-time.After(15 * time.Second):
		t.Fatal("timeout waiting for relay publish")
	}

	relayCancel()
	select {
	case <-relayDone:
	case <-time.After(5 * time.Second):
		t.Fatal("relay did not stop within 5s after cancel")
	}
}
```

- [ ] **Step 2.6: Write PlaceOrder rollback integration test (same file)**

Append to `examples/orders/integration/place_order_test.go`:

```go
// TestPlaceOrder_TxRollback wraps the real UoW to force an error after
// the handler's repo.Save + outbox.Stage succeed. The pgx tx must
// rollback, leaving zero rows in orders and outbox_records.
func TestPlaceOrder_TxRollback(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	codec := eventcodec.New()
	realUoW := application.UnitOfWorkFromTxManager(pgxdb.NewTxManager(sharedPool))
	repo := pgxrepo.New(sharedPool)
	store, err := pgxoutbox.NewStore(pgxoutbox.Config{Pool: sharedPool, Codec: codec})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	forceErr := errors.New("forced rollback")
	testUoW := application.UnitOfWorkFunc(func(ctx context.Context, fn func(context.Context) error) error {
		return realUoW.Do(ctx, func(ctx context.Context) error {
			if err := fn(ctx); err != nil {
				return err
			}
			return forceErr
		})
	})

	handler := apporder.NewPlaceOrderHandler(
		testUoW, repo, store, "orders.placed.rollback.test", uuid.NewString)

	_, err = handler.Handle(ctx, apporder.PlaceOrderCommand{
		OrderID:    "ord-rollback",
		CustomerID: "bob",
		Items:      []orderdom.Item{{SKU: "B", Quantity: 1, PriceCents: 99}},
	})
	if !errors.Is(err, forceErr) {
		t.Fatalf("handle err: want forceErr, got %v", err)
	}

	var orderCount, outboxCount int
	if err := sharedPool.QueryRow(ctx, `SELECT COUNT(*) FROM orders`).Scan(&orderCount); err != nil {
		t.Fatalf("count orders: %v", err)
	}
	if err := sharedPool.QueryRow(ctx, `SELECT COUNT(*) FROM outbox_records`).Scan(&outboxCount); err != nil {
		t.Fatalf("count outbox_records: %v", err)
	}
	if orderCount != 0 {
		t.Errorf("orders rows after rollback: want 0, got %d", orderCount)
	}
	if outboxCount != 0 {
		t.Errorf("outbox rows after rollback: want 0, got %d", outboxCount)
	}
}
```

- [ ] **Step 2.7: Run integration tests for the two new tests**

Run:
```bash
cd /Users/eason_tseng/playground/project/go-ddd-adapters/examples/orders
go test -tags=integration -race ./integration/ -run 'TestPlaceOrder_' -v
```

Expected: both `TestPlaceOrder_TransactionalOutbox` and `TestPlaceOrder_TxRollback` PASS.

If Docker is not running, the test will fail at testcontainer startup. Start Docker and re-run.

- [ ] **Step 2.8: Run the full integration suite to verify the TestMain refactor doesn't regress existing tests**

Run:
```bash
cd /Users/eason_tseng/playground/project/go-ddd-adapters/examples/orders
go test -tags=integration -race ./integration/ -v
```

Expected: all tests PASS — both new PlaceOrder ones and the existing `TestRoundTrip_*` / `TestPartitionByAggregate_*`. The new TestMain pattern (`run(m) int`) must keep these working unchanged.

- [ ] **Step 2.9: Lint with integration build tag**

Run:
```bash
cd /Users/eason_tseng/playground/project/go-ddd-adapters
golangci-lint run --build-tags=integration ./...
cd examples/orders
golangci-lint run --build-tags=integration ./...
```

Expected: `0 issues.` for both modules.

- [ ] **Step 2.10: Commit Task 2**

```bash
cd /Users/eason_tseng/playground/project/go-ddd-adapters
git add examples/orders/application/order/place_order.go \
        examples/orders/cmd/api/main.go \
        examples/orders/integration/main_test.go \
        examples/orders/integration/place_order_test.go \
        examples/orders/go.mod \
        examples/orders/go.sum
git commit -m "$(cat <<'EOF'
refactor(examples/orders): stage OrderPlaced through outbox

Refactors PlaceOrderHandler to use application.UnitOfWork and
eventbus.Outbox.Stage in place of the direct Publisher dependency.
cmd/api wires pgxpool + pgxdb.TxManager + pgxrepo + pgxoutbox.Store
and drops all Kafka client references — events flow into the outbox
table in the same transaction as the aggregate save; the relay binary
(Task 4) publishes to Kafka separately.

- application/order/place_order.go: handler signature is now
  (uow, repo, outbox, topic, newID). Handle's tx scope covers
  repo.Save + outbox.Stage only; domain logic runs outside the tx.
  ClearEvents runs after uow.Do returns nil.
- cmd/api/main.go: removes newPublisher, kafka.PublisherModule;
  adds pgxpool.New (with defer pool.Close()),
  pgxdb.NewTxManager, application.UnitOfWorkFromTxManager,
  pgxrepo.New, pgxoutbox.NewStore. registerCommands signature
  updates to (uow, repo, outbox).
- integration/main_test.go: TestMain refactored to the run(m) int
  wrapper pattern so deferred Postgres + Redpanda container
  terminations actually fire on exit. Postgres testcontainer
  (postgres:16-alpine) starts alongside Redpanda; dual-source
  migrations applied via golang-migrate library with
  x-migrations-table parameter per source (outbox_schema_migrations
  / orders_schema_migrations), pgx5:// scheme rewrite for the
  testcontainer DSN. truncate(t) helper clears all three tables
  between cases. Existing redpanda-only tests continue to work.
- integration/place_order_test.go: two targeted tests.
  TestPlaceOrder_TransactionalOutbox asserts (1) orders row,
  (2) outbox_records row with trace/causation/correlation headers
  set from ctx, (3) Relay publishes and Kafka envelope preserves
  those headers through outbox -> relay -> kafka.
  TestPlaceOrder_TxRollback wraps the UoW to force an error after
  Save+Stage; asserts handler returns the forced error and both
  tables are empty.

Pre-commit: integration tests + unit tests + lint
(--build-tags=integration) clean.
EOF
)"
```

Expected: commit succeeds.

---

## Task 3: Stage OrderShipped through outbox

**Goal:** Refactor `ShipOrderHandler` to UoW + Outbox; rewire `cmd/worker` to use pgxrepo + outbox; drop the hydrate-from-event block (replaced by `FindByID` from shared Postgres); add three integration tests (happy + rollback + not-found).

**Files:**
- Modify: `examples/orders/application/order/ship_order.go`
- Modify: `examples/orders/cmd/worker/main.go`
- Create: `examples/orders/integration/ship_order_test.go`

---

- [ ] **Step 3.1: Refactor `ship_order.go` handler**

Modify `examples/orders/application/order/ship_order.go`. Replace the entire file with:

```go
package order

import (
	"context"
	"errors"

	"github.com/slam0504/go-ddd-core/application"
	"github.com/slam0504/go-ddd-core/domain"
	"github.com/slam0504/go-ddd-core/eventbus"

	orderdom "github.com/slam0504/go-ddd-adapters/examples/orders/domain/order"
)

// ShipOrderCommand transitions an existing placed order to shipped.
type ShipOrderCommand struct {
	OrderID string
	Carrier string
}

func (ShipOrderCommand) CommandName() string { return "order.ship" }

type ShipOrderResult struct {
	OrderID string
}

// ShipOrderHandler stages OrderShipped into the outbox in the same
// transaction as the aggregate save. Already-shipped is treated as
// successful (idempotent ack path); not-found propagates so callers
// can map to broker nack.
type ShipOrderHandler struct {
	uow    application.UnitOfWork
	repo   orderdom.Repository
	outbox eventbus.Outbox
	topic  string
	newID  func() string
}

func NewShipOrderHandler(
	uow application.UnitOfWork,
	repo orderdom.Repository,
	outbox eventbus.Outbox,
	topic string,
	newID func() string,
) *ShipOrderHandler {
	return &ShipOrderHandler{uow: uow, repo: repo, outbox: outbox, topic: topic, newID: newID}
}

func (h *ShipOrderHandler) Handle(ctx context.Context, cmd ShipOrderCommand) (ShipOrderResult, error) {
	o, err := h.repo.FindByID(ctx, orderdom.ID(cmd.OrderID))
	if err != nil {
		return ShipOrderResult{}, err
	}

	if err := o.Ship(h.newID(), cmd.Carrier); err != nil {
		var rv *domain.RuleViolation
		if errors.As(err, &rv) && rv.Code == "ORDER_ALREADY_SHIPPED" {
			return ShipOrderResult{OrderID: cmd.OrderID}, nil
		}
		return ShipOrderResult{}, err
	}

	err = h.uow.Do(ctx, func(ctx context.Context) error {
		if err := h.repo.Save(ctx, o); err != nil {
			return err
		}
		return h.outbox.Stage(ctx, h.topic, o.DomainEvents()...)
	})
	if err != nil {
		return ShipOrderResult{}, err
	}

	o.ClearEvents()
	return ShipOrderResult{OrderID: cmd.OrderID}, nil
}
```

- [ ] **Step 3.2: Refactor `cmd/worker/main.go` wiring (drop hydrate-from-event)**

Modify `examples/orders/cmd/worker/main.go`. Replace the entire file with:

```go
// Command worker subscribes to OrderPlaced events and dispatches the ship
// command. The ship command loads the aggregate from the shared Postgres
// (api's commit is durable by the time relay publishes the event),
// transitions it to shipped, and stages OrderShipped via the outbox in
// the same transaction.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/slam0504/go-ddd-core/application"
	"github.com/slam0504/go-ddd-core/application/command"
	"github.com/slam0504/go-ddd-core/bootstrap"
	"github.com/slam0504/go-ddd-core/eventbus"
	"github.com/slam0504/go-ddd-core/ports/logger"

	"github.com/slam0504/go-ddd-adapters/eventbus/kafka"
	pgxoutbox "github.com/slam0504/go-ddd-adapters/eventbus/outbox/pgx"
	apporder "github.com/slam0504/go-ddd-adapters/examples/orders/application/order"
	"github.com/slam0504/go-ddd-adapters/examples/orders/cmd/internal/runtime"
	orderdom "github.com/slam0504/go-ddd-adapters/examples/orders/domain/order"
	"github.com/slam0504/go-ddd-adapters/examples/orders/infra/eventcodec"
	"github.com/slam0504/go-ddd-adapters/examples/orders/infra/pgxrepo"
	slogger "github.com/slam0504/go-ddd-adapters/logger/slogger"
	otelad "github.com/slam0504/go-ddd-adapters/observability/otel"
	pgxdb "github.com/slam0504/go-ddd-adapters/ports/database/pgx"
)

const (
	topicPlaced    = "orders.placed"
	topicShipped   = "orders.shipped"
	consumerGroup  = "orders-worker"
	serviceName    = "orders-worker"
	defaultCarrier = "demo-carrier"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()
	log := slogger.New(slogger.Config{Level: slog.LevelInfo}).With(logger.F("service", serviceName))

	brokers, err := runtime.BrokersFromEnv()
	if err != nil {
		return err
	}

	dbURL, err := runtime.RequiredEnv("DATABASE_URL")
	if err != nil {
		return err
	}

	prov, err := runtime.OTelProvider(ctx, serviceName, os.Getenv("OTEL_OTLP_ENDPOINT"))
	if err != nil {
		return err
	}

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("pgxpool: %w", err)
	}
	defer pool.Close()

	codec := eventcodec.New()
	store, err := pgxoutbox.NewStore(pgxoutbox.Config{Pool: pool, Codec: codec})
	if err != nil {
		return fmt.Errorf("pgxoutbox store: %w", err)
	}

	txMgr := pgxdb.NewTxManager(pool)
	uow := application.UnitOfWorkFromTxManager(txMgr)
	repo := pgxrepo.New(pool)

	sub, err := newSubscriber(brokers, codec)
	if err != nil {
		return err
	}

	cmdBus := registerCommands(uow, repo, store)

	// kafka.ConsumerModule's handle returns error — nil means Ack,
	// non-nil means Nack. Do NOT call env.Ack/Nack manually; the
	// adapter owns ack lifecycle based on the return value.
	handle := func(hctx context.Context, env eventbus.Envelope) error {
		return handleOrderPlaced(hctx, log, cmdBus, env)
	}

	// Stop order (reverse Use): ConsumerModule drains in-flight handlers
	// -> SubscriberModule closes the watermill subscriber -> otelad
	// flushes + shuts down.
	app := bootstrap.New(bootstrap.Options{Logger: log})
	app.Use(
		otelad.Module(prov),
		kafka.SubscriberModule(sub),
		kafka.ConsumerModule(sub, topicPlaced, log, handle),
	)
	return app.Run(ctx)
}

func newSubscriber(brokers []string, codec eventbus.Codec) (*kafka.Subscriber, error) {
	sub, err := kafka.NewSubscriber(kafka.SubscriberConfig{
		Brokers:       brokers,
		ConsumerGroup: consumerGroup,
		Codec:         codec,
	})
	if err != nil {
		return nil, fmt.Errorf("kafka subscriber: %w", err)
	}
	return sub, nil
}

func registerCommands(uow application.UnitOfWork, repo orderdom.Repository, outbox eventbus.Outbox) *command.InMemoryBus {
	bus := command.NewInMemoryBus()
	command.Register[apporder.ShipOrderCommand, apporder.ShipOrderResult](
		bus,
		apporder.NewShipOrderHandler(uow, repo, outbox, topicShipped, uuid.NewString),
	)
	return bus
}

func handleOrderPlaced(
	ctx context.Context,
	log logger.Logger,
	cmdBus *command.InMemoryBus,
	env eventbus.Envelope,
) error {
	placed, ok := env.Event.(*orderdom.OrderPlaced)
	if !ok {
		return fmt.Errorf("unexpected event type: %s", env.Name)
	}

	if _, err := cmdBus.Dispatch(ctx, apporder.ShipOrderCommand{
		OrderID: placed.AggregateID(),
		Carrier: defaultCarrier,
	}); err != nil {
		return fmt.Errorf("ship dispatch order=%s: %w", placed.AggregateID(), err)
	}

	log.Log(ctx, logger.LevelInfo, "shipped",
		logger.F("order_id", placed.AggregateID()),
		logger.F("carrier", defaultCarrier))
	return nil
}
```

Notes for the implementer:
- The entire hydrate-from-event block (the `orderdom.Hydrate(...)` call + `repo.Save(...)` in the old `handleOrderPlaced`) is gone. `ShipOrderHandler` loads the aggregate from shared Postgres via `repo.FindByID` — api's commit is already durable by the time the relay publishes the event.
- `defaultCarrier` changes from `"FedEx"` to `"demo-carrier"` per spec to make it obvious it's demo-only data.
- `kafka.PublisherModule(pub)` is removed; the worker no longer publishes anything.
- The handler signature `func(ctx, env) error` is the existing `kafka.ConsumerModule` contract (verified at `eventbus/kafka/module.go:56`). Returning `nil` Acks; returning non-nil Nacks. Do NOT call `env.Ack()` / `env.Nack()` manually — that would conflict with the adapter's ack lifecycle.

- [ ] **Step 3.3: Write ShipOrder happy-path integration test**

Create `examples/orders/integration/ship_order_test.go`:

```go
//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/slam0504/go-ddd-core/application"
	"github.com/slam0504/go-ddd-core/domain"
	"github.com/slam0504/go-ddd-core/eventbus"

	"github.com/slam0504/go-ddd-adapters/eventbus/kafka"
	"github.com/slam0504/go-ddd-adapters/eventbus/outbox"
	pgxoutbox "github.com/slam0504/go-ddd-adapters/eventbus/outbox/pgx"
	apporder "github.com/slam0504/go-ddd-adapters/examples/orders/application/order"
	orderdom "github.com/slam0504/go-ddd-adapters/examples/orders/domain/order"
	"github.com/slam0504/go-ddd-adapters/examples/orders/infra/eventcodec"
	"github.com/slam0504/go-ddd-adapters/examples/orders/infra/pgxrepo"
	pgxdb "github.com/slam0504/go-ddd-adapters/ports/database/pgx"
)

// seedPlacedOrder writes a placed order to the orders table directly,
// bypassing the outbox path. This keeps the ShipOrder outbox row count
// in tests at exactly one (OrderShipped) — seeding via PlaceOrderHandler
// would leave an OrderPlaced row that would pollute COUNT(*) assertions.
func seedPlacedOrder(t *testing.T, id, customer string, items []orderdom.Item, totalCents int64) {
	t.Helper()
	o := orderdom.Hydrate(orderdom.ID(id), customer, orderdom.StatusPlaced, 1, totalCents, items)
	repo := pgxrepo.New(sharedPool)
	if err := repo.Save(context.Background(), o); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// TestShipOrder_TransactionalOutbox seeds a placed order, dispatches
// ShipOrderCommand through ShipOrderHandler, then asserts: (1) orders
// row status moved to shipped + version incremented, (2) outbox_records
// has exactly one row with topic=orders.shipped + headers from ctx,
// (3) Relay publishes and Kafka envelope carries the headers.
func TestShipOrder_TransactionalOutbox(t *testing.T) {
	truncate(t)

	const (
		wantTrace       = "trace-ship"
		wantCausation   = "cause-ship"
		wantCorrelation = "correlation-ship"
		orderID         = "ord-ship-happy"
	)

	seedPlacedOrder(t, orderID, "carol",
		[]orderdom.Item{{SKU: "C", Quantity: 3, PriceCents: 100}}, 300)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	codec := eventcodec.New()
	uow := application.UnitOfWorkFromTxManager(pgxdb.NewTxManager(sharedPool))
	repo := pgxrepo.New(sharedPool)
	store, err := pgxoutbox.NewStore(pgxoutbox.Config{Pool: sharedPool, Codec: codec})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	topic := "orders.shipped.happy.test-" + uuid.NewString()
	handler := apporder.NewShipOrderHandler(uow, repo, store, topic, uuid.NewString)

	pubCtx := kafka.WithTraceID(ctx, wantTrace)
	pubCtx = kafka.WithCausationID(pubCtx, wantCausation)
	pubCtx = kafka.WithCorrelationID(pubCtx, wantCorrelation)

	if _, err := handler.Handle(pubCtx, apporder.ShipOrderCommand{
		OrderID: orderID,
		Carrier: "demo-carrier",
	}); err != nil {
		t.Fatalf("handle: %v", err)
	}

	// (i) orders row status = shipped, version incremented
	var status string
	var version int64
	if err := sharedPool.QueryRow(ctx,
		`SELECT status, version FROM orders WHERE id=$1`, orderID,
	).Scan(&status, &version); err != nil {
		t.Fatalf("orders row: %v", err)
	}
	if status != string(orderdom.StatusShipped) {
		t.Errorf("status: want %q, got %q", orderdom.StatusShipped, status)
	}
	if version != 2 {
		t.Errorf("version: want 2 (seed=1, Ship increments), got %d", version)
	}

	// (ii) exactly one outbox row for this aggregate, topic = orders.shipped, headers from ctx
	var rowCount int
	if err := sharedPool.QueryRow(ctx,
		`SELECT COUNT(*) FROM outbox_records WHERE aggregate_id=$1`, orderID,
	).Scan(&rowCount); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if rowCount != 1 {
		t.Errorf("outbox rows for %s: want 1, got %d", orderID, rowCount)
	}

	var (
		topicGot    string
		eventName   string
		headersJSON []byte
	)
	if err := sharedPool.QueryRow(ctx,
		`SELECT topic, event_name, headers FROM outbox_records WHERE aggregate_id=$1`,
		orderID,
	).Scan(&topicGot, &eventName, &headersJSON); err != nil {
		t.Fatalf("outbox row: %v", err)
	}
	if topicGot != topic {
		t.Errorf("outbox topic: want %q, got %q", topic, topicGot)
	}
	if eventName != orderdom.EventNameShipped {
		t.Errorf("outbox event_name: want %q, got %q", orderdom.EventNameShipped, eventName)
	}
	var hdrs map[string]string
	if err := json.Unmarshal(headersJSON, &hdrs); err != nil {
		t.Fatalf("headers json: %v", err)
	}
	if hdrs[eventbus.HeaderTraceID] != wantTrace {
		t.Errorf("outbox trace header: want %q, got %q", wantTrace, hdrs[eventbus.HeaderTraceID])
	}

	// (iii) Relay publishes; kafka envelope preserves headers
	sub, err := kafka.NewSubscriber(kafka.SubscriberConfig{
		Brokers:       sharedBrokers,
		ConsumerGroup: "test-ship-happy-" + uuid.NewString(),
		Codec:         codec,
	})
	if err != nil {
		t.Fatalf("new subscriber: %v", err)
	}
	t.Cleanup(func() { _ = sub.Close() })

	envelopes, err := sub.Subscribe(ctx, topic)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	pub, err := kafka.NewPublisher(kafka.PublisherConfig{
		Brokers:              sharedBrokers,
		Codec:                codec,
		PartitionByAggregate: true,
	})
	if err != nil {
		t.Fatalf("new publisher: %v", err)
	}
	t.Cleanup(func() { _ = pub.Close() })

	relay, err := outbox.NewRelay(
		outbox.RelayConfig{Store: store, Publisher: pub, Codec: codec, Logger: testLogger()},
		outbox.WithHeaderRestorer(kafka.RestoreCoreHeaders),
		outbox.WithPollInterval(50*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("new relay: %v", err)
	}

	relayCtx, relayCancel := context.WithCancel(ctx)
	relayDone := make(chan struct{})
	go func() {
		defer close(relayDone)
		if rerr := relay.Run(relayCtx); rerr != nil && !errors.Is(rerr, context.Canceled) {
			t.Errorf("relay.Run: %v", rerr)
		}
	}()

	select {
	case env := <-envelopes:
		meta := env.Raw.Metadata
		if got := meta.Get(eventbus.HeaderTraceID); got != wantTrace {
			t.Errorf("kafka trace header: want %q, got %q", wantTrace, got)
		}
		if got := meta.Get(eventbus.HeaderEventName); got != orderdom.EventNameShipped {
			t.Errorf("kafka event_name: want %q, got %q", orderdom.EventNameShipped, got)
		}
		env.Ack()
	case <-time.After(15 * time.Second):
		t.Fatal("timeout waiting for relay publish")
	}

	relayCancel()
	select {
	case <-relayDone:
	case <-time.After(5 * time.Second):
		t.Fatal("relay did not stop within 5s after cancel")
	}
}
```

- [ ] **Step 3.4: Write ShipOrder rollback + not-found tests (same file)**

Append to `examples/orders/integration/ship_order_test.go`:

```go
// TestShipOrder_TxRollback seeds a placed order, then forces a rollback
// inside the handler's UoW. Asserts (a) handler returns the forced
// error, (b) orders.status stays "placed" (no aggregate mutation
// persisted), (c) zero new outbox rows.
func TestShipOrder_TxRollback(t *testing.T) {
	truncate(t)

	const orderID = "ord-ship-rollback"
	seedPlacedOrder(t, orderID, "dan",
		[]orderdom.Item{{SKU: "D", Quantity: 1, PriceCents: 50}}, 50)

	ctx := context.Background()

	codec := eventcodec.New()
	realUoW := application.UnitOfWorkFromTxManager(pgxdb.NewTxManager(sharedPool))
	repo := pgxrepo.New(sharedPool)
	store, err := pgxoutbox.NewStore(pgxoutbox.Config{Pool: sharedPool, Codec: codec})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	forceErr := errors.New("forced rollback")
	testUoW := application.UnitOfWorkFunc(func(ctx context.Context, fn func(context.Context) error) error {
		return realUoW.Do(ctx, func(ctx context.Context) error {
			if err := fn(ctx); err != nil {
				return err
			}
			return forceErr
		})
	})

	handler := apporder.NewShipOrderHandler(
		testUoW, repo, store, "orders.shipped.rollback.test", uuid.NewString)

	_, err = handler.Handle(ctx, apporder.ShipOrderCommand{
		OrderID: orderID,
		Carrier: "demo-carrier",
	})
	if !errors.Is(err, forceErr) {
		t.Fatalf("handle err: want forceErr, got %v", err)
	}

	// Aggregate row still in placed state (the seed row), no mutation persisted.
	var status string
	var version int64
	if err := sharedPool.QueryRow(ctx,
		`SELECT status, version FROM orders WHERE id=$1`, orderID,
	).Scan(&status, &version); err != nil {
		t.Fatalf("orders row: %v", err)
	}
	if status != string(orderdom.StatusPlaced) {
		t.Errorf("status: want %q (seed unchanged), got %q", orderdom.StatusPlaced, status)
	}
	if version != 1 {
		t.Errorf("version: want 1 (seed unchanged), got %d", version)
	}

	// Zero outbox rows for this aggregate.
	var rowCount int
	if err := sharedPool.QueryRow(ctx,
		`SELECT COUNT(*) FROM outbox_records WHERE aggregate_id=$1`, orderID,
	).Scan(&rowCount); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if rowCount != 0 {
		t.Errorf("outbox rows after rollback: want 0, got %d", rowCount)
	}
}

// TestShipOrder_NotFound asserts pgxrepo.FindByID maps pgx.ErrNoRows
// to domain.ErrNotFound and ShipOrderHandler surfaces it unchanged
// (so the worker can decide to nack vs ack-and-log). This locks the
// not-found contract that the spec calls out explicitly.
func TestShipOrder_NotFound(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	codec := eventcodec.New()
	uow := application.UnitOfWorkFromTxManager(pgxdb.NewTxManager(sharedPool))
	repo := pgxrepo.New(sharedPool)
	store, err := pgxoutbox.NewStore(pgxoutbox.Config{Pool: sharedPool, Codec: codec})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	handler := apporder.NewShipOrderHandler(
		uow, repo, store, "orders.shipped.notfound.test", uuid.NewString)

	_, err = handler.Handle(ctx, apporder.ShipOrderCommand{
		OrderID: "does-not-exist",
		Carrier: "demo-carrier",
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("handle err: want domain.ErrNotFound, got %v", err)
	}
}
```

- [ ] **Step 3.5: Build everything to verify worker compile**

Run:
```bash
cd /Users/eason_tseng/playground/project/go-ddd-adapters/examples/orders
go build ./...
```

Expected: clean build. The worker uses `kafka.ConsumerModule(sub, topic, log, handle)` with `handle func(ctx, env) error` — same signature as before the refactor, just without the publisher module.

- [ ] **Step 3.6: Run the ShipOrder tests**

Run:
```bash
cd /Users/eason_tseng/playground/project/go-ddd-adapters/examples/orders
go test -tags=integration -race ./integration/ -run 'TestShipOrder_' -v
```

Expected: all three `TestShipOrder_*` tests PASS.

- [ ] **Step 3.7: Run the full integration suite**

Run:
```bash
cd /Users/eason_tseng/playground/project/go-ddd-adapters/examples/orders
go test -tags=integration -race ./integration/ -v
```

Expected: PlaceOrder × 2 + ShipOrder × 3 + existing redpanda tests all PASS.

- [ ] **Step 3.8: Lint with integration build tag**

Run:
```bash
cd /Users/eason_tseng/playground/project/go-ddd-adapters/examples/orders
golangci-lint run --build-tags=integration ./...
```

Expected: `0 issues.`

- [ ] **Step 3.9: Commit Task 3**

```bash
cd /Users/eason_tseng/playground/project/go-ddd-adapters
git add examples/orders/application/order/ship_order.go \
        examples/orders/cmd/worker/main.go \
        examples/orders/integration/ship_order_test.go
git commit -m "$(cat <<'EOF'
refactor(examples/orders): stage OrderShipped through outbox

Mirrors the PlaceOrder refactor on the worker side. ShipOrderHandler
now depends on application.UnitOfWork + eventbus.Outbox instead of
Publisher; the worker drops its per-process memrepo and the
hydrate-from-event workaround entirely, since the shared Postgres
makes repo.FindByID the canonical way to read aggregate state.

- application/order/ship_order.go: handler signature is now
  (uow, repo, outbox, topic, newID). FindByID + domain mutation
  outside the tx; Save + Stage inside uow.Do. ORDER_ALREADY_SHIPPED
  remains a graceful-ack path (returns nil error to support
  at-least-once redelivery from the broker). Not-found propagates as
  domain.ErrNotFound — the worker's handler-level error path decides
  whether to nack or ack-and-log.
- cmd/worker/main.go: drops kafka.NewPublisher + PublisherModule;
  wires pgxpool + pgxdb.TxManager + UoW + pgxrepo + pgxoutbox.Store.
  handleOrderPlaced is now a single Dispatch call — the old
  Hydrate(...) + repo.Save() block is gone because the api's
  transactional commit + relay's "publish committed rows only"
  invariant guarantees the order is in Postgres by the time worker
  sees the event. defaultCarrier renamed from "FedEx" to
  "demo-carrier" to make demo-only data obvious. defer pool.Close()
  ties the pool into the binary's lifecycle.
- integration/ship_order_test.go: three targeted tests.
  TestShipOrder_TransactionalOutbox seeds a placed order via repo.Save
  outside any UoW (so no OrderPlaced outbox row), dispatches Ship,
  asserts (1) status=shipped + version=2, (2) exactly one outbox
  row with topic=orders.shipped + ctx headers, (3) Kafka envelope
  preserves headers across outbox -> relay -> kafka.
  TestShipOrder_TxRollback forces rollback; asserts handler returns
  forceErr, the seed row stays at status=placed/version=1, zero new
  outbox rows.
  TestShipOrder_NotFound asserts pgxrepo.FindByID maps pgx.ErrNoRows
  to domain.ErrNotFound and the handler surfaces it unchanged.

Pre-commit: integration tests + unit tests + lint
(--build-tags=integration) clean.
EOF
)"
```

---

## Task 4: Outbox relay binary

**Goal:** Add `cmd/relay/main.go` as wiring-only binary that drains pgx outbox to Kafka, plus Dockerfile changes to build + ship it.

**Files:**
- Create: `examples/orders/cmd/relay/main.go`
- Modify: `examples/orders/Dockerfile`

---

- [ ] **Step 4.1: Write `cmd/relay/main.go`**

Create `examples/orders/cmd/relay/main.go`:

```go
// Command relay drains the pgx outbox to Kafka. It composes existing
// adapter components (pgxoutbox.Store + outbox.Relay + kafka.Publisher
// + kafka.RestoreCoreHeaders) into a standalone bootstrap binary —
// no new generic relay package is introduced.
//
// Topology invariants: relay is the only binary that publishes to
// Kafka, and the only binary that calls Outbox MarkSent / MarkFailed /
// Terminate. api and worker only Stage rows into outbox_records.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/slam0504/go-ddd-core/bootstrap"
	"github.com/slam0504/go-ddd-core/ports/logger"

	"github.com/slam0504/go-ddd-adapters/eventbus/kafka"
	"github.com/slam0504/go-ddd-adapters/eventbus/outbox"
	pgxoutbox "github.com/slam0504/go-ddd-adapters/eventbus/outbox/pgx"
	"github.com/slam0504/go-ddd-adapters/examples/orders/cmd/internal/runtime"
	"github.com/slam0504/go-ddd-adapters/examples/orders/infra/eventcodec"
	slogger "github.com/slam0504/go-ddd-adapters/logger/slogger"
	otelad "github.com/slam0504/go-ddd-adapters/observability/otel"
)

const serviceName = "orders-relay"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()
	log := slogger.New(slogger.Config{Level: slog.LevelInfo}).
		With(logger.F("service", serviceName))

	brokers, err := runtime.BrokersFromEnv()
	if err != nil {
		return err
	}

	dbURL, err := runtime.RequiredEnv("DATABASE_URL")
	if err != nil {
		return err
	}

	prov, err := runtime.OTelProvider(ctx, serviceName, os.Getenv("OTEL_OTLP_ENDPOINT"))
	if err != nil {
		return err
	}

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("pgxpool: %w", err)
	}
	defer pool.Close()

	codec := eventcodec.New()

	store, err := pgxoutbox.NewStore(pgxoutbox.Config{Pool: pool, Codec: codec})
	if err != nil {
		return fmt.Errorf("pgxoutbox store: %w", err)
	}

	pub, err := kafka.NewPublisher(kafka.PublisherConfig{
		Brokers:              brokers,
		Codec:                codec,
		PartitionByAggregate: true,
	})
	if err != nil {
		return fmt.Errorf("kafka publisher: %w", err)
	}

	relay, err := outbox.NewRelay(
		outbox.RelayConfig{Store: store, Publisher: pub, Codec: codec, Logger: log},
		outbox.WithHeaderRestorer(kafka.RestoreCoreHeaders),
	)
	if err != nil {
		return fmt.Errorf("new relay: %w", err)
	}

	// Stop order (reverse Use): RelayModule stops the polling loop ->
	// PublisherModule closes the watermill publisher -> otelad flushes
	// + shuts down.
	app := bootstrap.New(bootstrap.Options{Logger: log})
	app.Use(
		otelad.Module(prov),
		kafka.PublisherModule(pub),
		outbox.RelayModule(relay, log),
	)
	return app.Run(ctx)
}
```

- [ ] **Step 4.2: Update Dockerfile to build + ship orders-relay**

Modify `examples/orders/Dockerfile`. Replace lines 24-26 (the three `go build` commands) with:

```dockerfile
RUN go build -trimpath -ldflags='-s -w' -o /out/orders-api ./cmd/api && \
    go build -trimpath -ldflags='-s -w' -o /out/orders-worker ./cmd/worker && \
    go build -trimpath -ldflags='-s -w' -o /out/orders-reader ./cmd/reader && \
    go build -trimpath -ldflags='-s -w' -o /out/orders-relay ./cmd/relay
```

And replace lines 32-34 (the three COPY statements) with:

```dockerfile
COPY --from=build /out/orders-api /orders-api
COPY --from=build /out/orders-worker /orders-worker
COPY --from=build /out/orders-reader /orders-reader
COPY --from=build /out/orders-relay /orders-relay
```

- [ ] **Step 4.3: Build examples/orders to verify relay compile**

Run:
```bash
cd /Users/eason_tseng/playground/project/go-ddd-adapters/examples/orders
go build ./...
```

Expected: clean build. `cmd/relay` produces an executable.

- [ ] **Step 4.4: Lint**

Run:
```bash
cd /Users/eason_tseng/playground/project/go-ddd-adapters/examples/orders
golangci-lint run ./...
```

Expected: `0 issues.`

- [ ] **Step 4.5: Commit Task 4**

```bash
cd /Users/eason_tseng/playground/project/go-ddd-adapters
git add examples/orders/cmd/relay/ examples/orders/Dockerfile
git commit -m "$(cat <<'EOF'
feat(examples/orders): add outbox relay binary

cmd/relay drains pgx outbox_records to Kafka. It is the only binary
in the demo that publishes to Kafka and the only one that calls
MarkSent / MarkFailed / Terminate. api and worker stage events into
the outbox table inside their UoW; relay reads committed rows and
publishes them with headers restored from the row's JSONB headers
column.

Wiring-only: composes existing adapters
(pgxoutbox.Store + outbox.Relay + kafka.Publisher
+ kafka.RestoreCoreHeaders) — no new generic relay package.
DeadLetterRecorder is auto-discovered from the Store by NewRelay
when MaxAttempts > 0 (the default 10), so no explicit wire.
PartitionByAggregate=true matches api/worker so per-aggregate
ordering is preserved through the relay's publish path. Relay knobs
(MaxAttempts, PollInterval, BatchSize, Backoff, ClaimLease) stay at
defaults; no env knobs added in this PR.

Dockerfile gains a fourth go build target and runtime-stage COPY
for /orders-relay. docker-compose wiring follows in Task 5.

Pre-commit: go build + golangci-lint clean.
EOF
)"
```

---

## Task 5: Compose wiring

**Goal:** Add Postgres, two migrate init services, and the relay service to docker-compose. Update the dependency matrix and env variables.

**Files:**
- Modify: `examples/orders/docker-compose.yml`

---

- [ ] **Step 5.1: Rewrite docker-compose.yml**

Replace `examples/orders/docker-compose.yml` with:

```yaml
# Manual-exploration compose for the orders example.
#   docker compose up --build
# Then:
#   curl -X POST localhost:8080/orders -H 'content-type: application/json' \
#        -d '{"customer_id":"alice","items":[{"sku":"A","quantity":2,"price_cents":499}]}'
#   curl localhost:8081/orders/<order_id>
#   open http://localhost:16686  (Jaeger UI)

services:
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

  redpanda:
    image: docker.redpanda.com/redpandadata/redpanda:v24.2.7
    command:
      - redpanda
      - start
      - --overprovisioned
      - --smp=1
      - --memory=512M
      - --reserve-memory=0M
      - --node-id=0
      - --check=false
      - --kafka-addr=PLAINTEXT://0.0.0.0:9092
      - --advertise-kafka-addr=PLAINTEXT://redpanda:9092
    ports:
      - "9092:9092"
    healthcheck:
      test: ["CMD", "rpk", "cluster", "info"]
      interval: 5s
      timeout: 3s
      retries: 10

  jaeger:
    image: jaegertracing/all-in-one:1.62
    environment:
      COLLECTOR_OTLP_ENABLED: "true"
    ports:
      - "16686:16686"  # UI
      - "4317:4317"    # OTLP/gRPC

  orders-api:
    build:
      context: ../..
      dockerfile: examples/orders/Dockerfile
    command: ["/orders-api"]
    environment:
      DATABASE_URL: postgres://orders:orders@postgres:5432/orders?sslmode=disable
      KAFKA_BROKERS: redpanda:9092
      OTEL_OTLP_ENDPOINT: jaeger:4317
      HTTP_ADDR: ":8080"
    depends_on:
      orders-migrate:
        condition: service_completed_successfully
      jaeger:
        condition: service_started
    ports:
      - "8080:8080"

  orders-worker:
    build:
      context: ../..
      dockerfile: examples/orders/Dockerfile
    command: ["/orders-worker"]
    environment:
      DATABASE_URL: postgres://orders:orders@postgres:5432/orders?sslmode=disable
      KAFKA_BROKERS: redpanda:9092
      OTEL_OTLP_ENDPOINT: jaeger:4317
    depends_on:
      orders-migrate:
        condition: service_completed_successfully
      redpanda:
        condition: service_healthy
      jaeger:
        condition: service_started

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
      orders-migrate:
        condition: service_completed_successfully
      redpanda:
        condition: service_healthy
      jaeger:
        condition: service_started

  orders-reader:
    build:
      context: ../..
      dockerfile: examples/orders/Dockerfile
    command: ["/orders-reader"]
    environment:
      KAFKA_BROKERS: redpanda:9092
      OTEL_OTLP_ENDPOINT: jaeger:4317
      HTTP_ADDR: ":8081"
    depends_on:
      redpanda:
        condition: service_healthy
      jaeger:
        condition: service_started
    ports:
      - "8081:8081"
```

Notes for the implementer:
- `orders-api` deliberately omits `redpanda` from its `depends_on` — the api binary no longer talks to Kafka, so a false dependency would teach the wrong topology.
- `orders-reader` deliberately omits `DATABASE_URL` and `orders-migrate` — it still uses the in-memory projection (out of scope for this PR per the design spec).
- The command list-of-args form (`- "-path=..."`) avoids YAML interpreting `&` in the DSN as an anchor declaration.
- `migrate/migrate:v4.19.1` is the official image with all SQL drivers compiled in (lib/pq for the compose CLI path; the integration tests use the pgx5 driver via library).

- [ ] **Step 5.2: Smoke-test compose by booting Postgres + migrate services only**

Run:
```bash
cd /Users/eason_tseng/playground/project/go-ddd-adapters/examples/orders
docker compose up --build --abort-on-container-exit postgres outbox-migrate orders-migrate
```

Expected: postgres comes up healthy; `outbox-migrate` runs migrations 001 + 002 and exits 0; `orders-migrate` runs migration 001 and exits 0. The container chain produces success exit codes; compose exits because `--abort-on-container-exit` is set.

Then verify state inside the running Postgres:
```bash
docker compose exec postgres psql -U orders -d orders -c '\dt'
docker compose exec postgres psql -U orders -d orders -c 'SELECT version FROM outbox_schema_migrations;'
docker compose exec postgres psql -U orders -d orders -c 'SELECT version FROM orders_schema_migrations;'
```

Expected: three application tables (`orders`, `outbox_records`, `outbox_dead_letters`) plus two state tables. The outbox state table reports `version = 2`; the orders state table reports `version = 1`.

Then tear down:
```bash
docker compose down -v
```

- [ ] **Step 5.3: Smoke-test the full compose by booting everything**

Run:
```bash
cd /Users/eason_tseng/playground/project/go-ddd-adapters/examples/orders
docker compose up --build -d
sleep 8
```

Then exercise the end-to-end happy path:
```bash
ORDER_ID=$(uuidgen)
curl -X POST http://localhost:8080/orders \
     -H 'content-type: application/json' \
     -d "{\"order_id\":\"$ORDER_ID\",\"customer_id\":\"alice\",\"items\":[{\"sku\":\"A\",\"quantity\":2,\"price_cents\":499}]}"

sleep 2
curl "http://localhost:8081/orders/$ORDER_ID"
```

Expected:
- POST returns `{"OrderID":"<uuid>","TotalCents":998}` (HTTP 201).
- After 2s, GET returns a view with `status:"shipped"` (worker has consumed OrderPlaced → dispatched Ship → committed → relay published → reader consumed OrderShipped → projection updated).

If GET shows `status:"placed"` only, allow another second and retry. If still placed, the worker / relay chain has an issue — debug via:
```bash
docker compose logs orders-worker
docker compose logs orders-relay
```

Tear down:
```bash
docker compose down -v
```

- [ ] **Step 5.4: Commit Task 5**

```bash
cd /Users/eason_tseng/playground/project/go-ddd-adapters
git add examples/orders/docker-compose.yml
git commit -m "$(cat <<'EOF'
chore(examples/orders): wire Postgres, migrations, and relay in compose

Compose now boots a four-step lifecycle before app services start:
postgres (healthcheck) -> outbox-migrate -> orders-migrate ->
{api, worker, relay, reader}. The two migrate services use the
official migrate/migrate:v4.19.1 image with mounted migration
sources; x-migrations-table per source keeps adapter and example
migrations independently versioned (outbox_schema_migrations /
orders_schema_migrations) so future adapter migrations cannot
collide with the example's numbering.

Dependency-matrix changes worth highlighting:
- orders-api: no longer depends on redpanda — the api binary
  doesn't talk to Kafka. Keeping the dependency would teach the
  wrong topology.
- orders-worker / orders-relay: depend on Postgres (via the
  migrate chain), Redpanda, and Jaeger.
- orders-reader: unchanged. No DATABASE_URL; still uses the
  in-memory projection.

Environment additions:
- orders-api / orders-worker / orders-relay gain DATABASE_URL.
- orders-reader unchanged.

Pre-commit: docker compose up smoke-tests Postgres + migrate chain
(both state tables populated correctly) and end-to-end api -> outbox
-> relay -> worker -> reader path (POST returns 201, projection
shows status=shipped after worker + relay drain).
EOF
)"
```

---

## Task 6: Document the demo

**Goal:** Rewrite `examples/orders/README.md` to describe the new transactional outbox flow; add a `CHANGELOG.md` `[Unreleased]/Changed` entry.

**Files:**
- Modify: `examples/orders/README.md`
- Modify: `CHANGELOG.md`

---

- [ ] **Step 6.1: Rewrite `examples/orders/README.md`**

Replace `examples/orders/README.md` with:

````markdown
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
  through the outbox -> relay -> Kafka path.
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

- `cmd/api` and `cmd/worker` never publish to Kafka directly.
- Inside a UoW transaction, the handler writes the aggregate state to
  the `orders` table AND inserts the domain event row into
  `outbox_records`. If anything fails before commit, neither write
  persists.
- The codec automatically reads `trace_id` / `causation_id` /
  `correlation_id` from ctx and writes them into the outbox row's
  `headers` JSONB column.
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
Redpanda images (~5-10s of network); subsequent runs reuse them.

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
````

Notes for the implementer:
- The "Topology" ASCII diagram in the rewritten README replaces the old one; the old Postgres-less topology is gone.
- The old `## Known shortcuts (intentional, for the example)` section is fully replaced by `## Remaining intentional shortcuts`; the previous "No outbox" and "Per-process in-memory repos" bullets are gone (those shortcuts are closed by this PR).

- [ ] **Step 6.2: Add `[Unreleased]/Changed` entry to CHANGELOG**

Modify `CHANGELOG.md` line 8-10. Currently the `[Unreleased]` block looks like:

```markdown
## [Unreleased]

### Changed

- Bumped `github.com/slam0504/go-ddd-core` from `v0.3.0` → `v0.4.0`
  ...
```

Append a NEW bullet to the existing `### Changed` block (after the existing v0.4.0 bump bullet):

```markdown
- `examples/orders` upgraded to demo transactional outbox end-to-end.
  `cmd/api` and `cmd/worker` now share Postgres for the write model;
  aggregate Save + outbox `Stage` commit in one transaction. A new
  standalone `cmd/relay` binary drains the outbox to Kafka under
  `FOR UPDATE SKIP LOCKED`; `kafka.RestoreCoreHeaders` propagates
  trace / causation / correlation headers across the process
  boundary. `docker-compose.yml` gains a Postgres service, two
  migrate init services (`outbox-migrate` + `orders-migrate` using
  the official `migrate/migrate:v4.19.1` image), and an
  `orders-relay` service. The `cmd/reader` projection remains
  in-memory; the no-durable-inbox, no-exactly-once, and
  in-memory-reader shortcuts remain intentional and are documented
  in `examples/orders/README.md`.
```

- [ ] **Step 6.3: Commit Task 6**

```bash
cd /Users/eason_tseng/playground/project/go-ddd-adapters
git add examples/orders/README.md CHANGELOG.md
git commit -m "$(cat <<'EOF'
docs(examples/orders): document pgx outbox demo and remaining shortcuts

README rewrite reflects the new topology (Postgres shared between
api and worker, dedicated relay process, in-memory reader projection
still). The "Known shortcuts" section is replaced with "Remaining
intentional shortcuts" — the "No outbox" and "Per-process in-memory
repos" bullets are gone (closed by this PR); reader projection,
inbox dedup, exactly-once, authn/authz, and optimistic-locking
activation stay as deliberately-deferred follow-ups.

CHANGELOG [Unreleased]/Changed gets a paragraph describing the demo
upgrade so the next release notes have a starter entry.
EOF
)"
```

---

## Task 7: Agent memory bookkeeping + PR

**Goal:** Update `.agent/state.md` and `.agent/decisions.md` to record this cycle; push the branch and open the PR.

**Files:**
- Modify: `.agent/state.md`
- Modify: `.agent/decisions.md`
- Modify: `.agent/review-log.md` (optional, if any review findings during implementation)

---

- [ ] **Step 7.1: Update `.agent/state.md`**

Modify `.agent/state.md`:

- Update `Last verified` line to today's date with branch + verification summary.
- Update `Current Branch` section to add the in-flight `feat/examples-orders-pgx-outbox` pointer.
- Append a new bullet to `Current Status`:

```markdown
- **`examples/orders` pgx outbox demo cycle in flight** on branch
  `feat/examples-orders-pgx-outbox`. Closes the README's
  "No outbox" and "Per-process in-memory repos" shortcuts.
  `cmd/api` + `cmd/worker` share Postgres for write model;
  transactional Save + outbox.Stage via `application.UnitOfWork`
  bridged from `pgxdb.TxManager`. New `cmd/relay` binary drains
  outbox to Kafka under SKIP LOCKED. docker-compose gains
  Postgres + two `migrate/migrate:v4.19.1` init services
  (`outbox-migrate` + `orders-migrate` with per-source
  `x-migrations-table`). 4 targeted integration tests
  (PlaceOrder happy / rollback + ShipOrder happy / rollback /
  not-found). Reader projection / durable inbox / exactly-once
  remain intentional shortcuts. Design spec at
  `docs/superpowers/specs/2026-05-21-examples-orders-pgx-outbox-design.md`;
  plan at
  `docs/superpowers/plans/2026-05-21-examples-orders-pgx-outbox.md`.
```

- Update `Adapter Surface` table if anything new is exported (this cycle is example-only — no new public adapter API, so the table is unchanged).
- Append to `Open Items`:

```markdown
- **`examples/orders` pgx outbox demo cycle** in flight on branch
  `feat/examples-orders-pgx-outbox`. PR open pending CI green.
  Sub-items remaining (each its own future cycle, none gating this
  PR): `eventbus/inbox/pgx` adapter, pgx projection for
  `cmd/reader`, LISTEN/NOTIFY push relay, `claim_id` worker
  attribution, `orders.version` optimistic-locking activation.
```

- Append a verification block to `Verification`:

```markdown
Pre-push verification on `feat/examples-orders-pgx-outbox` (today):

- `go build ./...` clean (root + `examples/orders`).
- `go vet ./...` clean (root + `examples/orders`).
- `go test ./...` PASS.
- `go test -tags=integration -race ./integration/...` PASS in
  `examples/orders` (PlaceOrder × 2 + ShipOrder × 3 + existing
  Kafka round-trip + partition-by-aggregate tests).
- `golangci-lint run ./...` 0 issues.
- `golangci-lint run --build-tags=integration ./...` 0 issues.
- `docker compose up --build` smoke-tested end-to-end: POST returns
  201; reader projection shows `status:"shipped"` after relay
  drain.
```

- [ ] **Step 7.2: Update `.agent/decisions.md`**

Append a new section to `.agent/decisions.md`:

```markdown
## `examples/orders` pgx outbox demo (2026-05-21 cycle)

Demo-only design decisions; not public adapter API.

1. **Scope is B-outbox-only.** `cmd/api` + `cmd/worker` go through
   transactional outbox; `cmd/relay` is standalone; `cmd/reader`
   keeps its in-memory projection. Inbox dedup, pgx projection,
   exactly-once, embedded relay, full e2e tests are all
   out-of-scope and documented as intentional shortcuts in the
   README.

2. **`x-migrations-table` per source.** Adapter outbox migrations
   and example business migrations are different schema owners;
   sharing one `schema_migrations` table risks future
   adapter-migration-number collision with caller-numbered
   migrations. Used golang-migrate's `x-migrations-table` to give
   each source its own state table:
   `outbox_schema_migrations` for the adapter,
   `orders_schema_migrations` for the example. Compose mounts each
   source separately; integration tests apply each source via the
   library against the same table parameters.

3. **Item gets JSON tags.** Pre-existing bug — `price_cents` would
   silently decode to 0 because Go's `encoding/json` is
   case-insensitive but does not normalise underscores. Fixed in
   Task 1 of this cycle because pgxrepo's JSONB column would
   otherwise diverge from the HTTP wire shape.

4. **`Hydrate` gains items + defensive copy.** Required by
   pgxrepo's `FindByID` to rehydrate the aggregate with full state
   (including items). Defensive `append([]Item(nil), items...)`
   so caller mutation cannot leak in.

5. **Worker drops hydrate-from-event.** With shared Postgres, the
   old per-process memrepo + `Hydrate(from event)` workaround is
   unnecessary. `ShipOrderHandler` uses `repo.FindByID(ctx, id)`;
   api's transactional commit + relay's publish-only-committed
   contract guarantees the row is durable by the time worker sees
   the event. `OrderPlaced` payload does NOT gain an `Items` field.

6. **Transaction boundary is `Save + Stage`.** Domain logic (Place
   / Ship) runs before `uow.Do`; codec marshal, command bus
   dispatch, and Kafka consumer ack/nack are all outside the tx.
   `ClearEvents` runs after `uow.Do` returns nil.

7. **Kafka consumer ack/nack MUST stay out of the DB tx.**
   Mixing broker offset commit with DB tx would couple redelivery
   to DB failures; the watermill subscriber lifecycle owns
   ack/nack via the handler's return value.

8. **`cmd/relay` is wiring-only.** Composes `pgxoutbox.Store` +
   `outbox.Relay` + `kafka.Publisher` + `kafka.RestoreCoreHeaders`;
   no new generic relay package. `DeadLetterRecorder` is
   auto-discovered from `Store` by `NewRelay` (no explicit wire).
   Knobs (`MaxAttempts`, `PollInterval`, `BatchSize`, `Backoff`,
   `ClaimLease`) stay at defaults; no env exposure this cycle.

9. **`orders-api` does not depend on Redpanda in compose.** The
   binary has no Kafka client after the refactor. A false
   dependency would teach the wrong topology.

10. **`pgxrepo.FindByID` maps `pgx.ErrNoRows` to
    `domain.ErrNotFound`.** Mirrors `memrepo` so swapping repo
    implementations does not drift error semantics. Locked by
    `TestShipOrder_NotFound`.

11. **`orders.version` is reserved but unused.** BIGINT column
    matches `Order.Version() int64`. `Save` writes the value but
    does not gate UPDATE on a `WHERE version = $expected` clause;
    behaviour is last-write-wins. Optimistic-locking activation is
    a separate future cycle.

12. **Two-source migration validation included.** Compose
    smoke-test in Task 5 confirms re-running `docker compose up
    --build` is no-op: each migrate service sees its own table at
    target version and exits 0 with no schema changes.
```

- [ ] **Step 7.3: Run final verification (full pre-push check)**

Run:
```bash
cd /Users/eason_tseng/playground/project/go-ddd-adapters
go test ./...
go test -race ./eventbus/outbox/...

cd examples/orders
go test ./...
go test -tags=integration -race ./integration/...

cd /Users/eason_tseng/playground/project/go-ddd-adapters
golangci-lint run ./...
cd examples/orders
golangci-lint run ./...

cd /Users/eason_tseng/playground/project/go-ddd-adapters
golangci-lint run --build-tags=integration ./...
```

Expected: all PASS / 0 issues.

- [ ] **Step 7.4: Commit Task 7**

```bash
cd /Users/eason_tseng/playground/project/go-ddd-adapters
git add .agent/state.md .agent/decisions.md
git commit -m "$(cat <<'EOF'
chore(agent-memory): record examples/orders pgx outbox cycle

State file gets the new in-flight pointer, current-status entry,
and pre-push verification block. Decisions file gains a 12-entry
section covering scope, x-migrations-table per source, the
Item-tags fix, Hydrate items defensive copy, the worker
hydrate-from-event drop, transaction-boundary rule, ack/nack
isolation rule, relay-is-wiring-only stance, the
orders-api-not-depending-on-Redpanda topology choice, the
FindByID not-found contract, the orders.version reservation, and
the two-source migration validation step.
EOF
)"
```

- [ ] **Step 7.5: Push the branch and open the PR**

Run:
```bash
cd /Users/eason_tseng/playground/project/go-ddd-adapters
git push -u origin feat/examples-orders-pgx-outbox
```

Then open the PR:
```bash
gh pr create --title "feat(examples/orders): pgx outbox demo end-to-end" --body "$(cat <<'EOF'
## Summary

- Upgrades `examples/orders` from "save then publish" to a transactional outbox demo on v0.4.0 `eventbus/outbox/pgx` + `ports/database/pgx`.
- `cmd/api` + `cmd/worker` share Postgres for the write model; aggregate `Save` + outbox `Stage` commit in one transaction via `application.UnitOfWork` bridged from `pgxdb.TxManager`.
- New `cmd/relay` binary drains the outbox to Kafka under `FOR UPDATE SKIP LOCKED`. Wiring-only over existing adapters — no new generic relay package.
- README rewritten; closes the "No outbox" and "Per-process in-memory repos" intentional shortcuts. Reader projection, durable inbox, exactly-once, and optimistic locking remain explicitly deferred follow-ups.

## Design + plan

- Spec: `docs/superpowers/specs/2026-05-21-examples-orders-pgx-outbox-design.md`
- Plan: `docs/superpowers/plans/2026-05-21-examples-orders-pgx-outbox.md`

## Topology changes

- `docker-compose.yml`: adds Postgres (`postgres:16-alpine`), two migrate init services (`migrate/migrate:v4.19.1` with `x-migrations-table` per source), and `orders-relay`. `orders-api` no longer depends on Redpanda (the binary has no Kafka client). `orders-reader` keeps its in-memory projection.
- Migration sources tracked independently: `outbox_schema_migrations` for adapter outbox migrations, `orders_schema_migrations` for the example business schema.
- `Item` gets `json:"sku" / "quantity" / "price_cents"` tags (fixes a pre-existing decode-to-0 bug for the curl flow).
- Worker drops the hydrate-from-event workaround; `ShipOrderHandler` uses `repo.FindByID` from the shared Postgres.

## Verification

- [x] `go build ./...` (root + examples/orders) clean
- [x] `go vet ./...` clean
- [x] `go test ./...` PASS
- [x] `go test -tags=integration -race ./integration/...` PASS in examples/orders (5 new tests + existing Kafka tests)
- [x] `golangci-lint run ./...` 0 issues
- [x] `golangci-lint run --build-tags=integration ./...` 0 issues
- [x] `docker compose up --build` end-to-end smoke: POST returns 201, projection shows `status:"shipped"`

## Test plan

- [ ] CI: 5 jobs (root golangci-lint + build/test, examples/orders golangci-lint + build/test, integration testcontainers)
- [ ] CI: integration job exercises both Postgres + Redpanda containers
EOF
)"
```

Expected: PR URL printed. The branch shows 7 implementation commits + 2 spec commits + 1 plan commit (this commit is added at the end of Task 7 in step 7.6 below).

- [ ] **Step 7.6: Commit the plan document itself**

(The plan was created mid-cycle but never committed in earlier tasks; commit it now so the PR records it.)

```bash
cd /Users/eason_tseng/playground/project/go-ddd-adapters
git add docs/superpowers/plans/2026-05-21-examples-orders-pgx-outbox.md
git commit -m "$(cat <<'EOF'
docs(plan): implementation plan for examples/orders pgx outbox demo

Companion to the design spec (docs/superpowers/specs/2026-05-21-
examples-orders-pgx-outbox-design.md). Walks through the seven
implementation commits (this PR's commit graph) step-by-step:
files to touch, code to write, commands to run, expected outputs.

Captured here in the repo so future readers tracing this PR have
the design intent (spec) and the execution recipe (plan) in one
place.
EOF
)"

git push origin feat/examples-orders-pgx-outbox
```

Expected: plan commit lands on top of the branch; push succeeds; the PR updates automatically.

---

## Self-Review

The plan was self-reviewed for:

- **Spec coverage**: every section of the design spec maps to one or more steps in this plan. Section 2 (architecture) → Tasks 2-4; Section 5 (schema + migrations) → Tasks 1, 2, 5; Section 6 (handler refactor + pgxrepo) → Tasks 1, 2, 3; Section 7 (cmd/relay) → Task 4; Section 8 (docker-compose) → Task 5; Section 9 (integration tests) → Tasks 2, 3; Section 10 (README) → Task 6; Section 11 (commit decomposition) → Tasks 1-7 (one task per spec commit).
- **Placeholders**: no "TBD" / "implement later" / "similar to Task N" without code.
- **Type consistency**: `application.UnitOfWork`, `eventbus.Outbox`, `pgxoutbox.Store`, `pgxdb.NewTxManager`, `outbox.NewRelay` + `RelayConfig` + `WithHeaderRestorer` are used consistently across tasks; `Hydrate(id, customerID, status, version, totalCents, items)` signature is identical in every site.
- **Ambiguity**: none remaining after final review. The `kafka.ConsumerModule(sub, topic, log, handle)` signature is verified against `eventbus/kafka/module.go:56`; the handler returns `error` (nil = Ack, non-nil = Nack) and no manual `env.Ack/Nack` calls are needed.

---

## Out of plan scope (future cycles)

- `eventbus/inbox/pgx` adapter (consumer-side dedup).
- pgx projection variant for `cmd/reader`.
- `LISTEN/NOTIFY` push-based delivery for the relay.
- `claim_id` worker attribution.
- `orders.version` optimistic-locking activation.
- Full end-to-end docker-compose integration test in CI.
