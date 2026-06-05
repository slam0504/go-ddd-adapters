# v0.7.0 `auth/casbin` AuthZ Adapter — Design

- **Status**: DRAFT 2026-06-05 — pending user sign-off before plan handoff
- **Date**: 2026-06-05 (Asia/Taipei)
- **Branch**: TBD (likely `feat/authz-casbin-v0.7.0`)
- **Cross-repo paired work**: `go-ddd-core` AuthZ contract (already merged)
  - core main HEAD: `47e02fa` (PR #13 AuthZ contract merge `64b6bf6`, PR #14
    state bookkeeping merge `47e02fa`)
  - core has **not** tagged the AuthZ release; the version number (v0.6.x vs
    v0.7.0) was deliberately deferred until the first `authz/*` adapter consumer
    lands here. This spec is that consumer; the tag is the **last** step of the
    joint cycle (see §10).
- **Implementation plan**: TBD, produced by `writing-plans` skill after this
  spec is approved.

---

## 1. Goal

Ship the **adapter half** of the AuthZ release: a public `auth/casbin`
package that wraps a caller-built Casbin enforcer as a concrete
`go-ddd-core` `ports/auth.Authorizer`. This is the first consumer of core's
new `Authorizer` contract (`Allow(ctx, caller Identity, action string,
resource Resource) error`).

Success criteria:

