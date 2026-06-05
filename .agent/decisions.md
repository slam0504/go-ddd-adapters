# go-ddd-adapters Decisions

Last verified: 2026-05-18 Asia/Taipei

## Adapter Boundary

- Concrete Kafka/Sarama, slog, and OTel SDK dependencies belong in this repo.
- Core package imports should remain contract-level.
- Examples can use concrete adapters because they are wiring demonstrations.

## Kafka Consumer Lifecycle

- `ConsumerModule` takes an `eventbus.Subscriber` interface, not a concrete
  `*kafka.Subscriber`, so tests and alternate backends can substitute fakes.
- Runtime context uses `context.WithCancel(context.WithoutCancel(startCtx))`.
- Handler signature is:

  ```go
  func(ctx context.Context, env eventbus.Envelope) error
  ```

- Module owns Ack/Nack:
  - nil error -> Ack
  - non-nil error -> Nack
  - panic -> recover, log stack, Nack
- `ConsumerGroup` is a single `bootstrap.ModuleFunc`, not `[]bootstrap.Module`.
  It shares one context and WaitGroup across topics so Stop cancels all topics
  atomically.

## Observability Lifecycle

- `observability/otel.Module` wraps `Provider.Shutdown` for bootstrap stop.

## Upstream Contract Notes (go-ddd-core v0.3.0)

This repo does **not** currently implement these ports, but the contracts
are documented here so future adapter work (SQL/Redis inbox, GORM/pgx
outbox, UoW-aware repositories) starts from the right shape without
re-reading core source.

- **Inbox key shape**: `eventbus.InboxKey{Consumer, EventID}`. The
  `eventbus.Inbox` port is **best-effort dedup**: `Inbox.Record` returns
  only `error`, and the reference `Memory` implementation no-ops on
  duplicate, so a caller cannot distinguish first-write from
  duplicate-write. Under concurrent redelivery two consumers can both
  pass `Seen` and run the handler before either calls `Record`.
  **Handlers in any adapter — SQL inbox, Redis inbox, in-memory inbox —
  must be idempotent on at-least-once delivery.**
- **Outbox record split**: `OutboxRecord.ID` identifies the outbox row;
  `OutboxRecord.EventID` carries the domain event id used as broker
  message id and as the inbox dedup key. `DomainEvent.EventID()` is the
  canonical event id source.
- **Unit-of-Work bridge**: use cases depend on `application.UnitOfWork`.
  Adapters can supply a `ports/database.TxManager` and bridge it via
  `application.UnitOfWorkFromTxManager`. Use cases must **not** import
  `TxManager` directly — that boundary is what keeps application code
  driver-agnostic.

## Inbox Adapter Package (`eventbus/inbox`)

The in-process `Memory` Inbox originally shipped in `go-ddd-core` v0.2.0
and was relocated to this repo in PR #7 (merged 2026-05-18). Key
design choices recorded for future SQL/Redis adapter work:

- **Path is `eventbus/inbox` (flat package `inbox`), not
  `eventbus/inbox/memory`.** Three reasons:
  - Mirrors `go-ddd-core/eventbus/inbox` so existing users only change
    their import path; the call site (`inbox.NewMemory(...)`) stays
    identical.
  - Treats "in-process Inbox implementations" as one tech-cluster —
    `Memory` today, hypothetical future in-process variants (a buffered
    sliding-window dedup, an LRU cache) live next to it under the same
    package. This matches how `eventbus/kafka` groups Publisher /
    Subscriber / Codec under one tech-cluster package.
  - Future out-of-process implementations sit at sibling paths
    (`eventbus/inbox/sql`, `eventbus/inbox/redis`, ...) — different
    tech clusters with different transitive deps, so they get their
    own sub-packages.
- **No re-export of `eventbus.InboxKey` / `eventbus.Inbox` from this
  package.** Callers import the contract from `go-ddd-core/eventbus`
  and the implementation from `go-ddd-adapters/eventbus/inbox`. Two
  imports is the explicit boundary the adapter repo is built around.
- **`WithTTL` uses lazy filtering at read time, not eager removal.**
  - `Seen` returns false for entries whose age exceeds `ttl`, but does
    not delete from the map (stays under RLock, no write-side
    contention).
  - `Record` overwrites an expired existing entry in place so Size
    does not grow.
  - Actual memory reclamation falls to `WithMaxSize`-triggered
    `evictOldestHalf`. Long-running services with high event
    cardinality must pair `WithTTL` with `WithMaxSize`.
  - Boundary semantics: `age == ttl` is still fresh (strict `>`),
    `age == ttl + 1ns` is expired. Pinned in
    `TestTTL_BehaviorTable`.

## Outbox Adapter Package (`eventbus/outbox`)

This section pre-records design decisions for the in-process `Memory`
Outbox + `Relay` shipping in PR #9 (feature branch
`feat/outbox-relay-memory`). The package mirrors `eventbus/inbox`'s
flat-package shape: `Memory` is the in-process implementation;
sibling sub-packages (`eventbus/outbox/pgx`, ...) will hold
out-of-process implementations with their own driver-specific
transitive deps.

### Boundary / scope

- **Path is `eventbus/outbox`, flat package `outbox`.** Same rationale
  as `eventbus/inbox`: mirrors core's structure, treats in-process
  Outbox implementations as one tech-cluster, leaves room for sibling
  sub-packages (`outbox/pgx`, ...) without forcing callers to rename.
