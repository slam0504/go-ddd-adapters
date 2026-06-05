# v0.7.0 `auth/casbin` AuthZ Adapter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship `auth/casbin` (package `casbinauth`), the first adapter consumer of `go-ddd-core`'s `ports/auth.Authorizer` contract, by wrapping a caller-built Casbin enforcer behind a minimal one-method interface.

**Architecture:** A `casbinauth.Authorizer` holds an immutable `Enforcer` (a one-method interface that both `*casbin.Enforcer` and `*casbin.SyncedEnforcer` satisfy) plus a `RequestBuilder`. `Allow` validates input (malformed → `ErrInvalidAuthorizationRequest`, before a ctx check), builds the Casbin rvals via the builder (default `(sub, obj, act)` with `Type:ID` object encoding), calls `Enforce`, and maps `false → ErrForbidden` / engine+builder+ctx errors pass through verbatim. Concurrency safety is conditional on the caller's enforcer and is documented, not enforced by the adapter.

**Tech Stack:** Go 1.25, `github.com/casbin/casbin/v3 v3.10.0`, `github.com/slam0504/go-ddd-core` (`ports/auth`), standard `testing` (no testcontainers — Casbin is in-process).

**Spec:** `docs/superpowers/specs/2026-06-05-authz-casbin-adapter-design.md`
**Branch:** `feat/authz-casbin-v0.7.0` (already checked out; spec committed at `8975a0c`)

---

## File structure

| File | Responsibility |
|---|---|
| `auth/casbin/options.go` | `Option`, private `config`, `RequestBuilder` type, `WithRequestBuilder` |
| `auth/casbin/authorizer.go` | `Enforcer` interface, `Authorizer`, `New` (nil/typed-nil guards), `Allow`, `defaultRequestBuilder`, `isZeroIdentity`, constructor error vars, interface assertion |
| `auth/casbin/doc.go` | Package doc: purpose, default mapping, concurrency contract |
| `auth/casbin/authorizer_test.go` | `package casbinauth_test` — fake-`Enforcer` unit tests for `New` + `Allow` (all branches), default-builder encoding, `WithRequestBuilder` override, and the read-only race test (real `*casbin.SyncedEnforcer`) |
| `auth/casbin/integration_test.go` | `//go:build integration` — real `*casbin.Enforcer` + embedded model/policy, allow/deny end-to-end |

All test files use the external `casbinauth_test` package (black-box). No `export_test.go` seam is needed: the default tuple is asserted via the public `Allow` plus a fake enforcer that captures rvals.

---

## Task 1: Dependencies + kickoff bookkeeping

**Files:**
- Modify: `go.mod`, `go.sum` (root)
- Modify: `examples/orders/go.mod`, `examples/orders/go.sum`
- Modify: `.agent/state.md`

- [ ] **Step 1: Add Casbin and bump core in the root module**

Run from the repo root (`/Users/eason_tseng/playground/project/go-ddd-adapters`):

```bash
go get github.com/casbin/casbin/v3@v3.10.0
go get github.com/slam0504/go-ddd-core@47e02fa
go mod tidy
```

Expected: `go.mod` gains `github.com/casbin/casbin/v3 v3.10.0` in the primary `require` block, and the `github.com/slam0504/go-ddd-core` line changes from `v0.6.0` to a pseudo-version of the form `v0.6.1-0.<UTC-timestamp>-47e02fa<...>`. (`47e02fa` is core `main` HEAD carrying the merged `Authorizer` contract.) Casbin pulls ~4 transitive modules.

- [ ] **Step 2: Bump core in the examples/orders module**

The `examples/orders` module has its own core pin; bump it so its MVS stays consistent with the root (it `replace`s adapters → `../..`).

```bash
cd examples/orders
go get github.com/slam0504/go-ddd-core@47e02fa
go mod tidy
cd ../..
```

Expected: `examples/orders/go.mod` core pin moves to the same pseudo-version. Casbin is NOT added here (no example wiring this cycle).

- [ ] **Step 3: Verify both modules still build**

```bash
go build ./...
( cd examples/orders && go build ./... )
```

Expected: both succeed with no errors (no new code yet; this confirms the dependency graph resolves).

- [ ] **Step 4: Record cycle kickoff in `.agent/state.md`**

Open `.agent/state.md` and insert the following section immediately **before** the `## Current Branch` heading (currently around line 70, right after the v0.6.0 AuthN cycle section):

