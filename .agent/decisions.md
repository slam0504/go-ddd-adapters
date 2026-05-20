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
