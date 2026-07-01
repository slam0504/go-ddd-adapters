# cache/redis Adapter + ports/cache Maturation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship `cache/redis`, the first concrete adapter for core's `ports/cache.Cache`, folding a scoped `ports/cache` maturation (delete `TypedCache[T]`, add `cachetest` conformance suite, tighten godoc) into the same tag-gated cycle → `v0.11.0` on both repos.

**Architecture:** Two repos, one cycle. Phase A changes `go-ddd-core` (`ports/cache` godoc + `cachetest` suite). Phase B builds the `go-ddd-adapters` `cache/redis` driver against a pseudo-version pin of the merged core PR, running core's `cachetest.RunContract` as the primary conformance proof. Phase C runs the contract-first / tag-at-last-piece gate to `v0.11.0`. Same shape as idempotency (v0.8.0), jobs (v0.9.0), ratelimit (v0.10.0).

**Tech Stack:** Go 1.25; `github.com/redis/go-redis/v9 v9.20.0` (already a repo dependency); `github.com/slam0504/go-ddd-core` (`ports/cache`, `ports/cache/cachetest`, `pkg/errorsx`); testcontainers Redis via `internal/redistest`.

## Global Constraints

- **Spec:** `go-ddd-adapters/docs/superpowers/specs/2026-07-01-cache-redis-adapter-design.md` (ACCEPTED). This plan implements it verbatim.
- **Module paths:** core `github.com/slam0504/go-ddd-core`; adapters `github.com/slam0504/go-ddd-adapters`.
- **Adapter package:** `rediscache` in dir `cache/redis`.
- **errorsx API:** `errorsx.New(code, msg)`, `errorsx.Wrap(code, msg, cause)`, `errorsx.CodeOf(err) Code`. Codes used: `CodeInvalidArgument`, `CodeUnavailable`, `CodeInternal`. A backend error's `CodeOf` MUST never be `CodeUnknown`.
- **Miss sentinel:** `cache.ErrMiss` (from core), matched with `errors.Is`. Never a coded error.
- **Key encoding:** prefix-free `fmt.Sprintf("%d:%s:%s", len(keyPrefix), keyPrefix, key)`. Never a naive `prefix+":"+key`.
- **Error precedence** — `Set`: empty key → negative ttl → pre-cancelled/expired ctx → backend. `Get`/`Delete`/`Exists`: empty key → pre-cancelled/expired ctx → backend.
- **TTL:** `0` = no expiry (native go-redis); `< 0` = `CodeInvalidArgument`, no backend contact.
- **Godoc scope:** tighten ctx/empty-key/TTL/precedence prose only. Do NOT write byte copy/ownership prose into the *contract text*; ownership is pinned by `cachetest` invariants (§5.3 of spec).
- **Lint:** `golangci-lint` via the v2 binary `/usr/local/bin/golangci-lint` (the `~/go/bin` v1.64.8 chokes on the v2 config). goimports local-prefixes: `go-ddd-adapters/...` imports get their own group; core is third-party.
- **Docker unavailable locally** → the `//go:build integration` RUN is CI-only. Never claim a local integration pass. Non-integration unit tests DO run locally.
- **cachetest non-vacuity:** dev-time red proof only. Commit GREEN. No CI-breaking `t.Run("bad impl fails", ...)`, no subprocess guard. Record the red proof in this plan's checkboxes + review-log.
- **Commit trailer:** end every commit body with `Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>`.

---

# Phase A — Core (`go-ddd-core`)

> Work in the core repo: `/Users/eason_tseng/playground/project/go-ddd-core`, on a new branch `feat/cache-maturation` off `main`. Phase A ships as its own core PR, merged UNTAGGED. Its merge commit is the pseudo-version Phase B pins.

### Task A1: Delete `TypedCache[T]` + tighten `ports/cache` godoc

**Files:**
- Modify: `ports/cache/cache.go` (currently 31 lines)

**Interfaces:**
- Consumes: nothing.
- Produces: the matured `cache.Cache` interface (unchanged method set `Get`/`Set`/`Delete`/`Exists`) + `cache.ErrMiss`. `TypedCache[T]` no longer exists.

- [ ] **Step 1: Confirm zero consumers of `TypedCache` before deleting**

Run: `grep -rn "TypedCache" /Users/eason_tseng/playground/project --include='*.go'`
Expected: matches ONLY in `go-ddd-core/ports/cache/cache.go`. If any other file references it, STOP and report — the zero-consumer premise is the whole basis for the painless breaking change.

- [ ] **Step 2: Rewrite `ports/cache/cache.go`**

Replace the entire file with:

```go
// Package cache defines the key-value caching contract. Implementations wrap
// Redis, Memcached, in-process LRU, etc.
//
// # Context convention
//
// Every method requires a non-nil ctx (stdlib convention). Passing nil is a
// caller bug with unspecified behaviour; implementations need not nil-check it.
//
// # Key and error semantics
//
// An empty key ("") is malformed input on every method: implementations MUST
// return an errorsx.CodeInvalidArgument error before contacting the backend. A
// miss (absent key on Get) is reported as the ErrMiss sentinel, never a coded
// error. Any OTHER non-nil error means the operation could not reach a
// decision, and adapters return a coded errorsx whose CodeOf is never
// CodeUnknown.
package cache

import (
	"context"
	"errors"
	"time"
)

// Cache is a byte-level key-value cache with optional TTL per entry.
//
// TTL semantics (Set): a zero ttl means "no expiry". A negative ttl is
// malformed input and MUST return errorsx.CodeInvalidArgument before any
// backend contact — it is almost always a caller bug, and forwarding it risks
// a driver-specific "keep existing TTL" sentinel (e.g. go-redis KeepTTL == -1).
//
// Error precedence:
//   - Set:                 empty key -> negative ttl -> pre-cancelled/expired ctx -> backend
//   - Get / Delete / Exists: empty key -> pre-cancelled/expired ctx -> backend
//
// The first rung(s) are deterministic input validation, evaluated before ctx is
// observed; then a ctx already cancelled/expired returns the matching ctx error
// with no backend contact; only then a backend failure.
type Cache interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
	Exists(ctx context.Context, key string) (bool, error)
}

// ErrMiss is returned when a key is absent. Adapters must translate their
// native miss indicator into this sentinel.
var ErrMiss = errors.New("cache: miss")
```