```markdown
## v0.7.0 AuthZ cycle (IN FLIGHT — kickoff 2026-06-05)

First consumer of core's `ports/auth.Authorizer` contract (core `main`
HEAD `47e02fa`, not yet tagged). Phase A only.

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
- Status: kickoff complete, spec approved, plan drafted.
```

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum examples/orders/go.mod examples/orders/go.sum .agent/state.md
git commit -m "$(cat <<'EOF'
chore(authz): add casbin v3 dep, bump core to AuthZ contract, kickoff state

Adds github.com/casbin/casbin/v3 v3.10.0 and bumps go-ddd-core from v0.6.0
to the pseudo-version of core main 47e02fa (the merged Authorizer contract)
on both root and examples/orders. Records the v0.7.0 AuthZ cycle kickoff.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: `casbinauth` package — Enforcer, Authorizer, New, Allow

**Files:**
- Test: `auth/casbin/authorizer_test.go`
- Create: `auth/casbin/options.go`
- Create: `auth/casbin/authorizer.go`
- Create: `auth/casbin/doc.go`

- [ ] **Step 1: Write the failing unit-test suite**

Create `auth/casbin/authorizer_test.go`:

```go
package casbinauth_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/casbin/casbin/v3"
	casbinauth "github.com/slam0504/go-ddd-adapters/auth/casbin"
	"github.com/slam0504/go-ddd-core/ports/auth"
)

// fakeEnforcer implements casbinauth.Enforcer for black-box unit tests. It
// records the rvals it was called with and returns a scripted decision.
type fakeEnforcer struct {
	ok       bool
	err      error
	gotRvals []any
	calls    int
}

func (f *fakeEnforcer) Enforce(rvals ...any) (bool, error) {
	f.calls++
	f.gotRvals = rvals
	return f.ok, f.err
}

func validIdentity() auth.Identity { return auth.Identity{Subject: "alice"} }

func TestNew_NilEnforcer(t *testing.T) {
	cases := map[string]casbinauth.Enforcer{
		"plain nil interface":      nil,
		"typed-nil Enforcer":       (*casbin.Enforcer)(nil),
		"typed-nil SyncedEnforcer": (*casbin.SyncedEnforcer)(nil),
	}
	for name, enf := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := casbinauth.New(enf)
			if !errors.Is(err, casbinauth.ErrNilEnforcer) {
				t.Fatalf("New(%s): got err %v, want ErrNilEnforcer", name, err)
			}
		})
	}
}

func TestNew_NilRequestBuilder(t *testing.T) {
	_, err := casbinauth.New(&fakeEnforcer{}, casbinauth.WithRequestBuilder(nil))
	if !errors.Is(err, casbinauth.ErrNilRequestBuilder) {
		t.Fatalf("got err %v, want ErrNilRequestBuilder", err)
	}
}

func TestAllow_Allowed(t *testing.T) {
	f := &fakeEnforcer{ok: true}
	az, err := casbinauth.New(f)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := az.Allow(context.Background(), validIdentity(), "read", auth.Resource{Type: "order"}); err != nil {
		t.Fatalf("Allow: got %v, want nil", err)
	}
}

func TestAllow_Denied(t *testing.T) {
	f := &fakeEnforcer{ok: false}
	az, _ := casbinauth.New(f)
	err := az.Allow(context.Background(), validIdentity(), "read", auth.Resource{Type: "order"})
	if !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("Allow: got %v, want ErrForbidden", err)
	}
}

func TestAllow_EngineError(t *testing.T) {
	sentinel := errors.New("policy store unavailable")
	f := &fakeEnforcer{ok: false, err: sentinel}
	az, _ := casbinauth.New(f)
	err := az.Allow(context.Background(), validIdentity(), "read", auth.Resource{Type: "order"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Allow: got %v, want the engine error verbatim", err)
	}
	if errors.Is(err, auth.ErrForbidden) {
		t.Fatal("Allow: engine error must not be disguised as ErrForbidden")
	}
}

func TestAllow_MalformedInput(t *testing.T) {
	cases := map[string]struct {
		id       auth.Identity
		action   string
		resource auth.Resource
	}{
		"zero identity":      {auth.Identity{}, "read", auth.Resource{Type: "order"}},
		"empty action":       {validIdentity(), "", auth.Resource{Type: "order"}},
		"empty resource type": {validIdentity(), "read", auth.Resource{Type: ""}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakeEnforcer{ok: true}
			az, _ := casbinauth.New(f)
			err := az.Allow(context.Background(), c.id, c.action, c.resource)
			if !errors.Is(err, auth.ErrInvalidAuthorizationRequest) {
				t.Fatalf("Allow: got %v, want ErrInvalidAuthorizationRequest", err)
			}
			if f.calls != 0 {
				t.Fatalf("enforcer was called %d times; malformed input must short-circuit", f.calls)
			}
		})
	}
}

func TestAllow_MalformedBeforeCtxCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &fakeEnforcer{ok: true}
	az, _ := casbinauth.New(f)
	// Empty action AND cancelled ctx: malformed input must win (400, not ctx err).
	err := az.Allow(ctx, validIdentity(), "", auth.Resource{Type: "order"})
	if !errors.Is(err, auth.ErrInvalidAuthorizationRequest) {
		t.Fatalf("Allow: got %v, want ErrInvalidAuthorizationRequest (malformed before ctx)", err)
	}
}

func TestAllow_CtxCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &fakeEnforcer{ok: true}
	az, _ := casbinauth.New(f)
	err := az.Allow(ctx, validIdentity(), "read", auth.Resource{Type: "order"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Allow: got %v, want context.Canceled verbatim", err)
	}
	if f.calls != 0 {
		t.Fatalf("enforcer was called %d times; cancelled ctx must short-circuit", f.calls)
	}
}

func TestAllow_DefaultBuilderTuple(t *testing.T) {
	cases := map[string]struct {
		resource auth.Resource
		wantObj  string
	}{
		"collection-level (empty ID)": {auth.Resource{Type: "order"}, "order"},
		"instance-level (Type:ID)":    {auth.Resource{Type: "order", ID: "42"}, "order:42"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakeEnforcer{ok: true}
			az, _ := casbinauth.New(f)
			if err := az.Allow(context.Background(), auth.Identity{Subject: "alice"}, "read", c.resource); err != nil {
				t.Fatalf("Allow: %v", err)
			}
			want := []any{"alice", c.wantObj, "read"}
			if !reflect.DeepEqual(f.gotRvals, want) {
				t.Fatalf("rvals = %#v, want %#v", f.gotRvals, want)
			}
		})
	}
}

func TestAllow_BuilderOverride(t *testing.T) {
	f := &fakeEnforcer{ok: true}
	// 4-tuple (sub, dom, obj, act) using TenantID as the domain.
	builder := func(_ context.Context, caller auth.Identity, action string, resource auth.Resource) ([]any, error) {
		return []any{caller.Subject, caller.TenantID, resource.Type, action}, nil
	}
	az, err := casbinauth.New(f, casbinauth.WithRequestBuilder(builder))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	id := auth.Identity{Subject: "alice", TenantID: "acme"}
	if err := az.Allow(context.Background(), id, "read", auth.Resource{Type: "order"}); err != nil {
		t.Fatalf("Allow: %v", err)
	}
	want := []any{"alice", "acme", "order", "read"}
	if !reflect.DeepEqual(f.gotRvals, want) {
		t.Fatalf("rvals = %#v, want %#v", f.gotRvals, want)
	}
}

func TestAllow_BuilderError(t *testing.T) {
	sentinel := errors.New("builder failed")
	builder := func(_ context.Context, _ auth.Identity, _ string, _ auth.Resource) ([]any, error) {
		return nil, sentinel
	}
	f := &fakeEnforcer{ok: true}
	az, _ := casbinauth.New(f, casbinauth.WithRequestBuilder(builder))
	err := az.Allow(context.Background(), validIdentity(), "read", auth.Resource{Type: "order"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Allow: got %v, want builder error verbatim", err)
	}
	if f.calls != 0 {
		t.Fatalf("enforcer was called %d times; a builder error must short-circuit", f.calls)
	}
}

func TestAllow_BuilderEmptyTuple(t *testing.T) {
	builder := func(_ context.Context, _ auth.Identity, _ string, _ auth.Resource) ([]any, error) {
		return []any{}, nil
	}
	f := &fakeEnforcer{ok: true}
	az, _ := casbinauth.New(f, casbinauth.WithRequestBuilder(builder))
	err := az.Allow(context.Background(), validIdentity(), "read", auth.Resource{Type: "order"})
	if !errors.Is(err, auth.ErrInvalidAuthorizationRequest) {
		t.Fatalf("Allow: got %v, want ErrInvalidAuthorizationRequest", err)
	}
	if f.calls != 0 {
		t.Fatalf("enforcer was called %d times; an empty tuple must short-circuit", f.calls)
	}
}
```

