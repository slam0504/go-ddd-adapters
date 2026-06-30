# go-ddd-adapters State

Last verified: 2026-06-24 Asia/Taipei (on `main` `040228b` — v0.9.0
bookkeeping reconciliation: CHANGELOG cut to `[v0.9.0]`, README
Status/compat-matrix updated, this file + cross-repo
`.agent-memory/go-ddd.md` caught up from v0.8.0 to v0.9.0).
Source: `git log main --oneline` head is `040228b`
(`Merge pull request #30 from slam0504/chore/bump-core-v0.9.0`). Adapter
`v0.9.0` annotated at `040228b`, pushed; GitHub Release marked Latest at
https://github.com/slam0504/go-ddd-adapters/releases/tag/v0.9.0. Core
`v0.9.0` tag verified present (`8ef2fbe`, local + origin). This pass
touched docs / agent-memory only — no `.go` changed, so no `go build/test`
was run. `git status --short` currently shows the pending bookkeeping
edits (`M CHANGELOG.md`, `M README.md`, `M .agent/state.md`) not yet
committed, alongside `?? .serena/`.

## v0.6.0 AuthN cycle (CLOSED — shipped + tagged 2026-06-05)

Updated: 2026-06-05 Asia/Taipei. All five cross-repo tag-gate steps
done. Adapter `v0.6.0` annotated tag (tag object `a9d4bfb`) at merge
commit `1b0f3ae`, pushed; GitHub Release published as Latest (not
draft / not prerelease) at
https://github.com/slam0504/go-ddd-adapters/releases/tag/v0.6.0.
Downstream services can now pin via
`go get github.com/slam0504/go-ddd-adapters@v0.6.0`.

- **Phase A (`auth/jwt` / `authjwt`) + Phase B
  (`transport/http/stdlib/authmw`) both SHIPPED** via a single combined
  PR #23 (merge commit `ae76f78` on `main`). Branch
  `feat/auth-jwt-verifier-v0.6.0` deleted (local + remote). `authjwt`
  is the static-key JWT verifier (one key family per `Verifier`,
  algorithm-locked via `jwt.WithValidMethods`, secure-by-default);
  `authmw` is the net/http bearer middleware
  (`New(verifier, opts...) (Middleware, error)`, two-layer nil guard →
  `ErrNilVerifier`, strict single-header bearer extraction, sanitized
  responder + RFC 6750 `WWW-Authenticate`). An integration test wires
  authjwt + authmw end-to-end. CI green on PR #23 incl.
  `golangci-lint (.)` / `(examples/orders)` + `integration
  (testcontainers)`.
- **CHANGELOG doc-debt RESOLVED** (commit `3cdfff5`, direct to `main`
  by user choice): reconstructed the missing `[v0.5.0] - 2026-05-26`
  release section, moved the misplaced core-bump / `examples/orders`
  Changed blocks out of `[Unreleased]`, fixed compare links. The debt
  previously flagged in this section is now cleared.
- **Core `v0.6.0` TAGGED 2026-06-05** (core agent, separate repo):
  annotated tag (tag object `fd596cd`) landing on core merge `86b1e15`,
  GitHub Release marked Latest, core `main` head `f51aa46`. The
  cross-repo tag-gate was satisfied by adapters PR #23 (`ae76f78`)
  shipping the first `ports/auth` consumer.
- **Step 4 — dep-bump + bookkeeping PR #24** (merge commit `1b0f3ae`
  on `main`, CI 5/5 green, branch `chore/bump-core-v0.6.0` deleted):
  bumped the core pin pseudo-version
  `v0.5.1-0.20260604084748-aec4e2c9bef6` → `v0.6.0` on both root and
  `examples/orders` go.mod (transitive MVS). No adapter code changes.
  Local verification green before push: root `go build/vet ./...`,
  `go test ./...`, `go test -race ./auth/... ./eventbus/outbox/...`,
  `go test -tags=integration ./transport/http/stdlib/integration/...`,
  plus `examples/orders` build/vet/test. golangci-lint via CI (local
  binary go1.24 < module target 1.25.0). Bookkeeping rode in PR #24:
  CHANGELOG `[Unreleased]` → `[v0.6.0] - 2026-06-05` + narrative +
  v0.6.0 compare link; README Status (v0.6.0 now latest, authmw
  described) + adapter table (`authmw` row) + compat matrix (`v0.6.0`
  released); `.agent/decisions.md` tag-gate-satisfied marker.