- [ ] **Step 3: Build + vet**

Run: `go build ./... && go vet ./ports/cache/...`
Expected: clean (no reference to the deleted `TypedCache`).

- [ ] **Step 4: Commit**

```bash
git add ports/cache/cache.go
git commit -F - <<'EOF'
feat(ports/cache)!: delete dangling TypedCache[T]; tighten Cache godoc

TypedCache[T] was a dangling generic (no constructor, no codec seam, miss
semantics unstated) with zero consumers — remove it rather than build a
speculative typed layer. Tighten the byte-level Cache godoc: non-nil ctx
convention, empty-key -> CodeInvalidArgument, ttl==0 no-expiry / ttl<0
CodeInvalidArgument, and the Set / read error-precedence ladders. Byte
ownership is pinned by the cachetest suite (next commit), not contract prose.

BREAKING CHANGE: ports/cache.TypedCache[T] removed (no known consumers).

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
EOF
```

---

### Task A2: Add `ports/cache/cachetest` conformance suite

**Files:**
- Create: `ports/cache/cachetest/cachetest.go` (suite + exported `RunContract` + `Factory`)
- Create: `ports/cache/cachetest/reference_test.go` (unexported reference impl + a test running `RunContract` against it)

**Interfaces:**
- Consumes: `cache.Cache`, `cache.ErrMiss` (Task A1); `errorsx.CodeOf` / `errorsx.CodeInvalidArgument`.
- Produces: `cachetest.RunContract(t *testing.T, newCache Factory)` and `type Factory func(t *testing.T) cache.Cache`. Phase B's integration test calls these.

- [ ] **Step 1: Write the conformance suite**

Create `ports/cache/cachetest/cachetest.go`:

```go
// Package cachetest is the exported conformance suite for ports/cache.Cache.
// Any adapter runs RunContract against its own implementation to prove it
// honours the byte-level cache contract. The suite is deterministic and
// synchronous-only: it asserts invariants visible through the interface and
// deliberately excludes time-dependent behaviour (TTL expiry), which an
// adapter covers with its own intent test. Error assertions use errorsx.CodeOf,
// never an HTTP status. Imports only testing + the contract + errorsx (never
// pkg/errorsx/httpx) so adapters inherit no transport dependency.
package cachetest

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/slam0504/go-ddd-core/pkg/errorsx"
	"github.com/slam0504/go-ddd-core/ports/cache"
)

// Factory returns a fresh, state-isolated cache.Cache for one subtest. It
// receives the subtest's *testing.T so an adapter can key isolation on
// t.Name() and register t.Cleanup (matches ratelimittest/jobstest/
// idempotencytest, which all take *testing.T). Each call MUST return an
// implementation whose keyspace does not overlap any other call's (e.g. a new
// in-memory map, or a Redis client namespaced per call).
type Factory func(t *testing.T) cache.Cache

// RunContract runs the full deterministic cache.Cache conformance suite.
func RunContract(t *testing.T, newCache Factory) {
	t.Helper()

	ctx := context.Background()

	t.Run("GetAbsentKeyReturnsErrMiss", func(t *testing.T) {
		c := newCache(t)
		_, err := c.Get(ctx, "absent")
		if !errors.Is(err, cache.ErrMiss) {
			t.Fatalf("Get(absent) err = %v, want cache.ErrMiss", err)
		}
	})

	t.Run("SetThenGetReturnsValue", func(t *testing.T) {
		c := newCache(t)
		want := []byte("hello")
		if err := c.Set(ctx, "k", want, 0); err != nil {
			t.Fatalf("Set: %v", err)
		}
		got, err := c.Get(ctx, "k")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("Get = %q, want %q", got, want)
		}
	})

	t.Run("SetOverwriteWins", func(t *testing.T) {
		c := newCache(t)
		_ = c.Set(ctx, "k", []byte("v1"), 0)
		if err := c.Set(ctx, "k", []byte("v2"), 0); err != nil {
			t.Fatalf("Set overwrite: %v", err)
		}
		got, _ := c.Get(ctx, "k")
		if !bytes.Equal(got, []byte("v2")) {
			t.Fatalf("Get after overwrite = %q, want %q", got, "v2")
		}
	})

	t.Run("OverwriteWithShorterValue", func(t *testing.T) {
		c := newCache(t)
		_ = c.Set(ctx, "k", []byte("hello"), 0)
		if err := c.Set(ctx, "k", []byte("x"), 0); err != nil {
			t.Fatalf("Set shorter: %v", err)
		}
		got, _ := c.Get(ctx, "k")
		if !bytes.Equal(got, []byte("x")) {
			t.Fatalf("Get after shorter overwrite = %q, want %q", got, "x")
		}
	})

	t.Run("EmptyValueRoundTripsAndIsNotAMiss", func(t *testing.T) {
		c := newCache(t)
		if err := c.Set(ctx, "k", []byte{}, 0); err != nil {
			t.Fatalf("Set empty value: %v", err)
		}
		got, err := c.Get(ctx, "k")
		if err != nil {
			t.Fatalf("Get empty value err = %v, want nil (an empty value is present, not a miss)", err)
		}
		if len(got) != 0 {
			t.Fatalf("Get empty value len = %d, want 0", len(got))
		}
		if ok, _ := c.Exists(ctx, "k"); !ok {
			t.Fatal("Exists(empty value) = false, want true (empty value is present)")
		}
	})

	t.Run("DeleteThenGetMisses", func(t *testing.T) {
		c := newCache(t)
		_ = c.Set(ctx, "k", []byte("v"), 0)
		if err := c.Delete(ctx, "k"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := c.Get(ctx, "k"); !errors.Is(err, cache.ErrMiss) {
			t.Fatalf("Get after Delete err = %v, want cache.ErrMiss", err)
		}
	})

	t.Run("DeleteAbsentKeyIsIdempotent", func(t *testing.T) {
		c := newCache(t)
		if err := c.Delete(ctx, "absent"); err != nil {
			t.Fatalf("Delete(absent) = %v, want nil", err)
		}
	})

	t.Run("ExistsReflectsPresence", func(t *testing.T) {
		c := newCache(t)
		if ok, err := c.Exists(ctx, "k"); err != nil || ok {
			t.Fatalf("Exists(absent) = (%v,%v), want (false,nil)", ok, err)
		}
		_ = c.Set(ctx, "k", []byte("v"), 0)
		if ok, err := c.Exists(ctx, "k"); err != nil || !ok {
			t.Fatalf("Exists(present) = (%v,%v), want (true,nil)", ok, err)
		}
		_ = c.Delete(ctx, "k")
		if ok, err := c.Exists(ctx, "k"); err != nil || ok {
			t.Fatalf("Exists(after delete) = (%v,%v), want (false,nil)", ok, err)
		}
	})

	t.Run("EmptyKeyIsInvalidArgument", func(t *testing.T) {
		c := newCache(t)
		assertInvalid := func(name string, err error) {
			t.Helper()
			if errorsx.CodeOf(err) != errorsx.CodeInvalidArgument {
				t.Fatalf("%s empty-key CodeOf = %v, want CodeInvalidArgument", name, errorsx.CodeOf(err))
			}
		}
		_, gErr := c.Get(ctx, "")
		assertInvalid("Get", gErr)
		assertInvalid("Set", c.Set(ctx, "", []byte("v"), 0))
		assertInvalid("Delete", c.Delete(ctx, ""))
		_, eErr := c.Exists(ctx, "")
		assertInvalid("Exists", eErr)
	})

	t.Run("NegativeTTLIsInvalidArgumentAndNotWritten", func(t *testing.T) {
		c := newCache(t)
		err := c.Set(ctx, "k", []byte("v"), -1*time.Second)
		if errorsx.CodeOf(err) != errorsx.CodeInvalidArgument {
			t.Fatalf("Set(ttl<0) CodeOf = %v, want CodeInvalidArgument", errorsx.CodeOf(err))
		}
		// Validation failure must not have written the key.
		if _, gErr := c.Get(ctx, "k"); !errors.Is(gErr, cache.ErrMiss) {
			t.Fatalf("key written despite invalid ttl: Get err = %v, want ErrMiss", gErr)
		}
	})

	t.Run("PreCancelledCtxReturnsCtxError", func(t *testing.T) {
		c := newCache(t)
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := c.Get(cancelled, "k"); !errors.Is(err, context.Canceled) {
			t.Fatalf("Get(cancelled) err = %v, want context.Canceled", err)
		}
		if err := c.Set(cancelled, "k", []byte("v"), 0); !errors.Is(err, context.Canceled) {
			t.Fatalf("Set(cancelled) err = %v, want context.Canceled", err)
		}
		if err := c.Delete(cancelled, "k"); !errors.Is(err, context.Canceled) {
			t.Fatalf("Delete(cancelled) err = %v, want context.Canceled", err)
		}
		if _, err := c.Exists(cancelled, "k"); !errors.Is(err, context.Canceled) {
			t.Fatalf("Exists(cancelled) err = %v, want context.Canceled", err)
		}
	})

	t.Run("InputSliceNotAliased", func(t *testing.T) {
		c := newCache(t)
		v := []byte("hello")
		if err := c.Set(ctx, "k", v, 0); err != nil {
			t.Fatalf("Set: %v", err)
		}
		v[0] = 'X' // caller mutates its slice after Set
		got, _ := c.Get(ctx, "k")
		if !bytes.Equal(got, []byte("hello")) {
			t.Fatalf("cache aliased caller input: Get = %q, want %q", got, "hello")
		}
	})

	t.Run("OutputSliceNotAliased", func(t *testing.T) {
		c := newCache(t)
		_ = c.Set(ctx, "k", []byte("hello"), 0)
		a, _ := c.Get(ctx, "k")
		if len(a) > 0 {
			a[0] = 'X' // caller mutates returned slice
		}
		b, _ := c.Get(ctx, "k")
		if !bytes.Equal(b, []byte("hello")) {
			t.Fatalf("cache handed out aliased internal slice: Get = %q, want %q", b, "hello")
		}
	})
}
```