- [ ] **Step 2: Run the suite to verify it fails**

Run: `go test ./auth/casbin/...`
Expected: FAIL — the package `auth/casbin` does not exist yet (build error: `no Go files` / `undefined: casbinauth.New`).

- [ ] **Step 3: Create `auth/casbin/options.go`**

```go
package casbinauth

import (
	"context"

	"github.com/slam0504/go-ddd-core/ports/auth"
)

// RequestBuilder translates core's (caller, action, resource) into the rvals
// passed to Enforcer.Enforce. ctx is supplied so a builder may consult
// request-scoped values (e.g. a tenant pulled from context); the default
// builder ignores it. A builder that returns a non-nil error aborts the
// decision and the error is returned verbatim by Allow; a builder that returns
// an empty slice signals malformed input (Allow maps it to
// ErrInvalidAuthorizationRequest and never calls the enforcer).
type RequestBuilder func(ctx context.Context, caller auth.Identity, action string, resource auth.Resource) ([]any, error)

// Option configures the Authorizer built by New. Options are pure setters into
// the private config; all validation runs in New after every option is
// applied, so option order never changes the result. (Mirrors the authjwt
// private-config pattern.)
type Option func(*config)

type config struct {
	requestBuilder RequestBuilder
}

// WithRequestBuilder replaces the default (sub, obj, act) builder. Passing a
// nil fn makes New return ErrNilRequestBuilder (fail loud — this is a wiring
// gate, not a nil-ignore convention).
func WithRequestBuilder(fn RequestBuilder) Option {
	return func(c *config) {
		c.requestBuilder = fn
	}
}
```

