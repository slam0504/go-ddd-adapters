# v0.9.0 `jobs/asynq` Background-Jobs Adapter — Design

- **Status**: DRAFT 2026-06-16 — pending user sign-off before plan handoff
- **Date**: 2026-06-16 (Asia/Taipei)
- **Branch**: TBD (likely `feat/jobs-asynq-v0.9.0`)
- **Cross-repo paired work**: `go-ddd-core` `ports/jobs` contract (already merged)
  - core main HEAD: `784ef3e` (PR #22 `ports/jobs` contract merge `728c9d0`,
    plus `784ef3e` bookkeeping "record ports/jobs contract merged").
  - core has **not** tagged the jobs release; the version number (v0.9.0) was
    deliberately deferred until the first `jobs/*` adapter consumer passes the
    full tag-gate acceptance suite. This spec is that consumer; the core tag is
    a **later** step of the joint cycle (see §10), out of scope for the impl PR.
- **Session scope (decided 2026-06-16)**: this cycle's deliverable is the
  **impl PR merged at a core pseudo-version pin** (mirrors v0.8.0 cycle step 1).
  Core's `v0.9.0` tag, the adapter dep-bump PR, and the adapter `v0.9.0` tag are
  explicitly deferred to a later session.
- **Implementation plan**: TBD, produced by `writing-plans` after sign-off.
- **Spike provenance**: throwaway branch `spike/jobs-asynq` proved the Asynq
  mapping (build clean + 3 testcontainers shutdown-semantics tests PASS, recorded
  in `go-ddd-core/.agent/decisions.md` "Compile-tested spike"). This spec
  promotes that mapping to a production package; the spike branch is never merged.

---

## 1. Goal

Ship the **adapter half** of the background-jobs release: a public `jobs/asynq`
package that implements core's `ports/jobs.Enqueuer` and `ports/jobs.Worker`
contracts over [`github.com/hibiken/asynq`](https://github.com/hibiken/asynq)
(a Redis-backed task queue). This is the first production consumer of the
`ports/jobs` contract, and the consumer that closes the core tag-gate.

Success criteria:

1. A downstream service can enqueue and process background jobs by constructing
   one `jobsasynq.Enqueuer` and one `jobsasynq.Worker` from a Redis connection
   it already owns, registering `jobs.Handler`s by type, with **no service-side
   translation code** between core's `Job`/`Task` shapes and Asynq's
   `asynq.Task`.
2. The adapter maps cleanly onto core's error vocabulary via `pkg/errorsx`:
   malformed Enqueue → `CodeInvalidArgument`; unreachable backend →
   `CodeUnavailable`; any other non-ctx backend failure → a coded `errorsx`
   whose `CodeOf != CodeUnknown` (unclassifiable → `CodeInternal`); Register
   validation → `CodeInvalidArgument` / `CodeAlreadyExists`. **No per-adapter
   HTTP status table.**
3. The adapter passes the full **(0)+(a)–(v)** tag-gate acceptance suite
   (`go-ddd-core/.agent/decisions.md` "Tag gate"), all under `go test -race`
   against real Redis (testcontainers), plus core's
   `ports/jobs/jobstest.RunContract` (criterion 0).
4. `go build`, `go vet`, `go test`, `go test -race`, `golangci-lint` (default +
   `integration` build tags) all green on root + `examples/orders` against a
   pseudo-version-pinned `go-ddd-core` (core jobs commit `784ef3e`, not yet
   tagged v0.9.0).

## 2. In scope

### 2.1 New package (root module)

- `jobs/asynq/` — package name **`jobsasynq`** (not `asynq`) to avoid colliding
  with the upstream `github.com/hibiken/asynq` import in the same files. Mirrors
  the existing `pgxoutbox` / `pgxdb` / `redisidempotency` / `casbinauth` naming
  rule.
  - `Enqueuer` — concrete type implementing core `jobs.Enqueuer`.
  - `Worker` — concrete type implementing core `jobs.Worker`.
  - `NewEnqueuer(redis asynq.RedisConnOpt, opts ...Option) (*Enqueuer, error)`
  - `NewWorker(redis asynq.RedisConnOpt, opts ...Option) (*Worker, error)`
  - `Option` / private `config` — functional-options private-config pattern,
    mirroring `authjwt` / `casbinauth` / `redisidempotency` (§5).
  - Exported single-source declared values for the conformance fixtures
    (criterion v): `DefaultSchedulingHorizon`, `DefaultRetention`, and the
    declared shutdown/recover/redelivery bounds (§6).

### 2.2 Dependencies

- Add `github.com/hibiken/asynq v0.24.1` to the root module (spike-pinned;
  go.sum hash recorded in core decisions).
- Test-only: `github.com/testcontainers/testcontainers-go/modules/redis
  v0.42.0` — matches the repo's existing `testcontainers-go v0.42.0` (already a
  dependency from the idempotency/redis cycle).