- **No re-export of `eventbus.Outbox` / `OutboxStore` / `Relay` /
  `OutboxRecord` from this package.** Callers import the contract from
  `go-ddd-core/eventbus` and the implementation from
  `go-ddd-adapters/eventbus/outbox`. Same two-import boundary as
  `eventbus/inbox`.
- **Memory is explicitly NOT transactional.** Core's `eventbus.Outbox`
  docstring says implementations "must participate in the caller's
  transaction (typically by reading a tx handle from ctx)". Memory
  cannot — it only offers in-process mutex safety inside its own
  store. If aggregate persistence succeeds but `Stage` is not called,
  or the process crashes mid-flow, events are lost. **This adapter is
  test/dev/example-only.** The package `doc.go`, the README adapter
  row, the README usage sketch, the CHANGELOG bullet, and every
  external-facing docstring must repeat this warning verbatim.
  Production users need the SQL/pgx successor (follow-up cycle).

### Memory store

- **`Memory` implements both `eventbus.Outbox` and
  `eventbus.OutboxStore`.** Same backing slice powers Stage (producer
  side) and Fetch / MarkSent / MarkFailed (Relay side). Splitting the
  two would force callers to wire a shared backing store between two
  types with no real abstraction benefit at the in-process layer.
- **Stage is all-or-nothing.** Pattern: `marshal-all-first` (outside
  the lock), then `append-all-under-lock-once`. Any codec failure
  aborts the batch with zero records appended, so a caller-side tx
  rollback semantically matches Memory's no-op-on-failure. Empty
  events slice is a no-op (returns `nil`).
- **`OutboxRecord.EventName / AggregateID / AggregateType` come from
  the canonical `domain.DomainEvent` methods**, NOT from codec
  metadata (`msg.Metadata.Get(HeaderEventName)`, ...). The event
  interface is the source of truth for those identity fields; codec
  metadata is a serialization detail. A buggy codec must not be able
  to silently drop those columns from the outbox table. The pgx
  successor's schema-population code will read from the same
  interface methods.
- **ID generation is concurrency-safe via lock-held assignment.** The
  default monotonic generator is `atomic.Uint64`-backed
  (`mem-outbox-<n>`) so it stays correct outside the lock; AND Stage
  calls `nextID()` only AFTER taking `m.mu`. The post-lock placement
  is a deliberate insurance policy for users supplying a non-atomic
  generator via `WithIDGenerator`.
- **`Fetch` and `DeadLettered` return defensive copies of `Headers`
  and `Payload`.** `Payload []byte` is `slices.Clone`d; `Headers
  map[string]string` is shallow-copied per record. The top-level slice
  / map returned by `DeadLettered` is also fresh. Without copies, a
  Relay goroutine running a `HeaderRestorer` that mutates the headers
  map would corrupt the stored record under the Memory lock; test
  code mutating returned `Payload` would similarly violate the lock
  contract.
- **`WithMaxSize` is the only knob.** TTL doesn't fit Outbox semantics
  — unsent events never expire by clock; the only legitimate reasons
  to drop a record are explicit MarkSent or Terminate (DLQ). When the
  store is full, Stage returns `ErrOutboxFull` so the caller (and any
  enclosing tx) sees the failure rather than experiencing silent loss
  via eviction.
- **`MarkFailed.reason` is log-only in Memory.** Memory has no durable
  schema, so the reason is logged at write time and dropped. Adapter
  limitation, not a contract limitation: the pgx successor MUST add
  an adapter-private `last_error` column. Adapter-private fields are
  legitimate even when core's read contract doesn't expose them — the
  contract sets the floor, not the ceiling.

### DLQ (dead-letter quarantine)

- **MaxAttempts terminal state moves the record to an
  adapter-private DLQ map**, not back to the active store. Active
  Fetch never returns DLQ records; `DeadLettered() map[string]DeadLetterRecord`
  exposes them for test / operator inspection.
- **`DeadLetterRecorder` interface is defined inside the `outbox`
  package**, not in core. Core's `OutboxStore` has no DLQ primitive,
  so the Relay calls a separate, optional interface for termination.
- **`NewRelay` rejects at construction time** when `MaxAttempts > 0`
  and the supplied `Store` does not implement `DeadLetterRecorder`.
  Without this gate, MaxAttempts-without-DLQ would be an infinite
  redelivery loop. `MaxAttempts = 0` (unlimited) is the only path
  that accepts a DLQ-less store.
- **`DeadLetterRecord.Attempts` is the terminal attempt count**, set
  to `rec.Attempts + 1` at Terminate time. The `+1` is the attempt
  that caused termination; `rec.Attempts` was the prior survivable
  count. Pinning the off-by-one in the struct field (rather than
  inferring it from `Record.Attempts`) makes operator tooling
  unambiguous and matches what the pgx DLQ table will store.

### Relay

- **`Relay` is a separate type, driver-agnostic.** It depends on the
  `eventbus.OutboxStore` interface plus the local `DeadLetterRecorder`
  extension — no direct dependency on `*Memory`. Same Relay code
  drives the pgx successor.
- **Codec is captured at Stage time and at Relay time;
  `RelayConfig.Codec` MUST be the same instance** (or at least one
  with the same registry) as the one passed into Memory. Mismatch
  routes the record through `fail()`.