- [ ] **Step 4: Create `auth/casbin/authorizer.go`**

```go
package casbinauth

import (
	"context"
	"errors"

	"github.com/casbin/casbin/v3"
	"github.com/slam0504/go-ddd-core/ports/auth"
)

// Enforcer is the only Casbin behaviour this adapter depends on at the Allow
// runtime call path. Both *casbin.Enforcer and *casbin.SyncedEnforcer satisfy
// it. Depending on the interface — not the concrete *casbin.Enforcer — keeps
// the concurrency choice with the caller (see doc.go) and lets unit tests
// substitute a fake without building a real Casbin model.
type Enforcer interface {
	Enforce(rvals ...any) (bool, error)
}

// Constructor errors. These surface wiring bugs at New time and are never
// returned from Allow (Allow returns only the core auth sentinels, ctx errors,
// or passed-through engine/builder errors).
var (
	ErrNilEnforcer       = errors.New("casbinauth: enforcer must not be nil")
	ErrNilRequestBuilder = errors.New("casbinauth: request builder must not be nil")
)

// Authorizer wraps an Enforcer as a core auth.Authorizer. It is immutable after
// New: Allow reads enforcer + requestBuilder and mutates no internal state, so
// a single Authorizer is safe to share across goroutines (subject to the
// enforcer requirement documented in doc.go).
type Authorizer struct {
	enforcer       Enforcer
	requestBuilder RequestBuilder
}

var _ auth.Authorizer = (*Authorizer)(nil)

// New wraps enforcer as an auth.Authorizer. It returns ErrNilEnforcer for a nil
// interface or a typed-nil *casbin.Enforcer / *casbin.SyncedEnforcer, and
// ErrNilRequestBuilder if WithRequestBuilder(nil) was supplied. With no
// WithRequestBuilder, the default (sub, obj, act) builder is used.
func New(enforcer Enforcer, opts ...Option) (*Authorizer, error) {
	if enforcer == nil {
		return nil, ErrNilEnforcer
	}
	// An interface holding a typed-nil pointer is itself non-nil, so the check
	// above does not catch New((*casbin.Enforcer)(nil)). Type-switch on the two
	// public Casbin types this adapter documents and reject a nil pointer of
	// either. *casbin.SyncedEnforcer embeds *casbin.Enforcer but is a distinct
	// dynamic type, so both cases are required.
	switch e := enforcer.(type) {
	case *casbin.Enforcer:
		if e == nil {
			return nil, ErrNilEnforcer
		}
	case *casbin.SyncedEnforcer:
		if e == nil {
			return nil, ErrNilEnforcer
		}
	}

	cfg := &config{requestBuilder: defaultRequestBuilder}
	for _, opt := range opts {
		opt(cfg)
	}
	if cfg.requestBuilder == nil {
		return nil, ErrNilRequestBuilder
	}

	return &Authorizer{
		enforcer:       enforcer,
		requestBuilder: cfg.requestBuilder,
	}, nil
}

// Allow reports whether caller may perform action on resource, per the core
// ports/auth.Authorizer contract. Validation order: malformed input (zero
// Identity / empty action / empty Resource.Type) is rejected before the ctx
// check, so a caller bug is never masked by a cancelled context.
func (a *Authorizer) Allow(ctx context.Context, caller auth.Identity, action string, resource auth.Resource) error {
	if isZeroIdentity(caller) {
		return auth.ErrInvalidAuthorizationRequest
	}
	if action == "" {
		return auth.ErrInvalidAuthorizationRequest
	}
	if resource.Type == "" {
		return auth.ErrInvalidAuthorizationRequest
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	rvals, err := a.requestBuilder(ctx, caller, action, resource)
	if err != nil {
		return err
	}
	if len(rvals) == 0 {
		return auth.ErrInvalidAuthorizationRequest
	}

	ok, err := a.enforcer.Enforce(rvals...)
	if err != nil {
		return err
	}
	if !ok {
		return auth.ErrForbidden
	}
	return nil
}

// isZeroIdentity reports whether id is the zero Identity. Per the core contract
// a zero Identity is malformed input, not an anonymous principal.
func isZeroIdentity(id auth.Identity) bool {
	return id.Subject == "" && id.TenantID == "" && len(id.Roles) == 0 && len(id.Claims) == 0
}

// defaultRequestBuilder produces the classic Casbin (sub, obj, act) triple. A
// resource with a non-empty ID is encoded as "Type:ID"; a collection-level
// resource (empty ID) uses just "Type".
func defaultRequestBuilder(_ context.Context, caller auth.Identity, action string, resource auth.Resource) ([]any, error) {
	obj := resource.Type
	if resource.ID != "" {
		obj = resource.Type + ":" + resource.ID
	}
	return []any{caller.Subject, obj, action}, nil
}
```