- **Step 5 — adapter tag + Release DONE:** annotated `v0.6.0` (tag
  object `a9d4bfb`) cut at `1b0f3ae`, pushed to origin; GitHub Release
  published as Latest (verified not draft / not prerelease;
  `releases/latest` API returns `v0.6.0`), notes from CHANGELOG
  `[v0.6.0]`. Cross-repo `.agent-memory/go-ddd.md` synced.
  Plan at `/Users/eason_tseng/.claude/plans/go-ddd-core-linear-finch.md`.

## v0.7.0 AuthZ cycle (CLOSED — shipped 2026-06-05)

First consumer of core's `ports/auth.Authorizer` contract. Phase A only.
Released as adapter `v0.7.0` (annotated tag object `e304ec7` → merge
commit `a03cc12`), GitHub Release marked Latest.

- Branch: `feat/authz-casbin-v0.7.0` (off `main`).
- Spec: `docs/superpowers/specs/2026-06-05-authz-casbin-adapter-design.md`.
- Plan: `docs/superpowers/plans/2026-06-05-authz-casbin-adapter.md`.
- Scope: `auth/casbin` (`casbinauth`) adapter wrapping a caller-built
  Casbin enforcer behind a one-method `Enforcer` interface; default
  `(sub, obj, act)` builder with `Type:ID` object encoding; core auth
  sentinel mapping (`ErrForbidden` / `ErrInvalidAuthorizationRequest`);
  engine/ctx/builder errors passed through un-disguised.
- Dependency: added `github.com/casbin/casbin/v3 v3.10.0`; bumped core
  pin `v0.6.0` → pseudo-version of `47e02fa` on root + `examples/orders`.
- OUT of scope this cycle: HTTP enforcement middleware (Phase B) and
  `examples/orders` AuthZ wiring (Phase C).
- Status: Phase A MERGED to `main` via PR #25 (merge commit `9a715e1`,
  2026-06-05), CI 5/5 green at the pseudo-version pin.
- Tag-gate (spec §10) progress:
  1. ✅ Adapter impl PR #25 merged, CI green at pseudo-version pin.
  2. ✅ Core `v0.7.0` tagged (annotated tag object `c4a4dc1` → commit
     `3729add`), pushed; GitHub Release Latest. Verified resolvable via
     proxy (`go list -m go-ddd-core@v0.7.0` → `3729add`).
  3. ✅ Adapter dep-bump PR #26 (`chore/bump-core-v0.7.0`) merged (merge
     commit `a03cc12`, CI 5/5 green at the bumped tip): core pin
     pseudo-version `v0.6.1-0.20260605060735-47e02fa632a8` → `v0.7.0` on
     root + `examples/orders` go.mod (+ `go mod tidy`); no adapter code
     change. Bookkeeping (CHANGELOG `[Unreleased]` → `[v0.7.0]`, README
     status + compat matrix) rode in this PR.
  4. ✅ Adapter `v0.7.0` annotated (tag object `e304ec7` → `a03cc12`),
     pushed origin; GitHub Release marked Latest
     (`releases/latest` → `v0.7.0`).

## v0.8.0 idempotency-redis cycle (CLOSED — shipped + tagged 2026-06-09)

