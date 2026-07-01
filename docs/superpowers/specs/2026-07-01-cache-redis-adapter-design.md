# `cache/redis` Cache Adapter + `ports/cache` Maturation — Design

- **Status**: ACCEPTED 2026-07-01 — plan handoff
- **Date**: 2026-07-01 (Asia/Taipei)
- **Branch**: TBD (likely `feat/cache-redis`)
- **Target release**: `v0.11.0` on **both** repos (see Tag-gate below)
- **Four-quadrant slot**: B. Adapter selection — first concrete driver for
  core's `ports/cache.Cache`. Unlike ratelimit/jobs/idempotency (where core had
  already shipped a mature contract + conformance suite), `ports/cache` is a
  **pre-v0.5 contract with no conformance suite**, so this cycle folds a
  *scoped* contract maturation into the same cycle (contract audit → cachetest →
  adapter → docs → CI), rather than running an abstract core-only phase first.
- **Cross-repo paired work**: this cycle makes **real `go-ddd-core` changes**
  (delete a dangling generic, add `ports/cache/cachetest`, tighten godoc). It
  therefore runs the full contract-first / tag-at-last-piece gate used by
  idempotency (v0.8.0), jobs (v0.9.0), and ratelimit (v0.10.0) — NOT a
  single-repo dep-bump.

## 1. Goal & Context

Ship `cache/redis`, the first concrete adapter for core's `ports/cache.Cache`,
as a small, complete, tag-gated cycle. `ports/cache` and `ports/httpclient` are
the two adapter gaps that the roadmap's "v0.5.x — Cache + Outbound HTTP client"
cycle planned but leapfrogged (v0.5.0 jumped straight to v0.6.0 AuthN).