- Core pin: pseudo-version of core `main` `784ef3e` on root +
  `examples/orders` go.mod (NOT a tagged version — see §10).
- **No `miniredis`.** The acceptance behaviour depends on Asynq's real Redis
  state machine (active lease, retry/archive, completed retention, Inspector
  queries); a second backend path would risk testing false stability.

### 2.3 Documentation

- `doc.go` declares the adapter's policy surface (the contract delegates these
  to adapters): retry/backoff schedule, dead-letter/archive policy, fatal-code
  taxonomy, scheduling horizon, durability boundary, at-least-once guarantee.
- `README.md` adapter-table row + a short usage sketch.
- CHANGELOG `[Unreleased]` accumulates the entry; the release narrative /
  version heading lands in the later dep-bump PR (per the v0.7.0 / v0.8.0
  impl-PR-vs-dep-bump-PR convention).

## 3. Out of scope (this cycle)

- **River adapter** (`jobs/river`) — the spike validated only a compile-level
  Postgres mapping; runtime verification needs Postgres and is deferred to its
  own adapter cycle.
- **HTTP enforcement / transport middleware** for jobs (there is no obvious one;
  jobs is a producer/worker primitive, not a request-path concern).
- **`examples/orders` jobs wiring** — deferred, mirroring the AuthZ / idempotency
  "adapter first, wiring later" split.
- **Cron / recurring scheduling**, queue/lane routing across heterogeneous
  workers, enqueue-side dedup, result/await — all deliberately excluded by the
  core contract (YAGNI until consumer evidence).
- **Core `v0.9.0` tag, adapter dep-bump PR, adapter `v0.9.0` tag** — later
  session (§10).

## 4. Enqueue & dispatch semantics

### 4.1 `Enqueuer.Enqueue` (criteria g, i, m, q, f, t)

Fixed-precedence error evaluation, matching the contract:

1. **Class 1 (deterministic validation, before observing ctx or backend):**
   - empty `Job.Type` → `errorsx.New(CodeInvalidArgument, …)`, zero `JobInfo`.
   - `ProcessAt` beyond the declared scheduling horizon (`!ProcessAt.IsZero()
     && ProcessAt.After(now + horizon)`) → `CodeInvalidArgument`, zero
     `JobInfo`, **nothing written** (rejected before any backend I/O).
   - Precedence within class 1: empty Type is checked first, then horizon.
2. **snapshot-before-submit:** copy `Job.Payload` into a fresh slice before any
   backend call. A nil and an empty payload are equivalent (delivered as
   zero-length); the copy isolates an accepted-but-ack-lost job from later
   caller mutation (criterion t).
3. **Class 2a (ctx entry check, before backend contact):** `ctx.Err()` — if
   non-nil, return it directly (`errors.Is` Canceled / DeadlineExceeded). This
   precedes backend contact even when the backend is also unreachable
   (criterion q).