- **Backoff lives in `Relay`, not `Memory`.** `Memory.MarkFailed`
  stores whatever `nextAttemptAt` the Relay computes. Default
  backoff is exponential with jitter, capped at 60s.
- **Same-`*Relay` reentry is guarded by `atomic.Bool`** inside
  `Run`. Second concurrent `Run` on the SAME `*Relay` returns
  `ErrRelayAlreadyRunning`. Two different `*Relay` instances against
  the same `*Memory` are NOT defended — adding in-process claims to
  Memory would turn a test/dev adapter into a half-baked scheduler
  with non-real crash semantics. Documented as user responsibility;
  pgx successor handles multi-Relay safely via
  `SELECT FOR UPDATE SKIP LOCKED`.
- **Per-record `defer recover` in `Relay.process`.** Panic anywhere
  in decode → restore → publish → MarkSent is recovered, logged with
  stack, and routed through the same `fail(ctx, rec, reason)` path
  that handles a returned error. So MaxAttempts / DLQ still apply,
  and a single poison record cannot kill the drain loop. Mirrors
  `kafka/processEnvelope`'s recover-and-Nack shape.

### Header propagation

- **Subset propagation via Relay callback option.** Stage stores the
  full `Metadata` map in `OutboxRecord.Headers`. By default, Relay
  does NOT propagate any header back into the publish path — that
  would silently couple Relay to codec internals. With
  `WithHeaderRestorer(fn)`, Relay calls the user-supplied callback
  to re-inject selected headers into the publish ctx before invoking
  `Publisher.Publish`.
- **`kafka.RestoreCoreHeaders` is the canonical restorer** for
  callers running the kafka codec. It promotes the three core
  well-known headers — `eventbus.HeaderTraceID`,
  `HeaderCausationID`, `HeaderCorrelationID` — from the stored
  Headers map back into ctx via this package's `WithTraceID` /
  `WithCausationID` / `WithCorrelationID` helpers; the JSON codec
  then re-promotes them to message metadata on its Marshal.
- **Arbitrary (non-well-known) headers are NOT propagated
  end-to-end.** Documented limitation. Bypassing
  `Publisher.Publish` to push raw `*message.Message` directly is a
  future "broker-direct Relay" variant, out of scope here.

### Bootstrap wiring

- **`RelayModule(*Relay, logger.Logger) bootstrap.ModuleFunc`** wraps
  Relay's lifecycle. Start derives `runCtx` from
  `context.WithCancel(context.WithoutCancel(startCtx))` so trace
  values flow through but Run is not killed by a short-lived
  startCtx; Run runs in one goroutine; Stop cancels and waits for
  Run to return, bounded by `stopCtx`. `context.Canceled` from Run
  is silent (clean shutdown). Any other Run error is logged at Error
  level but not surfaced to bootstrap because the loop is the
  intended terminal state. Per-record panic recovery is in
  `Relay.process`; the module goroutine is panic-free by
  construction.

### Limitations the pgx successor MUST address

Tracked here so the follow-up cycle doesn't re-discover them. **All
five are CLOSED in the pgx Outbox Adapter Package section below
(2026-05-20).**

1. ~~Real transactional Stage~~ ✅ pgx decision #11: Stage reads tx
   handle from ctx via `pgxdb.TxFromContext`; absent → `ErrNoTx`.
2. ~~Durable `last_error` column~~ ✅ pgx schema:
   `outbox_records.last_error TEXT NULL` populated by MarkFailed.
3. ~~Multi-Relay safety via SKIP LOCKED~~ ✅ pgx decision #10 + #21:
   `SELECT ... FOR UPDATE SKIP LOCKED` claim model with `claim_token`
   guard against stale worker writes.
4. ~~DLQ as a separate table with terminal `Attempts`~~ ✅ pgx decision
   #4: `outbox_dead_letters` with `attempts INTEGER NOT NULL` storing
   `active.attempts + 1` (terminal count).
5. ~~Memory's `WithMaxSize` / `WithIDGenerator` / `WithClock` knobs do
   NOT carry over~~ ✅ pgx adapter exposes only `WithClaimLease(d)`;
   memory-specific knobs are intentionally absent.

## pgx Outbox Adapter Package (`eventbus/outbox/pgx` + `ports/database/pgx`)

Locked 2026-05-20 after four plan revisions and three Codex review
rounds. The 23 decisions below are the source of truth for the pgx
implementation; see `<workspace-root>/.claude/plans/outbox-relay-agile-orbit.md`
for the long-form plan with rationale tables.

### Surface

- `ports/database/pgx` (package `pgxdb`): `TxManager` implementing
  core's `ports/database.TxManager`, `WithTx`/`TxFromContext`/`Executor`
  helpers, `ErrNoTx`. Pool-based; `WithTxOptions(pgx.TxOptions)` sets
  TxManager-level default isolation/access mode.
- `eventbus/outbox/pgx` (package `pgxoutbox`): `Store` implementing
  `eventbus.Outbox` + `eventbus.OutboxStore` + `outbox.DeadLetterRecorder`.
  Re-exports `ErrNoTx` from pgxdb. Adds `ErrMalformedID` for the
  adapter-private id encoding.
