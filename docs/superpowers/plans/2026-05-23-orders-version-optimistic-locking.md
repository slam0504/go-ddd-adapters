# `orders.version` Optimistic Locking Activation — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Activate the existing `orders.version` column as a SQL-encoded optimistic lock on `examples/orders` `pgxrepo.Save`, so a stale write returns `domain.ErrConcurrencyConflict` (which the existing UoW tx rolls back together with the staged outbox row) instead of silently overwriting.

**Architecture:** Add a `WHERE orders.version = EXCLUDED.version - 1` clause to the existing `ON CONFLICT (id) DO UPDATE` upsert, then inspect `cmd.RowsAffected()`; 0 rows ⇒ wrap and return `domain.ErrConcurrencyConflict`. Two new `//go:build integration` tests prove the guard at both repo level (concurrent snapshot) and handler level (duplicate `PlaceOrderCommand` + outbox-row tx rollback). `cmd/api` gains a partial `errors.Is(err, domain.ErrConcurrencyConflict) → 409` mapping; full HTTP error taxonomy stays a separate cycle. No `go-ddd-core` change.

**Tech Stack:** Go 1.25, pgx/v5, golang-migrate v4.19.1, testcontainers Postgres `postgres:16-alpine`, `go-ddd-core` v0.4.0 (`domain.ErrConcurrencyConflict` already exists at `domain/errors.go:10`).

**Spec:** `docs/superpowers/specs/2026-05-23-orders-version-optimistic-locking-design.md` (APPROVED 2026-05-23).

**Branch:** `feat/orders-version-optimistic-locking` (cut from `main` head `e6b1672`).

---

## File Structure

Files this plan touches (all paths relative to repo root):

| Path | Action | Responsibility |
| --- | --- | --- |
| `examples/orders/infra/pgxrepo/order_repo.go` | Modify `Save` (lines 62–90) | SQL-encoded prior-version guard + `RowsAffected()==0` → `ErrConcurrencyConflict`; invariant comment |
| `examples/orders/infra/memrepo/order_repo.go` | Modify package doc (lines 1–4) | One-line note marking memrepo as example-only / no OL enforcement |
| `examples/orders/cmd/api/main.go` | Modify `routes` (lines 119–123) | Pre-empt the generic 400 branch with a `domain.ErrConcurrencyConflict → 409` case; add `errors` + `domain` imports; TODO note for follow-up cycle |
| `examples/orders/integration/optimistic_lock_test.go` | **Create** | Two `//go:build integration` tests: `TestSave_OptimisticLock_ConcurrentUpdate` (repo layer) and `TestPlaceOrder_DuplicateID_Conflict` (handler + outbox rollback) |
| `.agent/decisions.md` | Append | New section `## orders.version optimistic locking activation (2026-05-23 cycle)` |
| `.agent/state.md` | Modify (post-merge) | Open-Items cleanup + Current-Status entry summarising cycle as CLOSED |

No schema migration is needed (the `version` column already exists in `001_create_orders.up.sql`).

---

## Task 1: Add SQL-encoded prior-version guard to `pgxrepo.Save` (TDD)

**Files:**
- Create: `examples/orders/integration/optimistic_lock_test.go`
- Modify: `examples/orders/infra/pgxrepo/order_repo.go` (function `Save`, current body at lines 62–90)
- Test: `examples/orders/integration/optimistic_lock_test.go` (both new tests)

Both tests are written **before** any production change so we observe the red→green transition explicitly.

- [ ] **Step 1.1: Create the test file with both failing tests**

Write the file `examples/orders/integration/optimistic_lock_test.go`:

```go
//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/slam0504/go-ddd-core/application"
	"github.com/slam0504/go-ddd-core/domain"

	pgxoutbox "github.com/slam0504/go-ddd-adapters/eventbus/outbox/pgx"
	apporder "github.com/slam0504/go-ddd-adapters/examples/orders/application/order"
	orderdom "github.com/slam0504/go-ddd-adapters/examples/orders/domain/order"
	"github.com/slam0504/go-ddd-adapters/examples/orders/infra/eventcodec"
	"github.com/slam0504/go-ddd-adapters/examples/orders/infra/pgxrepo"
	pgxdb "github.com/slam0504/go-ddd-adapters/ports/database/pgx"
)

// TestSave_OptimisticLock_ConcurrentUpdate exercises the SQL guard
// directly at the pgxrepo layer: two snapshots of the same row both
// transition to shipped in memory; the first Save wins, the second
// must return ErrConcurrencyConflict. The DB must show exactly one
// shipped row at version=2 (no double-apply, no revert to seed state).
func TestSave_OptimisticLock_ConcurrentUpdate(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	repo := pgxrepo.New(sharedPool)

	const orderID orderdom.ID = "ord-ol-concurrent"
	seed := orderdom.Hydrate(
		orderID,
		"alice",
		orderdom.StatusPlaced,
		1, // version
		99,
		[]orderdom.Item{{SKU: "A", Quantity: 1, PriceCents: 99}},
	)
	if err := repo.Save(ctx, seed); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	findA, err := repo.FindByID(ctx, orderID)
	if err != nil {
		t.Fatalf("findA: %v", err)
	}
	findB, err := repo.FindByID(ctx, orderID)
	if err != nil {
		t.Fatalf("findB: %v", err)
	}

	if err := findA.Ship(uuid.NewString(), "carrierA"); err != nil {
		t.Fatalf("ship A: %v", err)
	}
	if err := findB.Ship(uuid.NewString(), "carrierB"); err != nil {
		t.Fatalf("ship B: %v", err)
	}

	if err := repo.Save(ctx, findA); err != nil {
		t.Fatalf("save A: want nil, got %v", err)
	}

	err = repo.Save(ctx, findB)
	if !errors.Is(err, domain.ErrConcurrencyConflict) {
		t.Fatalf("save B: want ErrConcurrencyConflict, got %v", err)
	}

	// DB shows exactly one shipped transition.
	// Note: Ship() does not mutate items/total_cents, so findA vs findB
	// cannot be distinguished on the orders row alone — the assertion
	// is that version advanced by exactly one and status moved to shipped.
	var (
		status  string
		version int64
	)
	if err := sharedPool.QueryRow(ctx,
		`SELECT status, version FROM orders WHERE id=$1`,
		string(orderID),
	).Scan(&status, &version); err != nil {
		t.Fatalf("post query: %v", err)
	}
	if status != string(orderdom.StatusShipped) {
		t.Errorf("status: want %q, got %q", orderdom.StatusShipped, status)
	}
	if version != 2 {
		t.Errorf("version: want 2, got %d", version)
	}
}

// TestPlaceOrder_DuplicateID_Conflict drives the same conflict through
// the PlaceOrderHandler: a second PlaceOrderCommand with the same
// OrderID must return ErrConcurrencyConflict, and the tx rollback
// must leave exactly one outbox_records row (the first call's stage)
// rather than two.
func TestPlaceOrder_DuplicateID_Conflict(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	codec := eventcodec.New()
	uow := application.UnitOfWorkFromTxManager(pgxdb.NewTxManager(sharedPool))
	repo := pgxrepo.New(sharedPool)
	store, err := pgxoutbox.NewStore(pgxoutbox.Config{Pool: sharedPool, Codec: codec})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	const topic = "orders.placed.duplicate.test"
	handler := apporder.NewPlaceOrderHandler(uow, repo, store, topic, uuid.NewString)

	cmd := apporder.PlaceOrderCommand{
		OrderID:    "ord-dup",
		CustomerID: "alice",
		Items:      []orderdom.Item{{SKU: "A", Quantity: 1, PriceCents: 99}},
	}

	var preCount int64
	if err := sharedPool.QueryRow(ctx,
		`SELECT COUNT(*) FROM outbox_records WHERE aggregate_id=$1`,
		cmd.OrderID,
	).Scan(&preCount); err != nil {
		t.Fatalf("pre count: %v", err)
	}
	if preCount != 0 {
		t.Fatalf("pre outbox count: want 0, got %d", preCount)
	}

	if _, err := handler.Handle(ctx, cmd); err != nil {
		t.Fatalf("first place: want nil, got %v", err)
	}

	_, err = handler.Handle(ctx, cmd)
	if !errors.Is(err, domain.ErrConcurrencyConflict) {
		t.Fatalf("second place: want ErrConcurrencyConflict, got %v", err)
	}

	var postCount int64
	if err := sharedPool.QueryRow(ctx,
		`SELECT COUNT(*) FROM outbox_records WHERE aggregate_id=$1`,
		cmd.OrderID,
	).Scan(&postCount); err != nil {
		t.Fatalf("post count: %v", err)
	}
	if postCount != 1 {
		t.Errorf("outbox rows after conflict: want 1 (first call's stage only), got %d", postCount)
	}
}
```