1. A downstream service can satisfy `auth.Authorizer` by constructing one
   `casbinauth.Authorizer` from a Casbin enforcer it already owns, with **no
   service-side translation code** between core's `(caller, action,
   resource)` shape and Casbin's `Enforce(rvals...)` shape for the common
   `(sub, obj, act)` model.
2. The adapter maps cleanly onto core's error vocabulary: deny → `ErrForbidden`
   (403), malformed input → `ErrInvalidAuthorizationRequest` (400), engine /
   builder errors passed through un-disguised (uncoded → 500), all via
   `pkg/errorsx` + `pkg/errorsx/httpx` **without any per-adapter status table**.
3. The adapter depends only on a minimal one-method `Enforcer` interface, so
   both `*casbin.Enforcer` and `*casbin.SyncedEnforcer` satisfy it and unit
   tests can use a fake enforcer without building a real Casbin model.
4. `go build`, `go vet`, `go test`, `go test -race`, `golangci-lint`
   (default + integration build tags) all green on root + `examples/orders`
   against a pseudo-version-pinned `go-ddd-core` (core's AuthZ commit
   `47e02fa`, not yet tagged v0.7.0).

## 2. In scope

### 2.1 New package (root module)

- `auth/casbin/` — package name **`casbinauth`** (not `casbin`) to avoid
  colliding with the upstream `github.com/casbin/casbin/v3` import in the same
  file. Mirrors the existing `pgxoutbox` / `pgxdb` / `authjwt` naming rule.
  - `Enforcer` — the single-method interface the adapter depends on (§5.1).
  - `Authorizer` — concrete type implementing core `auth.Authorizer`.
  - `New(enforcer Enforcer, opts ...Option) (*Authorizer, error)` — primary
    constructor.
  - `Option` / private `config` — functional-options, `authjwt` private-config
    pattern (§5.2).
  - `RequestBuilder` + `WithRequestBuilder` — pluggable `(caller, action,
    resource)` → `[]any` translation, with a secure-by-default builder (§5.3).

### 2.2 Tests

- Unit tests (fake `Enforcer`) covering the full `Allow` control flow (§5.4),
  the default builder's `:` encoding, `WithRequestBuilder` override, and
  constructor guards.
- Integration test (build tag `integration`) using a real `*casbin.Enforcer`
  with an embedded RBAC model + policy string to prove allow/deny actually
  traverse Casbin.
- `-race` test using a `*casbin.SyncedEnforcer` under concurrent `Allow`
  calls to verify no data race.

### 2.3 go.mod

- Root `go.mod`: add `github.com/casbin/casbin/v3 v3.10.0`; bump
  `github.com/slam0504/go-ddd-core` from `v0.6.0` to the pseudo-version derived
  from core's AuthZ commit `47e02fa` (so CI sees the `Authorizer` contract).
  Committed for CI to be green.
- `examples/orders/go.mod`: same core bump (it `replace`s back to root but must
  independently resolve core's new types). Casbin is **not** added to
  `examples/orders` this cycle (no example wiring — that is Phase C).

### 2.4 Documentation

- `auth/casbin/doc.go` — package overview, default `(sub, obj, act)` mapping,
  and the concurrency contract (§6).
- `CHANGELOG.md` — add the AuthZ Casbin adapter entry under `[Unreleased]`
  (lands as `v0.7.0` at the tag-gate).
- `README.md` — adapter inventory gains one row.
- `.agent/state.md` Open Items — add the v0.7.0 AuthZ cycle as in-flight at
  kickoff (§11).
- `.agent/decisions.md` / `.agent/review-log.md` / cross-repo
  `<workspace-root>/.agent-memory/go-ddd.md` — synced at end of cycle once the
  approach is verified by passing tests (matches prior-cycle pattern).

## 3. Out of scope (explicit follow-ups, NOT this PR)

- **HTTP enforcement middleware** (`transport/http/stdlib/authzmw` or similar) —
  middleware that calls `Allow` per request and maps the result onto an HTTP
  response. This is **Phase B**, a separate cycle. The point of this PR is to
  land the adapter and prove it via its own tests.
- **`examples/orders` AuthZ wiring** — demonstrating the authorizer in the
  example service. This is **Phase C**, a separate cycle. `examples/orders`
  does not depend on the new adapter yet.
- **OPA / Rego adapter.** Casbin is the chosen first engine (4 transitive
  modules vs OPA's ~126). A future `auth/opa` package can live alongside; the
  package path `auth/casbin/` is deliberately driver-named so it does not
  block other engines.
- **Adapter `go-ddd-core` v0.7.0 tag bump.** That is the gate step, not part of
  the implementation PR (see §10).
- **Changes to core's `ports/auth` contract.** If the `Authorizer` /
  `Resource` / sentinel surface is found insufficient mid-implementation, the
  hard rule from durable memory applies: push the change back into core first;
  do **NOT** grow a shim on the adapter side.

## 4. Non-goals

- A policy-authoring framework or model/policy loader. The adapter wraps an
  enforcer the caller has **already** built and loaded; it does not read
  `.conf` / `.csv` files, manage policy storage, or expose policy-management
  APIs.
- Owning the enforcer's concurrency. The adapter depends on the `Enforcer`
  interface and documents the requirement (§6); it does not add its own
  locking around a caller-supplied enforcer.
- Multi-tenant / domain resolution. The default builder produces the classic
  `(sub, obj, act)` triple. A `(sub, dom, obj, act)` or tenant-scoped mapping
  is achievable via `WithRequestBuilder` but is the caller's policy decision,
  not a built-in.

## 5. Surface design

### 5.1 The `Enforcer` interface (★ approved deviation from the literal proposal)

```go
package casbinauth

// Enforcer is the only Casbin behaviour this adapter depends on at the
// Allow runtime call path. Both *casbin.Enforcer and *casbin.SyncedEnforcer
// satisfy it (verified: both expose Enforce(rvals ...interface{}) (bool,
// error); `any` ≡ `interface{}`). Depending on the interface — not the
// concrete *casbin.Enforcer — keeps the concurrency choice with the caller
// (see §6) and lets unit tests substitute a fake without building a real
// Casbin model. Mirrors the repo convention of ConsumerModule taking
// eventbus.Subscriber rather than a concrete bus.
type Enforcer interface {
    Enforce(rvals ...any) (bool, error)
}
```

The original proposal was `New(enforcer *casbin.Enforcer, ...)` (concrete). The
interface form was reviewed and **approved**: `*casbin.Enforcer` satisfies it
implicitly, so caller call sites (`New(e, ...)`) are unchanged.

**Boundary nuance.** The *runtime* `Allow` path depends only on the `Enforcer`
interface. The package nonetheless imports the concrete
`github.com/casbin/casbin/v3` types in **one** place — the `New` typed-nil
guard (§5.2) — to type-switch on the two public Casbin types it documents. This
is a deliberate, narrow exception to "interface only", not a widening of the
runtime dependency.

### 5.2 Constructor + options (`authjwt` private-config pattern)

```go
// Authorizer wraps an Enforcer as a core auth.Authorizer. It is immutable
// after New: Allow reads enforcer + requestBuilder and mutates no internal
// state, so a single Authorizer is safe to share (subject to §6's enforcer
// requirement).
type Authorizer struct { /* enforcer Enforcer; requestBuilder RequestBuilder */ }