- [ ] **Step 2: Write the reference impl + a test that runs the suite against it**

Create `ports/cache/cachetest/reference_test.go`:

```go
package cachetest

import (
	"context"
	"testing"
	"time"

	"github.com/slam0504/go-ddd-core/pkg/errorsx"
	"github.com/slam0504/go-ddd-core/ports/cache"
)

// refCache is a correct in-memory reference used only to author and prove the
// suite (dev-time red proof). It is NOT shipped as a production adapter. It
// copies on store and on return so it satisfies the byte-ownership invariants,
// and it ignores positive-TTL expiry (the deterministic suite never asserts
// expiry).
type refCache struct {
	m map[string][]byte
}

func newRefCache(t *testing.T) cache.Cache { return &refCache{m: map[string][]byte{}} }

func (c *refCache) validateKey(key string) error {
	if key == "" {
		return errorsx.New(errorsx.CodeInvalidArgument, "cachetest: empty key")
	}
	return nil
}

func (c *refCache) Get(ctx context.Context, key string) ([]byte, error) {
	if err := c.validateKey(key); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	v, ok := c.m[key]
	if !ok {
		return nil, cache.ErrMiss
	}
	out := make([]byte, len(v)) // copy on return
	copy(out, v)
	return out, nil
}

func (c *refCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if err := c.validateKey(key); err != nil {
		return err
	}
	if ttl < 0 {
		return errorsx.New(errorsx.CodeInvalidArgument, "cachetest: negative ttl")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	stored := make([]byte, len(value)) // copy on store
	copy(stored, value)
	c.m[key] = stored
	return nil
}

func (c *refCache) Delete(ctx context.Context, key string) error {
	if err := c.validateKey(key); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	delete(c.m, key)
	return nil
}

func (c *refCache) Exists(ctx context.Context, key string) (bool, error) {
	if err := c.validateKey(key); err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	_, ok := c.m[key]
	return ok, nil
}

// TestReferenceImplementationSatisfiesContract proves the suite is runnable and
// GREEN against a correct implementation. Its purpose is to author the suite;
// non-vacuity is proven at dev time (see Step 4) and NOT committed as a
// red-on-purpose test.
func TestReferenceImplementationSatisfiesContract(t *testing.T) {
	RunContract(t, newRefCache)
}
```

