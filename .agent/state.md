# go-ddd-adapters State

Last verified: 2026-05-25 Asia/Taipei (on `main` after PR #21 merge — bookkeeping pass only, no CI re-run)
Source: `git log main --oneline -10` shows merge commit `d9c7324`
(`Merge pull request #21 from slam0504/feat/transport-http-stdlib-v0.5.0`).
PR #21's CI was 5/5 green at merge tip (build+test root 1m7s +
`examples/orders` 1m20s, golangci-lint root 48s + `examples/orders`
48s, integration testcontainers 1m26s — workflow run
26404116334). `git status --short` shows only the expected
local-only set after this commit lands
(`?? .agent/context-checkpoint.log`, `?? .agent/session-checkpoint.md`,
`?? .serena/`).

## Current Branch

- working tree: on `main` (this bookkeeping commit). `.serena/`,
  `.agent/context-checkpoint.log`, `.agent/session-checkpoint.md`
  remain untracked / local-only.
- main: `d9c7324` (HEAD as of 2026-05-25, post PR #21 merge —
  v0.5.0 `transport/http/stdlib` + health adapter implementation).
- previous main: `1dbbfdb` (post PR #20 merge,
  `orders.version` optimistic-locking activation).
- previous main: `e6b1672` (post PR #19 merge, `examples/orders`
  pgx outbox demo).