- [ ] **Step 5: Create `auth/casbin/doc.go`**

```go
// Package casbinauth adapts a Casbin enforcer to the go-ddd-core
// ports/auth.Authorizer contract. It wraps an enforcer the caller has already
// built and loaded; it does not read model/policy files or manage policy
// storage.
//
// # Request mapping
//
// By default Allow maps the core (caller, action, resource) into the classic
// Casbin (sub, obj, act) triple:
//
//	sub = caller.Subject
//	obj = resource.Type            // when resource.ID == ""
//	obj = resource.Type + ":" + resource.ID  // when resource.ID != ""
//	act = action
//
// WithRequestBuilder replaces this mapping wholesale, e.g. to emit a
// (sub, dom, obj, act) tuple for a domain/tenant-scoped policy.
//
// # Error mapping
//
//	nil                              allowed (Enforce returned true).
//	auth.ErrForbidden                policy denied (Enforce returned false) → 403.
//	auth.ErrInvalidAuthorizationRequest  malformed input (zero Identity, empty
//	                                 action, empty Resource.Type, or a builder
//	                                 that returned an empty tuple) → 400.
//	ctx.Err()                        the context was cancelled before evaluation.
//	any other error                  the decision could not be made; the engine
//	                                 or builder error is returned verbatim and
//	                                 never disguised as a denial.
//
// # Concurrency
//
// Authorizer.Allow mutates no internal state; the Authorizer is immutable after
// New and safe to share across goroutines. New requires that the supplied
// Enforcer.Enforce is safe to call concurrently. If the policy/model is
// reloaded or mutated at runtime, pass a *casbin.SyncedEnforcer (its Enforce
// takes an RLock) or otherwise synchronise access. A plain *casbin.Enforcer
// (no internal lock) is only appropriate when the model/policy is not modified
// after construction. Allow honours ctx cancellation by checking ctx.Err()
// before invoking the enforcer; Casbin's Enforce is synchronous and not
// interruptible mid-evaluation.
package casbinauth
```

