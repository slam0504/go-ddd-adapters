# eventbus/inbox/pgx — durable inbox design (core maturation + adapter + worker wiring)

Date: 2026-07-02. Status: accepted (user-reviewed in-session).
Closes the `examples/orders` README "no durable Inbox / consumer-side dedup"
shortcut — the last correctness-class shortcut from the pgx outbox demo cycle.

## 1. Goal & context

Three phases, full cross-repo tag-gate (core contract changes this cycle —
unlike v0.12.0's simplified gate):

- **Phase A (core)**: mature the `eventbus.Inbox` contract — duplicate-Record
  semantics, field validation, `inboxtest.RunContract` conformance suite.
- **Phase B (adapters)**: `eventbus/inbox/pgx` adapter + migrations; align the
  existing in-process `Memory` inbox to the matured contract (**BREAKING**).
- **Phase C (examples/orders)**: minimal worker wiring proving exactly-once
  per consumer end-to-end; README shortcut removed/narrowed.

Candidate evaluation (recorded to avoid re-litigation): chosen over
`storage/*` (last zero-consumer port, but larger/more divergent design space —
next cycle candidate), examples-orders small-debt bundle (cleanup, not an
adapter investment), and a ristretto verification spike (short spike material,
not a main cycle).

### Why the contract must change first (Phase A before B)

The two-step `Seen`/`Record` shape is check-then-act: under concurrent
duplicate delivery both workers observe `Seen == false` and both run the
handler. The only correct guard is transactional: `Record` participates in the
handler's DB transaction and a duplicate surfaces as an ERROR that rolls the
loser's transaction back. Today the contract does not say what duplicate
`Record` returns, and the existing `Memory` impl treats it as silent no-op —
silent success cannot roll anything back, so the pgx adapter's required
behavior would CONTRADICT the incumbent semantics. That conflict is pinned in
core first, not discovered mid-adapter.

## 2. Versions & tag-gate (full four-step)

1. Adapter impl PR merges at a core **pseudo-version pin** (CI green).
2. Core tags **v0.13.0** (publishes matured `Inbox` godoc + `ErrAlreadyRecorded`
   + `eventbus/inboxtest`).
3. Adapters dep-bump PR: pseudo → `v0.13.0` (root + examples/orders) +
   CHANGELOG flip + README compat row.
4. Adapters tag **v0.13.0** + GitHub Release Latest.

Version coincidence with core (both v0.13.0) is accidental; alignment policy
remains dropped.

## 3. Phase A — core `eventbus.Inbox` maturation

### 3.1 New sentinel

```go
// ErrAlreadyRecorded is returned by Inbox.Record when the key is already
// recorded. Callers running Record inside the handler's transaction treat it
// as "duplicate delivery": roll back and acknowledge without re-running side
// effects.
var ErrAlreadyRecorded = errors.New("eventbus: inbox key already recorded")
```

Plain sentinel (matches `cache.ErrMiss` / `storage.ErrNotFound` style);
implementations may wrap it — callers test with `errors.Is`.

### 3.2 Godoc tightening (semantic contract)

- **`Record` MUST return `ErrAlreadyRecorded`** (possibly wrapped) when the
  key is already recorded — including the concurrent case, where the conflict
  may only surface at `Record`/commit time. Duplicate `Record` is NEVER a
  silent success.
- **`Seen` is an advisory fast-path**: it may be stale under concurrency; it
  exists to skip handler work cheaply. Correctness rests on `Record` running
  in the same transaction as the handler's side effects and the caller
  rolling back on `ErrAlreadyRecorded`.
- **Validation precedence** (both methods): empty `Consumer` OR empty
  `EventID` → `errorsx.CodeInvalidArgument`, checked BEFORE ctx and backend;
  a valid key with a cancelled/expired ctx returns the ctx error. (Mirrors
  the `ports/cache` empty-key precedence.)

### 3.3 `eventbus/inboxtest.RunContract(t, Factory)`

Deterministic conformance suite (no sleeps, no containers), authored against
an unexported in-core reference impl and red-proven by temporary breakage
(cachetest precedent). Subtests:

1. `SeenUnrecordedReturnsFalse`
2. `RecordThenSeenReturnsTrue`
3. `DuplicateRecordReturnsErrAlreadyRecorded` (errors.Is)
4. `PerConsumerIsolation` — same EventID, different Consumer: both record.
5. `PerEventIsolation` — same Consumer, different EventID: both record.
6. `EmptyConsumerInvalidArgument` (Seen + Record)
7. `EmptyEventIDInvalidArgument` (Seen + Record)
8. `EmptyKeyPrecedesCancelledCtx` — empty field + cancelled ctx →
   CodeInvalidArgument, not ctx error.
9. `ValidKeyCancelledCtxReturnsCtxError` (Seen + Record)

Concurrency-race behavior (blocking INSERT, loser rollback) is NOT in the
deterministic suite — it is an adapter-level integration test (Phase B).

## 4. Phase B — `eventbus/inbox/pgx` + `Memory` alignment

### 4.1 pgx adapter

- Dir `eventbus/inbox/pgx`, package name proposal `pgxinbox` (open to veto;
  mirrors sibling naming).
- Table (migrations dir + embed, mirroring `eventbus/outbox/pgx/migrations`):

```sql
CREATE TABLE inbox_records (
    consumer    TEXT        NOT NULL,
    event_id    TEXT        NOT NULL,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (consumer, event_id)
);
```

  `recorded_at` carries no v1 behavior — it exists to keep the path open for
  a future retention/cleanup migration (documented limitation, see §7).
- **`Record` is a plain `INSERT`** — deliberately NOT `ON CONFLICT DO
  NOTHING` (silent success cannot stop the race). Postgres `23505
  unique_violation` → wrapped `ErrAlreadyRecorded`. Under concurrent
  duplicates the second INSERT blocks on the first tx's lock, then raises
  23505 after the winner commits → the loser's transaction rolls back. That
  blocking-INSERT behavior IS the transactional guard.
- `Seen` = `SELECT EXISTS`.
- Both methods execute via `pgxdb.Executor(ctx, pool)`-style resolution:
  join the ambient ctx transaction when present, else the pool — same shape
  as outbox `Stage`.
- Other backend errors follow the sibling pgx adapters' conventions (wrapped,
  never silently swallowed); ctx errors pass through verbatim.