- `eventbus/outbox/pgx/migrations`: versioned SQL files +
  `//go:embed *.sql ⇒ migrations.FS` for testcontainers / example use.
  Adapter does NOT ship a runtime `Migrate(ctx, db)` API.
- `internal/pgxtest`: shared testcontainers Postgres boot helper for
  this module's integration tests. Not exported (Go internal/ rule).

### Locked decisions (23)

1. **pgx/v5 native** (`*pgxpool.Pool` / `pgx.Tx`); no database/sql shim.
2. **Schema = versioned SQL files** under
   `eventbus/outbox/pgx/migrations/`. No runtime `Migrate(ctx, db)`
   as primary API.
3. **Bundle `ports/database/pgx` + `eventbus/outbox/pgx` in one PR.**
4. **Separate DLQ table** `outbox_dead_letters`. Terminate is atomic
   via `DELETE ... RETURNING` CTE (see #23).
5. **Package names** `pgxdb` and `pgxoutbox` to avoid alias clash with
   `github.com/jackc/pgx/v5`.
6. **TxManager-level default `pgx.TxOptions`** via
   `pgxdb.WithTxOptions(...)`. No per-call override (core contract has
   no slot).
7. **`examples/orders` wiring is OUT of scope** (next PR; same posture
   as memory PR #9).
8. **README migration docs** canonical `golang-migrate` snippet + one
   line "the same SQL works with goose / atlas / flyway".
9. **Postgres baseline = 12+** (IDENTITY, SKIP LOCKED, gen_random_uuid
   via pgcrypto on 12 — see #16). `postgres:16-alpine` for tests.
10. **Claim model = lease-based, short tx**. Fetch issues a fresh
    `claim_token` per row and sets `claimed_until = now() + lease`.
    `process()` does NOT hold a row lock. Mark*/Terminate run as
    independent short tx via pool.
11. **`Stage` without ctx tx = `ErrNoTx`** (hard error, no implicit
    autocommit).
12. **`OutboxRecord.ID` boundary type**: DB `id` is `BIGINT IDENTITY`;
    surfaced to core interface as opaque string per #21.
13. **`payload = BYTEA`, `headers = JSONB`.**
14. **Lease default = `5 × PollInterval`** (5s default).
    `WithClaimLease(d)` overrides; lower bound 100ms.
15. **Relay reuse**: this PR does NOT introduce a new Relay. Existing
    `eventbus/outbox.Relay` drives the pgx Store.
16. **Mixed clock model, per-column ownership pinned.**
    **DB clock**: `outbox_records.created_at` (Stage,
    `clock_timestamp()`), `outbox_records.available_at` at Stage time
    (`clock_timestamp()`), Fetch lease check + compute (`now()`),
    MarkFailed `claimed_until = NULL` reset, Terminate
    `failed_at = now()`. **Relay app clock**:
    `outbox_records.available_at` when written by `MarkFailed` —
    Relay computes `relay.now().Add(backoff(attempts+1))` and passes
    the absolute timestamp into the Store, which writes verbatim.
    **Why mixed**: `OutboxStore.MarkFailed(..., nextAttemptAt time.Time)`
    is a core interface contract; rewriting it to be DB-clock-derived
    would require touching core / Relay. Out of scope; doc.go
    recommends NTP sync across Relay hosts AND DB host.
17. **At-least-once is the explicit contract.** Lease expiry, slow
    `Publisher.Publish`, network timeout, or worker crash can cause
    duplicate publish. `OutboxRecord.EventID` is the broker message
    id; downstream consumers MUST use `eventbus/inbox` (or
    equivalent) for dedup.
18. **Concrete dependency pins** (resolved via `go list -m -versions`
    2026-05-19): `pgx/v5 v5.9.2`, `testcontainers-go v0.42.0`,
    `testcontainers-go/modules/postgres v0.42.0`,
    `golang-migrate/v4 v4.19.1` (+ `source/iofs`,
    `database/pgx/v5`). Intentionally avoid `database/postgres`
    (lib/pq).
19. **`internal/pgxtest` shared helper** for testcontainers boot.
    Each test package's own `TestMain` calls
    `pgxtest.StartContainer(ctx)`.
20. **`ports/database/pgx` integration tests use self-contained
    `pgxdb_test_kv` table**, NOT outbox migrations.
21. **`claim_token UUID` column on `outbox_records` + ID encoding
    `"<dbid>:<UUID>"`.** Fetch generates `gen_random_uuid()` per
    claimed row and returns it; `OutboxRecord.ID =
    fmt.Sprintf("%d:%s", dbID, claimToken)`. Mark*/Terminate parse
    the id and guard `AND claim_token = $token`. Stale worker calls
    become 0-row no-ops. Core `OutboxStore` interface unchanged
    (id-only); encoding is adapter-private (same opacity rule as
    memory's `"mem-outbox-<n>"`).
22. **Zero-row `MarkSent` / `MarkFailed` / `Terminate` = silent
    success (return nil).** Matches memory adapter and the
    at-least-once contract. Stale workers no-op silently.
23. **`Terminate` atomic via single `DELETE ... RETURNING` CTE.**
    `WITH del AS (DELETE FROM outbox_records WHERE id = $1 AND
    claim_token = $2 RETURNING *) INSERT INTO outbox_dead_letters
    (...) SELECT ..., attempts + 1, $3, now() FROM del;`. Only the
    worker whose DELETE actually returned a row inserts into DLQ;
    duplicate DLQ rows under concurrent stale Terminate are
    impossible.

### `gen_random_uuid()` on Postgres 12

Postgres 13+ ships `gen_random_uuid()` as a built-in. For the 12+
baseline (#9), migration `001_create_outbox_records.up.sql` issues
`CREATE EXTENSION IF NOT EXISTS pgcrypto;` first. Postgres 13+
treats the extension as no-op (already present). Avoids depending on
`uuid-ossp` which adds more functions than needed.

### Adapter-private gaps the pgx adapter intentionally leaves open

(These belong to a future cycle, not the v0.4.0 PR.)

- **`claim_id`-based worker attribution** for operator visibility into
  WHICH worker holds a row. The current `claim_token` is opaque
  (uniqueness-only); a future `claim_owner TEXT` column could carry a
  human-readable worker identifier.
- **`LISTEN/NOTIFY` push delivery** to skip polling latency. Polling
  Relay is the only mode this PR ships.
- **Per-call `pgx.TxOptions` override** for isolation/access mode.
  Core's `database.TxManager.WithinTx` has no options slot.
- **`MarkSent` audit row.** Successful sends are DELETEd, not moved
  to a sent table. Broker / consumer side is the durable source of
  truth for "what was sent."
- **`eventbus/inbox/pgx`** — durable consumer-side inbox is a sibling
  adapter, separate cycle.

## Pre-announced v0.4.0 Migration (status)

Core has pre-announced that `eventbus/inbox/memory.go` will be removed
from `go-ddd-core` (see core CHANGELOG `### Deprecated` and
`docs/anti-patterns.md` "Note on `eventbus/inbox/memory.go`"). The new
home in this repo is now in place under `eventbus/inbox/` (this PR).
Core can remove its copy in a subsequent core release; downstream
services migrate by changing the import path from
`go-ddd-core/eventbus/inbox` to `go-ddd-adapters/eventbus/inbox`.


## `examples/orders` pgx outbox demo (2026-05-22 cycle)

Demo-only design decisions; not public adapter API. Captured during
the brainstorm session whose spec lives at
`docs/superpowers/specs/2026-05-21-examples-orders-pgx-outbox-design.md`.

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
   library against the same table parameters. Verified via
   `docker compose up` smoke: state tables are independent;
   adapter and example migrations can evolve their version
   numbering independently.

3. **`Item` gets JSON tags.** Pre-existing bug — `price_cents`
   would silently decode to 0 because Go's `encoding/json` is
   case-insensitive but does not normalise underscores. Fixed
   because pgxrepo's JSONB column would otherwise diverge from
   the HTTP wire shape (round-trip inconsistency).

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
   ack/nack via the handler's return value. `kafka.ConsumerModule`
   enforces this — the handle signature is
   `func(ctx, env) error`; nil = Ack, non-nil = Nack; do NOT call
   `env.Ack/Nack` manually.

8. **`cmd/relay` is wiring-only.** Composes `pgxoutbox.Store` +
   `outbox.Relay` + `kafka.Publisher` + `kafka.RestoreCoreHeaders`;
   no new generic relay package. `DeadLetterRecorder` is
   auto-discovered from `Store` by `NewRelay` (no explicit wire).
   Knobs (`MaxAttempts`, `PollInterval`, `BatchSize`, `Backoff`,
   `ClaimLease`) stay at defaults; no env exposure this cycle.

9. **`orders-api` does not depend on Redpanda in compose.** The
   binary no longer publishes to Kafka after the refactor (and
   never subscribed). A false dependency would teach the wrong
   topology. (The binary still imports the kafka package for
   ctx-bound header helpers used by the shared codec; that is a
   Go import edge, not a runtime broker connection.)

10. **`pgxrepo.FindByID` maps `pgx.ErrNoRows` to
    `domain.ErrNotFound`.** Mirrors `memrepo` so swapping repo
    implementations does not drift error semantics. Locked by
    `TestShipOrder_NotFound`.

11. **`orders.version` is reserved but unused.** BIGINT column
    matches `Order.Version() int64`. `Save` writes the value but
    does not gate UPDATE on a `WHERE version = $expected` clause;
    behaviour is last-write-wins. Optimistic-locking activation is
    a separate future cycle.

12. **Worker restores Kafka headers + overwrites causation_id
    before dispatching ShipOrderCommand.** Surfaced during the
    Checkpoint B review: the subscriber decodes the envelope but
    does NOT restore Kafka metadata into ctx, so without explicit
    restoration the staged OrderShipped row would carry no
    trace/correlation and distributed tracing would break at the
    worker process boundary. The handler in `workerflow.HandleOrderPlaced`
    calls `kafka.RestoreCoreHeaders(map[string]string(env.Raw.Metadata))`
    and then `kafka.WithCausationID(placed.EventID())` —
    OrderShipped reports the consumed OrderPlaced as its cause,
    while trace and correlation pass through unchanged. Locked by
    `TestWorker_HandleOrderPlaced_PropagatesHeaders`.

13. **`handleOrderPlaced` lives in `examples/orders/workerflow/`,
    not `cmd/internal/`.** Go's `internal/` rule restricts imports
    to siblings of the `internal/` directory; the integration test
    package needs to call this adapter directly, which is not a
    sibling of `cmd/internal/`. The sibling-of-cmd location keeps
    the function testable without further indirection.

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
- Scope boundary: no `go-ddd-core` change, no schema migration (the
  `version` column already existed in `001_create_orders.up.sql`).
  `memrepo` remains example-only and carries a one-line package-doc
  note that it does not enforce optimistic locking; deletion is a
  separate hygiene cycle (zero importers and zero tests post-PR #19).

## v0.5.0 transport/http/stdlib + health (2026-05-25 cycle)

Decisions locked at kickoff (2026-05-25) and confirmed at the
implementation tip (branch `feat/transport-http-stdlib-v0.5.0`,
verification matrix green at `c528261`).

- **Package path**: `transport/http/stdlib`, pkg name `httpstdlib`.
  The `/stdlib` suffix on the import path leaves room for `chi/`,
  `gin/`, `fiber/` siblings without renaming this one. The pkg-name
  alias avoids awkward `stdlib.New(...)` call sites.
- **Primary surface = `New` + `Server.Module`** (not just a
  package-level `Module`). `Server.Start` performs
  `net.Listen("tcp", addr)` synchronously, records the resolved
  listener address, and only then launches `Serve` in a goroutine.
  This shape is what makes `Server.Addr()` observable for `:0`
  ephemeral ports and what makes bind errors surface as a startup
  error rather than a goroutine-only log line.
- **Convenience `Module(addr, handler, opts...)`** is exactly
  `New(addr, handler, opts...).Module()` — no separate code path.
  Provided for call sites that don't need `Addr()`.
- **Handler panic semantics are NOT the adapter's responsibility.**
  `net/http` already recovers per-request panics; the adapter does
  not install request middleware and does not promise a 500 on
  panic. Spec §5.1 was revised mid-spec to lock this; the originally
  planned `TestModule_PanicInHandlerRecovered` test was removed
  before any code landed.
- **Health endpoint semantics (locked)**:
  - `/healthz` always 200, dep-free; runs no Check (a failing
    dependency must NOT cause an orchestrator to restart the pod).
  - `/readyz` runs every Check sequentially in registration order,
    each under `SetProbeTimeout` (default 2s). 200 when every Check
    returns nil; 503 when any fails. Body always lists every Check
    on both paths so operators see partial-failure state.
  - Empty registry → 200 with `"checks":[]` (not null). Encoded by
    omitting `omitempty` on the readiness `checks` field.
  - Only GET; other methods → 405 (provided by Go's method-aware
    `net/http.ServeMux`).
- **Registry guards** — `Register` rejects empty names with
  `health.ErrEmptyCheckName` and duplicate names with a wrapped
  `"duplicate check name %q"` error; failed `Register` does NOT
  mutate state (early returns before the mutation block, and the
  map-write is guarded inside the dup-lookup miss path).
  `MustRegister` panics on `Register` error. Empty-name rejection
  is a spec extension beyond the original draft — added at kickoff
  per the "fail loud at startup" intent.
- **Sequential Check execution** — handler iterates the snapshot
  serially, per-Check `context.WithTimeout`, error in one Check
  does not short-circuit the loop. Test pins this with a shared
  atomic counter (each Check captures its sequence number; values
  must be `0,1,2` in registration order — concurrent execution
  would race).
- **`Handler()` routing — exact path + `http.StripPrefix`** for
  prefix mounting. Suffix routing was floated in an early spec
  draft but rejected as too magical (§5.2 revised mid-cycle).
  Callers that want `/admin/healthz` use
  `http.StripPrefix("/admin", reg.Handler())`.
- **`/healthz` runs no Check** — pinned explicitly by registering a
  failing Check whose fn atomic-counts invocations; counter must
  stay 0 across the liveness call.
- **No shim around core**. The `ports/health.Check` +
  `NewCheck(name, fn)` contract was used verbatim from core
  `main` @ `53fd5b2` (PR #6 merge `dce1154`). If the contract had
  proven insufficient mid-implementation, the cycle would have
  paused and the change pushed to core first. None was needed.
- **`examples/orders/cmd/api` NOT migrated** in this cycle. The
  existing `runtime.HTTPModule` (cmd/internal) keeps working
  unchanged; migrating it to `httpstdlib.Module` is an explicit
  follow-up cycle to avoid bloating the release PR.
- **Dev-time dependency** = core pseudo-version
  `v0.4.1-0.20260525111413-53fd5b2404d4` pinned in both
  `go.mod` and `examples/orders/go.mod` for the duration of the
  cycle. Local `replace` was explicitly rejected (would break CI
  and contaminate the committed module graph).
- **Tag-step gate** (4-step sequence, executable form of core's
  "Tag at the last piece of the cycle, not the first"):
  1. Adapter PR merged on `main`, CI green at the pseudo-version pin.
  2. Core annotates + pushes `v0.5.0`.
  3. Adapter `go.mod` + `examples/orders/go.mod` bumped from the
     pseudo-version to `v0.5.0`; CI green at the bumped tip.
  4. *Then* annotate + push the adapter's `v0.5.0` tag.

Verification at the implementation tip (`c528261`, 2026-05-25):

- Root: `go build / vet / test / test -tags=integration`,
  `golangci-lint v2.5.0` default and `--build-tags=integration` —
  all green.
- `examples/orders`: same matrix plus
  `go test -count=1 -tags=integration -race ./integration/...`
  (Docker-backed, 23.678s) — all green.
- HTTP-only integration test at
  `transport/http/stdlib/integration` exercises the bootstrap.App
  lifecycle with two `httpstdlib.New` servers (main + admin) and
  the toggleable Check; no Docker / testcontainers in this cycle.

## v0.6.0 AuthN — `auth/jwt` static-key verifier (2026-06-05 cycle, Phase A)

First consumer of core's untagged v0.6.0 `ports/auth` contract
(`Identity` / `TokenVerifier` / `ErrTokenMissing|Invalid|Expired`).
Decisions locked across a 13-round plan review; implementation tip
is branch `feat/auth-jwt-verifier-v0.6.0` (local-only — not yet
committed/pushed/PR'd when this entry was written). Plan file:
`~/.claude/plans/go-ddd-core-linear-finch.md`.

- **Library = `github.com/golang-jwt/jwt/v5` (v5.3.1)**. Chosen for
  minimal dependency footprint (root + jwt only) and production-ready
  positioning. Rejected `coreos/go-oidc` (semantics are OIDC ID-Token
  verification, wrong shape for a general bearer verifier) and
  `lestrrat-go/jwx` (heavier deps; mainline v4 wants Go 1.26 +
  `GOEXPERIMENT=jsonv2`, misaligned with our Go 1.25 floor).
- **Static keys only this cycle; JWKS deferred.** roadmap says
  "static keys **or** JWKS" — we ship the static half. No background
  key refresh / rotation, no `Authorizer` (AuthZ), no issuer/session.
- **Package name `authjwt`** (path `auth/jwt`) to avoid colliding
  with golang-jwt's own `jwt` package, mirroring the repo's
  `pgxoutbox`/`pgxdb` convention.
- **One key family per Verifier** — exactly one of
  `WithHMACSecret` / `WithRSAPublicKey` / `WithECDSAPublicKey`. Zero
  or >1 → `New` error. No alg-based multi-key selection (compose
  multiple Verifiers if you need it).
- **Family default + `WithAllowedAlgorithms` narrowing.** Without the
  option, the accepted set is what the key type can *technically*
  verify (HMAC=HS256/384/512, RSA=RS256/384/512, ECDSA=one ES per
  curve). This is a **convenience default, flagged in doc.go as NOT
  the best security posture**; README/examples always pin a single
  alg. The option **intersects** with the family set and **never
  crosses families** — `WithAllowedAlgorithms("HS256")` on an RSA key
  → `New` error (alg-confusion killed at construction, not "verify as
  HMAC"). Fed to `jwt.WithValidMethods([...])`, which is enforced
  before keyfunc runs, so `alg:none` and cross-family confusion are
  rejected pre-signature-check.
- **`exp` mandatory (secure-by-default).** golang-jwt v5 does NOT
  require `exp` by default — a token without it would be a permanent
  credential. We always pass `jwt.WithExpirationRequired()`; missing
  `exp` → `ErrTokenInvalid` (not Expired — the token is non-conformant,
  not stale). No opt-out option this cycle.
- **All validation in `New` → option order is irrelevant.** Options
  are pure setters into `config`; every cross-field check (key-family
  count, alg ∩ family, structural checks) runs after all options
  apply. `New(WithAllowedAlgorithms("RS256"), WithRSAPublicKey(k))`
  ≡ reversed order.
- **Key set exactly once — fail, not last-wins.** Any key option
  called >1 times (same family twice, or mixed families) → `New`
  error. Silent key overwrite is a security risk and almost always a
  config mistake.
- **HMAC secret floor = RFC 7518 §3.2**, checked in `New`: secret
  MUST be ≥ the hash output of the largest accepted HMAC alg. Family
  default (HS256/384/512) → ≥64 bytes; `WithAllowedAlgorithms("HS256")`
  → ≥32; `("HS256","HS384")` → ≥48. Too-short secret is a deploy-time
  misconfig → fails at `New`, not `Verify`.
- **RSA structural checks in `New`**: `N != nil && N.Sign() > 0`;
  **`N.BitLen() >= 2048`** (NIST SP 800-57 — stricter than Go
  crypto/rsa's 1024 floor); **exponent odd and ≥3**. Deep-copied via
  `&rsa.PublicKey{N: new(big.Int).Set(k.N), E: k.E}`.
- **ECDSA structural checks in `New`**: curve must be P-256/384/521
  (→ ES256/384/512, uniquely determining the allowed alg); on-curve
  validity via `(*ecdsa.PublicKey).ECDH()` (the non-deprecated
  equivalent of `elliptic.IsOnCurve`, rejects nil coords / off-curve /
  point-at-infinity). **Deep-copied via x509 `MarshalPKIXPublicKey` →
  `ParsePKIXPublicKey` round-trip** — chosen specifically because Go
  1.25 deprecates the `ecdsa.PublicKey.X`/`.Y` fields (SA1019 under
  golangci-lint staticcheck), so the obvious `new(big.Int).Set(k.X)`
  clone would fail CI lint.
- **Security-gate vs extraction-name empty-option split.**
  Security-gate options provided-but-empty → `New` error
  (`WithIssuer("")`, `WithAudience("")`, empty/blank/zero-arg
  `WithAllowedAlgorithms`) — silently disabling a check is unsafe.
  Extraction-name options empty → silently keep default
  (`WithTenantClaim("")` → tenant stays off; `WithRolesClaim("")` →
  stays `"roles"`), following the repo's existing nil/empty-ignore
  convention. "Not called at all" is the normal way to express "don't
  validate this" and is distinct from "set but empty".
- **`Verify` check order: empty token BEFORE ctx.** `token == ""` →
  `ErrTokenMissing` first (holds core's contract even on a cancelled
  ctx). Then `ctx.Err()` → returns the **raw** `context.Canceled` /
  `DeadlineExceeded`, NOT wrapped in an auth sentinel (cancellation is
  not a token problem).
- **nbf-not-yet-valid → `ErrTokenInvalid`, NOT Expired.** Semantically
  opposite to expiry (retry will eventually succeed). Core has only
  Missing/Invalid/Expired; mapping not-yet-valid to Invalid is the
  honest choice among those three. No new core sentinel this cycle.
- **`sub` strict**: missing / empty / non-string → `ErrTokenInvalid`
  (a verified principal needs a stable string id). `roles` lenient:
  `[]any` keeps string elements in order (skips non-strings), single
  string → one-element slice, anything else → nil. `TenantID` lenient:
  off by default; non-string/empty/missing → `""`.
- **`Identity.Claims` = raw `MapClaims` verbatim** (no normalization —
  that would lose info). core's `Identity.clone()` shallow-copies the
  top-level map only; nested values are read-only by core's own
  contract. Claims are re-parsed per `Verify`, never shared across
  requests, so no cross-request contamination — authjwt adds no extra
  deep-copy. doc.go documents the read-only nested contract.
- **Deterministic time tests** via an unexported `now func() time.Time`
  (default `time.Now`) wired through `jwt.WithTimeFunc`; tests inject a
  fixed clock through `auth/jwt/export_test.go` `SetNow`. The seam is
  test-only, not public API. Concurrency safety (core contract)
  verified with `-race`: N goroutines on one Verifier, mixed
  valid/expired/bad-signature tokens, each result correct, no data race.

Local verification at the implementation tip (2026-06-05, dirty tree):
`go build/vet ./...`, `go test ./auth/...`, `go test -race ./auth/...`
all green; one intentional `//nolint:staticcheck` for the off-curve
construction + key-immutability mutation tests that must read the
deprecated ECDSA coordinate fields. golangci-lint deferred to CI.

v0.6.0 tag-gate satisfied + delivered (2026-06-05): Phase A
(`auth/jwt`) and Phase B (`transport/http/stdlib/authmw`) shipped
together as PR #23 (merge `ae76f78`), which is the first `ports/auth`
consumer that unblocks core's tag. Core cut `v0.6.0` (tag object
`fd596cd` on core merge `86b1e15`, GitHub Release Latest). Adapter
step 4 (`chore/bump-core-v0.6.0`) swaps the core pin pseudo-version
`v0.5.1-0.20260604084748-aec4e2c9bef6` → `v0.6.0` on root +
`examples/orders`; step 5 (adapter `v0.6.0` tag + Release) is the only
remaining gate step. Same two-step finish as v0.5.0.

## v0.7.0 auth/casbin AuthZ adapter (2026-06-05 cycle)

- **Engine: Casbin v3.10.0.** ~4 transitive modules vs OPA/Rego's ~126;
  passes the repo's low-dependency bar. OPA deferred to a future
  `auth/opa` package; the `auth/casbin` driver-named path leaves room.
- **Depend on a one-method `Enforcer` interface, not concrete
  `*casbin.Enforcer`.** Keeps the concurrency choice with the caller
  (core "safe for concurrent use" is conditional on the supplied
  enforcer) and lets unit tests use a fake. Constructor imports the
  concrete Casbin types in ONE place only — the typed-nil guard.
- **Typed-nil guard via explicit type switch on `*casbin.Enforcer` /
  `*casbin.SyncedEnforcer`; no generic reflect.** Covers exactly the
  documented public types.
- **`Option func(*config)` private-config pattern** (mirrors authjwt);
  Authorizer is immutable after New.
- **Error discipline:** only `Enforce → false` becomes `ErrForbidden`;
  malformed input → `ErrInvalidAuthorizationRequest` before the ctx
  check; engine/ctx/builder errors passed through verbatim (a failed
  decision must not be masked as a 403).
- **No shim around core.** The contract was sufficient as merged; no
  adapter-side workaround was needed.
- **Phase A only.** HTTP enforcement middleware (Phase B) and
  `examples/orders` wiring (Phase C) are separate cycles.

Local verification at the implementation tip (2026-06-05): root
`go build/vet ./...`, `go test ./...`, `go test -race ./auth/casbin/...`,
`go test -tags=integration ./auth/casbin/...`, plus `examples/orders`
build/vet/test all green. golangci-lint also run locally and clean
(0 issues on `./...` and `./auth/casbin/...`) via the Homebrew v2.12.2
binary at `/usr/local/bin/golangci-lint`. Correction to the first draft
of this note: the earlier "defer to CI because local lint rejects the
v2 config" was a binary-resolution mistake — the `v1.64.8` at `~/go/bin`
(first on the agent's PATH) can't parse a `version: "2"` config, but the
v2 binary reads it fine. CI's `golangci-lint (.)` still independently
caught a goimports `local-prefixes` grouping miss in the test files
(fixed `a7ca011`); see review-log.