- [ ] **Step 6: Run the suite to verify it passes**

Run: `go test ./auth/casbin/...`
Expected: PASS — all `TestNew_*` and `TestAllow_*` subtests green.

- [ ] **Step 7: Vet + race the new package**

Run:
```bash
go vet ./auth/casbin/...
go test -race ./auth/casbin/...
```
Expected: both clean (the unit tests have no concurrency yet; this confirms `-race` builds the package).

- [ ] **Step 8: Commit**

```bash
git add auth/casbin/options.go auth/casbin/authorizer.go auth/casbin/doc.go auth/casbin/authorizer_test.go
git commit -m "$(cat <<'EOF'
feat(authz): add auth/casbin Authorizer adapter

casbinauth wraps a caller-built Casbin enforcer behind a one-method Enforcer
interface as a core ports/auth.Authorizer. Default (sub, obj, act) builder with
Type:ID object encoding; malformed input → ErrInvalidAuthorizationRequest
(before the ctx check); deny → ErrForbidden; engine/ctx/builder errors passed
through verbatim. New rejects nil and typed-nil casbin enforcers.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Read-only concurrency (race) test

**Files:**
- Modify: `auth/casbin/authorizer_test.go` (append)

- [ ] **Step 1: Append the race test**

Add to the bottom of `auth/casbin/authorizer_test.go`. Note the new imports `sync` and `.../casbin/v3/model` — add them to the existing import block.

```go
// aclModel is the classic Casbin (sub, obj, act) ACL/RBAC request model used by
// the concurrency and integration tests.
const aclModel = `
[request_definition]
r = sub, obj, act

[policy_definition]
p = sub, obj, act

[policy_effect]
e = some(where (p.eft == allow))

[matchers]
m = r.sub == p.sub && r.obj == p.obj && r.act == p.act
`

