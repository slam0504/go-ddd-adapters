# `ratelimit/redisrate` Inbound Rate-Limit Adapter — Design

- **Status**: DRAFT 2026-06-30 — pending user sign-off before plan handoff
- **Date**: 2026-06-30 (Asia/Taipei)
- **Branch**: TBD (likely `feat/ratelimit-redisrate`)
- **Four-quadrant slot**: B. Adapter selection — first concrete driver for
  core's `ports/ratelimit.Limiter` (A-quadrant's final contract, now shipped).
- **Cross-repo paired work**: `go-ddd-core` `ports/ratelimit` contract is
  **already merged** to core `main` at `b882796` (PR #26), but core has **not**
  tagged the ratelimit release — the version number was deliberately deferred
  until the first `ratelimit/*` adapter consumer ships, per the contract-first /
  tag-at-last-piece gate used by jobs (v0.9.0) and idempotency (v0.8.0). **This
  spec is that consumer.** The core tag, the adapter dep-bump PR, and the adapter
  release tag are **later** steps of the joint cycle (see §10), out of scope for
  the impl PR.
- **Session scope**: this cycle's deliverable is the **impl PR merged at a core
  pseudo-version pin** (mirrors the jobs/idempotency cycle step 1). Core's
  ratelimit tag, the dep-bump PR, and the adapter tag are explicitly deferred.
- **Implementation plan**: TBD, produced by `writing-plans` after sign-off.

## 1. Goal & scope

Ship the first concrete `ratelimit.Limiter`: a **distributed, Redis-backed**
inbound request limiter wrapping `github.com/go-redis/redis_rate/v10` (GCRA).
The adapter answers core's single question — "may this key proceed right now?"
— returning the decision as `Result` data and projecting redis_rate's GCRA
state onto core's advisory metadata.

Distributed (not in-process) is the deliberate first choice: inbound throttling
on a horizontally-scaled service needs all instances to share one quota; an
in-process limiter would let each instance count independently (real quota = N×).
This also matches the repo's existing Redis-backed adapters (`idempotency/redis`,
`jobs/asynq`).

### Scope-out (this cycle)

| Item | Reason |
|---|---|
| HTTP enforcement middleware (emit 429 from `Allowed==false`, set quota headers) | adapter-first split, mirrors AuthZ / idempotency; keeps this PR to limiter correctness, not HTTP header policy |
| in-process `ratelimit/memory` / `ratelimit/local` (x/time/rate) | a later sibling for test/dev use; not the production-shaped first consumer |
| self-written Lua sliding-window / token-bucket | a future sibling package; would make GCRA correctness, Redis TIME, TTL, rounding, replication all our own proof burden in v1 |
| outbound quota / blocking `Wait`/`Reserve`, `AllowN`/cost | core deferred these at the contract level; no inbound consumer evidence |
| `ResetAt` absolute-time projection | needs client-clock + Redis-duration → skew risk; deferred to an explicit `WithClock` option when a middleware consumer needs the header |

## 2. Dependency & package

- New module dependency: `github.com/go-redis/redis_rate/v10` (latest `v10.0.1`).
  Reuses the existing `github.com/redis/go-redis/v9 v9.20.0` + testcontainers
  redis module already in `go.mod`.
- Package path: **`ratelimit/redisrate`** (package name `redisratelimit`).
  Driver-named path makes the external-driver binding explicit; a future
  self-written-Lua limiter can be added as a sibling (`ratelimit/redislua`, etc.)
  without re-litigating "the canonical Redis limiter". Mirrors `auth/casbin`,
  `jobs/asynq` driver-in-path naming.
- Files (mirroring `idempotency/redis`):
  `doc.go`, `limiter.go`, `options.go`, `key.go`, `limiter_test.go`,
  `integration_test.go`.

## 3. Types & construction

```go
func New(client redis.UniversalClient, limit redis_rate.Limit, opts ...Option) (*Limiter, error)
```

- `client redis.UniversalClient` — covers `*redis.Client` / `*redis.ClusterClient`
  / `*redis.Ring`. `redis.Scripter` is **insufficient**: redis_rate's internal
  `rediser` interface also requires `Del`, which `UniversalClient` satisfies.
- `limit redis_rate.Limit` — **positional, required**. Rate/burst/period have no
  universal safe default; making it an option would turn "forgot to set it" into
  a silent bug. The driver type is exposed directly (the package is already
  driver-bound; wrapping it would be premature).