// Option configures the Authorizer built by New. Options are pure setters into
// the private config; all validation runs in New after every option is
// applied, so option order never changes the result. (authjwt pattern.)
type Option func(*config)

type config struct {
    requestBuilder RequestBuilder
}

// New constructs an Authorizer. Returns a constructor error (not a sentinel)
// when:
//   - enforcer is nil (including a typed-nil *casbin.Enforcer passed through
//     the interface — see guard note below);
//   - WithRequestBuilder(nil) was supplied.
// When no WithRequestBuilder is given, the default builder (§5.3) is used.
func New(enforcer Enforcer, opts ...Option) (*Authorizer, error)
```

**Constructor errors** are package-level errors (or `errorsx`-coded values)
with a `casbinauth:` message prefix — distinct from the core `auth` sentinels,
which are reserved for `Allow` return values. A nil-enforcer or nil-builder is
a wiring bug, surfaced loud at construction, never as a runtime denial.

**Typed-nil guard (explicit mechanism).** Because `Enforcer` is an interface, a
caller passing a nil `*casbin.Enforcer` produces a *non-nil* interface wrapping
a nil pointer, which `enforcer == nil` alone will not catch. New therefore:

1. rejects the plain-nil interface (`enforcer == nil`), then
2. type-switches on the two **concrete public Casbin types this adapter
   documents** and rejects a nil pointer of either:

   ```go
   switch e := enforcer.(type) {
   case *casbin.Enforcer:
       if e == nil { return nil, errNilEnforcer }
   case *casbin.SyncedEnforcer:
       if e == nil { return nil, errNilEnforcer }
   }
   ```

   Both cases are required: `*casbin.SyncedEnforcer` embeds `*casbin.Enforcer`
   but is a distinct dynamic type, so it does not match the `*casbin.Enforcer`
   case.

**No generic `reflect` guard.** A `reflect`-based "is this any nil pointer-like
value" check is deliberately **not** promised. The two concrete type-switch
cases cover exactly the public Casbin types the adapter documents and tests;
keeping reflect out preserves the repo's "avoid reflect unless needed" stance.
A caller's own custom `Enforcer` implementation passing a typed-nil is that
caller's bug to avoid — the adapter does not undertake to catch arbitrary
typed-nils. This guard mirrors the *intent* of the `authmw` `ErrNilVerifier`
two-shape guard, narrowed to a known, documented type set.

### 5.3 Default `RequestBuilder`

```go
// RequestBuilder translates core's (caller, action, resource) into the rvals
// passed to Enforcer.Enforce. ctx is supplied so a builder may consult
// request-scoped values (e.g. a tenant from context); the default ignores it.
type RequestBuilder func(ctx context.Context, caller auth.Identity, action string, resource auth.Resource) ([]any, error)