func TestAllow_ConcurrentReadOnly(t *testing.T) {
	m, err := model.NewModelFromString(aclModel)
	if err != nil {
		t.Fatalf("NewModelFromString: %v", err)
	}
	se, err := casbin.NewSyncedEnforcer(m)
	if err != nil {
		t.Fatalf("NewSyncedEnforcer: %v", err)
	}
	if _, err := se.AddPolicy("alice", "order", "read"); err != nil {
		t.Fatalf("AddPolicy: %v", err)
	}

	az, err := casbinauth.New(se)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Claim under test: concurrent read-only Allow calls are race-free (the
	// adapter adds no shared mutable state on the call path). NOT under test:
	// safety under concurrent policy reload/mutation — delegated to Casbin's
	// SyncedEnforcer contract.
	caller := auth.Identity{Subject: "alice"}
	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			if err := az.Allow(context.Background(), caller, "read", auth.Resource{Type: "order"}); err != nil {
				t.Errorf("Allow: unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
}
```

- [ ] **Step 2: Run with the race detector to verify it passes**

Run: `go test -race -run TestAllow_ConcurrentReadOnly ./auth/casbin/...`
Expected: PASS with no `DATA RACE` report.

- [ ] **Step 3: Run the full package suite again**

Run: `go test ./auth/casbin/...`
Expected: PASS (all unit tests + the new race test).

- [ ] **Step 4: Commit**

```bash
git add auth/casbin/authorizer_test.go
git commit -m "$(cat <<'EOF'
test(authz): cover concurrent read-only Allow with SyncedEnforcer

Proves the adapter adds no shared mutable state on the Allow call path under
-race. Reload/mutation safety is delegated to Casbin's SyncedEnforcer contract.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Integration test (real Casbin, build-tagged)

**Files:**
- Create: `auth/casbin/integration_test.go`

- [ ] **Step 1: Write the build-tagged integration test**

Create `auth/casbin/integration_test.go`. The first line MUST be the build tag, followed by a blank line, then the package clause. It reuses the `aclModel` constant defined in `authorizer_test.go` (same package, `casbinauth_test`).

```go
//go:build integration

package casbinauth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/casbin/casbin/v3"
	"github.com/casbin/casbin/v3/model"
	casbinauth "github.com/slam0504/go-ddd-adapters/auth/casbin"
	"github.com/slam0504/go-ddd-core/ports/auth"
)

func TestIntegration_AllowDeny(t *testing.T) {
	m, err := model.NewModelFromString(aclModel)
	if err != nil {
		t.Fatalf("NewModelFromString: %v", err)
	}
	e, err := casbin.NewEnforcer(m)
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}
	// In-memory policy: alice may read orders, nothing else.
	if _, err := e.AddPolicy("alice", "order", "read"); err != nil {
		t.Fatalf("AddPolicy: %v", err)
	}

	az, err := casbinauth.New(e)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()
	alice := auth.Identity{Subject: "alice"}

	// Permitted: the default builder's (alice, order, read) tuple matches the
	// policy end-to-end through a real Casbin enforcer.
	if err := az.Allow(ctx, alice, "read", auth.Resource{Type: "order"}); err != nil {
		t.Fatalf("Allow read: got %v, want nil", err)
	}

	// Denied: no policy grants write.
	if err := az.Allow(ctx, alice, "write", auth.Resource{Type: "order"}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("Allow write: got %v, want ErrForbidden", err)
	}
}
```

- [ ] **Step 2: Run to verify it fails under the default build (excluded), passes with the tag**

Run (default build — the test is excluded, so this just confirms the tag works):
```bash
go test ./auth/casbin/...
```
Expected: PASS, and the integration test is NOT run (no `TestIntegration_AllowDeny` in output).

Run (integration build):
```bash
go test -tags=integration -run TestIntegration_AllowDeny ./auth/casbin/...
```
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add auth/casbin/integration_test.go
git commit -m "$(cat <<'EOF'
test(authz): add casbin integration test for allow/deny

Builds a real *casbin.Enforcer from an embedded ACL model + in-memory policy
and asserts the default (sub, obj, act) tuple traverses Casbin end-to-end.
Build-tagged 'integration' per repo convention.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Public docs (CHANGELOG + README)

**Files:**
- Modify: `CHANGELOG.md`
- Modify: `README.md`

- [ ] **Step 1: Add the CHANGELOG entry**

Open `CHANGELOG.md`. Under the `## [Unreleased]` heading, add (or extend) an `### Added` subsection with this bullet. Match the surrounding indentation/format of existing entries:

```markdown
### Added

- `auth/casbin` (`casbinauth`): an `Authorizer` adapter wrapping a
  caller-built Casbin enforcer as `go-ddd-core` `ports/auth.Authorizer`.
  Depends on a one-method `Enforcer` interface (both `*casbin.Enforcer`
  and `*casbin.SyncedEnforcer` satisfy it); default `(sub, obj, act)`
  request builder with `Type:ID` object encoding, overridable via
  `WithRequestBuilder`. Deny → `ErrForbidden`, malformed input →
  `ErrInvalidAuthorizationRequest`; engine/ctx/builder errors are passed
  through un-disguised.
```

- [ ] **Step 2: Add the README adapter-inventory row**

Open `README.md`, find the adapter inventory/catalog table, and add a row for the new adapter. Match the existing column layout (locate the `authmw` / `auth/jwt` rows as the template and add an analogous row), e.g.:

```markdown
| `auth/casbin` | `casbinauth` | Wraps a Casbin enforcer as a core `auth.Authorizer` (AuthZ). |
```

(Adjust columns to match the actual table header in the file.)

- [ ] **Step 3: Verify the modules still build/test (docs-only, smoke check)**

Run: `go build ./... && go test ./auth/casbin/...`
Expected: PASS (no code changed; this is a guard against accidental edits).

- [ ] **Step 4: Commit**

```bash
git add CHANGELOG.md README.md
git commit -m "$(cat <<'EOF'
docs(authz): document auth/casbin adapter in CHANGELOG and README

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Full verification matrix + close-out bookkeeping

**Files:**
- Modify: `.agent/decisions.md`
- Modify: `.agent/review-log.md`
- Modify: `.agent/state.md`

- [ ] **Step 1: Run the full verification matrix (root module)**

Run from the repo root:
```bash
go build ./...
go vet ./...
go test ./...
go test -race ./auth/casbin/...
go test -tags=integration ./auth/casbin/...
golangci-lint run ./...
golangci-lint run --build-tags=integration ./...
```
Expected: all green. (If the local `golangci-lint` Go version is below the module's `go 1.25.0`, note it and defer lint to CI — this matches the prior-cycle practice recorded in `.agent/state.md`.)

- [ ] **Step 2: Run the examples/orders verification (core bump only)**

```bash
cd examples/orders
go build ./...
go vet ./...
go test ./...
golangci-lint run ./...
cd ../..
```
Expected: all green (examples/orders only saw the core pseudo-version bump; no Casbin wiring).

- [ ] **Step 3: Record the decision in `.agent/decisions.md`**

Append a section (match the heading style of existing entries):

```markdown
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
```

- [ ] **Step 4: Append a review-log entry in `.agent/review-log.md`**

Match the existing format; record the spec/plan review rounds and the verification result, e.g.:

```markdown
## 2026-06-05 — v0.7.0 auth/casbin (Phase A)

Spec reviewed and approved after two refinements: (1) typed-nil guard
mechanism made explicit (concrete type switch on *casbin.Enforcer /
*casbin.SyncedEnforcer, no generic reflect); (2) race-test claim narrowed
to concurrent read-only Allow (reload safety delegated to Casbin).
Implementation verified: root build/vet/test, -race on auth/casbin,
integration (real casbin), examples/orders build/vet/test all green;
golangci-lint via CI.
```

- [ ] **Step 5: Flip the `.agent/state.md` cycle section to verified**

In `.agent/state.md`, update the `## v0.7.0 AuthZ cycle` section's `Status:` line to reflect completion and record the verification, e.g. change the final bullet to:

```markdown
- Status: Phase A implemented + locally verified (root build/vet/test,
  -race auth/casbin, integration real-casbin, examples/orders all green;
  lint via CI). Pending: PR + merge, then the v0.7.0 cross-repo tag-gate
  (out of scope of this PR — see the spec §10).
```

- [ ] **Step 6: Commit**

```bash
git add .agent/decisions.md .agent/review-log.md .agent/state.md
git commit -m "$(cat <<'EOF'
chore(authz): record v0.7.0 auth/casbin decisions and verification

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

- [ ] **Step 7: Push the branch and open the PR (only when the user asks)**

Do NOT push or open a PR automatically. When the user confirms, push `feat/authz-casbin-v0.7.0` and open a PR targeting `main` whose body notes: Phase A scope, the core pseudo-version pin (to be bumped to `v0.7.0` at the tag-gate), and that CI must be green before the tag-gate proceeds.

---

## Out of scope (do NOT implement in this plan)

- HTTP enforcement middleware that calls `Allow` per request (Phase B — separate cycle).
- `examples/orders` AuthZ wiring (Phase C — separate cycle).
- OPA/Rego adapter.
- The v0.7.0 tag-gate ritual: core tagging v0.7.0, bumping the adapter pin from the pseudo-version to `v0.7.0`, cutting the adapter tag + GitHub Release. These are post-merge release steps (spec §10), not implementation tasks.
- Any change to core's `ports/auth` contract. If a gap is found mid-implementation, pause and push the change to core first — no adapter-side shim (spec §3, §9.2).

---

## Self-review notes

- **Spec coverage:** §2.1 package/surface → Task 2; §2.2 tests (unit/integration/race) → Tasks 2–4; §2.3 go.mod (casbin + core bump, both modules) → Task 1; §2.4 docs (doc.go/CHANGELOG/README/.agent/*) → Tasks 2,5,6; §5 surface design → Task 2 code; §6 concurrency contract → doc.go (Task 2) + race claim (Task 3); §7 error mapping → Task 2 `Allow` + tests; §8 test plan incl. verification matrix → Tasks 2–4,6; §9 invariants → covered by error tests + no-shim note (Task 6 decisions); §10 tag-gate → Task 6 Step 7 + out-of-scope; §11 rejected alternatives → decisions (Task 6); §12 kickoff state.md → Task 1 Step 4.
- **Type consistency:** `Enforcer.Enforce(rvals ...any) (bool, error)`, `New(enforcer Enforcer, opts ...Option) (*Authorizer, error)`, `Allow(ctx, caller auth.Identity, action string, resource auth.Resource) error`, `RequestBuilder func(ctx, caller, action, resource) ([]any, error)`, `WithRequestBuilder(fn RequestBuilder) Option`, `ErrNilEnforcer`, `ErrNilRequestBuilder` — all spellings match between `authorizer.go`, `options.go`, and every test.
- **No placeholders:** every code step contains complete, compilable Go; every run step has an exact command and expected result.