- **Guards in `New` (fail-loud)**:
  - nil / typed-nil `client` (explicit type switch on the three concrete go-redis
    types, mirroring `redisidempotency`'s typed-nil guard) → `ErrNilClient`.
  - `limit.Rate <= 0 || limit.Burst <= 0 || limit.Period <= 0` → `ErrInvalidLimit`.
- **Options**: `WithKeyPrefix(string)` only (see §6). **No public `WithLimiter`** —
  v1 is production-shaped and validated against real Redis via testcontainers;
  exposing a fake limiter would muddy the required-`client` semantics of
  `New(client, limit)`. Error-classification paths are tested with an invalid /
  closed Redis client instead (closer to real adapter behaviour).

## 4. `Allow` flow (precedence mirrors the contract)

`Allow(ctx, key)` enforces the contract's fixed precedence **before** any
backend contact:

1. `key == ""` → `errorsx.CodeInvalidArgument` (no Redis contact). An empty key
   is a missing partition key, not an anonymous caller.
2. `ctx.Err() != nil` → return the matching ctx error verbatim
   (`errors.Is(context.Canceled / context.DeadlineExceeded)`, no Redis contact).
3. `redis_rate.Allow(ctx, compositeKey, limit)` → map per §5 / §7.

Precedence: empty key → pre-cancelled/expired ctx → backend (identical to
`jobs.Enqueuer`).

## 5. `Result` mapping

`redis_rate.Result{Allowed int, Remaining int, RetryAfter, ResetAfter time.Duration, Limit}`
→ core `ratelimit.Result`:

| core field | source | note |
|---|---|---|
| `Allowed` | `res.Allowed > 0` | redis_rate `Allowed` is a count (0 = limited) |
| `RetryAfter` | allowed → **`0`**; denied → `res.RetryAfter` | ⚠️ redis_rate returns `RetryAfter == -1` when allowed; mapping it raw would leak `-1` and break core's "allowed → RetryAfter MUST be 0". Explicit zero is **required**, not cosmetic. Denied `res.RetryAfter` is `> 0` (≤ Period), satisfying "denied & nil err → RetryAfter > 0". |
| `Limit` | `limit.Burst` | **GCRA instantaneous-burst projection**, not a fixed-window quota |
| `Remaining` | `res.Remaining` | instantaneous capacity; GCRA guarantees `≤ Burst`, so `Remaining ≤ Limit` holds |
| `ResetAt` | **zero (absent)** | avoids client-clock skew; see scope-out |

**Projection semantics (spec note)**: `Limit`/`Remaining` describe the GCRA
*instantaneous burst* the key may consume right now — NOT a fixed-window
"X requests per window" quota. This is the honest accurate-or-absent reading of
GCRA. If a future HTTP middleware's header convention (e.g. a strict
`RateLimit-Limit` fixed-window field) cannot faithfully express this projection,
the middleware MAY omit those headers rather than fabricate a window number.

## 6. Key encoding (prefix-free, P1)

The Redis key is the tuple `(keyPrefix, key)`. A naive `prefix + ":" + key`
is **not injective**: a client-supplied `key` can flatten into another
namespace (the exact risk `idempotency/redis` fixed in v0.8.0). Encode with a
length prefix on `keyPrefix`:

```go
compositeKey = fmt.Sprintf("%d:%s:%s", len(keyPrefix), keyPrefix, key)
```

`len(keyPrefix)` makes the prefix boundary unambiguous, so distinct
`(prefix, key)` tuples never collide. `WithKeyPrefix` defaults to a non-empty
namespace (e.g. `"ratelimit"`); an empty/zero override still encodes injectively
(`"0::" + key`).

## 7. Error mapping

`redis_rate.Allow` error →
- `errors.Is(err, context.Canceled / context.DeadlineExceeded)` → return verbatim
  (CLASS 2 ctx; this covers cancellation *during* the call).
- otherwise a coded `errorsx` whose `CodeOf != CodeUnknown`: unreachable backend
  → `CodeUnavailable`; unclassifiable → `CodeInternal`.

Ordinary denial is **never** an error: `Result{Allowed:false}, nil`. The adapter
never mints `errorsx.CodeRateLimited`.

## 8. Testing strategy