- [ ] **Step 3: Run the suite GREEN**

Run: `go test ./ports/cache/cachetest/ -v`
Expected: PASS — every `RunContract` subtest green against `refCache`.

- [ ] **Step 4: Dev-time non-vacuity red proof (DO NOT COMMIT the breakage)**

For EACH of these temporary one-line breakages, apply it to `reference_test.go`, run `go test ./ports/cache/cachetest/`, confirm the named subtest FAILS, then REVERT:

1. In `Set`, delete the `copy(stored, value)` line and store `value` directly → `InputSliceNotAliased` must fail.
2. In `Get`, `return v, nil` directly (skip the copy) → `OutputSliceNotAliased` must fail.
3. In `Set`, remove the `if ttl < 0` guard → `NegativeTTLIsInvalidArgumentAndNotWritten` must fail.
4. In `validateKey`, `return nil` unconditionally → `EmptyKeyIsInvalidArgument` must fail.
5. In `Get`, drop the `ctx.Err()` check → `PreCancelledCtxReturnsCtxError` must fail.
6. In `Get`, treat an empty value as absent (`if !ok || len(v) == 0 { return nil, cache.ErrMiss }`) → `EmptyValueRoundTripsAndIsNotAMiss` must fail.

Record in the review-log: "cachetest non-vacuity proven at dev time via 6 reference-impl breakages (aliasing×2, negTTL, empty-key, ctx, empty-value-as-miss); each failed the matching subtest; all reverted before commit." Confirm the tree is GREEN again: `go test ./ports/cache/cachetest/`.

- [ ] **Step 5: Lint + commit**

Run: `gofmt -l ports/cache/cachetest/ && go vet ./ports/cache/cachetest/`
Expected: no output from gofmt, clean vet.

```bash
git add ports/cache/cachetest/
git commit -F - <<'EOF'
feat(ports/cache/cachetest): add RunContract conformance suite

Deterministic, synchronous-only suite (Factory + RunContract) mirroring
idempotencytest/jobstest/ratelimittest. Asserts miss=ErrMiss, set/get/overwrite,
delete idempotency, exists, empty-key + negative-ttl CodeInvalidArgument
precedence, pre-cancelled ctx, and the two byte-ownership invariants (input +
output aliasing) that protect a future in-process adapter. Imports errorsx only
(no httpx). Non-vacuity proven at dev time against the reference impl; only the
GREEN suite is committed.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
EOF
```

- [ ] **Step 6: Whole-Phase-A verification + open core PR**

Run: `go build ./... && go test ./... && gofmt -l . && go vet ./...`
Expected: all clean/PASS. Then push `feat/cache-maturation` and open the core PR ("ports/cache maturation — delete TypedCache, add cachetest"). Merge it (UNTAGGED). **Record the merge commit SHA — Phase B pins it.**

---

# Phase B — Adapter (`go-ddd-adapters`)

> **Phase B pre-flight (tag-gate ordering):** Phase A's core PR is merged to core `main` at commit `H`. On the adapters `feat/cache-redis` branch, pin core to the pseudo-version of `H`:
> `go get github.com/slam0504/go-ddd-core@H` (run in repo root, then in `examples/orders`). This makes `ports/cache/cachetest` importable. Confirm `go build ./...` resolves. If iterating before `H` exists, a temporary `replace` directive to the local core checkout is acceptable, but the committed PR MUST pin the pseudo-version, never a `replace`.

### Task B1: Key encoding (`key.go`)

**Files:**
- Create: `cache/redis/key.go`
- Create: `cache/redis/key_test.go`

**Interfaces:**
- Produces: `encode(keyPrefix, key string) string` (package-private).

- [ ] **Step 1: Write the failing injective test**

Create `cache/redis/key_test.go`:

```go
package rediscache

import "testing"

// TestEncodeInjective proves the length prefix makes (keyPrefix, key) injective.
// A naive prefix+":"+key collapses ("a","b:c") and ("a:b","c") both to "a:b:c".
func TestEncodeInjective(t *testing.T) {
	got1 := encode("a", "b:c")
	got2 := encode("a:b", "c")
	if got1 == got2 {
		t.Fatalf("encode collision: encode(%q,%q)=%q == encode(%q,%q)=%q", "a", "b:c", got1, "a:b", "c", got2)
	}
	if want := "1:a:b:c"; got1 != want {
		t.Fatalf("encode(\"a\",\"b:c\") = %q, want %q", got1, want)
	}
	if want := "3:a:b:c"; got2 != want {
		t.Fatalf("encode(\"a:b\",\"c\") = %q, want %q", got2, want)
	}
}

func TestEncodeEmptyPrefixInjective(t *testing.T) {
	if got, want := encode("", "k"), "0::k"; got != want {
		t.Fatalf("encode(\"\",\"k\") = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cache/redis/ -run TestEncode`
Expected: FAIL — `undefined: encode`.

- [ ] **Step 3: Write `encode`**

Create `cache/redis/key.go`:

```go
package rediscache

import "fmt"

// encode builds a prefix-free Redis key from (keyPrefix, key). Length-prefixing
// keyPrefix makes the boundary unambiguous, so distinct (keyPrefix, key) tuples
// never collide: a client-supplied key cannot flatten into another namespace
// (the flatten bug idempotency/redis fixed in v0.8.0). An empty keyPrefix still
// encodes injectively ("0::" + key).
func encode(keyPrefix, key string) string {
	return fmt.Sprintf("%d:%s:%s", len(keyPrefix), keyPrefix, key)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cache/redis/ -run TestEncode -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cache/redis/key.go cache/redis/key_test.go
git commit -F - <<'EOF'
feat(cache/redis): prefix-free length-encoded key

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
EOF
```

---

### Task B2: Options + construction errors (`options.go`)

**Files:**
- Create: `cache/redis/options.go`
- Create: `cache/redis/options_test.go`

**Interfaces:**
- Produces: `type Option func(*config)`; `type config struct { keyPrefix string }`; `WithKeyPrefix(prefix string) Option`; `defaultKeyPrefix = "cache"`; `var ErrNilClient error`.

- [ ] **Step 1: Write the failing options test**

Create `cache/redis/options_test.go`:

```go
package rediscache

import "testing"

func TestDefaultKeyPrefix(t *testing.T) {
	cfg := config{keyPrefix: defaultKeyPrefix}
	if cfg.keyPrefix != "cache" {
		t.Fatalf("default keyPrefix = %q, want %q", cfg.keyPrefix, "cache")
	}
}

func TestWithKeyPrefixOverrides(t *testing.T) {
	cfg := config{keyPrefix: defaultKeyPrefix}
	WithKeyPrefix("sessions")(&cfg)
	if cfg.keyPrefix != "sessions" {
		t.Fatalf("keyPrefix after WithKeyPrefix = %q, want %q", cfg.keyPrefix, "sessions")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cache/redis/ -run 'TestDefaultKeyPrefix|TestWithKeyPrefix'`
Expected: FAIL — `undefined: config` / `defaultKeyPrefix` / `WithKeyPrefix`.

- [ ] **Step 3: Write `options.go`**

Create `cache/redis/options.go`:

```go
package rediscache

import "github.com/slam0504/go-ddd-core/pkg/errorsx"

// defaultKeyPrefix namespaces keys when the caller supplies no WithKeyPrefix.
const defaultKeyPrefix = "cache"

// ErrNilClient is returned by New when client is a nil interface or a typed-nil
// of one of the documented concrete go-redis client types. It is an
// errorsx-coded CodeInvalidArgument (mirrors ratelimit/redisrate) so
// errorsx.CodeOf(ErrNilClient) is never CodeUnknown, consistent with the
// empty-key / negative-ttl coding used elsewhere in this adapter.
var ErrNilClient = errorsx.New(errorsx.CodeInvalidArgument, "rediscache: nil redis client")

type config struct {
	keyPrefix string
}

// Option configures a Cache at construction time.
type Option func(*config)

// WithKeyPrefix overrides the key namespace (default "cache"). Two caches with
// different prefixes never share an entry for the same key (see key.go).
func WithKeyPrefix(prefix string) Option {
	return func(c *config) { c.keyPrefix = prefix }
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cache/redis/ -run 'TestDefaultKeyPrefix|TestWithKeyPrefix' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cache/redis/options.go cache/redis/options_test.go
git commit -F - <<'EOF'
feat(cache/redis): config + WithKeyPrefix option + ErrNilClient

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
EOF
```

---

### Task B3: `Cache` struct, `New` guard, methods (`cache.go`)

**Files:**
- Create: `cache/redis/cache.go`
- Create: `cache/redis/cache_test.go` (non-integration unit tests for the nil/typed-nil guard only)

**Interfaces:**
- Consumes: `encode` (B1); `config`/`Option`/`defaultKeyPrefix`/`ErrNilClient` (B2); `cache.Cache`/`cache.ErrMiss`; `errorsx`.
- Produces: `func New(client redis.Cmdable, opts ...Option) (*Cache, error)`; `type Cache struct{...}` implementing `cache.Cache`.

- [ ] **Step 1: Write the failing guard test**

Create `cache/redis/cache_test.go`:

```go
package rediscache

import (
	"errors"
	"testing"

	"github.com/redis/go-redis/v9"
)

func TestNewRejectsNilInterface(t *testing.T) {
	if _, err := New(nil); !errors.Is(err, ErrNilClient) {
		t.Fatalf("New(nil) err = %v, want ErrNilClient", err)
	}
}

func TestNewRejectsTypedNilClient(t *testing.T) {
	var c *redis.Client // typed nil
	if _, err := New(c); !errors.Is(err, ErrNilClient) {
		t.Fatalf("New(typed-nil *redis.Client) err = %v, want ErrNilClient", err)
	}
}

func TestNewAcceptsRealClient(t *testing.T) {
	// A non-nil client need not reach a server for construction to succeed.
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:0"})
	t.Cleanup(func() { _ = client.Close() })
	c, err := New(client, WithKeyPrefix("t"))
	if err != nil {
		t.Fatalf("New(real client) err = %v, want nil", err)
	}
	if c == nil {
		t.Fatal("New returned nil Cache")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cache/redis/ -run TestNew`
Expected: FAIL — `undefined: New`.

- [ ] **Step 3: Write `cache.go`**

Create `cache/redis/cache.go`:

```go
package rediscache

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/slam0504/go-ddd-core/pkg/errorsx"
	"github.com/slam0504/go-ddd-core/ports/cache"
)

// Cache is a Redis-backed implementation of core's ports/cache.Cache. It is
// safe for concurrent use (go-redis clients are). Keys are namespaced with a
// prefix-free encoding (see key.go).
type Cache struct {
	client    redis.Cmdable
	keyPrefix string
}

// New builds a Cache over client. client may be *redis.Client,
// *redis.ClusterClient, or *redis.Ring (all satisfy redis.Cmdable). redis.Cmdable
// is not a minimal interface but is the existing public go-redis entry that
// exposes Get/Set/Del/Exists without the lifecycle/subscribe semantics of
// UniversalClient. New rejects a nil interface and a typed-nil of the three
// documented concrete types; a typed-nil CUSTOM Cmdable is not guarded and
// surfaces on first call (the caller's bug).
func New(client redis.Cmdable, opts ...Option) (*Cache, error) {
	if client == nil {
		return nil, ErrNilClient
	}
	switch c := client.(type) {
	case *redis.Client:
		if c == nil {
			return nil, ErrNilClient
		}
	case *redis.ClusterClient:
		if c == nil {
			return nil, ErrNilClient
		}
	case *redis.Ring:
		if c == nil {
			return nil, ErrNilClient
		}
	}
	cfg := config{keyPrefix: defaultKeyPrefix}
	for _, opt := range opts {
		opt(&cfg)
	}
	return &Cache{client: client, keyPrefix: cfg.keyPrefix}, nil
}

// Get returns the value for key or cache.ErrMiss if absent. Precedence:
// empty key -> pre-cancelled ctx -> backend.
func (c *Cache) Get(ctx context.Context, key string) ([]byte, error) {
	if key == "" {
		return nil, errInvalidKey()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b, err := c.client.Get(ctx, encode(c.keyPrefix, key)).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, cache.ErrMiss
		}
		return nil, mapBackendErr("get", err)
	}
	return b, nil
}

// Set stores value under key with the given ttl. ttl == 0 means no expiry; ttl
// < 0 is invalid input. Precedence: empty key -> negative ttl -> pre-cancelled
// ctx -> backend.
func (c *Cache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if key == "" {
		return errInvalidKey()
	}
	if ttl < 0 {
		return errorsx.New(errorsx.CodeInvalidArgument, "rediscache: negative ttl")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.client.Set(ctx, encode(c.keyPrefix, key), value, ttl).Err(); err != nil {
		return mapBackendErr("set", err)
	}
	return nil
}

// Delete removes key. Deleting an absent key is not an error (idempotent).
// Precedence: empty key -> pre-cancelled ctx -> backend.
func (c *Cache) Delete(ctx context.Context, key string) error {
	if key == "" {
		return errInvalidKey()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.client.Del(ctx, encode(c.keyPrefix, key)).Err(); err != nil {
		return mapBackendErr("delete", err)
	}
	return nil
}

// Exists reports whether key is present. Precedence: empty key ->
// pre-cancelled ctx -> backend.
func (c *Cache) Exists(ctx context.Context, key string) (bool, error) {
	if key == "" {
		return false, errInvalidKey()
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	n, err := c.client.Exists(ctx, encode(c.keyPrefix, key)).Result()
	if err != nil {
		return false, mapBackendErr("exists", err)
	}
	return n > 0, nil
}

func errInvalidKey() error {
	return errorsx.New(errorsx.CodeInvalidArgument, "rediscache: empty key")
}

// mapBackendErr wraps a non-ctx backend error as a coded errorsx whose CodeOf
// is never CodeUnknown. A ctx error observed during the call is returned
// verbatim so callers can errors.Is it.
func mapBackendErr(op string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return errorsx.Wrap(classifyBackendErr(err), "rediscache: "+op, err)
}

// classifyBackendErr maps a non-ctx backend error to CodeUnavailable (transport
// / reachability) or CodeInternal (everything else) — never CodeUnknown.
// Mirrors ratelimit/redisrate and jobs/asynq.
func classifyBackendErr(err error) errorsx.Code {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return errorsx.CodeUnavailable
	}
	msg := err.Error()
	for _, s := range []string{"connection refused", "no such host", "i/o timeout", "dial tcp", "connect:", "EOF", "broken pipe", "reset by peer"} {
		if strings.Contains(msg, s) {
			return errorsx.CodeUnavailable
		}
	}
	return errorsx.CodeInternal
}

// Compile-time assertion that *Cache implements core's cache.Cache.
var _ cache.Cache = (*Cache)(nil)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cache/redis/ -run 'TestNew|TestEncode|TestKeyPrefix|TestDefaultKeyPrefix|TestWithKeyPrefix' -v`
Expected: PASS. Also `go build ./...` clean.

- [ ] **Step 5: Lint + commit**

Run: `/usr/local/bin/golangci-lint run ./cache/redis/...`
Expected: 0 issues.

```bash
git add cache/redis/cache.go cache/redis/cache_test.go
git commit -F - <<'EOF'
feat(cache/redis): Cache with New guard, Get/Set/Delete/Exists

redis.Cmdable entry; nil + typed-nil guard on the three concrete client types;
redis.Nil -> cache.ErrMiss; empty-key/negative-ttl -> CodeInvalidArgument before
backend; ctx-verbatim then classifyBackendErr (CodeUnavailable/CodeInternal,
never CodeUnknown). Precedence: Set empty->negTTL->ctx->backend; reads
empty->ctx->backend.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
EOF
```

---

### Task B4: Integration test (conformance + intent) + `doc.go`

**Files:**
- Create: `cache/redis/integration_test.go` (`//go:build integration`)
- Create: `cache/redis/doc.go`

**Interfaces:**
- Consumes: `New`/`WithKeyPrefix`/`Cache` (B2/B3); `cachetest.RunContract` (Phase A); `internal/redistest.StartContainer`; `cache.ErrMiss`.
- Produces: nothing (leaf test + doc).

- [ ] **Step 1: Write `doc.go`**

Create `cache/redis/doc.go`:

```go
// Package rediscache is a Redis-backed implementation of core's
// ports/cache.Cache, over github.com/redis/go-redis/v9.
//
// # Construction
//
//	c, err := rediscache.New(client, rediscache.WithKeyPrefix("sessions"))
//
// client may be *redis.Client, *redis.ClusterClient, or *redis.Ring (any
// redis.Cmdable). WithKeyPrefix overrides the key namespace (default "cache");
// keys are encoded prefix-free so distinct (prefix, key) tuples never collide.
//
// # Semantics
//
// Get returns cache.ErrMiss for an absent key. Set treats ttl == 0 as no expiry
// and rejects ttl < 0 as CodeInvalidArgument before backend contact (guards the
// go-redis KeepTTL == -1 footgun). Delete is idempotent. An empty key is
// CodeInvalidArgument on every method. Byte ownership: Get returns a fresh copy
// (wire decode) and Set does not retain the caller's slice (go-redis copies to
// the wire) — the cachetest input/output aliasing invariants hold.
//
// # Errors
//
// A non-nil error other than cache.ErrMiss means the call could not reach a
// decision: empty key / negative ttl -> CodeInvalidArgument (no backend); a ctx
// cancelled/expired -> that ctx error verbatim; a backend failure -> coded
// errorsx (unreachable -> CodeUnavailable, else CodeInternal), never
// CodeUnknown.
//
// # Redis Cluster / Ring
//
// Every operation is single-key, so it stays within one hash slot by
// construction — *redis.ClusterClient / *redis.Ring SHOULD work with no hash
// tag. API-derived claim; CI exercises single-node redis:7-alpine.
package rediscache
```

- [ ] **Step 2: Write the integration test (conformance + intent)**

Create `cache/redis/integration_test.go`:

