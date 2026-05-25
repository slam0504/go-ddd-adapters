# `orders.version` Optimistic Locking Activation — Design

- **Status**: APPROVED 2026-05-23 (user sign-off, with two §6 text fixes applied); ready for writing-plans hand-off
- **Date**: 2026-05-23 (Asia/Taipei)
- **Branch**: TBD (likely `feat/orders-version-optimistic-locking`)
- **Brainstorm session**: this document captures decisions reached on 2026-05-23
- **Implementation plan**: TBD, produced by `writing-plans` skill after this spec is approved
- **Prior cycle**: builds on PR #19 (`examples/orders` pgx outbox demo, merge `e6b1672`)

---

## 1. Goal

Activate the existing `orders.version` column as an optimistic-locking
guard on the `examples/orders` write path. Today the column is read,
written, and incremented by the aggregate, but `pgxrepo.Save` does not
check it on UPDATE — a stale write silently overwrites a fresher row.

Success means a stale write returns `domain.ErrConcurrencyConflict`
instead, and the existing transactional-outbox tx contract (Save +
`outbox.Stage` atomic) carries the rollback to `outbox_records` for
free.

## 2. In scope

- `examples/orders/infra/pgxrepo/order_repo.go` — `Save` gains a
  SQL-encoded prior-version guard and a `RowsAffected()==0` →
  `domain.ErrConcurrencyConflict` mapping.
- `examples/orders/infra/memrepo/order_repo.go` — one-line package /
  type comment marking that this repo is example-only and does **not**
  enforce optimistic locking. No behavior change.
- `examples/orders/cmd/api/main.go` — partial transport-error mapping:
  `errors.Is(err, domain.ErrConcurrencyConflict)` → `http.StatusConflict
  (409)`. All other error mappings unchanged. A `// TODO` notes this is
  the OL-specific case and that full HTTP error taxonomy is a separate
  cycle.
- New file `examples/orders/integration/optimistic_lock_test.go` with
  two `//go:build integration` tests (detailed in §6).
- `.agent/state.md` Open Items: remove `orders.version optimistic-locking
  activation`; add follow-up "HTTP error mapping polish (full transport
  error taxonomy)".
- `.agent/decisions.md`: add `## orders.version optimistic locking
  activation (2026-05-23 cycle)` section recording the SQL-encoded
  approach and its invariants.

## 3. Out of scope (explicit follow-ups)

- Full HTTP error mapping (not-found → 404, rule violation → 422, etc).
  Captured as a new Open Item in `.agent/state.md`.