- **`ratelimittest.RunContract`** against real Redis (testcontainers). Factory
  configures the deterministic profile `redis_rate.Limit{Rate:1, Burst:1,
  Period: 1h}` — burst 1 makes the first same-key `Allow` allowed and the second
  denied; the long period guarantees no GCRA refill races the suite's no-sleep
  invariants.
  - **State isolation per factory call is mandatory.** core's `Factory` doc
    requires a fresh, isolated limiter on every call, but `RunContract` reuses
    fixed keys (`ratelimittest:a` / `:b`) across subtests. Against a shared
    Redis + shared prefix, an earlier subtest depletes a key and a later one
    misjudges. The factory MUST give each returned limiter a **unique
    namespace**, e.g. `WithKeyPrefix("ratelimittest:" + t.Name() + ":" + uuid)`
    (or an atomic counter) — the long period alone does NOT isolate state.
- **Adapter integration tests** (`//go:build integration`):
  - Redis unavailable (closed / invalid-addr client) → `CodeUnavailable`.
  - Recovery after wait: deplete, sleep past the refill window, next `Allow`
    allowed again (the timing-dependent behaviour `RunContract` deliberately omits).
  - **Key-encoding injectivity (adversarial UNIT test, not just integration).**
    The §6 `encode(prefix, key)` MUST be injective. A "two limiters, different
    prefix, same key" test is INSUFFICIENT — naive `prefix+":"+key` passes it
    (distinct prefixes are already distinct strings; it never exercises the
    flatten bug). Assert directly that tuples which COLLIDE under naive encoding
    map to DISTINCT keys, e.g. `(prefix="a", key="b:c")` vs `(prefix="a:b",
    key="c")` — naive both `a:b:c`; length-prefixed `1:a:b:c` vs `3:a:b:c`.
  - Key-prefix isolation (integration): two `Limiter`s with different prefixes
    do not share a bucket for the same `key` (end-to-end check on top of the
    unit test above).
  - ctx precedence: pre-cancelled / pre-expired ctx with a valid key → ctx error,
    no Redis contact.
- **Cluster/Ring**: API-derived claim only (every script is single-key →
  same hash slot by construction); CI exercises single-node `redis:7-alpine`.

## 9. Design decisions (key trade-offs)

1. **Distributed Redis (redis_rate/GCRA) as the first driver** — inbound
   throttling is meaningless per-process under horizontal scaling; production
   shape first, in-process sibling later.
2. **`redis_rate` over self-written Lua (v1)** — GCRA correctness + Redis TIME +
   TTL + rounding + replication are the library's proven burden, not ours; a
   Lua sibling stays a clean future option behind the driver-named path.
3. **`UnknownCount`/absent over fabricated values** — `ResetAt` absent rather
   than a skew-prone `now + ResetAfter`; metadata is advisory, never a blend.
4. **Positional required `limit`** — fail-loud over a silent default.
5. **No public `WithLimiter` (P2)** — real-Redis testing covers v1; a fake would
   muddy required-client semantics. Classification paths use invalid/closed
   clients.
6. **Prefix-free key encoding (P1)** — length-prefixed `keyPrefix` so a
   client-supplied key cannot flatten into another namespace; the documented
   `WithKeyPrefix` isolation must be *provable*, not asserted.
7. **`RetryAfter` explicit-zero on allow** — redis_rate's `-1`-when-allowed makes
   raw mapping a contract violation; this is a required correction, not polish.

## 10. Tag-gate / cross-repo cycle steps (deferred past the impl PR)

Mirrors the jobs/idempotency joint cycle:

1. **(this cycle)** adapter impl PR merged at a **core pseudo-version pin**
   (core `main` `b882796`+), CI green incl. the ratelimit integration leg.
2. core tags the ratelimit release (next minor), publishing `ports/ratelimit`.
3. adapter dep-bump PR: core pin pseudo → the new tag on root + `examples/orders`;
   bookkeeping (CHANGELOG `[Unreleased]` → `[vX.Y.0]`, README Status + compat
   matrix + adapter table row) rides this PR.
4. adapter release tag at the dep-bump merge; GitHub Release Latest.

## 11. Verification

- `gofmt -l .` (no output), `go build ./...`, `go vet ./...`.
- `go test ./ratelimit/...` (unit: mapping, key encoding, guards).
- `go test -tags=integration -race ./ratelimit/...` (RunContract + integration
  against testcontainers Redis).
- `golangci-lint run ./...` and `--build-tags=integration` (CI; local lint via
  the v2 binary).