- [ ] **Step 1.2: Run the new tests and confirm they fail in the expected way**

Run from `examples/orders`:

```bash
cd examples/orders
go test -tags=integration -race ./integration/ -run 'TestSave_OptimisticLock_ConcurrentUpdate|TestPlaceOrder_DuplicateID_Conflict' -v
```

Expected output (red):

- `TestSave_OptimisticLock_ConcurrentUpdate` — FAIL on the line `t.Fatalf("save B: want ErrConcurrencyConflict, got %v", err)` (current `Save` silently UPDATEs the row, returning `nil`).
- `TestPlaceOrder_DuplicateID_Conflict` — FAIL on `t.Fatalf("second place: want ErrConcurrencyConflict, got %v", err)` (current handler chain succeeds because `Save` silently UPDATEs and the second outbox stage row is committed).

If a test fails on a *different* line (e.g. seed save, FindByID), stop and investigate — the test set-up is wrong, not the production code.

- [ ] **Step 1.3: Modify `pgxrepo.Save` to enforce the prior-version guard**

Replace the body of `Save` in `examples/orders/infra/pgxrepo/order_repo.go` (lines 62–90 in the current file) with:

```go
// Save upserts the order, enforcing optimistic locking via the
// EXCLUDED.version - 1 guard on the UPDATE branch. The invariant
// aggregate.Version() == priorLoadedVersion + 1 holds in this example
// because Order.Place / Order.Ship each call IncrementVersion exactly
// once and are each followed by exactly one Save. A future use case
// that stages multiple events per Save (Version() jumps by > 1) must
// switch to an explicit loadedVersion field on the aggregate.
func (r *OrderRepository) Save(ctx context.Context, o *orderdom.Order) error {
	exec := pgxdb.Executor(ctx, r.pool)
	itemsJSON, err := json.Marshal(o.Items())
	if err != nil {
		return fmt.Errorf("pgxrepo: encode items for %s: %w", o.ID(), err)
	}
	cmd, err := exec.Exec(ctx, `
		INSERT INTO orders (id, customer_id, status, version, total_cents, items)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (id) DO UPDATE SET
			customer_id = EXCLUDED.customer_id,
			status      = EXCLUDED.status,
			version     = EXCLUDED.version,
			total_cents = EXCLUDED.total_cents,
			items       = EXCLUDED.items,
			updated_at  = now()
		WHERE orders.version = EXCLUDED.version - 1
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
	if cmd.RowsAffected() == 0 {
		return fmt.Errorf("pgxrepo: optimistic lock conflict on order %s: %w",
			o.ID(), domain.ErrConcurrencyConflict)
	}
	return nil
}
```

No new imports are needed — `domain`, `pgxdb`, `json`, `fmt`, and `errors` are already imported in this file.

- [ ] **Step 1.4: Run the two new tests and confirm green**

```bash
cd examples/orders
go test -tags=integration -race ./integration/ -run 'TestSave_OptimisticLock_ConcurrentUpdate|TestPlaceOrder_DuplicateID_Conflict' -v
```

Expected: `PASS` for both.

- [ ] **Step 1.5: Run the full `examples/orders` integration suite to catch regressions**

```bash
cd examples/orders
go test -tags=integration -race ./integration/...
```

Expected: 10 tests pass (8 pre-existing from PR #19 + 2 new). The pre-existing happy-path Place/Ship tests must continue to PASS — they always invoke `Save` once per aggregate transition, so the `priorVersion + 1` invariant holds and the new WHERE clause is satisfied.

If any pre-existing test fails, do **not** continue. Likely root cause: the new WHERE clause does not match the seed/setup path of that test — investigate by running the failing test in isolation with `-v`.

- [ ] **Step 1.6: Commit**

```bash
git add examples/orders/integration/optimistic_lock_test.go \
        examples/orders/infra/pgxrepo/order_repo.go