4. **Class 2b (backend):** call `client.EnqueueContext(ctx, asynq.NewTask(type,
   payload), opts...)`. On error: if it `errors.Is` a ctx error, pass through;
   otherwise wrap as a coded `errorsx` — `CodeUnavailable` for an unreachable
   backend, `CodeInternal` for an otherwise-unclassifiable failure, **never
   `CodeUnknown`** (criterion f). On success return `jobs.JobInfo{ID: info.ID}`
   with a non-empty, redelivery-stable ID (criteria e, …).

Asynq options applied per Enqueue: `asynq.ProcessAt(job.ProcessAt)` when
non-zero; `asynq.Retention(cfg.retention)`; `asynq.Queue(cfg.queue)`;
`asynq.MaxRetry(cfg.maxRetry)`. `ProcessAt` zero ⇒ ASAP (no option). A past
`ProcessAt` is already eligible, never an error (criterion p).

### 4.2 Dispatch — exact-type-match map (criteria k, u, d)

`Worker` keeps a **self-managed `map[string]jobs.Handler`**, NOT
`asynq.ServeMux` (whose longest-prefix matching would violate the contract's
exact-type-match requirement). A single root `asynq.HandlerFunc` looks the task
type up by exact key:

- **hit:** hand the handler a `jobs.Task{ID, Type, Payload}` whose `Payload` is
  a private per-delivery copy (mutation does not pollute redelivery — criterion
  d); return the handler's error to Asynq (nil acks; non-nil = failed attempt).
- **miss (unhandled type):** return a non-nil error → never acked as success;
  follows the **documented** unhandled-job policy (Asynq retries per the
  retry/backoff schedule, then archives). The doc states this is a deployment
  precondition violation (homogeneous worker pool), surfaced loudly, not routed
  around (criterion u).

### 4.3 `Worker.Register` (criteria o, plus jobstest)

Argument validation first, then duplicate check (fixed precedence):

- empty `jobType` or interface-nil `h` → `CodeInvalidArgument` (even if the type
  is already registered).
- well-formed re-registration of an existing type → `CodeAlreadyExists`, the
  original handler stays installed — no silent replace (criterion o).

Register is wiring-time, single-threaded, all calls before `Run`.

## 5. Constructors, options, and validation

`NewEnqueuer` / `NewWorker` take the **required** dependency (`asynq.RedisConnOpt`)
as a positional parameter and **optional policy** as `Option func(*config)`.
After applying all options, the constructor validates the resolved config and
fails loud, returning `(nil, error)` rather than constructing a half-valid
adapter.

### 5.1 Options

| Option | Applies to | Default | Validation |
| --- | --- | --- | --- |
| `WithSchedulingHorizon(d)` | Enqueuer | `DefaultSchedulingHorizon` = `30*24h` | `d > 0` |
| `WithRetention(d)` | Enqueuer | `DefaultRetention` = `1h` | `d >= 1s` (Asynq stores `int64(d.Seconds())`; sub-second truncates to `PEXPIRE`-equivalent 0 → lost completion evidence) |
| `WithQueue(name)` | both | `"default"` | non-empty |
| `WithMaxRetry(n)` | Enqueuer | Asynq default (25) | `n >= 0` |
| `WithRetryDelay(fn)` | Worker | Asynq default exponential | non-nil |
| `WithConcurrency(n)` | Worker | a small fixed default (e.g. 10) | `n > 0` |
| `WithShutdownTimeout(d)` | Worker | a fixed default (e.g. 8s) | `d > 0` |
| `WithLogger(asynq.Logger)` | Worker | Asynq default | non-nil |

`WithMaxRetry` pairs with `WithRetryDelay` so retry/dead-letter policy is fully
tunable, and so the archive-path acceptance test can set a small max instead of
driving Asynq's default 25 attempts.

### 5.2 `RedisConnOpt` validation (constructor-returns-error, not panic)

Asynq panics on an unsupported / nil `RedisConnOpt` when it builds the
client/server/inspector. The constructors MUST instead return a coded error:

- reject a nil `RedisConnOpt` up front (`CodeInvalidArgument`);
- build the Asynq client (Enqueuer) / inspector eagerly inside the constructor,
  recovering any panic into a coded `errorsx` (`CodeInvalidArgument` for a
  malformed opt). The Worker's `asynq.Server` is built lazily in `Run` (its
  lifecycle is tied to `Run`), but the same connection opt is validated at
  construction by building the shared inspector / a cheap client probe so a bad
  opt fails at `New`, not deep inside `Run`.

## 6. Single-source declared values & test introspection (criteria v, s)

Per criterion (v), the scheduling horizon, durability boundary, and the
`ShutdownWithin` / `RecoverWithin` / `RedeliverWithin` bounds the conformance
fixtures assert against MUST be the **same exported constants/options** the
adapter itself uses — no second copy.

- `DefaultSchedulingHorizon` (30 days) and `DefaultRetention` (1h) are exported
  consts; `WithSchedulingHorizon` / `WithRetention` override them; the Enqueuer
  reads the resolved value, and the horizon-rejection test reads the same value.
- **Shutdown bound:** the Worker's declared `ShutdownWithin` equals its resolved
  `WithShutdownTimeout` (plus a small fixed margin for Run's own return), exposed
  as an exported method/const the fixture references. Asynq requeues in-flight
  active tasks back to pending on graceful `srv.Shutdown()`, so the
  shutdown path gives an **immediate** active→pending requeue bounded by the
  shutdown timeout (criterion c, and the fast path for s2).
- **Recover / redelivery bounds (Asynq runtime facts, v0.24.1):** the lease is
  30s; the recoverer reclaims only leases expired ≥30s and polls ~every 1
  minute. Tests that exercise the **crash path** (no graceful shutdown) declare
  `RecoverWithin` / `RedeliverWithin` as exported consts that fold in
  `30s lease + 1min poll + margin` (the spike's 3-minute bound is the proven
  reference). Tests that can use the **graceful-shutdown immediate-requeue path**
  do so (faster, deterministic) and assert against `ShutdownWithin`.
- **Test introspection** (`JobState(ctx, id)` classifier): a test-scoped helper
  over `asynq.Inspector` mapping a job to `completed` / `pendingRetryable` /
  `activeLeased` / `lostDiscarded`. not-found with no completion evidence =
  `lostDiscarded`; `WithRetention` supplies the completion-evidence channel so a
  pruned-but-completed job is not misread as lost. Introspection errors =
  `t.Fatalf`. This helper lives in the adapter's `_test` files (it is test
  support, not part of the fire-and-forget public surface).

## 7. Run lifecycle (criteria c, h, j, l, s)

- ctx already cancelled at entry → return `nil` without starting (no server
  built).
- build `asynq.Server` with resolved Concurrency / ShutdownTimeout / Logger /
  RetryDelayFunc; `srv.Start(root)`.
  - start error (independent fatal, no cancellation) → coded `errorsx`
    (`CodeUnavailable` unreachable / `CodeInternal` otherwise), never a ctx
    error (criterion h, endpoint B).
- `<-ctx.Done()` → `srv.Shutdown()` (bounded graceful drain; requeue/teardown
  failures are Asynq-internal, logged via the configured logger, **never**
  change the return) → return `nil` (criterion c, endpoint A). The
  teardown-failure variant (injected shutdown-path backend error) still returns
  `nil`.
- Handlers receive an Asynq server-derived ctx that is cancelled at shutdown
  (criterion j). A handler ignoring its cancelled ctx is orphaned and MUST NOT
  block Run's return (liveness over graceful drain); post-return ack of a
  straggler is binary (completed atomically, or not at all — Asynq's `Done` is
  an atomic Lua script), upholding the recoverable-state model.
- Concurrent Enqueue + Run under `-race` is clean; the two Run endpoints are
  asserted without a simultaneous-overlap tie-break (criterion l).