// WithRequestBuilder replaces the default builder. A nil fn makes New return a
// constructor error (fail loud — this is a wiring gate, not the nil-ignore
// convention used by extraction-name options elsewhere).
func WithRequestBuilder(fn RequestBuilder) Option
```

Default builder (classic Casbin `(sub, obj, act)` model):

```go
sub := caller.Subject
obj := resource.Type
if resource.ID != "" {
    obj = resource.Type + ":" + resource.ID   // ":" separator
}
act := action
return []any{sub, obj, act}, nil
```

`WithRequestBuilder` replaces the whole tuple, e.g. to emit `(sub, dom, obj,
act)` for a domain/tenant-scoped policy.

### 5.4 `Allow` control flow (fixed validation order)

```
Allow(ctx, caller, action, resource):
  1. zero Identity        → auth.ErrInvalidAuthorizationRequest
  2. action == ""         → auth.ErrInvalidAuthorizationRequest
  3. resource.Type == ""  → auth.ErrInvalidAuthorizationRequest
  4. ctx.Err() != nil     → return ctx.Err() verbatim (not deny / not invalid)
  5. rvals, err := builder(ctx, caller, action, resource)
       err != nil          → return err verbatim (NOT wrapped as forbidden)
       len(rvals) == 0      → auth.ErrInvalidAuthorizationRequest
  6. ok, err := enforcer.Enforce(rvals...)
       err != nil           → return err verbatim (engine error ≠ deny)
       !ok                  → auth.ErrForbidden
       ok                   → nil