- previous main: `b9696f6` (post PR #18 merge, `go-ddd-core` bump to v0.4.0)
- tags present in repo: `v0.1.0`, `v0.2.0`, `v0.3.0`, **`v0.4.0`**
  (annotated, pushed 2026-05-20; GitHub Release marked Latest at
  https://github.com/slam0504/go-ddd-adapters/releases/tag/v0.4.0).
  `v0.4.0` points at merge commit `bc9b041`; `v0.3.0` points at
  `3ce2a23`. PR #21 ships **no** tag yet — the adapter `v0.5.0`
  tag is gated on the 4-step tag-gate (see Open Items below).
- latest commits on `main`:
  - `d9c7324` `Merge pull request #21 from slam0504/feat/transport-http-stdlib-v0.5.0`
  - `ee0d6cf` `docs(decisions): record v0.5.0 transport/http/stdlib + health cycle`
  - `c528261` `test(transport/http/stdlib): satisfy lint in HTTP tests`
  - `96e7fb7` `test(transport/http/stdlib): HTTP-only integration test for main+admin lifecycle`
  - `c0ba5f7` `docs(transport/http/stdlib/health): package doc with endpoint semantics`
  - `dfd6516` `docs(transport/http/stdlib/health): drop misleading 'Go 1.22' from Handler doc`
  - `0609bff` `feat(transport/http/stdlib/health): Handler combines /healthz + /readyz`
  - `2f01fb9` `test(transport/http/stdlib/health): cover per-Check ProbeTimeout`
  - `6ae65d4` `feat(transport/http/stdlib/health): ReadinessHandler with stable, sequential checks`
  - `58440fc` `feat(transport/http/stdlib/health): LivenessHandler always 200, no Check invocation`

## Current Status

- **v0.5.0 `transport/http/stdlib` + health adapter implementation
  shipped** (PR #21 merged 2026-05-25 at merge commit `d9c7324`).
  CI 5/5 green at merge tip: build+test root 1m7s, build+test
  `examples/orders` 1m20s, golangci-lint root 48s + `examples/orders`
  48s, integration testcontainers 1m26s (workflow run 26404116334).
  New packages `transport/http/stdlib` (server adapter with
  synchronous bind, `Server.Addr()` for `:0`, graceful shutdown
  with `WithShutdownTimeout`; package-level `Module(...)` is
  exactly `New(...).Module()`) and `transport/http/stdlib/health`
  (Registry + `LivenessHandler` always 200 with no Check execution
  + `ReadinessHandler` sequential with per-Check ProbeTimeout
  default 2s + `Handler()` exact-path mux with
  `http.StripPrefix("/admin", ...)` for prefix mounts). Core dep
  pinned to pseudo-version
  `v0.4.1-0.20260525111413-53fd5b2404d4` (core `main` @ `53fd5b2`,
  PR #6 contract) — to be swapped to `v0.5.0` once core tags
  (see Open Items below). Spec at
  `docs/superpowers/specs/2026-05-25-v0.5.0-transport-http-stdlib.md`;
  plan at
  `docs/superpowers/plans/2026-05-25-v0.5.0-transport-http-stdlib.md`;
  cycle decisions in `.agent/decisions.md` under
  "v0.5.0 transport/http/stdlib + health (2026-05-25 cycle)". PR
  #21 ships **no tag** — the adapter `v0.5.0` annotated tag is
  the last step of the joint cycle and is blocked on core.
- **`orders.version` optimistic-locking cycle is CLOSED** (PR #20
  merged 2026-05-25 19:24 +0800 at merge commit `1dbbfdb`).
  `pgxrepo.Save` now uses a SQL-encoded `EXCLUDED.version - 1`
  guard on the ON CONFLICT UPDATE branch; `RowsAffected()==0`
  surfaces as wrapped `domain.ErrConcurrencyConflict`, which the
  existing UoW tx rolls back together with the staged outbox row.
  `cmd/api` gains a partial `ErrConcurrencyConflict → 409`
  mapping (full HTTP error taxonomy is the new follow-up cycle).
  `memrepo` carries a one-line package-doc note that it does not
  enforce optimistic locking. 2 new `//go:build integration`
  tests in `examples/orders/integration/optimistic_lock_test.go`
  (`TestSave_OptimisticLock_ConcurrentUpdate`,
  `TestPlaceOrder_DuplicateID_Conflict`); the pre-existing 8
  stayed green. PR #20 ships **no** tag — example-only. Design
  spec at
  `docs/superpowers/specs/2026-05-23-orders-version-optimistic-locking-design.md`;
  plan at
  `docs/superpowers/plans/2026-05-23-orders-version-optimistic-locking.md`;
  cycle decisions captured in `.agent/decisions.md` under
  "orders.version optimistic locking activation (2026-05-23 cycle)".
- **`examples/orders` pgx outbox demo cycle is CLOSED** (PR #19
  merged 2026-05-22 16:31 +0800 at merge commit `e6b1672`). Closed
  the README's "No outbox" and "Per-process in-memory repos"
  shortcuts. `cmd/api` + `cmd/worker` share Postgres for the write
  model; transactional `Save` + `outbox.Stage` via
  `application.UnitOfWork` bridged from `pgxdb.TxManager`. New
  `cmd/relay` binary drains outbox to Kafka under SKIP LOCKED.
  docker-compose gains Postgres + two `migrate/migrate:v4.19.1`
  init services (`outbox-migrate` + `orders-migrate` with
  per-source `x-migrations-table`). 8 integration tests under
  `//go:build integration` in `examples/orders/integration/` (8/8
  pass — see Pre-push verification block at bottom). Worker
  restores Kafka headers + sets causation_id on Ship dispatch
  (commit `54e0691`, caught during Checkpoint B review). Reader
  projection / durable inbox / exactly-once remain intentional
  shortcuts (now tracked as their own future cycles in Open
  Items). PR #19 ships **no** tag — example-only. Design spec at
  `docs/superpowers/specs/2026-05-21-examples-orders-pgx-outbox-design.md`;
  plan at
  `docs/superpowers/plans/2026-05-21-examples-orders-pgx-outbox.md`.
- v0.3.0 dependency-bump cycle is **CLOSED**. Both root `go.mod` and
  `examples/orders/go.mod` require `github.com/slam0504/go-ddd-core v0.3.0`.
- Doc / agent-memory alignment cycle is **CLOSED** (PR #6 merged
  2026-05-18 morning).
- v0.4.0 inbox-memory relocation cycle is **CLOSED** (PR #7 merged
  2026-05-18 afternoon; bookkeeping PR #8 2026-05-18 evening).
- **v0.4.0 outbox-adapter cycle is CLOSED** (PR #9 merged
  2026-05-19 afternoon, merge commit `6abeafd`). The new package
  `eventbus/outbox` (flat package `outbox`) hosts the in-process
  `Memory` store + driver-agnostic polling `Relay` +
  `RelayModule` bootstrap helper; plus `kafka.RestoreCoreHeaders`
  for the well-known-headers propagation pattern. **Memory is
  explicitly non-transactional** — labelled in package doc, README,
  CHANGELOG, and `.agent/decisions.md` "Outbox Adapter Package" —
  production users must wait for the pgx successor. All 23 design
  decisions recorded in decisions.md.
- **`v0.3.0` release cycle is CLOSED** (PR #11 merged 2026-05-19
  evening, tag `v0.3.0` annotated at `3ce2a23`, GitHub Release
  published). First tagged release on the v0.3.x line; aligned
  adapters with `go-ddd-core v0.3.0`. Downstream services can pin
  via `go get github.com/slam0504/go-ddd-adapters@v0.3.0`.
- **Core v0.4.0 shipped 2026-05-21** at core `main` head `aadde89`
  (merge of core PR #4); annotated tag `v0.4.0` pushed to origin,
  GitHub Release Latest at
  https://github.com/slam0504/go-ddd-core/releases/tag/v0.4.0.
  Core's v0.4.0 physically removed `eventbus/inbox/memory.go` +
  `memory_test.go` (breaking; `Inbox` interface in parent
  `eventbus` package preserved). Adapters' `go.mod` (root +
  `examples/orders`) bumped `go-ddd-core` v0.3.0 → v0.4.0 on this
  branch; non-breaking for adapters' import graph since no `.go`
  file references the removed sub-package. Both inbox-memory
  removal gating conditions now resolved AND delivered.
- **`v0.4.0` release cycle is CLOSED** (PR #16 merged 2026-05-20,
  tag `v0.4.0` annotated at `bc9b041`, GitHub Release published as
  Latest at https://github.com/slam0504/go-ddd-adapters/releases/tag/v0.4.0).
  Ships pgx-Postgres Outbox + pgx TxManager (PR #14). Closes all
  five memory-adapter limitations recorded in `.agent/decisions.md`
  (real tx Stage, durable last_error, multi-Relay safety, separate
  DLQ table, claim_token UUID stale-worker guard). Downstream
  services can now pin via `go get
  github.com/slam0504/go-ddd-adapters@v0.4.0`. Go floor bumped
  1.24 → 1.25 on the adapter root; tagged v0.3.x and v0.2.x are
  unaffected. The `[Unreleased]` CHANGELOG section is now the
  accumulator for the next cycle.
- **v0.4.0 pgx-Postgres outbox cycle is CLOSED** (PR #14 merged
  2026-05-20 at merge commit `2e9e96d`). New packages
  `eventbus/outbox/pgx` (transactional Outbox + OutboxStore + DLQ
  via `claim_token` UUID + `FOR UPDATE SKIP LOCKED` + atomic CTE
  Terminate) and `ports/database/pgx` (TxManager + ctx helpers +
  Executor) closed the five memory-adapter limitations recorded in
  `.agent/decisions.md`. Plan locked at
  `~/.claude/plans/outbox-relay-agile-orbit.md` revision 6 (commit
  8 pulled forward to land CI coverage before Store impl). Three
  Codex review findings closed in-band: `c028ef3` (CI coverage
  gap), `fde15ce` (`UPDATE ... RETURNING` row order via outer
  SELECT), `d1db48c` (SKIP LOCKED wording "partition" → "claim
  disjoint rows"). CI 5/5 green first try at merge tip including
  the first ever pgx testcontainers run against real Postgres
  (1m10s). **Go floor bumped 1.24 → 1.25** on the adapter root
  module (required by pgx/v5 v5.9.2 + testcontainers v0.42.0 +
  golang-migrate v4.19.1 + current OTel releases); CI runner and
  `examples/orders/Dockerfile` followed.
- Kafka and OTel bootstrap module helpers are on `main`.
- `ConsumerGroup` is a single `bootstrap.ModuleFunc` with shared context
  and WaitGroup, so Stop cancels all topic consumers atomically.

## Adapter Surface (current)

| Adapter | Port | Notes |
| --- | --- | --- |
| `eventbus/kafka` | `eventbus.Publisher` / `Subscriber` / `Codec` | watermill-kafka v3 (Sarama); `RestoreCoreHeaders` outbox bridge |
| `eventbus/inbox` | `eventbus.Inbox` | in-process `Memory`; `WithMaxSize` + `WithTTL` |
| `eventbus/outbox` | `eventbus.Outbox` / `OutboxStore` / `Relay` | in-process `Memory` + polling `Relay` — **non-transactional test/dev only**; DLQ via local `DeadLetterRecorder`; `HeaderRestorer` callback |
| `eventbus/outbox/pgx` | `eventbus.Outbox` / `OutboxStore` / `outbox.DeadLetterRecorder` | pgx/v5 + Postgres 12+; lease-based claim with `FOR UPDATE SKIP LOCKED`, `claim_token` UUID guard, separate `outbox_dead_letters` table, safe for multiple Relay instances |
| `ports/database/pgx` | `database.TxManager` | pgx/v5 pool + ctx-bound transaction handle (`pgxdb.WithTx` / `pgxdb.TxFromContext` / `pgxdb.Executor`) |
| `logger/slogger` | `logger.Logger` | stdlib `log/slog` |
| `observability/otel` | `observability.Provider` | OTel SDK v1.32 |

## Open Items

- **v0.5.0 release cycle — tag gate PENDING** (adapter
  implementation merged at `d9c7324`, but the joint release is
  blocked on go-ddd-core tagging `v0.5.0`). Step 1 of the 4-step
  gate is done; steps 2-4 remain:
  1. ✅ Adapter PR #21 merged on `main` (`d9c7324`), CI 5/5 green
     at the pseudo-version pin
     (`v0.4.1-0.20260525111413-53fd5b2404d4`).
  2. ⏳ **go-ddd-core annotates + pushes `v0.5.0`** on its `main`
     (core agent's responsibility; core `main` @ `53fd5b2` already
     carries the `ports/health.Check` contract from core PR #6).
  3. ⏳ Adapter `go.mod` + `examples/orders/go.mod` bumped from
     the pseudo-version to `v0.5.0`; CI green at the bumped tip
     (small follow-up PR).
  4. ⏳ Adapter annotates + pushes `v0.5.0` (annotated tag, then
     GitHub Release Latest, mirroring the v0.4.0 pattern).
  Spec, plan, and cycle decisions remain authoritative at
  `docs/superpowers/specs/2026-05-25-v0.5.0-transport-http-stdlib.md`,
  `docs/superpowers/plans/2026-05-25-v0.5.0-transport-http-stdlib.md`,
  and `.agent/decisions.md` "v0.5.0 transport/http/stdlib + health
  (2026-05-25 cycle)". Hard rule preserved: if core's contract
  proves insufficient before tagging, the change is pushed to core
  first — no adapter-side shim.

- ~~**v0.4.0 core-side removal**~~ ✅ resolved + delivered
  2026-05-21. Both gating conditions satisfied (Gate (i) adapters'
  `v0.4.0` tag at `bc9b041` on 2026-05-20; Gate (ii) downstream
  consumer inventory formally closed as **empty** under Option 2
  on 2026-05-21, verified via workspace grep of
  `go-ddd-core/eventbus/inbox` returning zero Go `import`
  statements across `/Users/eason_tseng/playground/project`).
  Core PR #4 (`release/v0.4.0-prep` → `main`) merged 2026-05-21
  at `aadde89`; physically removed `eventbus/inbox/memory.go` +
  `memory_test.go`; core `v0.4.0` tag pushed + GitHub Release
  Latest. Adapters' dependency bumped on this branch
  (`chore/bump-core-v0.4.0`).
- ~~**`examples/orders` pgx outbox demo cycle**~~ ✅ resolved
  2026-05-22 (PR #19 merged at `e6b1672`). Remaining shortcuts
  from the demo and scoped-out follow-ups from the v0.4.0 pgx
  outbox adapter cycle are tracked once in "Pending follow-up
  cycles" below to avoid drift.
- ~~`examples/orders` outbox wiring~~ ✅ resolved by the
  `feat/examples-orders-pgx-outbox` branch (above). Closes both the
  "no outbox" shortcut and the "per-process in-memory repos"
  shortcut documented in the example README. Reader projection
  stays in-memory by design (separate follow-up).
- **Pending follow-up cycles** (each independent, none gating PR
  #19 or the v0.4.0 adapter release):
  - ~~`orders.version` optimistic-locking activation~~ ✅ resolved
    by PR #20 at merge commit `1dbbfdb` (2026-05-25). `pgxrepo.Save`
    enforces the prior-version guard via SQL; conflict surfaces as
    `domain.ErrConcurrencyConflict`. See `.agent/decisions.md`
    "orders.version optimistic locking activation (2026-05-23 cycle)".
  - **NEW follow-up — HTTP error mapping polish**: extend `cmd/api`
    to a full transport-error taxonomy (not-found → 404, rule
    violation → 422, etc.). Today only `ErrConcurrencyConflict → 409`
    is wired (partial mapping by design; flagged in `cmd/api/main.go`
    with a `TODO` comment).
  - pgx projection for `cmd/reader` — reader still uses
    `infra/memrepo` in-memory store.
  - `eventbus/inbox/pgx` adapter + durable inbox / exactly-once
    consumer semantics — only in-process `Memory` inbox exists
    today, and the demo README documents durable inbox as a
    shortcut. Design these together because the Inbox contract's
    duplicate-handling semantics shape what "exactly-once" can
    promise.
  - LISTEN/NOTIFY push-based relay variant — polling optimisation;
    defer until durable/correctness items above are stable.
  - `claim_id` worker attribution — operational visibility, not
    correctness; current lease only answers "is this row claimed?"
    via wall-clock `claimed_until`, not which worker holds it.
- ~~adapters tag~~ ✅ done 2026-05-19 (`v0.3.0` at `3ce2a23`) +
  2026-05-20 (`v0.4.0` at `bc9b041`, marked Latest at
  https://github.com/slam0504/go-ddd-adapters/releases/tag/v0.4.0).

## Verification

Last green CI run (PR #11, 2026-05-19 — release prep):

- `go test ./...` PASS (root + `examples/orders`).
- `go test -race ./eventbus/outbox/...` PASS locally before push.
- `golangci-lint run ./...` 0 issues.
- `integration (testcontainers)` PASS.
- Tag `v0.3.0` cut at `3ce2a23` (same tree as PR #11 merge); no
  separate CI on the tag itself, but the underlying commit is the
  green-CI artifact of PR #11.
- Tag `v0.4.0` cut at `bc9b041` (same tree as PR #16 merge); no
  separate CI on the tag itself. PR #16 itself is a prose-only
  release-prep diff (CHANGELOG + README) on top of `55c3bea`
  which already carried the v0.4.0 feature surface (PR #14 + PR
  #15); CI on PR #16 green at merge.

PR #14 CI verdict at merge tip (`2e9e96d`, 2026-05-20):

- 5/5 checks green first try, including the first ever pgx
  testcontainers run against real Postgres (1m10s).
- `integration-test` job's new root-module step
  (`go test -tags=integration -race ./ports/database/pgx/...
  ./eventbus/outbox/pgx/...`, added in `c028ef3`) PASS.
- `examples/orders` integration leg PASS.
- `golangci-lint run ./...` and `golangci-lint run
  --build-tags=integration ./...` 0 issues.

Default verification before any release-related work:

```sh
go test ./...
go test -race ./eventbus/outbox/...
cd examples/orders && go test ./...
golangci-lint run ./...
```

Pre-push verification on `chore/bump-core-v0.4.0` (2026-05-21):

- `go build ./...` clean (root + `examples/orders`).
- `go vet ./...` clean (root + `examples/orders`).
- `go test ./...` PASS (root: 5 cached + 3 fresh; eventbus/outbox
  3.5s; eventbus/kafka 1.0s; `examples/orders` no test files).
- `go test -race ./eventbus/outbox/...` PASS (3.7s).
- `golangci-lint run ./...` 0 issues (root + `examples/orders`).
- `golangci-lint run --build-tags=integration ./...` 0 issues.
- Container-driven integration tests (pgx outbox testcontainer)
  deferred to CI per cycle convention.

Pre-push verification on `feat/examples-orders-pgx-outbox` (2026-05-22):

- `go build ./...` clean (root + `examples/orders`).
- `go vet ./...` clean.
- `go test -tags=integration -race ./integration/...` PASS in
  `examples/orders`: 8/8 — `TestPartitionByAggregate_PreservesOrder`,
  `TestPlaceOrder_TransactionalOutbox`, `TestPlaceOrder_TxRollback`,
  `TestRoundTrip_AllHeaders`, `TestShipOrder_TransactionalOutbox`,
  `TestShipOrder_TxRollback`, `TestWorker_HandleOrderPlaced_PropagatesHeaders`,
  `TestShipOrder_NotFound`.
- `golangci-lint run --build-tags=integration ./...` 0 issues
  (root + `examples/orders`).
- `docker compose up --build -d` smoke-tested end-to-end:
  POST `/orders` returns 201 with `TotalCents=998` (json
  "price_cents" tag fix proves out); GET on reader at `:8081`
  shows `status="shipped"`, `carrier="demo-carrier"`, `version=2`
  after worker → outbox → relay → kafka → reader drain. Both
  migrate services exited 0; postgres state tables show
  `outbox_schema_migrations.version=2` and
  `orders_schema_migrations.version=1` (independent per-source
  tracking verified).