Scope is deliberately **cache only**; `httpclient/std` is a separate later cycle
(different behavioural model: cache is a stateful backend adapter, httpclient is
outbound transport policy — mixing them dilutes review). `storage`, `database`
siblings, and `transport/grpc|graphql` stay out — the roadmap explicitly defers
grpc/graphql ("demand signal weaker than HTTP; defer until a real service
requests one") and there is no consumer signal to overturn that.

### Evidence base (verified this session)

A prior contract-maturity audit (16-agent workflow: baseline standard →
per-contract deep read → adversarial verification of each finding against
over-design) established the gate decision:

- **The contract is buildable as-is.** A useful `cache/redis` adapter can be
  written against the current 30-line contract; no fundamental method is
  missing (`Get`/`Set`-with-ttl/`Delete`/`Exists` covers the driver).
- **No independent "mature the core contract" cycle is warranted.** Of 10 audit
  findings, adversarial verification confirmed only 2 as must-do; the rest were
  deferred (let the adapter drive them) or rejected (over-design / already
  decided by roadmap). The 2 confirmed items plus the user-requested tightenings
  fold into THIS cycle.
- **Both `ports/cache` and `ports/httpclient` have ZERO downstream consumers**
  (workspace grep) → breaking changes need no migration PR (same "formal
  empty-inventory" situation as the v0.4.0 inbox removal).
- **go-redis v9.20.0 is already an adapters dependency** (`idempotency/redis`,
  `ratelimit/redisrate` use it) → `cache/redis` adds no new module.
- **Contract semantics already align with the driver**: the contract's
  "zero TTL = no expiry" matches go-redis `Set(...,0)` natively.

## 2. Scope

**IN**

- core `ports/cache` scoped maturation (only what the cache adapter needs).
- adapters `cache/redis` single vertical slice + integration tests + docs + CI.

**OUT (each its own later discussion)**

- `cache/ristretto` in-process L1 (introduces its own copy/ownership pressure;
  the byte-ownership contract this cycle pins in `cachetest` is what would later
  protect it — but the driver itself is deferred).
- `cache/redis` `health.Check` export (roadmap mentions it; not needed to ship
  the adapter).
- `httpclient/std` (next cycle).
- A typed/marshalling cache layer (see `TypedCache[T]` deletion below; let a
  real consumer drive any typed shape adapter-side).

## 3. Core contract maturation (`go-ddd-core/ports/cache`)

Only the changes the cache adapter needs; **no** blanket MUST/MAY godoc rewrite.

### 3.1 Delete `TypedCache[T]`

`TypedCache[T]` is a dangling generic interface — no constructor, no codec
injection point, its own miss semantics unstated. Audit finding F6 (CONFIRMED).
Delete it (breaking, but zero-consumer → painless). Do **NOT** build a
`New[T]`/`Codec[T]`/`(found bool)` seam now — that is premature abstraction with
no consumer, contradicting the repo's own fold-first doctrine. The byte-level
`Cache` interface and `ErrMiss` sentinel stay.

### 3.2 Add `ports/cache/cachetest/` conformance suite

New sibling test-helper package mirroring `idempotencytest` / `jobstest` /
`ratelimittest`: exported `RunContract(t, Factory)` + a `Factory` callback that
returns a fresh, state-isolated `cache.Cache` per subtest. Imports only
`testing` + `pkg/errorsx` (never `pkg/errorsx/httpx`). See §5.

### 3.3 Godoc tightening (cache-necessary subset)

Fold into `ports/cache/cache.go` godoc:

- **ctx convention line** (one line, matching `jobs`/`ratelimit`): every
  ctx-taking method requires a non-nil ctx (stdlib convention); passing nil is a
  caller bug; no runtime nil-check required.
- **`Set` TTL semantics** (contract-level, because the adapter enforces it and
  `cachetest` asserts it): `ttl == 0` means no expiry; **`ttl < 0` is invalid
  input → `errorsx.CodeInvalidArgument`, evaluated before any backend contact.**
  Rationale: a negative TTL is almost always a caller bug, and passing it
  through risks the go-redis `redis.KeepTTL == -1` footgun (a sentinel that
  *keeps* the existing TTL rather than expiring) — fail loud instead.
- **empty-key semantics**: `""` is invalid input → `CodeInvalidArgument`,
  before backend contact.
- **`Set` error precedence** (fixed): `empty key → negative TTL →
  pre-cancelled/expired ctx → backend`. The first two are deterministic class-1
  input validation (evaluated before ctx is even observed, per the mature-
  contract standard S3); then a pre-cancelled/expired ctx returns the matching
  ctx error with no backend contact; only then a backend failure.
- `Get`/`Delete`/`Exists` precedence: `empty key → pre-cancelled ctx → backend`.

**Not** in this cycle's godoc: a full per-field MUST/MAY rewrite, byte
copy/ownership prose in the *contract* text (the ownership guarantee is instead
pinned by `cachetest` invariants — see §5.3 — which is where a future in-process
adapter will actually be held to it).

## 4. `cache/redis` adapter design

Package `rediscache` (dir `cache/redis`), matching the `redis<port>` naming of
`redisidempotency` / `redisratelimit` (avoids colliding with the go-redis
import).

### 4.1 Constructor & client

```go
func New(client redis.Cmdable, opts ...Option) (*Cache, error)
```

- **`redis.Cmdable`**: the go-redis interface that already exposes
  `Get`/`Set`/`Del`/`Exists`. It is NOT a semantically minimal interface
  (go-redis's `Cmdable` is large), but it is an existing, publicly usable,
  fit-for-purpose entry point that drops the lifecycle/subscribe semantics
  `UniversalClient` carries — appropriate for a cache. `*redis.Client`,
  `*redis.ClusterClient`, `*redis.Ring` all satisfy it.
- **nil / typed-nil guard** (mirrors `redisratelimit` / `redisidempotency`):
  reject a nil interface (`ErrNilClient`); type-switch the three concrete client
  types and reject a nil pointer of each. A typed-nil *custom* `Cmdable` is NOT
  guarded and surfaces on first call — documented, the caller's bug.

### 4.2 Options

`config` struct + `Option func(*config)` (sealed pattern, mirrors range of
existing adapters). `WithKeyPrefix(prefix string)` overrides the namespace;
default `"cache"`.

### 4.3 Key encoding (prefix-free, mandatory)

Reuse the length-prefix injective encoding proven in `redisratelimit`
(P1) and `redisidempotency` (the v0.8.0 flatten-bug fix):

```go
func encode(keyPrefix, key string) string {
    return fmt.Sprintf("%d:%s:%s", len(keyPrefix), keyPrefix, key)
}
```

Length-prefixing `keyPrefix` makes the `(keyPrefix, key)` boundary unambiguous:
`encode("a","b:c") == "1:a:b:c"` ≠ `encode("a:b","c") == "3:a:b:c"`. Without it, a
client-supplied key could flatten into another Store's namespace. `key.go` +
`key_test.go` assert the COLLIDING-tuples-map-to-DISTINCT-keys invariant (not
the vacuous "different prefixes differ").

### 4.4 Methods

For every method: `empty key → CodeInvalidArgument` (before ctx); then ctx error
verbatim; then backend via `classifyBackendErr`. `Set` adds the `ttl < 0 →
CodeInvalidArgument` rung between empty-key and ctx.

- `Get(ctx, key) ([]byte, error)`: `client.Get(ctx, encode(prefix,key)).Bytes()`;
  translate `redis.Nil → cache.ErrMiss`. Returned slice is a fresh copy (wire
  decode) — safe for the caller to mutate.
- `Set(ctx, key, value, ttl)`: validate `ttl` (`<0` rejected, `0` = no expiry);
  `client.Set(ctx, encode(prefix,key), value, ttl).Err()`. The adapter must not
  retain the caller's `value` slice beyond the call (go-redis copies to the
  wire; guaranteed by the driver).
- `Delete(ctx, key)`: `client.Del(ctx, encode(prefix,key)).Err()` — idempotent
  (deleting an absent key is not an error).
- `Exists(ctx, key)`: `client.Exists(ctx, encode(prefix,key)).Result()` → `> 0`.

### 4.5 Errors

Reuse the `classifyBackendErr` shape from `redisratelimit` / `jobsasynq`:
ctx errors (`context.Canceled` / `DeadlineExceeded`) returned verbatim; any
other backend error wrapped as a coded `errorsx` whose `CodeOf` is **never**
`CodeUnknown` (unreachable → `CodeUnavailable`; unclassifiable → `CodeInternal`).
`ErrNilClient` for construction. `cache.ErrMiss` is the only non-error "outcome"
sentinel and comes from core.

Compile-time assertion: `var _ cache.Cache = (*Cache)(nil)`.

`doc.go` sectioned like `redisratelimit` (Why / Construction / Semantics /
Errors / Cluster-Ring, with the Cluster/Ring claim marked API-derived vs
CI-verified single-node).

## 5. `cachetest.RunContract` design

### 5.1 Shape

`RunContract(t *testing.T, newCache Factory)` where
`Factory func() cache.Cache` returns a fresh, state-isolated cache. One exported
entry point; deterministic/synchronous-only default suite (no clocks, no sleeps
— cannot flake). Error assertions use `errorsx.CodeOf` / `errors.Is`, never an
HTTP status.

### 5.2 Deterministic invariants (default suite)

- `Get` on absent key → `ErrMiss`.
- `Set` then `Get` → byte-equal value.
- `Set` overwrite → second value wins.
- `Delete` then `Get` → `ErrMiss`; `Delete` on absent key → no error
  (idempotent).
- `Exists`: true after `Set`, false after `Delete` / on absent key.
- **empty-key precedence**: `Get`/`Set`/`Delete`/`Exists` with `""` →
  `CodeInvalidArgument`, no backend mutation.
- **negative-TTL precedence** (NEW, per §3.3): `Set(ctx, key, value, ttl<0)` →
  `CodeInvalidArgument`, and the key is NOT written (no backend contact for the
  validation failure). Ordered after empty-key, before ctx.
- **pre-cancelled ctx**: a ctx cancelled at entry → matching ctx error, no
  backend mutation. (Ordered after the two class-1 validations.)

### 5.3 Byte-ownership invariants (NEW — data-correctness contract)

Two deterministic invariants that a Redis adapter passes for free (values cross
the wire) but that pin the ownership contract for any future in-process adapter
(e.g. ristretto), preventing an aliasing bug:

- **input aliasing**: `v := []byte(...); Set(k, v); mutate v` → a subsequent
  `Get(k)` returns the original bytes (the cache must not alias the caller's
  input slice).
- **output aliasing**: `a := Get(k); mutate a` → a subsequent `Get(k)` returns
  the original bytes (the cache must not hand out an aliased internal slice).

### 5.4 Out of the default suite

Time-dependent behaviour (TTL expiry) is NOT in the deterministic suite (mature-
contract standard S1). It is covered by an **adapter intent test** using
`internal/redistest` with a short real TTL, asserting a key is gone after
expiry.

### 5.5 Non-vacuity (dev-time red proof, commit GREEN only)

Per the user decision: prove the suite is not vacuous at **dev time** by
temporarily breaking a reference in-memory implementation and confirming
`RunContract` fails, then revert. The committed tree contains only:

- `RunContract` itself,
- a reference in-memory good implementation that passes it (drives suite authoring),
- edge-case assertions covering intent.

Do **NOT** commit a red-on-purpose `t.Run("bad impl should fail", ...)` (it
breaks CI), and do **NOT** build a subprocess permanent guard (too heavy for a
first small suite). Record in the plan / review-log: "temporarily broke
Set/Get/Delete/TTL/negative-TTL behaviour and confirmed cachetest.RunContract
failed; reverted before commit." Escalate to a subprocess guard only if the
suite later grows complex or a vacuous regression actually occurs.

## 6. Testing / Docs / CI

- `internal/redistest.StartContainer` (redis:7-alpine, `//go:build integration`)
  reused unchanged.
- `cache/redis/integration_test.go` (`//go:build integration`): runs
  `cachetest.RunContract` against a real container + adapter intent tests
  (TTL expiry, negative-TTL reject, keyPrefix isolation, redis.Nil→ErrMiss,
  typed-nil guard, empty-key precedence).
- Non-integration unit tests: `options_test.go`, `key_test.go`.
- CHANGELOG `[Unreleased]`: `cache/redis` adapter + `ports/cache` maturation
  (note the `TypedCache[T]` removal as a breaking change + the new `cachetest`).
- README adapter table row + Status paragraph.
- CI: a dedicated `cache/redis` integration step (mirrors jobs/asynq and
  ratelimit having their own integration legs).

## 7. Tag-gate flow (both repos → `v0.11.0`)

Full contract-first / tag-at-last-piece gate (has core changes):

1. **core PR**: delete `TypedCache[T]` + add `ports/cache/cachetest` + godoc
   tightening → merge to core `main`, **left untagged**.
2. **adapter PR**: `cache/redis`, pinning core at a **pseudo-version** of the
   core-PR merge; CI runs `cachetest.RunContract` green under the pin → merge.
3. **core tag `v0.11.0`** (publishes the matured `ports/cache` + `cachetest`);
   verify resolvable via proxy.
4. **adapter dep-bump PR**: core pseudo → `v0.11.0` on root + `examples/orders`.
5. **adapter tag `v0.11.0`** at the dep-bump merge + GitHub Release Latest.

## 8. Verification strategy

- Local: `go build ./...`, `go vet ./...`, `go test ./...` (unit),
  `golangci-lint run ./...` via the **v2 binary** `/usr/local/bin/golangci-lint`
  (the `~/go/bin` v1.64.8 chokes on the v2 config). goimports local-prefixes:
  `go-ddd-adapters/...` imports get their own group (core is third-party).
- CI: `cachetest.RunContract` + adapter intent tests vs real redis:7-alpine.
  Docker is unavailable locally, so the integration RUN is CI-only (same as
  every prior Redis cycle) — state that honestly, do not claim a local
  integration pass.
- cachetest non-vacuity: dev-time red proof (§5.5), recorded in the plan.
- go.sum/go.mod hash byte-identical pseudo → `v0.11.0` check at the dep-bump
  (proves core tagged the exact content the adapter was conformance-tested
  against).

## 9. Decisions log

**User-set:**
- Single `cache/redis` cycle; `httpclient/std` separate later cycle.
- Maturation folded INTO the adapter cycle (no abstract core-only phase);
  mature only what the cache adapter needs.
- cachetest non-vacuity via dev-time red proof, commit GREEN only.
- negative-TTL reject promoted to **core contract + cachetest case** (not
  adapter-only intent test); `Set` precedence `empty → negTTL → ctx → backend`.
- Key encoding must be prefix-free length-prefixed (reuse the existing model).
- cachetest adds the two byte-ownership invariants (input + output aliasing).
- `redis.Cmdable` accepted (a fit-for-purpose public entry, not "minimal");
  typed-nil guard on the three concrete types, custom typed-nil documented.

**Self-decided (open to veto during spec review):**
- Package name `rediscache`, dir `cache/redis`.
- `WithKeyPrefix` default `"cache"`.
- Reference in-memory impl for cachetest authoring lives in the core test
  package (unexported) — used to author + red-prove the suite, not shipped as a
  production adapter.

## 10. Deferred / explicitly out (recorded so future sessions don't re-litigate)

- Audit findings deferred to a future consumer, NOT this cycle: miss-as-data
  error model (F2 — revisit repo-wide with `database.ErrNoRows`/
  `storage.ErrNotFound`, not cache-only), full MUST/MAY godoc rewrite (F4),
  a `ports/cache` "maturity cycle" as a standalone effort (F7 rejected).
- `cache/ristretto` L1, `cache/redis` health.Check export, `httpclient/std`,
  `storage/*`, `database/sql|mongo`, `transport/grpc|graphql`.