```

- **Malformed-input checks (1–3) precede the ctx check (4)**, matching
  `authjwt`'s empty-token-before-ctx order: a caller bug must not be masked by
  a cancelled context.
- **zero Identity** is judged by all fields being zero:
  `Subject == "" && TenantID == "" && len(Roles) == 0 && len(Claims) == 0`.
  Per core doc, a zero Identity is malformed input, **not** an anonymous
  principal.
- `Allow` reads `caller` to build the tuple and never mutates it (honours
  core's read-only-borrow contract). The adapter does not retain `caller`
  beyond the call, so no `Clone` is required.

## 6. Concurrency & ctx contract (written into `doc.go`)

The wording is precise about the boundary between what the adapter guarantees
and what it requires of the caller — it does **not** downgrade core's "safe for
concurrent use" requirement, it states the external precondition that makes it
true:

- `Authorizer.Allow` mutates no internal state; the `Authorizer` itself is
  immutable after `New` and safe to share across goroutines.
- `New` **requires** that the supplied `Enforcer.Enforce` is safe to call
  concurrently.
- If the policy/model is reloaded or mutated at runtime, the caller should pass
  a `*casbin.SyncedEnforcer` (its `Enforce` takes an `RLock`) or otherwise
  synchronise access.
- A plain `*casbin.Enforcer` (no internal lock) is only appropriate when the
  model/policy is **not** modified after construction.

`Allow` honours `ctx` cancellation by checking `ctx.Err()` before invoking the
enforcer (step 4). Casbin's `Enforce` is itself synchronous and ctx-unaware, so
the adapter's contribution is the pre-call check; this matches the contract's
"must honour ctx cancellation" without pretending the engine is interruptible
mid-evaluation.

## 7. Error mapping

| Situation | Return | HTTP (via errorsx/httpx) |
|---|---|---|
| Allowed (`Enforce` → true) | `nil` | 200 |
| Policy denies (`Enforce` → false) | `auth.ErrForbidden` | 403 |
| zero Identity / empty action / empty `Resource.Type` / builder empty tuple | `auth.ErrInvalidAuthorizationRequest` | 400 |
| ctx cancelled | `ctx.Err()` verbatim | (caller handles) |
| builder error | original error verbatim | coded → mapped; uncoded → 500 |
| `Enforce` engine error | original error verbatim | uncoded → 500 |
| nil enforcer / `WithRequestBuilder(nil)` | constructor error (`casbinauth:` prefix) | n/a (construction-time) |

No per-adapter status table: the `auth` sentinels are tamper-proof
`codedError`s that map through `pkg/errorsx/httpx` automatically. The adapter
only returns those sentinels (for its two mapped cases) or passes engine/ctx
errors through unchanged.

## 8. Test plan

### 8.1 Unit tests (`*_test.go`, default build, fake `Enforcer`)

| Test | What it verifies |
|---|---|
| `TestAllow_Allowed` | Fake enforcer returns `(true, nil)` → `Allow` returns `nil`. |
| `TestAllow_Denied` | Fake returns `(false, nil)` → `Allow` returns `auth.ErrForbidden` (assert via `errors.Is`). |
| `TestAllow_ZeroIdentity` | Zero `Identity{}` → `auth.ErrInvalidAuthorizationRequest`; enforcer never called. |
| `TestAllow_EmptyAction` | `action == ""` → `ErrInvalidAuthorizationRequest`; enforcer never called. |
| `TestAllow_EmptyResourceType` | `Resource{Type:""}` → `ErrInvalidAuthorizationRequest`; enforcer never called. |
| `TestAllow_MalformedBeforeCtxCancel` | Cancelled ctx **and** empty action → `ErrInvalidAuthorizationRequest` (malformed wins over ctx). |
| `TestAllow_CtxCancelled` | Valid input + cancelled ctx → `ctx.Err()` verbatim; enforcer never called. |
| `TestAllow_EngineError` | Fake returns `(false, someErr)` → `Allow` returns `someErr` verbatim (NOT `ErrForbidden`). |
| `TestAllow_DefaultBuilderTuple` | Fake captures rvals; assert `{sub, "order:42", act}` for `Resource{Type:"order", ID:"42"}` and `{sub, "order", act}` for empty ID. |
| `TestAllow_BuilderOverride` | `WithRequestBuilder` emits a 4-tuple; assert captured rvals + that override path is taken. |
| `TestAllow_BuilderError` | Custom builder returns `(nil, err)` → `Allow` returns `err` verbatim. |
| `TestAllow_BuilderEmptyTuple` | Custom builder returns `([]any{}, nil)` → `ErrInvalidAuthorizationRequest`. |
| `TestNew_NilEnforcer` | `New(nil)`, `New((*casbin.Enforcer)(nil))`, and `New((*casbin.SyncedEnforcer)(nil))` all return a constructor error (covers the plain-nil interface + both documented typed-nil cases). |
| `TestNew_NilBuilder` | `New(fake, WithRequestBuilder(nil))` returns a constructor error. |

`export_test.go` white-box seam for the default builder is **optional** — if
the default tuple can be asserted via public `Allow` + a fake enforcer that
captures rvals (it can, per `TestAllow_DefaultBuilderTuple`), no white-box seam
is added. Decided at implementation time; not a design blocker.

### 8.2 Integration test (build tag `integration`, real Casbin)

`auth/casbin/integration/enforce_test.go` (or in-package with a build tag):

- Build a real `*casbin.Enforcer` from an embedded RBAC model string + a small
  policy (e.g. `alice` may `read` `order`, denied `write`).
- Wrap with `casbinauth.New`; assert `Allow` returns `nil` for the permitted
  tuple and `auth.ErrForbidden` for the denied one.
- Confirms the default builder's tuple shape actually matches a classic Casbin
  `(sub, obj, act)` model end-to-end.

### 8.3 Race test (narrow claim)

`TestAllow_ConcurrentReadOnly` (default build, `go test -race`):

- Wrap a `*casbin.SyncedEnforcer` (no policy reload/mutation during the test)
  and fire `Allow` from N goroutines.
- **Claim under test:** concurrent **read-only** `Allow` calls are race-free —
  i.e. the adapter adds no shared mutable state of its own on the call path.
- **Explicitly NOT under test:** safety under concurrent policy reload/mutation.
  That is delegated to Casbin's own `SyncedEnforcer` contract (its `Enforce`
  takes an `RLock`); the adapter does not re-test Casbin internals. §6's
  reload guidance points the caller at `SyncedEnforcer` precisely so this
  concern stays inside Casbin, not the adapter.

### 8.4 Verification matrix (mandatory before PR)

Root module:

- `go build ./...`
- `go vet ./...`
- `go test ./...`
- `go test -race ./...`
- `golangci-lint run ./...`
- `go test -tags=integration ./...`
- `golangci-lint run --build-tags=integration ./...`

`examples/orders` module (core bump only, no Casbin wiring this cycle):

- `go build ./...`
- `go vet ./...`
- `go test ./...`
- `golangci-lint run ./...`
- `golangci-lint run --build-tags=integration ./...`

## 9. Key invariants

1. **Adapter never imports `examples/`.** It is downstream-consumable code.
2. **No shim around core.** If `ports/auth` is found insufficient
   mid-implementation, the cycle pauses and the change is pushed to core first.
   Recorded in durable memory and `.agent/decisions.md`.
3. **Engine/ctx/builder errors are never disguised as denial.** Only an
   explicit `Enforce → false` becomes `ErrForbidden`. A failed decision
   surfaces as the original error so a 503/500 is not masked as a 403.
4. **Malformed input fails as 400, before ctx and before the engine.** A
   caller bug is never masked as a policy denial or a cancellation.
5. **Concurrency safety is conditional on the caller's enforcer**, and that
   condition is documented, not silently assumed.

## 10. Tag-step gate (last action of the joint cycle)

Mirrors the durable-memory sequence ("Tag at the last piece of the cycle, not
the first"):

1. Adapter PR merged on `main`, CI green at the pseudo-version pin
   (core `47e02fa`).
2. Core decides the version (proposed **v0.7.0**) and tags it on its `main`
   (annotated, pushed); core's `[Unreleased]` CHANGELOG lands as v0.7.0.
3. Adapter `go.mod` is bumped from the pseudo-version to `v0.7.0`;
   `examples/orders/go.mod` likewise. A bookkeeping commit lands on `main`.
4. Adapter CI green at the bumped tip → **then** annotate + push the adapter's
   `v0.7.0` tag and open the GitHub Latest Release.

Steps 2–4 are explicitly out of scope of the implementation PR — they are the
release ritual after merge. **v0.7.0 is independent of the v0.6.0 AuthN cycle**
(already closed/tagged); the version semantics stay clean: v0.6.x is reserved
for AuthN/JWT/Bearer bugfix or compatible additions, v0.7.0 introduces AuthZ.

## 11. Rejected alternatives

- **Concrete `*casbin.Enforcer` in `New`.** Locks the adapter to one concurrency
  profile and forces a real Casbin model into every unit test. Rejected in
  favour of the one-method `Enforcer` interface (§5.1).
- **`Option func(*Authorizer)` mutating the constructed type.** Clashes with the
  "immutable after New" semantics and diverges from the `authjwt` private-config
  pattern. Rejected in favour of `Option func(*config)` (§5.2).
- **Generic `reflect`-based typed-nil guard.** Would catch any nil pointer-like
  `Enforcer` implementation, but pulls in `reflect` against the repo's
  "avoid reflect unless needed" stance for a case the two-type switch already
  covers. Rejected in favour of the explicit `*casbin.Enforcer` /
  `*casbin.SyncedEnforcer` type switch (§5.2).
- **Wrapping builder/engine errors as `ErrForbidden`.** Would mask an
  unavailable policy engine (503) or a wiring bug as an access denial (403) —
  the exact failure-disguise the contract warns against. Rejected (§9.3).
- **Adapter-owned locking around the enforcer.** Pretends to fix a concurrency
  problem the adapter cannot see (caller may mutate the enforcer externally).
  Rejected; the contract states the precondition instead (§6).
- **OPA/Rego as the first engine.** ~126 transitive modules vs Casbin's 4;
  fails the repo's low-dependency bar for a first consumer. Deferred to a
  future `auth/opa` package.
- **Bundling Phase B (HTTP middleware) / Phase C (examples wiring) into this
  PR.** Bloats the release PR and couples adapter correctness to transport and
  example concerns. Split into follow-up cycles (§3).

## 12. Adapter-side `.agent/state.md` touches at kickoff (this commit, before any code)

- Add to Open Items:
  ```
  - [ ] v0.7.0 auth/casbin AuthZ adapter (Phase A)
        Branch: feat/authz-casbin-v0.7.0
        Spec:   docs/superpowers/specs/2026-06-05-authz-casbin-adapter-design.md
        Plan:   docs/superpowers/plans/2026-06-05-authz-casbin-adapter.md (TBD)
        Status: kickoff complete, spec drafted, plan TBD
  ```
- No change to existing Open Items.
- No change to `.agent/decisions.md` yet (decisions land at end of cycle once
  the approach is verified by passing tests, matching the prior-cycle pattern).