```go
//go:build integration

package rediscache_test

import (
	"context"
	"errors"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/slam0504/go-ddd-core/pkg/errorsx"
	"github.com/slam0504/go-ddd-core/ports/cache"
	"github.com/slam0504/go-ddd-core/ports/cache/cachetest"

	rediscache "github.com/slam0504/go-ddd-adapters/cache/redis"
	"github.com/slam0504/go-ddd-adapters/internal/redistest"
)

var testClient *redis.Client

// cacheCounter salts each Factory / test keyspace so subtests stay isolated on
// the shared container. A package-level atomic counter (not a per-Test var, not
// the wall clock) stays unique across -count>1 / -race reruns within one go
// test process; mirrors ratelimit/redisrate's factoryCounter.
var cacheCounter atomic.Uint64

func nextPrefix(tag string) string {
	return tag + "-" + strconv.FormatUint(cacheCounter.Add(1), 10)
}

func TestMain(m *testing.M) {
	ctx := context.Background()
	client, cleanup, err := redistest.StartContainer(ctx)
	if err != nil {
		panic(err)
	}
	testClient = client
	code := m.Run()
	cleanup()
	os.Exit(code)
}

// TestRunContract runs core's cache conformance suite against the Redis adapter.
// Each Factory call gets a globally-unique key prefix (subtest name + counter)
// so subtests are state-isolated against the shared container.
func TestRunContract(t *testing.T) {
	cachetest.RunContract(t, func(t *testing.T) cache.Cache {
		c, err := rediscache.New(testClient, rediscache.WithKeyPrefix(nextPrefix("cachetest:"+t.Name())))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return c
	})
}

// TestTTLExpiry is the adapter intent test the deterministic suite excludes: a
// key set with a short TTL is gone after it elapses.
func TestTTLExpiry(t *testing.T) {
	c, err := rediscache.New(testClient, rediscache.WithKeyPrefix(nextPrefix("ttl")))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if err := c.Set(ctx, "k", []byte("v"), 50*time.Millisecond); err != nil {
		t.Fatalf("Set: %v", err)
	}
	time.Sleep(120 * time.Millisecond)
	if _, err := c.Get(ctx, "k"); !errors.Is(err, cache.ErrMiss) {
		t.Fatalf("Get after TTL err = %v, want cache.ErrMiss", err)
	}
}

// TestKeyPrefixIsolation proves two prefixes never share an entry for one key.
func TestKeyPrefixIsolation(t *testing.T) {
	ctx := context.Background()
	a, _ := rediscache.New(testClient, rediscache.WithKeyPrefix(nextPrefix("A")))
	b, _ := rediscache.New(testClient, rediscache.WithKeyPrefix(nextPrefix("B")))
	if err := a.Set(ctx, "shared", []byte("va"), 0); err != nil {
		t.Fatalf("a.Set: %v", err)
	}
	if _, err := b.Get(ctx, "shared"); !errors.Is(err, cache.ErrMiss) {
		t.Fatalf("prefix leak: b.Get(shared) err = %v, want cache.ErrMiss", err)
	}
}

// TestNegativeTTLRejected confirms the validation-first precedence end to end
// (also covered deterministically by the suite).
func TestNegativeTTLRejected(t *testing.T) {
	c, _ := rediscache.New(testClient, rediscache.WithKeyPrefix(nextPrefix("neg")))
	err := c.Set(context.Background(), "k", []byte("v"), -1)
	if errorsx.CodeOf(err) != errorsx.CodeInvalidArgument {
		t.Fatalf("Set(ttl<0) CodeOf = %v, want CodeInvalidArgument", errorsx.CodeOf(err))
	}
}
```

Note: the `TestMain` above matches `ratelimit/redisrate/integration_test.go` and `idempotency/redis/integration_test.go` (`code := m.Run(); cleanup(); os.Exit(code)` with `"os"` imported — verified against those files 2026-07-01).

- [ ] **Step 3: Build the integration test (compile only — Docker is CI-only)**

Run: `go vet -tags=integration ./cache/redis/...`
Expected: compiles clean. The RUN happens in CI. Do NOT claim a local integration pass.

- [ ] **Step 4: Lint (integration tag) + commit**

Run: `/usr/local/bin/golangci-lint run --build-tags=integration ./cache/redis/...`
Expected: 0 issues.

```bash
git add cache/redis/integration_test.go cache/redis/doc.go
git commit -F - <<'EOF'
test(cache/redis): cachetest.RunContract + intent tests; package doc

Runs core's cachetest conformance suite against real redis (per-subtest key
prefix isolation) plus adapter intent tests: TTL expiry, prefix isolation,
negative-ttl rejection. Sectioned package doc.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
EOF
```

---

### Task B5: CHANGELOG + README + CI step + open adapter PR

**Files:**
- Modify: `CHANGELOG.md` (`[Unreleased]`)
- Modify: `README.md` (adapter table row + Status)
- Modify: the CI workflow under `.github/workflows/` (add a `cache/redis` integration leg)
- Modify: `go.mod` + `go.sum` (root + `examples/orders`) — already pinned to core pseudo-version `H` from the pre-flight.

**Interfaces:** none (docs/CI/release wiring).

- [ ] **Step 1: Read the current CI workflow + README adapter table + CHANGELOG head**

Run: `ls .github/workflows/ && sed -n '1,40p' CHANGELOG.md`
Then read the workflow file and locate the `ratelimit/redisrate` / `jobs/asynq` integration step to mirror. Read the README adapter table (the `| Adapter | Port | Notes |` block) to match row style.

- [ ] **Step 2: Add the CHANGELOG `[Unreleased]` entry**

Under `## [Unreleased]`, add (matching existing bullet style):

```markdown
### Added
- `cache/redis` (`rediscache`): first adapter for core `ports/cache.Cache` —
  go-redis v9, prefix-free length-encoded key, `redis.Nil`→`cache.ErrMiss`,
  `WithKeyPrefix`, ttl==0 no-expiry / ttl<0 rejected, coded backend errors.
- `ports/cache` matured (via go-ddd-core): `cachetest.RunContract` conformance
  suite consumed by this adapter.

### Changed
- Bumped `go-ddd-core` to the `ports/cache` maturation (pseudo-version pending
  the core `v0.11.0` tag; see the dep-bump entry at release).

### Removed
- **BREAKING (core):** `ports/cache.TypedCache[T]` deleted (dangling generic,
  no consumers).
```

- [ ] **Step 3: Add the README adapter-table row + Status paragraph**

Add a table row (mirror the exact column format of the existing rows):

```markdown
| `cache/redis` | `cache.Cache` | go-redis v9 (`redis.Cmdable`); prefix-free key, `redis.Nil`→`ErrMiss`, `WithKeyPrefix`, ttl==0 no-expiry / ttl<0 rejected, coded backend errors (never `CodeUnknown`). Redis any version |
```