First consumer of core's new `ports/idempotency.Store` contract (core
PR #17, commit `0e1292d`, deliberately left UNTAGGED pending a real
adapter consumer — this adapter closed that tag-gate and drove core to
`v0.8.0`). Phase scope: the Redis-backed `Store` adapter only. Released
as adapter `v0.8.0` (annotated tag at merge commit `fbd6f65`), GitHub
Release marked Latest (`releases/latest` → `v0.8.0`). Downstream services
can pin via `go get github.com/slam0504/go-ddd-adapters@v0.8.0`.

- Branch: `feat/idempotency-redis-v0.8.0` (off `main` `2b9ffde`), deleted.
- Spec / plan: `/Users/eason_tseng/.claude/plans/recursive-painting-glade.md`.
  New package `idempotency/redis` (`redisidempotency`): a `redis.Scripter`-
  backed `Store` (accepts `*redis.Client` / `*redis.ClusterClient` /
  `*redis.Ring`) whose `Begin`/`Finish`/`Cancel` each run one single-key
  atomic Lua script; in-progress `PEXPIRE` lease IS the reclaim/liveness
  mechanism; lease token minted in Go (`crypto/rand`); completed records
  carry a separate non-sliding retention TTL. `compositeKey`
  length-prefixes BOTH `keyPrefix` and `scope` (prefix-free encoding —
  guards tuple-flatten AND cross-Store namespace collisions). Primary
  tests ARE core's `RunStoreContract` + `RunReclaimContract` against real
  Redis via testcontainers; plus adapter-specific reclaim + retention
  (expiry / non-sliding) tests.
- Dependency: added `redis/go-redis/v9 v9.20.0` +
  `testcontainers-go/modules/redis v0.42.0` (matching the repo's existing
  `testcontainers-go v0.42.0`). Core pin promoted pseudo-version
  `v0.7.1-0.20260608093712-0e1292d20462` → `v0.8.0` on root +
  `examples/orders` at the dep-bump. Redis floor is **4.0+** (multi-field
  `HSET`); cluster/ring support is an API-derived claim (single-key per
  script), NOT a live multi-node CI test.
- OUT of scope this cycle: any in-memory `Store` adapter; HTTP
  enforcement middleware / key+fingerprint extraction / scope builders /
  response capture-replay; `examples/orders` idempotency wiring (deferred,
  mirroring the AuthZ "adapter first, middleware later" split).
- Tag-gate (spec §10) progress:
  1. ✅ Adapter impl PR #27 merged (merge commit `5248bd5`, 2026-06-09),
     CI 5/5 green at the core pseudo-version pin. Three review findings
     closed in-band before merge: P1 `compositeKey` cross-Store namespace
     collision (now length-prefixes `keyPrefix` too, `d3b5f2f`); P2
     `StatusMismatch` doc 422 → HTTP 409 per core contract (`489783c`);
     P2 premature v0.8.0 release framing reverted to the impl-PR
     convention (`489783c`).
  2. ✅ Core `v0.8.0` tagged (core agent: tag object `202d437` → merge
     commit `b0a0e74`; release-prep PR #19 + bookkeeping PR #20; core
     `main` head `14c7165`), GitHub Release Latest. Publishes
     `ports/idempotency`. go.sum `/go.mod` hash byte-identical
     pseudo → `v0.8.0` (released from the same contract commit content).
  3. ✅ Adapter dep-bump PR #28 (`chore/bump-core-v0.8.0`) merged (merge
     commit `fbd6f65`, CI 5/5 green): core pin pseudo → `v0.8.0` on root +
     `examples/orders` (+ `go mod tidy`, no extra drift); no adapter code
     change. Bookkeeping rode in this PR (CHANGELOG `[Unreleased]` →
     `[v0.8.0] - 2026-06-09` + narrative + compare links; README v0.8.0
     Status paragraph + compat matrix row).
  4. ✅ Adapter `v0.8.0` annotated (tag at `fbd6f65`), pushed origin;
     GitHub Release marked Latest (`releases/latest` → `v0.8.0`).

## v0.9.0 jobs-asynq cycle (CLOSED — shipped + tagged 2026-06-16)