- Validation precedence per §3.2.

### 4.2 `Memory` alignment (**BREAKING**)

- Duplicate `Record` changes from silent no-op to `ErrAlreadyRecorded`.
  CHANGELOG records this explicitly as **BREAKING** (behavior, not API).
- **`WithTTL` semantics made precise**: within the retention window a
  duplicate `Record` returns `ErrAlreadyRecorded`; after expiry the key is
  treated as NOT recorded — `Seen` returns false and a fresh `Record`
  succeeds. (Anything else would nullify `WithTTL`.) `WithMaxSize` eviction
  behaves the same way: an evicted key is re-recordable.
- `Memory` must pass `inboxtest.RunContract` (it becomes the suite's second
  conformer); TTL/eviction re-record behavior gets adapter-specific intent
  tests (deterministic — injected clock or short-TTL with explicit expiry
  check, following the repo's no-flaky-sleep norms).

### 4.3 Phase B tests

- `inboxtest.RunContract` vs real Postgres (testcontainers), new CI
  integration step only if the existing pgx integration leg doesn't already
  cover the package path (reuse the outbox/pgx leg's pattern otherwise).
- Adapter-specific concurrent-duplicate race test: two parallel transactions
  Record the same key — exactly one commits, the other observes
  `ErrAlreadyRecorded` after the winner's commit and rolls back.
- `Memory`: RunContract + TTL/eviction intent tests.

## 5. Phase C — examples/orders worker wiring (minimal)

### 5.1 Confirmed constraint (verified against source, 2026-07-02)

- `ports/database/pgx/txmanager.go`: `WithinTx` ALWAYS `pool.BeginTx` — it
  does not join an ambient ctx transaction.
- `application/unitofwork.go` (core): `UnitOfWorkFromTxManager` is a thin
  passthrough to `tm.WithinTx`.
- `examples/orders` `ship_order.go`: `ShipOrderHandler` opens `h.uow.Do(...)`
  internally.

Therefore wrapping the worker's message path in an outer `WithinTx` would
NEST a second transaction inside the handler — the outer tx would not cover
the handler's side effects. This is a designed-around constraint, not an
open question.

### 5.2 Solution — examples-local joining UoW (core untouched)

`examples/orders` gains a local UnitOfWork decorator:

```go
// joiningUnitOfWork joins an ambient pgx transaction when one is present on
// ctx (pgxdb.TxFromContext), and only opens a new transaction otherwise.
// This lets the worker's outer per-message transaction cover the handler's
// side effects without changing core or the handler.
func joiningUnitOfWork(txMgr database.TxManager) application.UnitOfWork {
    return application.UnitOfWorkFunc(func(ctx context.Context, fn func(context.Context) error) error {
        if _, ok := pgxdb.TxFromContext(ctx); ok {
            return fn(ctx) // already inside the worker's tx — join it
        }
        return txMgr.WithinTx(ctx, fn)
    })
}
```

(Exact adapter-to-`application.UnitOfWork` shape follows what
`application.UnitOfWorkFromTxManager` returns — if no `UnitOfWorkFunc`
adapter exists in core, the decorator implements the interface locally in
examples. Core is NOT modified.)

Worker message path becomes ONE transaction:

```
txMgr.WithinTx(ctx):
    Seen(consumer, eventID)      — true → return nil (ack, skip handler)
    dispatch (ShipOrder handler: save + outbox.Stage — joins via joining UoW)
    Record(consumer, eventID)    — ErrAlreadyRecorded → return it (rollback)
outer layer: errors.Is(err, ErrAlreadyRecorded) → nil (ack)
```

Consumer name: a stable constant (e.g. `"orders-worker"`).

### 5.3 Phase C tests + docs

- Integration test: deliver the same `OrderPlaced` twice (sequentially) →
  exactly ONE ship side effect and ONE staged outbox row. Concurrent-dup
  coverage lives at the adapter race test (§4.3), not re-proven here.
- New migration for `inbox_records` wired into docker-compose's migrate
  services (per-source pattern already established).
- README: remove the durable-inbox shortcut; replace with a precise remaining
  list (reader projection still in-memory; inbox table unbounded growth —
  see §7).

## 6. Verification / success criteria

- Phase A: inboxtest 9 subtests green vs reference impl; red-proof recorded.
- Phase B: RunContract green vs Postgres (CI) AND vs `Memory`; race test
  deterministic-pass under `-race`; lint both tag sets 0 issues.
- Phase C: duplicate-delivery integration test green; full examples suite
  stays green.
- Gate: four steps completed; `releases/latest` → adapters `v0.13.0`; proxy
  resolves both new tags.

## 7. Deferred / explicitly out (do not re-litigate without new evidence)

- Reader pgx projection, LISTEN/NOTIFY relay variant, `claim_id` worker
  attribution (pre-existing open items; deliberately excluded from this
  cycle by user decision 2026-07-02).
- **Inbox retention/cleanup**: v1 accepts unbounded `inbox_records` growth,
  recorded as a limitation in the adapter doc + examples README;
  `recorded_at` preserves the future cleanup-migration path.
- `storage/*` adapter: next-cycle candidate (last zero-consumer port).
- ristretto verification spike: unchanged from httpclient spec §8.

## Self-decided (open to veto during plan review)

- Package name `pgxinbox`; table name `inbox_records`; consumer constant
  `"orders-worker"`.
- inboxtest Factory signature mirrors `cachetest.Factory` (`func(t) Inbox`).
- Memory TTL intent tests use the repo's deterministic-clock idiom rather
  than wall-clock sleeps where the existing Memory impl allows it.