Update the Status paragraph to name `cache/redis` as the newest adapter (keep the prior rate-limiting slice intact, mirroring how the v0.10.0 dep-bump kept the v0.9.0 paragraph).

- [ ] **Step 4: Add the CI integration leg**

The CI integration job (`.github/workflows/ci.yml`) runs a SINGLE step ("pgx adapter integration tests", currently ci.yml:77) whose `go test -tags=integration -race` takes a space-separated package list — it is NOT one step per adapter. Do NOT add a new step; append `./cache/redis/...` to that list:

```yaml
      - name: pgx adapter integration tests
        run: go test -tags=integration -race ./ports/database/pgx/... ./eventbus/outbox/pgx/... ./idempotency/redis/... ./jobs/asynq/... ./ratelimit/redisrate/... ./cache/redis/...
```

Read `ci.yml` first to confirm the exact current package list, then append `./cache/redis/...` to the end.

- [ ] **Step 5: Local verification**

Run:
```bash
go build ./... && go vet ./...
go test ./...                 # unit (root)
cd examples/orders && go build ./... && go vet ./... && cd ..
/usr/local/bin/golangci-lint run ./...
/usr/local/bin/golangci-lint run --build-tags=integration ./...
gofmt -l .
```
Expected: all clean/PASS. (Integration RUN is CI-only.)

- [ ] **Step 6: Commit + push + open adapter PR**

```bash
git add CHANGELOG.md README.md .github/workflows/ go.mod go.sum examples/orders/go.mod examples/orders/go.sum
git commit -F - <<'EOF'
docs(cache/redis): CHANGELOG + README + CI integration leg

Pins go-ddd-core at the ports/cache maturation pseudo-version; adds the
cache/redis integration step. Release framing (pseudo -> v0.11.0) lands in the
dep-bump PR per the tag-at-last-piece convention.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
EOF
```

Push `feat/cache-redis` and open the adapter PR. CI must be 5/5 green (incl. `integration (testcontainers)` running `cachetest.RunContract` vs real redis) BEFORE merge. Merge at the pseudo-version pin.

---

# Phase C — Tag-gate to `v0.11.0`

> External dependency: after Phase B's adapter PR merges green at the pseudo-pin, the core agent tags core `v0.11.0` (publishing the matured `ports/cache` + `cachetest`). Verify resolvable via proxy: `go list -m github.com/slam0504/go-ddd-core@v0.11.0`.

### Task C1: Adapter dep-bump to `v0.11.0`

**Files:** Modify `go.mod`/`go.sum` (root + `examples/orders`); `CHANGELOG.md`; `README.md`.

- [ ] **Step 1: Bump the core pin**

Run (root, then `examples/orders`):
```bash
go get github.com/slam0504/go-ddd-core@v0.11.0
go mod tidy
```

- [ ] **Step 2: Verify pseudo→tag content identity**

Run: `git diff go.sum` and confirm the `go-ddd-core` `/go.mod` + module hashes are byte-identical between the pseudo-version and `v0.11.0` (proves core tagged the exact content the adapter was conformance-tested against).

- [ ] **Step 3: Finalize CHANGELOG + README for the release**

Move the `[Unreleased]` cache block into `## [v0.11.0] - 2026-07-01`; fix compare links (`[v0.11.0]` added, `[Unreleased]` reset to `v0.11.0...HEAD`); README compat-matrix `v0.11.0` row.

- [ ] **Step 4: Verify + commit + PR**

Run: `go build ./... && go test ./... && /usr/local/bin/golangci-lint run ./...`
Expected: clean. Commit (`chore(deps): bump go-ddd-core to v0.11.0`), open the dep-bump PR, ensure CI 5/5 green, merge.

- [ ] **Step 5: Tag + Release**

At the dep-bump merge commit, cut the annotated `v0.11.0` tag, push, publish the GitHub Release as Latest (notes from CHANGELOG `[v0.11.0]`). Verify `releases/latest` → `v0.11.0`.

- [ ] **Step 6: Cross-repo memory sync**

Update `.agent/state.md`, `.agent/decisions.md`, `.agent/review-log.md`, and `<workspace-root>/.agent-memory/go-ddd.md` recording the v0.11.0 cache cycle CLOSED on both repos. **`.agent/` is gitignored-but-tracked**: run `git add .agent/*.md` and `git commit ...` as SEPARATE Bash calls — the `&&`-chained form exits 1 on the ignore advisory. Delete merged branches (`feat/cache-maturation` on core, `feat/cache-redis` + dep-bump branch on adapters).

---

## Self-Review

**Spec coverage:**
- §3.1 delete TypedCache → Task A1 ✅
- §3.2 cachetest → Task A2 ✅
- §3.3 godoc tightening (ctx/empty-key/TTL/precedence) → Task A1 ✅
- §4 adapter (Cmdable, guard, key, methods, errors) → Tasks B1–B3 ✅
- §5 cachetest invariants incl. byte-ownership + negTTL + empty-key → Task A2 ✅; non-vacuity dev-time proof → Task A2 Step 4 ✅
- §5.4 TTL-expiry intent test → Task B4 ✅
- §6 testing/docs/CI → Tasks B4, B5 ✅
- §7 tag-gate → Phase A merge (untagged) / Phase B pseudo-pin / Phase C tag ✅
- §8 verification (v2 lint, CI-only integration, hash identity) → B5 Step 5, C1 Step 2 ✅

**Placeholder scan:** No TBD/TODO. One deliberate "read the neighbouring file and mirror" instruction in B4 Step 2 (TestMain idiom) and B5 (README/CI style) — these are match-existing-pattern directions with the fallback code shown, not placeholders.

**Type consistency:** `New(redis.Cmdable, ...Option) (*Cache, error)`, `encode(string,string) string`, `WithKeyPrefix(string) Option`, `config{keyPrefix string}`, `ErrNilClient` (errorsx-coded CodeInvalidArgument), `cachetest.RunContract(*testing.T, Factory)`, `Factory func(t *testing.T) cache.Cache`, `cache.ErrMiss`, method set `Get/Set/Delete/Exists` — consistent across A2/B1/B2/B3/B4.