First consumer of core's `ports/jobs` contract (core left the contract
UNTAGGED pending a real adapter consumer — this adapter closed that
tag-gate and drove core to `v0.9.0`). Phase scope: the asynq-backed
`Enqueuer` + `Worker` only. Released as adapter `v0.9.0` (annotated tag at
merge commit `040228b`), GitHub Release marked Latest
(`releases/latest` → `v0.9.0`, named "v0.9.0 — jobs/asynq adapter").
Downstream pins via `go get github.com/slam0504/go-ddd-adapters@v0.9.0`.

- Branch: `feat/jobs-asynq-v0.9.0` (off `main`), merged via PR #29.
- New package `jobs/asynq` (`jobsasynq`): a Redis-backed `Enqueuer` +
  `Worker` over `github.com/hibiken/asynq v0.24.1`. Exact-type-match
  dispatch (NOT `asynq.ServeMux`), two-class Enqueue error mapping, a
  30-day default scheduling horizon (out-of-horizon `ProcessAt` rejected
  at Enqueue), 1h default completed-task retention, at-least-once delivery
  with retry→archive dead-lettering. Functional options `WithQueue` /
  `WithSchedulingHorizon` / `WithRetention` / `WithMaxRetry` /
  `WithRetryDelay` / `WithTaskTimeout` / `WithConcurrency` /
  `WithShutdownTimeout` / `WithLogger`. Redis floor 4.0+.
- Tests: core's `jobstest.RunContract` plus the full (0)+(a)–(v) tag-gate
  acceptance suite under `-race` + testcontainers Redis; CI runs the
  `jobs/asynq` integration suite as its own step.
- Dependency: added `github.com/hibiken/asynq v0.24.1`; core pin promoted
  pseudo-version → `v0.9.0` on root + `examples/orders` at the dep-bump.
- Tag-gate progress:
  1. ✅ Adapter impl PR #29 merged (merge commit `1f8a685`, 2026-06-16),
     CI green at the core pseudo-version pin. enqueuer.go panic-recover
     added; the `(t)` accepted-but-ack-lost shutdown test was made
     deterministic via a go-redis hook (write confirmed before injecting
     `DeadlineExceeded`, replacing the flaky 1ns-deadline approach).
  2. ✅ Core `v0.9.0` tagged (core agent), publishing `ports/jobs`;
     resolvable via proxy (PR #30's `go mod` bump succeeded against it).
  3. ✅ Adapter dep-bump PR #30 (`chore/bump-core-v0.9.0`) merged (merge
     commit `040228b`): core pin pseudo → `v0.9.0` on root +
     `examples/orders`; no adapter code change.
  4. ✅ Adapter `v0.9.0` annotated (tag at `040228b`), pushed origin;
     GitHub Release marked Latest (`releases/latest` → `v0.9.0`).
- OUT of scope this cycle: cron / recurring scheduling (deliberately
  excluded at the core contract — no consumer evidence yet for schedule
  identity / overlap / missed-run semantics). `examples/orders` adds no
  jobs demo this cycle.

## Current Branch

- working tree: on `main` (this bookkeeping commit). `.serena/`,
  `.agent/context-checkpoint.log`, `.agent/session-checkpoint.md`
  remain untracked / local-only.
- main: `040228b` (HEAD as of 2026-06-16, post PR #30 merge —
  dep-bump core → `v0.9.0`; adapter `v0.9.0` annotated here, GitHub
  Release Latest).
- previous main: `fbd6f65` (post PR #28 merge — dep-bump core →
  `v0.8.0`; adapter `v0.8.0` tag here).
- previous main: `a03cc12` (post PR #26 merge — dep-bump core →
  `v0.7.0`; adapter `v0.7.0` tag here).
- previous main: `1b0f3ae` (post PR #24 merge — dep-bump core →
  `v0.6.0`; adapter `v0.6.0` tag here).