git commit -m "feat(examples/orders): pgxrepo.Save enforces orders.version optimistic lock

Adds WHERE orders.version = EXCLUDED.version - 1 to the existing
ON CONFLICT upsert and maps RowsAffected()==0 to a wrapped
domain.ErrConcurrencyConflict. Two new //go:build integration
tests cover the repo-layer conflict and the handler-layer tx
rollback (no extra outbox_records row on conflict).

Invariant pinned in the Save doc comment: aggregate.Version() ==
priorLoadedVersion + 1 holds because Order.Place and Order.Ship
each call IncrementVersion exactly once per Save in the demo.

Refs spec docs/superpowers/specs/2026-05-23-orders-version-optimistic-locking-design.md"
```

---

## Task 2: Partial HTTP 409 mapping in `cmd/api`

**Files:**
- Modify: `examples/orders/cmd/api/main.go` (imports block at lines 7–34; `routes` handler at lines 100–127)

No new test in this cycle (manual curl smoke is sufficient — the dispatch error path is otherwise untested in `cmd/api` and a full transport-error taxonomy is the explicit follow-up cycle).

- [ ] **Step 2.1: Add `errors` and `domain` imports**

In `examples/orders/cmd/api/main.go`, the current standard-library import block is:

```go
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
	...
)
```

Add `"errors"` to the std-lib group and `"github.com/slam0504/go-ddd-core/domain"` to the core group. The resulting head of the import block looks like:

```go
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/slam0504/go-ddd-core/application"
	"github.com/slam0504/go-ddd-core/application/command"
	"github.com/slam0504/go-ddd-core/bootstrap"
	"github.com/slam0504/go-ddd-core/domain"
	"github.com/slam0504/go-ddd-core/eventbus"
	"github.com/slam0504/go-ddd-core/ports/logger"
    ...
)
```

- [ ] **Step 2.2: Pre-empt the generic 400 branch with a 409 case**

In the same file, replace the block currently at lines 119–123:

```go
		if err != nil {
			log.Log(r.Context(), logger.LevelWarn, "place order failed", logger.F("err", err.Error()))
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
```

with:

```go
		if err != nil {
			log.Log(r.Context(), logger.LevelWarn, "place order failed", logger.F("err", err.Error()))
			if errors.Is(err, domain.ErrConcurrencyConflict) {
				// TODO: full transport-error taxonomy (not-found → 404,
				// rule violation → 422, etc.) is a separate cycle; this
				// branch is the optimistic-locking-specific case only.
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
```

All other paths are unchanged.

- [ ] **Step 2.3: Verify the package builds and vets**

```bash
cd examples/orders
go build ./...
go vet ./...
```

Expected: both clean.

- [ ] **Step 2.4: Commit**

```bash
git add examples/orders/cmd/api/main.go
git commit -m "feat(examples/orders): map ErrConcurrencyConflict to HTTP 409 in cmd/api

Partial transport-error mapping: ErrConcurrencyConflict is the only
domain sentinel translated here. Full HTTP error taxonomy is tracked
as a separate follow-up cycle.

Refs spec docs/superpowers/specs/2026-05-23-orders-version-optimistic-locking-design.md"
```

---

## Task 3: memrepo divergence note (one-line doc)

**Files:**
- Modify: `examples/orders/infra/memrepo/order_repo.go` (package doc, lines 1–4)

No behavior change. memrepo currently has zero importers and zero tests post-PR #19; deletion is a separate hygiene cycle.

- [ ] **Step 3.1: Append the divergence note to the package doc**

The current package doc (lines 1–4) is:

```go
// Package memrepo is an in-memory implementation of the Order Repository.
// Real services would back this with Postgres / DynamoDB / etc; the example
// keeps it in memory to focus on the adapter wiring instead of the storage.
package memrepo
```

Replace with:

```go
// Package memrepo is an in-memory implementation of the Order Repository.
// Real services would back this with Postgres / DynamoDB / etc; the example
// keeps it in memory to focus on the adapter wiring instead of the storage.
//
// Note: this in-memory repository is example-only and does not enforce
// optimistic locking; use pgxrepo for production-grade conflict semantics.
package memrepo
```

- [ ] **Step 3.2: Verify the package still builds and vets**

```bash
cd examples/orders
go build ./...
go vet ./...
```

Expected: both clean.

- [ ] **Step 3.3: Commit**

```bash
git add examples/orders/infra/memrepo/order_repo.go
git commit -m "docs(examples/orders): note memrepo lacks optimistic-locking enforcement

memrepo is example-only and has zero importers / zero tests
post-PR #19. The note steers readers to pgxrepo for production-grade
conflict semantics; deletion is a separate hygiene cycle."
```

---

## Task 4: Append cycle decision to `.agent/decisions.md`

**Files:**
- Modify: `.agent/decisions.md` (append at end of file; current file ends at line 521)

- [ ] **Step 4.1: Append a new section**

Append (preserving existing content; add a blank line before the new heading):

```markdown

## orders.version optimistic locking activation (2026-05-23 cycle)

- `examples/orders/infra/pgxrepo/order_repo.go` `Save` enforces optimistic
  locking via the SQL clause `WHERE orders.version = EXCLUDED.version - 1`
  on the `ON CONFLICT (id) DO UPDATE` branch. `cmd.RowsAffected() == 0`
  is wrapped as `domain.ErrConcurrencyConflict`.
- Four operating cases are documented in §5.2 of the design spec:
  - INSERT path (fresh ID) is unaffected.
  - Duplicate `Place` ⇒ UPDATE branch's WHERE evaluates false ⇒ conflict.
  - First `Ship` writer of a loaded snapshot ⇒ success.
  - Second `Ship` writer of the same loaded snapshot ⇒ conflict.
- Invariant pinned in the `Save` doc comment:
  `aggregate.Version() == priorLoadedVersion + 1` for every Save in this
  example. Holds because `Order.Place` and `Order.Ship` each call
  `IncrementVersion` exactly once and are each followed by exactly one
  Save. A future use case that stages multiple events per Save must
  introduce an explicit `loadedVersion` field on the aggregate
  (Pattern (B) from the brainstorm).
- Rejected alternatives:
  - Pattern (B) — explicit `loadedVersion` field on the aggregate.
    Adds aggregate state for a property the SQL `-1` already encodes
    in the current single-event-per-Save shape. Re-evaluate when the
    invariant breaks.
  - Insert/update split — separate `INSERT ... RETURNING` for Place and
    a separate `UPDATE ... WHERE version = $loaded` for Ship. Doubles
    the SQL surface and the round-trip count for the duplicate-Place
    case, with no behavioural gain over the single upsert.
- Tx atomicity carries outbox rollback: the "no extra `outbox_records`
  row on conflict" property depends on `application.UnitOfWork` (via
  `pgxdb.TxManager`) wrapping `Save` and `outbox.Stage` in the same tx
  — contract established by PR #19, not touched in this cycle.
- Retry policy lives in the transport layer: handlers never catch
  `ErrConcurrencyConflict`. Kafka redelivery + aggregate rule-layer
  idempotency (`ORDER_ALREADY_SHIPPED`) together complete the
  reload-retry loop without any new code. No new test asserts this
  redelivery path in this cycle — see spec §6.4 for the rationale.
- `cmd/api` partial 409 mapping: `errors.Is(err,
  domain.ErrConcurrencyConflict)` returns `http.StatusConflict (409)`
  ahead of the generic 400 branch. Full HTTP error taxonomy (not-found
  → 404, rule violation → 422) is the explicit follow-up cycle.
- `memrepo` carries a one-line package-doc note that it does not enforce
  optimistic locking; deletion is a separate hygiene cycle (memrepo
  has zero importers and zero tests post-PR #19).
```

- [ ] **Step 4.2: Commit**

```bash
git add .agent/decisions.md
git commit -m "chore(agent-memory): record orders.version optimistic-locking cycle decisions

Captures the SQL-encoded guard, the four operating cases, the
Version-by-one invariant, the two rejected alternatives, and the
boundary that retry policy lives in the transport layer (no
handler-side reload/retry)."
```

---

## Task 5: Pre-PR verification

**Files:** none modified — this task runs the matrix the spec §7 mandates.

- [ ] **Step 5.1: Root module checks**

Run from repo root:

```bash
go build ./...
go vet ./...
go test ./...
golangci-lint run ./...
golangci-lint run --build-tags=integration ./...
```

Expected: every command exits 0; lint reports 0 issues.

- [ ] **Step 5.2: `examples/orders` module checks**

```bash
cd examples/orders
go build ./...
go vet ./...
go test ./...
go test -tags=integration -race ./integration/...
golangci-lint run ./...
```

Expected:

- `go test ./...` — no test files outside `integration/`, so the run is no-op aside from build verification.
- `go test -tags=integration -race ./integration/...` — **10 tests pass**: 8 pre-existing (`TestPartitionByAggregate_PreservesOrder`, `TestPlaceOrder_TransactionalOutbox`, `TestPlaceOrder_TxRollback`, `TestRoundTrip_AllHeaders`, `TestShipOrder_TransactionalOutbox`, `TestShipOrder_TxRollback`, `TestWorker_HandleOrderPlaced_PropagatesHeaders`, `TestShipOrder_NotFound`) plus 2 new (`TestSave_OptimisticLock_ConcurrentUpdate`, `TestPlaceOrder_DuplicateID_Conflict`).
- `golangci-lint run ./...` — 0 issues.

- [ ] **Step 5.3: Manual HTTP 409 smoke check (optional but recommended)**

The 409 mapping has no automated test. Quick verification via `docker compose`:

```bash
docker compose up --build -d
# Wait for migrate jobs to exit 0:
docker compose ps

curl -i -X POST http://localhost:8080/orders \
  -H 'Content-Type: application/json' \
  -d '{"order_id":"ord-409-smoke","customer_id":"alice","items":[{"sku":"A","quantity":1,"price_cents":99}]}'
# Expect: HTTP/1.1 201 Created

curl -i -X POST http://localhost:8080/orders \
  -H 'Content-Type: application/json' \
  -d '{"order_id":"ord-409-smoke","customer_id":"alice","items":[{"sku":"A","quantity":1,"price_cents":99}]}'
# Expect: HTTP/1.1 409 Conflict
# Body contains: "domain: concurrency conflict"

docker compose down -v
```

If the second response is **not** 409, the mapping is wrong; do not push.

---

## Task 6: Open PR

**Files:** none — this is a git/gh action.

- [ ] **Step 6.1: Push the branch**

```bash
git push -u origin feat/orders-version-optimistic-locking
```

- [ ] **Step 6.2: Open PR with `gh`**

```bash
gh pr create \
  --title "feat(examples/orders): orders.version optimistic locking activation" \
  --body "$(cat <<'EOF'
## Summary

- pgxrepo.Save now enforces optimistic locking via a SQL-encoded prior-version guard (`WHERE orders.version = EXCLUDED.version - 1` on the ON CONFLICT UPDATE branch); RowsAffected()==0 ⇒ wrapped `domain.ErrConcurrencyConflict`.
- cmd/api gets partial HTTP error mapping: `ErrConcurrencyConflict` → 409; all other paths unchanged. Full HTTP error taxonomy stays a follow-up cycle.
- memrepo carries a one-line note that it does not enforce optimistic locking. No behavior change.
- 2 new `//go:build integration` tests in `examples/orders/integration/optimistic_lock_test.go`: repo-layer concurrent update + handler-layer duplicate-PlaceOrder with outbox-row rollback assertion.

Refs spec `docs/superpowers/specs/2026-05-23-orders-version-optimistic-locking-design.md`.

## Test plan

- [x] `go test -tags=integration -race ./integration/...` in `examples/orders`: 10/10 PASS (8 pre-existing + 2 new).
- [x] `go build ./...`, `go vet ./...` clean (root + `examples/orders`).
- [x] `golangci-lint run ./...` and `golangci-lint run --build-tags=integration ./...` 0 issues.
- [x] Manual `docker compose` smoke: duplicate POST /orders returns HTTP 409.

## Out of scope (explicit follow-ups)

- Full HTTP error taxonomy (not-found → 404, rule violation → 422). New Open Item.
- Deletion of memrepo (zero importers / zero tests). Separate hygiene cycle.
- Pattern (B) — explicit `loadedVersion` aggregate field. Revisit if a future use case stages multiple events per Save.
EOF
)"
```

Capture the returned PR URL for the post-merge bookkeeping step.

---

## Task 7: Post-merge bookkeeping

**Files:**
- Modify: `.agent/state.md`

This task runs **after** the PR merges to `main`. The PR # and merge commit replace `<PR#>` and `<MERGE_COMMIT>` below.

- [ ] **Step 7.1: Update `.agent/state.md` Open Items**

In `.agent/state.md`, locate the bullet under `## Open Items` → `**Pending follow-up cycles**`:

```markdown
  - `orders.version` optimistic-locking activation — column exists
    on the orders table; not yet wired into `Save`.
```

Replace with:

```markdown
  - ~~`orders.version` optimistic-locking activation~~ ✅ resolved
    by PR #<PR#> at merge commit `<MERGE_COMMIT>`. `pgxrepo.Save`
    enforces the prior-version guard via SQL; conflict surfaces as
    `domain.ErrConcurrencyConflict`. See `.agent/decisions.md`
    "orders.version optimistic locking activation (2026-05-23 cycle)".
  - **NEW follow-up — HTTP error mapping polish**: extend `cmd/api`
    to a full transport-error taxonomy (not-found → 404, rule
    violation → 422, etc.). Today only `ErrConcurrencyConflict → 409`
    is wired (partial mapping by design; flagged in `cmd/api/main.go`
    with a `TODO` comment).
```

- [ ] **Step 7.2: Add a Current-Status entry**

Insert at the top of the `## Current Status` bullet list (just below the heading):

```markdown
- **`orders.version` optimistic-locking cycle is CLOSED** (PR
  #<PR#> merged 2026-MM-DD at merge commit `<MERGE_COMMIT>`).
  `pgxrepo.Save` now uses a SQL-encoded `EXCLUDED.version - 1`
  guard on the ON CONFLICT UPDATE branch; `RowsAffected()==0`
  surfaces as wrapped `domain.ErrConcurrencyConflict`, which the
  existing UoW tx rolls back together with the staged outbox row.
  `cmd/api` gains a partial `ErrConcurrencyConflict → 409`
  mapping; memrepo carries a one-line note that it does not
  enforce optimistic locking. 2 new integration tests
  (`TestSave_OptimisticLock_ConcurrentUpdate`,
  `TestPlaceOrder_DuplicateID_Conflict`); existing 8 stayed
  green. Design spec at
  `docs/superpowers/specs/2026-05-23-orders-version-optimistic-locking-design.md`;
  plan at
  `docs/superpowers/plans/2026-05-23-orders-version-optimistic-locking.md`.
```

- [ ] **Step 7.3: Update the "Last verified" header line**

Replace the existing `Last verified:` line at the top of `.agent/state.md` (line 3) with the current date and merge-commit reference, mirroring the established format used for PR #19's entry.

- [ ] **Step 7.4: Commit on `main`**

```bash
git checkout main
git pull --ff-only
git add .agent/state.md
git commit -m "chore(agent-memory): close orders.version optimistic-locking cycle

PR #<PR#> merged at <MERGE_COMMIT>. state.md Open Items updated to
remove the resolved follow-up and add the new HTTP error-mapping
polish follow-up; Current Status gains the cycle-CLOSED entry."
git push
```

---

## Notes on out-of-scope items

These are listed in spec §3 and are deliberately **not** addressed by any task in this plan:

- Full HTTP error mapping (not-found → 404, rule violation → 422, etc). The plan only wires `ErrConcurrencyConflict → 409`; the rest of the dispatch path keeps the existing generic 400. Captured as the new Open Item in Task 7.
- Deletion of `memrepo` (zero importers / zero tests post-PR #19). Separate hygiene cycle.
- Explicit `loadedVersion` field on the aggregate (Pattern (B) from the brainstorm). Re-evaluate if a future use case stages multiple events per Save such that `Version() == priorLoaded + 1` no longer holds.
- Any change to `eventbus/outbox/pgx`, `ports/database/pgx`, or the `go-ddd-core` module.
- New worker-redelivery integration test that asserts the §5.4 reasoning ("Kafka redelivery + `ORDER_ALREADY_SHIPPED` idempotency completes the reload-retry loop"). See spec §6.4 for the rationale; add the test only if a future reviewer wants it as an executable success criterion.