## 8. Testing & acceptance mapping

All acceptance tests under `//go:build integration` + testcontainers Redis 7
(`redis:7-alpine`); Docker unavailable counts as a **gate failure, not a skip**.
Plain `go test` (no tag) covers only pure units: option validation, error-code
mapping, payload-copy helper.

| Criterion | Coverage |
| --- | --- |
| (0) | `jobstest.RunContract` over a testcontainers Backend |
| (a) | at-least-once redelivery incl. concurrent-duplicate tolerance |
| (b) | dispatch not before `ProcessAt` on the backend scheduling clock |
| (c) | Run nil within declared `ShutdownWithin` + teardown-failure variant |
| (d) | handler payload mutation does not pollute redelivery |
| (e) | ID stable across redeliveries |
| (f) | unreachable backend → `CodeUnavailable`; any non-ctx failure ≠ `CodeUnknown` |
| (g) | malformed Enqueue writes nothing (Inspector introspection) |
| (h) | fatal startup → coded `errorsx`, not a ctx error |
| (i) | enqueue payload snapshot |
| (j) | handler ctx cancelled on shutdown |
| (k) | exact-type-match dispatch rejects prefixes |
| (l) | concurrent Enqueue clean under -race + Run endpoints |
| (m) | out-of-horizon `ProcessAt` rejected at Enqueue (precedence over cancelled ctx) |
| (n) | accepted scheduled job retained past `ProcessAt` without a worker |
| (o) | duplicate Register keeps the original handler (h1 receives, not h2) |
| (p) | past `ProcessAt` immediately eligible |
| (q) | ctx precedes backend within class 2 (both ctx-error kinds, unavailable backend) |
| (r) | worker stops before dequeue; a NEW Worker over the same store delivers |
| (s) | shutdown in-flight recoverability — (s1) late-ack, (s2) never-ack |
| (t) | accepted-but-ack-lost fault: class-2 error + zero JobInfo + snapshot isolation |
| (u) | unhandled type never acked; follows documented policy (introspection) |
| (v) | single-source declared values shared by adapter and fixtures |

CI: extend the `integration-test` job's root-module step to include
`go test -tags=integration -race ./jobs/asynq/...`.

## 9. Risks / decisions to validate in the plan

- **Worker connection validation timing** (§5.2): confirm the cheapest reliable
  way to fail a bad `RedisConnOpt` at `New` without prematurely opening a
  long-lived server connection. Candidate: build the shared `asynq.Inspector`
  at `New` (also reused by tests) and `Ping`.
- **(s2) path selection:** prefer the graceful-shutdown immediate-requeue path
  where the contract allows, to keep the suite fast and deterministic; reserve
  the 3-minute recoverer-bound crash path only where (s2)'s "new Worker actually
  redelivers" genuinely requires a non-graceful stop.
- **Unhandled-job policy wording:** the doc must state Asynq's concrete
  behaviour (retry per schedule → archive) as the adapter's declared policy, so
  criterion (u) asserts against a documented value, not an accident.

## 10. Tag-gate (joint cycle, mostly deferred)

Same discipline as v0.5.0–v0.8.0. Steps:

1. **(this session)** Adapter impl PR merged, CI green at the **core
   pseudo-version pin**; the full `(0)+(a)–(v)` suite green under `-race`.
2. **(later)** Core tags `v0.9.0` once this adapter has landed (core agent,
   separate repo); GitHub Release Latest.
3. **(later)** Adapter dep-bump PR: core pin pseudo-version → `v0.9.0` on root +
   `examples/orders`; release narrative + CHANGELOG `[v0.9.0]` + README ride in.
4. **(later)** Adapter `v0.9.0` annotated tag at the dep-bump merge + GitHub
   Release Latest.

Until `(0)+(a)–(v)` pass on the merged adapter, core stays untagged
(pseudo-version-only compensating control).