- Deletion of `memrepo` (zero importers / zero tests post-PR #19).
  Hygiene cycle, independent of this one.
- Explicit `loadedVersion` field on the aggregate (Pattern (B) from
  the brainstorm). Would be revisited if a future use case stages
  multiple events per Save such that `Version() == priorLoaded + 1`
  no longer holds.
- Any change to `eventbus/outbox/pgx`, `ports/database/pgx`, or the
  `go-ddd-core` module.

## 4. Non-goals

- This is not a generic adapter feature. The `orders.version` column
  is example-specific. No new helper is extracted into the root module.
- No new sentinel error is introduced — `domain.ErrConcurrencyConflict`
  already exists at `go-ddd-core/domain/errors.go:10` and is the
  intended target.
- No schema migration. The `version` column already exists in
  `001_create_orders.up.sql`.

## 5. Design

### 5.1 SQL-encoded prior-version guard

`pgxrepo.Save` becomes:

```sql
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
```

Followed by:

```go
cmd, err := exec.Exec(ctx, sql, ...)
if err != nil {
    return fmt.Errorf("pgxrepo: upsert order %s: %w", o.ID(), err)
}
if cmd.RowsAffected() == 0 {
    return fmt.Errorf("pgxrepo: optimistic lock conflict on order %s: %w",
        o.ID(), domain.ErrConcurrencyConflict)
}
return nil
```

### 5.2 Four operating cases

| Case | Path | Outcome |
| --- | --- | --- |
| `Place` (new ID) | INSERT succeeds, ON CONFLICT not triggered | 1 row → success |
| `Place` (duplicate ID) | ON CONFLICT triggered → UPDATE branch; existing `version ≥ 1`, `EXCLUDED.version - 1 = 0` ⇒ WHERE false | 0 rows → `ErrConcurrencyConflict` |
| `Ship` (first writer for a loaded snapshot) | UPDATE branch; existing.version=1, EXCLUDED.version=2, WHERE true | 1 row → success |
| `Ship` (second writer of same loaded snapshot) | UPDATE branch; existing.version=2 (post first writer), EXCLUDED.version=2, WHERE false | 0 rows → `ErrConcurrencyConflict` |

### 5.3 Invariant

**`aggregate.Version() == priorLoadedVersion + 1` for every Save in the
demo.**

This is guaranteed in scope by the fact that `Order.Place` and
`Order.Ship` each call `IncrementVersion()` exactly once and are each
followed by exactly one `Save`. The invariant is pinned with a short
code comment on `Save`; a future cycle that stages multiple events per
Save would need Pattern (B) from the brainstorm (explicit
`loadedVersion`).

### 5.4 Error propagation chain

```
pgxrepo.Save                          (RowsAffected()==0)
  └─ wraps domain.ErrConcurrencyConflict
     │
     └─ application.UnitOfWork.Do returns the error
        │  TxManager triggers ROLLBACK:
        │    • orders row unchanged
        │    • outbox_records staged row also rolled back
        │
        └─ PlaceOrderHandler / ShipOrderHandler propagate verbatim
           (no catch, no reload, no retry)
           │
           ├─ HTTP (cmd/api): errors.Is(... ErrConcurrencyConflict)
           │                    → http.StatusConflict (409)
           │
           └─ Kafka (cmd/worker): error bubble → subscriber Nack
                                  → broker redelivery
                                  → next FindByID reads fresh row
                                  → aggregate.Ship() hits
                                    ORDER_ALREADY_SHIPPED rule path
                                  → handler returns success → Ack
```

Two invariants worth naming:

1. **Tx atomicity carries outbox rollback.** The "no extra
   `outbox_records` row on conflict" property depends on `uow.Do`
   wrapping Save and Stage in the same tx — a contract established by
   PR #19. This cycle does not touch it.
2. **Retry policy lives in the transport layer.** Handlers never
   catch `ErrConcurrencyConflict`. Kafka redelivery + aggregate
   rule-layer idempotency (`ORDER_ALREADY_SHIPPED`) together complete
   the reload-retry loop without any new code.

### 5.5 memrepo divergence note

A one-line package-level note is added near `memrepo`'s `package` or
`OrderRepository` doc comment:

```go
// Note: this in-memory repository is example-only and does not enforce
// optimistic locking; use pgxrepo for production-grade conflict semantics.
```

No behavior change. `memrepo` is currently a reference artifact with
zero importers and zero tests post-PR #19; deletion is a separate
hygiene cycle.

### 5.6 HTTP partial mapping

`cmd/api/main.go` keeps its existing dispatch error handler
(`http.StatusBadRequest`) but inserts an `ErrConcurrencyConflict`
branch ahead of it:

```go
if errors.Is(err, domain.ErrConcurrencyConflict) {
    // TODO: full transport-error taxonomy (not-found → 404,
    // rule violation → 422, etc.) is a separate cycle; this is the
    // optimistic-locking-specific case only.
    http.Error(w, err.Error(), http.StatusConflict)
    return
}
```

All other error paths remain untouched.

## 6. Test plan

One new file: `examples/orders/integration/optimistic_lock_test.go`,
under `//go:build integration`, reusing the `TestMain`, `sharedPool`,
and truncate helpers in `examples/orders/integration/main_test.go`
already in place from PR #19. No new test-infra package is introduced.

### 6.1 `TestSave_OptimisticLock_ConcurrentUpdate`

Validates the SQL guard directly at the `pgxrepo` layer.

```
seed: orders row id=X, status=placed, version=1
findA := repo.FindByID(ctx, X)         // version 1
findB := repo.FindByID(ctx, X)         // version 1 (independent snapshot)
findA.Ship(eventIDA, "carrierA")       // → version 2 in memory
findB.Ship(eventIDB, "carrierB")       // → version 2 in memory

require.NoError(t, repo.Save(ctx, findA))                   // 1 row affected
err := repo.Save(ctx, findB)
require.ErrorIs(t, err, domain.ErrConcurrencyConflict)

// DB shows exactly one Ship transition (not the seed state, not double-applied).
// Note: Ship() does not mutate items / total_cents, so findA vs findB cannot be
// distinguished on the orders row alone — the assertion is that the version
// advanced by exactly one and status moved to shipped.
SELECT status, version FROM orders WHERE id=X
expect: status="shipped", version=2
```

### 6.2 `TestPlaceOrder_DuplicateID_Conflict`

Validates the handler path + tx rollback of the outbox stage row.

```
preCount := count(*) FROM outbox_records WHERE aggregate_id=X
require.Equal(t, int64(0), preCount)

require.NoError(t, handler.Handle(ctx, PlaceOrderCommand{OrderID: X, ...}))

err := handler.Handle(ctx, PlaceOrderCommand{OrderID: X, ...})
require.ErrorIs(t, err, domain.ErrConcurrencyConflict)

postCount := count(*) FROM outbox_records WHERE aggregate_id=X
require.Equal(t, int64(1), postCount)    // exactly the first call's stage row
```

### 6.3 Regression guard

Existing PR #19 tests remain unchanged and continue to act as
happy-path regression:

- `TestPlaceOrder_TransactionalOutbox`
- `TestShipOrder_TransactionalOutbox`
- `TestShipOrder_TxRollback`
- `TestShipOrder_NotFound`
- `TestWorker_HandleOrderPlaced_PropagatesHeaders`
- `TestPartitionByAggregate_PreservesOrder`
- `TestRoundTrip_AllHeaders`
- `TestPlaceOrder_TxRollback`

### 6.4 Worker redelivery path is **not** covered by a new test

The §5.4 reasoning that Kafka redelivery + `ORDER_ALREADY_SHIPPED`
idempotency together complete the reload-retry loop is **behavioural
inference**, not new test coverage in this cycle. Justification:
`ShipOrderHandler` and the aggregate's `ORDER_ALREADY_SHIPPED` rule
are not touched here, so existing happy-path + rule-layer tests still
hold. If a future reviewer wants this turned into an executable
success criterion, add a third integration test (e.g. simulate a
second `OrderPlaced` delivery against an already-shipped aggregate
and assert handler returns success with no extra outbox row) — out
of scope for this PR.

## 7. Verification before merge

Run from repo root unless noted.

```sh
# root module (lint must stay clean; no code touched here, but keep the matrix honest)
go build ./...
go vet ./...
go test ./...
golangci-lint run ./...
golangci-lint run --build-tags=integration ./...

# examples/orders module
cd examples/orders
go build ./...
go vet ./...
go test ./...
go test -tags=integration -race ./integration/...   # 10 tests = 8 existing + 2 new
golangci-lint run ./...
```

CI matrix already covers both modules. Integration job already runs
`./integration/...` under build-tag `integration`, so the new tests
ride that lane without workflow changes.

## 8. Cycle-close bookkeeping

After merge:

- `.agent/state.md`:
  - Remove `orders.version optimistic-locking activation` from the
    examples/orders pending follow-ups.
  - Add follow-up: "HTTP error mapping polish — extend `cmd/api` to
    full transport-error taxonomy (not-found → 404, rule violation →
    422, etc.). Today only `ErrConcurrencyConflict → 409` is wired."
  - Add new entry under `## Current Status` summarising the cycle as
    CLOSED with merge commit + PR number.
- `.agent/decisions.md`: append `## orders.version optimistic locking
  activation (2026-05-23 cycle)` with the SQL-encoded approach,
  invariant, and the rejected alternatives (explicit `loadedVersion`,
  insert/update split).
- `.agent/review-log.md`: untouched unless CR findings are added or
  resolved during the cycle.
- `<workspace-root>/.agent-memory/go-ddd.md`: untouched — this cycle
  does not cross repos.

## 9. Risks and mitigations

- **Risk**: A future use case stages multiple events per Save such that
  `Version()` jumps by more than one, silently breaking the implicit
  `-1` assumption.
  **Mitigation**: Code comment on `Save` names the invariant. Open
  Item carries Pattern (B) as the upgrade path.

- **Risk**: Postgres's `ON CONFLICT ... DO UPDATE ... WHERE` returning
  0 rows could be mistaken for "no matching row" rather than "guarded
  update declined". This is the intended translation here, but a code
  comment + the named sentinel disambiguate at the call site.

- **Risk**: HTTP partial mapping leaves a `TODO` in `cmd/api`. A
  future reader may mistake it for unfinished work in this cycle
  rather than a deliberate scope boundary. **Mitigation**: comment is
  explicit ("separate cycle"), and `.agent/state.md` Open Item
  formalises the follow-up.