- previous main: `45274dd` (post PR #22 merge —
  dep-bump core pseudo-version → `v0.5.0`).
- previous main: `d9c7324` (post PR #21 merge, v0.5.0
  `transport/http/stdlib` + health adapter implementation).
- previous main: `1dbbfdb` (post PR #20 merge,
  `orders.version` optimistic-locking activation).
- previous main: `e6b1672` (post PR #19 merge, `examples/orders`
  pgx outbox demo).
- previous main: `b9696f6` (post PR #18 merge, `go-ddd-core` bump to v0.4.0)
- tags present in repo: `v0.1.0`, `v0.2.0`, `v0.3.0`, `v0.4.0`,
  `v0.5.0`, `v0.6.0`, `v0.7.0`, `v0.8.0`, **`v0.9.0`** (latest;
  annotated, pushed 2026-06-16; GitHub Release marked Latest at
  https://github.com/slam0504/go-ddd-adapters/releases/tag/v0.9.0).
  All annotated. Tag → merge-commit targets: `v0.9.0` → `040228b`;
  `v0.8.0` → `fbd6f65`; `v0.7.0` → `a03cc12`; `v0.6.0` → `1b0f3ae`;
  `v0.5.0` → `45274dd`; `v0.4.0` → `bc9b041`; `v0.3.0` → `3ce2a23`;
  `v0.2.0` → `413e069`; `v0.1.0` → `b928be7`.
- latest commits on `main`:
  - `040228b` `Merge pull request #30 from slam0504/chore/bump-core-v0.9.0`
  - `70014c0` `chore(deps): bump go-ddd-core to v0.9.0`
  - `1f8a685` `Merge pull request #29 from slam0504/feat/jobs-asynq-v0.9.0`
  - `7766add` `test(jobs/asynq): make criterion (t) deterministically test accepted-but-ack-lost`
  - `3c40dac` `docs(jobs/asynq): CHANGELOG [Unreleased] entry`
  - `e056970` `docs(jobs/asynq): README adapter row + usage sketch`
  - `3441935` `ci(jobs/asynq): run the integration suite in CI`
  - `e04a248` `test(jobs/asynq): shutdown/recoverability criteria (c,h,j,l,s1,s2,t,f)`
  - `5fb8172` `test(jobs/asynq): delivery criteria (a,b,d,e,n,r)`
  - `86937ef` `test(jobs/asynq): RunContract + validation/dispatch criteria (0,g,m,q,p,o,k,u)`
  - `ac7a5a9` `fix(jobs/asynq): recover MakeRedisClient panic into a constructor error`
  - `2adf521` `test(jobs/asynq): integration harness (container, JobState, hooks, logger)`

## Current Status

- **v0.9.0 `jobs/asynq` background-jobs release cycle is CLOSED**
  (adapter `v0.9.0` annotated at `040228b` on 2026-06-16, GitHub
  Release Latest; first consumer of core `ports/jobs`, drove core to
  `v0.9.0`). Full detail in the "v0.9.0 jobs-asynq cycle" section
  above. The v0.6.0–v0.8.0 cycles are likewise CLOSED (see their
  dedicated sections above).
- **v0.5.0 `transport/http/stdlib` + health release cycle is CLOSED**
  (joint cycle with go-ddd-core; adapter `v0.5.0` annotated at
  `45274dd` on 2026-05-26, tag object `a02f6d4`, GitHub Release
  marked Latest at
  https://github.com/slam0504/go-ddd-adapters/releases/tag/v0.5.0).
  All four tag-gate steps done: (1) PR #21 (impl) merged
  `d9c7324` 2026-05-25 CI 5/5 green workflow 26404116334, under
  the pseudo-version pin `v0.4.1-0.20260525111413-53fd5b2404d4`;
  (2) core annotated `v0.5.0` 2026-05-25 at core commit `e2ee2bb`;
  (3) PR #22 (dep bump) merged `45274dd` 2026-05-26 CI 5/5 green
  workflow 26409070511, swapping the pseudo-version → `v0.5.0`
  on both root and `examples/orders`; (4) adapter `v0.5.0`
  annotated + pushed + GitHub Release Latest. Downstream services
  can now pin via `go get
  github.com/slam0504/go-ddd-adapters@v0.5.0`. New packages
  `transport/http/stdlib` (`New(addr, handler, opts...) *Server`
  primary surface; synchronous bind; `Server.Addr()` for
  `127.0.0.1:0` ephemeral ports; graceful shutdown via
  `WithShutdownTimeout` default 15s, returns
  `context.DeadlineExceeded` on timeout; package-level
  `Module(...)` = `New(...).Module()` wrapper) and
  `transport/http/stdlib/health` (`Registry` zero-value usable,
  empty-name + duplicate-name guards; `LivenessHandler` always
  200 with no Check execution; `ReadinessHandler` sequential per-
  Check with `SetProbeTimeout` default 2s, body always lists
  every Check on both 200 and 503; `Handler()` exact-path mux
  with `http.StripPrefix("/admin", reg.Handler())` for prefix
  mounts). Spec at
  `docs/superpowers/specs/2026-05-25-v0.5.0-transport-http-stdlib.md`;
  plan at
  `docs/superpowers/plans/2026-05-25-v0.5.0-transport-http-stdlib.md`;
  cycle decisions in `.agent/decisions.md` under
  "v0.5.0 transport/http/stdlib + health (2026-05-25 cycle)".
  Hard rule preserved: contract gaps go back to core first — no
  adapter-side shim (none was needed; the
  `ports/health.Check`/`NewCheck` contract was used verbatim).
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
| `idempotency/redis` | `idempotency.Store` | go-redis v9 (any `redis.Scripter`); atomic single-key Lua `Begin`/`Finish`/`Cancel`, `crypto/rand` lease-token ownership, `PEXPIRE`-based reclaim, non-sliding completed-record retention; `WithKeyPrefix` / `WithLeaseTTL` / `WithRetention` (>= 1ms). Redis 4.0+ |
| `jobs/asynq` | `jobs.Enqueuer` / `jobs.Worker` | hibiken/asynq v0.24.1 (Redis-backed); exact-type-match dispatch, two-class Enqueue error mapping, 30-day default scheduling horizon, 1h default completed-task retention, at-least-once with retry→archive dead-lettering; `WithQueue` / `WithSchedulingHorizon` / `WithRetention` / `WithMaxRetry` / `WithRetryDelay` / `WithTaskTimeout` / `WithConcurrency` / `WithShutdownTimeout` / `WithLogger`. Redis 4.0+ |
| `logger/slogger` | `logger.Logger` | stdlib `log/slog` |
| `observability/otel` | `observability.Provider` | OTel SDK v1.32 |

## Open Items

- ~~**v0.5.0 `transport/http/stdlib` + health release cycle**~~ ✅
  CLOSED 2026-05-26. All four tag-gate steps done: PR #21 (impl)
  merged `d9c7324` 2026-05-25; core annotated `v0.5.0` 2026-05-25
  at `e2ee2bb`; PR #22 (dep bump) merged `45274dd` 2026-05-26;
  adapter `v0.5.0` annotated at `45274dd` (tag object `a02f6d4`),
  GitHub Release marked Latest at
  https://github.com/slam0504/go-ddd-adapters/releases/tag/v0.5.0.
  See Current Status above for the full summary. Follow-ups
  preserved below.

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
  - **NEW follow-up — `examples/orders/cmd/api` migration to
    `httpstdlib.Module`**: the example currently uses the
    `cmd/internal/runtime.HTTPModule` ad-hoc helper. Migrating to
    the public adapter from v0.5.0 was deliberately scoped out of
    the release PR (kept it focused on the adapter surface); the
    migration is a separate cycle once downstream adopters validate
    the v0.5.0 API.
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
