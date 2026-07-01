# `ratelimit/redisrate` Inbound Rate-Limit Adapter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the first concrete `ports/ratelimit.Limiter` — a distributed,
Redis-backed inbound request limiter wrapping `redis_rate/v10` (GCRA) — merged
at a core pseudo-version pin with CI green including the integration leg.

**Architecture:** A `*Limiter` wraps `redis_rate.NewLimiter(client)` plus a fixed
`redis_rate.Limit` and a key namespace. `Allow(ctx, key)` enforces the contract's
fixed precedence (empty key → pre-cancelled/expired ctx → backend) before any
Redis contact, projects `redis_rate.Result` onto core's advisory `ratelimit.Result`
(with the mandatory `RetryAfter`-explicit-zero-on-allow correction), and maps
backend errors to coded `errorsx` (never `CodeUnknown`). Redis keys are encoded
prefix-free (length-prefixed namespace) so a client-supplied key can never
flatten into another namespace. Tests are core's `ratelimittest.RunContract`
against testcontainers Redis plus adapter-level unit + integration tests.

**Tech Stack:** Go 1.25; `github.com/go-redis/redis_rate/v10` (GCRA);
`github.com/redis/go-redis/v9 v9.20.0` (already present); core
`ports/ratelimit` contract; `internal/redistest` testcontainers helper;
`pkg/errorsx` coded errors.

## Global Constraints

Every task's requirements implicitly include this section.

- **Module:** `github.com/slam0504/go-ddd-adapters`; Go floor `1.25.0`.
- **Package:** path `ratelimit/redisrate`, package name `redisratelimit`
  (driver-in-path naming, mirrors `jobsasynq` / `pgxoutbox`).
- **Core pin = pseudo-version of `b882796`** (core `main`, PR #26 — the
  **untagged** `ports/ratelimit` contract). This cycle MUST stay on a
  pseudo-version pin. Do NOT bump core to a release tag — the core tag, the
  dep-bump PR, and the adapter tag are later cross-repo steps (spec §10), out of
  scope here.
- **New dependency:** `github.com/go-redis/redis_rate/v10` (latest `v10`,
  expected `v10.0.1`). It imports `github.com/redis/go-redis/v9` (same major as
  the existing pin) — no version conflict expected. Reuses the existing
  `internal/redistest` testcontainers helper.
- **Constructor surface (locked):**
  `New(client redis.UniversalClient, limit redis_rate.Limit, opts ...Option) (*Limiter, error)`.
  `limit` is positional and required (no safe default; fail loud). Only
  `WithKeyPrefix` is public — **NO public `WithLimiter`** (spec P2): real-Redis
  testcontainers cover v1; error-classification paths use invalid/closed clients.
- **Error discipline (contract):** ordinary quota exhaustion is **DATA**, not an
  error → `Result{Allowed:false}, nil`. The adapter NEVER mints
  `errorsx.CodeRateLimited`. `Allow` returns a non-nil error only for: empty key
  → `errorsx.CodeInvalidArgument` (no backend contact); a ctx cancelled/expired
  at entry or during the call → the matching ctx error verbatim
  (`errors.Is(context.Canceled / context.DeadlineExceeded)`, no backend contact
  for the pre-check); a backend failure → a coded `errorsx` whose `CodeOf` is
  **never `CodeUnknown`** (unreachable → `CodeUnavailable`; unclassifiable →
  `CodeInternal`). Fixed precedence: **empty key → pre-cancelled/expired ctx →
  backend.**
- **`Result` mapping (spec §5):** `Allowed = res.Allowed > 0` (redis_rate
  `Allowed` is a count); `RetryAfter` = **0 when allowed** (redis_rate returns
  `-1` when allowed — raw mapping leaks `-1` and breaks the contract), `=
  res.RetryAfter` when denied; `Limit = limit.Burst` (GCRA instantaneous-burst
  projection); `Remaining = res.Remaining`; `ResetAt` = zero (absent, avoids
  client-clock skew).
- **Key encoding prefix-free (spec P1):** `encode(keyPrefix, key) =
  fmt.Sprintf("%d:%s:%s", len(keyPrefix), keyPrefix, key)`. The length prefix
  makes distinct `(keyPrefix, key)` tuples non-colliding.
- **Import grouping (CI `goimports` `local-prefixes`):** three groups — (1)
  stdlib, (2) third-party **including `go-ddd-core`**, (3) `go-ddd-adapters/...`
  in its own trailing group. Getting this wrong is a recurring red
  `golangci-lint (.)`. Mirror `idempotency/redis/integration_test.go` exactly.
- **Bookkeeping split (spec §10):** this impl PR adds a CHANGELOG `[Unreleased]`
  entry + a README adapter-table row only. The README Status paragraph + compat
  matrix version bump ride the LATER dep-bump PR. **No tag in this PR.**
- **Local lint:** run the v2 binary `/usr/local/bin/golangci-lint` (the
  `~/go/bin` v1.64.8 chokes on the `version: "2"` config).
- **`.agent/*.md` commit quirk:** `.agent/` is globally gitignored but the `.md`
  files are tracked; `git add .agent/*.md && git commit` exits 1 on the advisory
  — run `git add` and `git commit` as SEPARATE Bash calls.

## File Structure

| File | Responsibility |
|---|---|
| `ratelimit/redisrate/key.go` | `encode(keyPrefix, key)` prefix-free length-prefixed Redis key. |
| `ratelimit/redisrate/key_test.go` | Adversarial injectivity unit test (colliding tuples → distinct keys). |
| `ratelimit/redisrate/options.go` | `Option`, private `config`, `defaultKeyPrefix`, `WithKeyPrefix`, `ErrNilClient`, `ErrInvalidLimit`. |
| `ratelimit/redisrate/options_test.go` | Default-namespace-non-empty + override unit tests (white-box). |
| `ratelimit/redisrate/limiter.go` | `Limiter` type, `New` (+ typed-nil guard), `Allow` (precedence), `mapResult`, `mapError`, `classifyBackendErr`. |
| `ratelimit/redisrate/limiter_test.go` | Unit tests (white-box): `New` guards, `mapResult` (incl. `-1` trap), `mapError`/`classifyBackendErr`, `Allow` no-backend precedence. |
| `ratelimit/redisrate/integration_test.go` | `//go:build integration`: `RunContract` (unique-namespace factory) + Redis-unavailable + recovery-after-refill + prefix-isolation. |
| `ratelimit/redisrate/doc.go` | Package documentation (distributed/GCRA, scope-out, `ResetAt` absence, cluster/ring single-key claim). |
| `.github/workflows/ci.yml` | Add `./ratelimit/...` to the integration step. |
| `CHANGELOG.md` | `[Unreleased]` `### Added` entry. |
| `README.md` | Adapter-table row. |

---

### Task 1: Pin core to the untagged `ports/ratelimit` contract

**Files:**
- Modify: `go.mod` (core require line)
- Modify: `examples/orders/go.mod` (core require line)
- Modify: `go.sum`, `examples/orders/go.sum` (via tooling)

**Interfaces:**
- Consumes: nothing (dependency setup).
- Produces: `github.com/slam0504/go-ddd-core/ports/ratelimit` becomes importable
  at the `b882796` contract (the `Limiter` / `Result` / `UnknownCount` symbols
  later tasks depend on).

- [ ] **Step 1: Bump the core pin on both modules to the `b882796` pseudo-version**

Run (root module first):

```bash
cd /Users/eason_tseng/playground/project/go-ddd-adapters
go get github.com/slam0504/go-ddd-core@b882796
go mod tidy
```

Then the examples module:

```bash
cd /Users/eason_tseng/playground/project/go-ddd-adapters/examples/orders
go get github.com/slam0504/go-ddd-core@b882796
go mod tidy
cd /Users/eason_tseng/playground/project/go-ddd-adapters
```

Expected: both `go.mod` files now require a `go-ddd-core` pseudo-version of the
form `v0.9.1-0.<timestamp>-b882796xxxxx` (replacing the `v0.9.0` tag). If
`go get` fails to resolve `b882796`, STOP — the contract commit is not reachable
on the proxy/origin and the cycle is blocked.

- [ ] **Step 2: Verify the contract package resolves and the tree still builds**

Run:

```bash
go list github.com/slam0504/go-ddd-core/ports/ratelimit
go build ./...
```

Expected: `go list` prints the package path (no error); `go build ./...` is
clean. No `.go` files changed yet, so nothing new compiles — this only proves
the pin is healthy.

- [ ] **Step 3: Confirm the pin shape**

Run:

```bash
go list -m github.com/slam0504/go-ddd-core
grep go-ddd-core go.mod examples/orders/go.mod
```

Expected: a `v0.9.1-0.*-b882796*` pseudo-version on both modules (NOT a bare
`v0.9.0`).

- [ ] **Step 4: Commit**

```bash
git add go.mod go.sum examples/orders/go.mod examples/orders/go.sum
git commit -m "chore(deps): pin go-ddd-core to the untagged ports/ratelimit contract (b882796)"
```

---

### Task 2: Prefix-free key encoding

**Files:**
- Create: `ratelimit/redisrate/key.go`
- Test: `ratelimit/redisrate/key_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `func encode(keyPrefix, key string) string` (unexported; used by
  `Limiter.Allow` in Task 7).

- [ ] **Step 1: Write the failing test**

Create `ratelimit/redisrate/key_test.go`:

```go
package redisratelimit

import "testing"

// TestEncodeInjective proves the length prefix makes (keyPrefix, key) injective.
// A naive prefix+":"+key collapses ("a","b:c") and ("a:b","c") both to "a:b:c".
// Asserting "two DIFFERENT-prefix limiters get different keys" would NOT catch
// this — distinct prefixes are already distinct strings and never exercise the
// flatten bug. We must assert the COLLIDING tuples map to DISTINCT keys.
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

// TestEncodeEmptyPrefixInjective: an empty keyPrefix still encodes injectively.
func TestEncodeEmptyPrefixInjective(t *testing.T) {
	if got, want := encode("", "k"), "0::k"; got != want {
		t.Fatalf("encode(\"\",\"k\") = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ratelimit/redisrate/ -run TestEncode -v`
Expected: FAIL — build error `undefined: encode`.

- [ ] **Step 3: Write minimal implementation**

Create `ratelimit/redisrate/key.go`:

```go
package redisratelimit

import "fmt"

// encode builds a prefix-free Redis key from (keyPrefix, key). Length-prefixing
// keyPrefix makes the prefix boundary unambiguous, so distinct (keyPrefix, key)
// tuples never collide: a client-supplied key cannot flatten into another
// namespace (the flatten bug idempotency/redis fixed in v0.8.0). An empty
// keyPrefix still encodes injectively ("0::" + key).
func encode(keyPrefix, key string) string {
	return fmt.Sprintf("%d:%s:%s", len(keyPrefix), keyPrefix, key)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./ratelimit/redisrate/ -run TestEncode -v`
Expected: PASS (both tests).

- [ ] **Step 5: Commit**

```bash
git add ratelimit/redisrate/key.go ratelimit/redisrate/key_test.go
git commit -m "feat(ratelimit/redisrate): prefix-free length-encoded Redis key"
```

---

### Task 3: Options, defaults, and sentinel errors

**Files:**
- Create: `ratelimit/redisrate/options.go`
- Test: `ratelimit/redisrate/options_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `type Option func(*config)`; `type config struct { keyPrefix string }`;
  `const defaultKeyPrefix = "ratelimit"`; `func WithKeyPrefix(prefix string) Option`;
  `var ErrNilClient`, `var ErrInvalidLimit` (both `*errorsx.Error`,
  `CodeInvalidArgument`). All consumed by `New` in Task 4.

- [ ] **Step 1: Write the failing test**

Create `ratelimit/redisrate/options_test.go`:

```go
package redisratelimit

import "testing"

// TestDefaultKeyPrefixNonEmpty: a non-empty default namespace is a design
// requirement (spec §6) — the adapter's keys must not sit at the bare top level
// of a shared Redis.
func TestDefaultKeyPrefixNonEmpty(t *testing.T) {
	if defaultKeyPrefix == "" {
		t.Fatal("defaultKeyPrefix is empty; the adapter must namespace its keys by default")
	}
}

// TestWithKeyPrefixOverrides: WithKeyPrefix replaces the default in the config.
func TestWithKeyPrefixOverrides(t *testing.T) {
	cfg := config{keyPrefix: defaultKeyPrefix}
	WithKeyPrefix("tenant-x")(&cfg)
	if cfg.keyPrefix != "tenant-x" {
		t.Fatalf("WithKeyPrefix: keyPrefix = %q, want %q", cfg.keyPrefix, "tenant-x")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ratelimit/redisrate/ -run 'TestDefaultKeyPrefixNonEmpty|TestWithKeyPrefixOverrides' -v`
Expected: FAIL — build error `undefined: defaultKeyPrefix` / `config` / `WithKeyPrefix`.

- [ ] **Step 3: Write minimal implementation**

Create `ratelimit/redisrate/options.go`:

```go
package redisratelimit

import "github.com/slam0504/go-ddd-core/pkg/errorsx"

// defaultKeyPrefix namespaces this limiter's Redis keys. Non-empty by design so
// the adapter's keys never sit at the bare top level of a shared Redis. It still
// encodes injectively via encode (see key.go).
const defaultKeyPrefix = "ratelimit"

var (
	// ErrNilClient is returned by New for a nil client interface or a typed-nil
	// of the three documented go-redis concrete types.
	ErrNilClient = errorsx.New(errorsx.CodeInvalidArgument, "redisratelimit: nil Redis client")
	// ErrInvalidLimit is returned by New when Rate, Burst, or Period is not > 0.
	ErrInvalidLimit = errorsx.New(errorsx.CodeInvalidArgument, "redisratelimit: limit must have positive Rate, Burst, and Period")
)

// Option configures a Limiter. Options are pure setters into the private config;
// validation runs in New AFTER every option is applied, so option order never
// changes the result (mirrors redisidempotency / jobsasynq).
type Option func(*config)

type config struct {
	keyPrefix string
}

// WithKeyPrefix overrides the Redis key namespace (default "ratelimit"). Two
// Limiters with different prefixes never share a bucket for the same key.
func WithKeyPrefix(prefix string) Option {
	return func(c *config) { c.keyPrefix = prefix }
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./ratelimit/redisrate/ -run 'TestDefaultKeyPrefixNonEmpty|TestWithKeyPrefixOverrides' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add ratelimit/redisrate/options.go ratelimit/redisrate/options_test.go
git commit -m "feat(ratelimit/redisrate): options, default namespace, sentinel errors"
```

---

### Task 4: `Limiter` type + `New` with typed-nil + invalid-limit guards

**Files:**
- Create: `ratelimit/redisrate/limiter.go`
- Create: `ratelimit/redisrate/limiter_test.go`

**Interfaces:**
- Consumes: `config`, `defaultKeyPrefix`, `WithKeyPrefix`, `ErrNilClient`,
  `ErrInvalidLimit` (Task 3).
- Produces: `type Limiter struct{ limiter *redis_rate.Limiter; limit redis_rate.Limit; keyPrefix string }`;
  `func New(client redis.UniversalClient, limit redis_rate.Limit, opts ...Option) (*Limiter, error)`.
  Both consumed by Tasks 5–8.

This task introduces the `redis_rate/v10` dependency.

- [ ] **Step 1: Add the redis_rate dependency**

Run:

```bash
go get github.com/go-redis/redis_rate/v10
```

Expected: `go.mod` gains `github.com/go-redis/redis_rate/v10 v10.0.1` (or the
latest `v10`). `go mod tidy` runs at the end of Step 5 (after code imports it).

- [ ] **Step 2: Write the failing test**

Create `ratelimit/redisrate/limiter_test.go`:

```go
package redisratelimit

import (
	"errors"
	"testing"
	"time"

	"github.com/go-redis/redis_rate/v10"
	"github.com/redis/go-redis/v9"
)

var validLimit = redis_rate.Limit{Rate: 1, Burst: 1, Period: time.Hour}

func TestNewRejectsNilClient(t *testing.T) {
	if _, err := New(nil, validLimit); !errors.Is(err, ErrNilClient) {
		t.Fatalf("New(nil): err = %v, want ErrNilClient", err)
	}
	if _, err := New((*redis.Client)(nil), validLimit); !errors.Is(err, ErrNilClient) {
		t.Fatalf("New((*redis.Client)(nil)): err = %v, want ErrNilClient", err)
	}
	if _, err := New((*redis.ClusterClient)(nil), validLimit); !errors.Is(err, ErrNilClient) {
		t.Fatalf("New((*redis.ClusterClient)(nil)): err = %v, want ErrNilClient", err)
	}
	if _, err := New((*redis.Ring)(nil), validLimit); !errors.Is(err, ErrNilClient) {
		t.Fatalf("New((*redis.Ring)(nil)): err = %v, want ErrNilClient", err)
	}
}

func TestNewRejectsInvalidLimit(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:0"})
	t.Cleanup(func() { _ = client.Close() })
	for _, tc := range []struct {
		name  string
		limit redis_rate.Limit
	}{
		{"zero value", redis_rate.Limit{}},
		{"zero rate", redis_rate.Limit{Rate: 0, Burst: 1, Period: time.Hour}},
		{"zero burst", redis_rate.Limit{Rate: 1, Burst: 0, Period: time.Hour}},
		{"zero period", redis_rate.Limit{Rate: 1, Burst: 1, Period: 0}},
		{"negative rate", redis_rate.Limit{Rate: -1, Burst: 1, Period: time.Hour}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(client, tc.limit); !errors.Is(err, ErrInvalidLimit) {
				t.Fatalf("New(%+v): err = %v, want ErrInvalidLimit", tc.limit, err)
			}
		})
	}
}

func TestNewValid(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:0"})
	t.Cleanup(func() { _ = client.Close() })
	l, err := New(client, validLimit, WithKeyPrefix("custom"))
	if err != nil {
		t.Fatalf("New valid: %v", err)
	}
	if l == nil {
		t.Fatal("New valid returned nil Limiter")
	}
	if l.keyPrefix != "custom" {
		t.Fatalf("keyPrefix = %q, want %q", l.keyPrefix, "custom")
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./ratelimit/redisrate/ -run TestNew -v`
Expected: FAIL — build error `undefined: New`.

- [ ] **Step 4: Write minimal implementation**

Create `ratelimit/redisrate/limiter.go`:

```go
package redisratelimit

import (
	"github.com/go-redis/redis_rate/v10"
	"github.com/redis/go-redis/v9"
)

// Limiter is a distributed inbound rate limiter backed by Redis (redis_rate's
// GCRA). It implements core's ratelimit.Limiter and is safe for concurrent use.
type Limiter struct {
	limiter   *redis_rate.Limiter
	limit     redis_rate.Limit
	keyPrefix string
}

// New builds a Limiter wrapping client under the given redis_rate limit. client
// may be *redis.Client, *redis.ClusterClient, or *redis.Ring (all satisfy
// redis.UniversalClient, which in turn satisfies redis_rate's internal rediser
// — that needs Del, which redis.Scripter lacks). New rejects a nil interface and
// a typed-nil of those three concrete types; a typed-nil custom client is NOT
// guarded and surfaces on first Allow. limit is positional and required: Rate,
// Burst, and Period MUST all be > 0 — there is no universal safe default, and a
// silent one would turn "forgot to configure" into a silent bug.
func New(client redis.UniversalClient, limit redis_rate.Limit, opts ...Option) (*Limiter, error) {
	if client == nil {
		return nil, ErrNilClient
	}
	// An interface holding a nil pointer is itself non-nil, so the check above
	// does not catch New((*redis.Client)(nil), ...). Type-switch the documented
	// concrete types and reject a nil pointer of each (mirrors redisidempotency).
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
	if limit.Rate <= 0 || limit.Burst <= 0 || limit.Period <= 0 {
		return nil, ErrInvalidLimit
	}
	cfg := config{keyPrefix: defaultKeyPrefix}
	for _, opt := range opts {
		opt(&cfg)
	}
	return &Limiter{
		limiter:   redis_rate.NewLimiter(client),
		limit:     limit,
		keyPrefix: cfg.keyPrefix,
	}, nil
}
```

- [ ] **Step 5: Run test + tidy + verify it passes**

Run:

```bash
go mod tidy
go test ./ratelimit/redisrate/ -run TestNew -v
```

Expected: `go mod tidy` keeps `redis_rate/v10` (now imported); tests PASS.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum ratelimit/redisrate/limiter.go ratelimit/redisrate/limiter_test.go
git commit -m "feat(ratelimit/redisrate): Limiter type + New with typed-nil and invalid-limit guards"
```

---

### Task 5: `mapResult` — GCRA projection with the `RetryAfter` `-1` correction

**Files:**
- Modify: `ratelimit/redisrate/limiter.go` (add `mapResult`)
- Modify: `ratelimit/redisrate/limiter_test.go` (append tests + the `ratelimit` import is NOT needed; see note)

**Interfaces:**
- Consumes: `Limiter`, `redis_rate.Limit` (Task 4).
- Produces: `func mapResult(res *redis_rate.Result, limit redis_rate.Limit) ratelimit.Result`
  (unexported; used by `Allow` in Task 7).

- [ ] **Step 1: Write the failing test**

Append to `ratelimit/redisrate/limiter_test.go` (the existing import block
already has `redis_rate` and `time`; no new imports needed — `mapResult`
returns a `ratelimit.Result` but the test reads its fields by value, so no
explicit `ratelimit` import is required):

```go
func TestMapResultAllowed(t *testing.T) {
	// redis_rate returns RetryAfter = -1 when allowed; the contract REQUIRES 0.
	res := &redis_rate.Result{
		Allowed:    1,
		Remaining:  9,
		RetryAfter: -1,
		ResetAfter: time.Second,
		Limit:      redis_rate.Limit{Rate: 10, Burst: 10, Period: time.Second},
	}
	got := mapResult(res, redis_rate.Limit{Rate: 10, Burst: 10, Period: time.Second})
	if !got.Allowed {
		t.Fatal("Allowed=1 must map to Allowed=true")
	}
	if got.RetryAfter != 0 {
		t.Fatalf("allowed RetryAfter = %v, want 0 — raw -1 must NOT leak (contract: allowed ⇒ RetryAfter 0)", got.RetryAfter)
	}
	if got.Limit != 10 {
		t.Fatalf("Limit = %d, want 10 (Burst)", got.Limit)
	}
	if got.Remaining != 9 {
		t.Fatalf("Remaining = %d, want 9", got.Remaining)
	}
	if !got.ResetAt.IsZero() {
		t.Fatalf("ResetAt = %v, want zero (absent)", got.ResetAt)
	}
}

func TestMapResultDenied(t *testing.T) {
	res := &redis_rate.Result{
		Allowed:    0,
		Remaining:  0,
		RetryAfter: 250 * time.Millisecond,
		ResetAfter: time.Second,
		Limit:      redis_rate.Limit{Rate: 1, Burst: 1, Period: time.Second},
	}
	got := mapResult(res, redis_rate.Limit{Rate: 1, Burst: 1, Period: time.Second})
	if got.Allowed {
		t.Fatal("Allowed=0 must map to Allowed=false")
	}
	if got.RetryAfter != 250*time.Millisecond {
		t.Fatalf("denied RetryAfter = %v, want 250ms (the backend hint)", got.RetryAfter)
	}
	if got.Remaining > got.Limit {
		t.Fatalf("Remaining (%d) > Limit (%d) while both known", got.Remaining, got.Limit)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ratelimit/redisrate/ -run TestMapResult -v`
Expected: FAIL — build error `undefined: mapResult`.

- [ ] **Step 3: Write minimal implementation**

Add the `ratelimit` import to `limiter.go`'s import block and append `mapResult`.
The `limiter.go` import block becomes:

```go
import (
	"github.com/go-redis/redis_rate/v10"
	"github.com/redis/go-redis/v9"
	"github.com/slam0504/go-ddd-core/ports/ratelimit"
)
```

Append to `limiter.go`:

```go
// mapResult projects redis_rate's GCRA result onto core's advisory Result.
//   - Allowed: redis_rate.Allowed is a COUNT (0 = denied, >0 = allowed).
//   - RetryAfter: redis_rate returns -1 when allowed; the contract REQUIRES 0
//     on allow, so it is mapped explicitly (raw mapping would leak -1). On
//     denial res.RetryAfter is > 0 and <= Period.
//   - Limit: the GCRA instantaneous burst (limit.Burst), NOT a fixed-window
//     quota.
//   - Remaining: instantaneous capacity; GCRA guarantees <= Burst, so
//     Remaining <= Limit.
//   - ResetAt: absent (zero) — avoids client-clock skew (see doc.go scope-out).
func mapResult(res *redis_rate.Result, limit redis_rate.Limit) ratelimit.Result {
	out := ratelimit.Result{
		Allowed:   res.Allowed > 0,
		Limit:     limit.Burst,
		Remaining: res.Remaining,
	}
	if !out.Allowed {
		out.RetryAfter = res.RetryAfter
	}
	// Allowed: RetryAfter stays the zero value; res.RetryAfter (-1) is NEVER read.
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./ratelimit/redisrate/ -run TestMapResult -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add ratelimit/redisrate/limiter.go ratelimit/redisrate/limiter_test.go
git commit -m "feat(ratelimit/redisrate): map redis_rate.Result with RetryAfter-zero-on-allow correction"
```

---

### Task 6: `mapError` + `classifyBackendErr` — coded errors, never `CodeUnknown`

**Files:**
- Modify: `ratelimit/redisrate/limiter.go` (add `mapError`, `classifyBackendErr`)
- Modify: `ratelimit/redisrate/limiter_test.go` (append tests)

**Interfaces:**
- Consumes: nothing new.
- Produces: `func mapError(err error) error`;
  `func classifyBackendErr(err error) errorsx.Code` (both unexported; `mapError`
  used by `Allow` in Task 7).

- [ ] **Step 1: Write the failing test**

Append to `ratelimit/redisrate/limiter_test.go`, and extend its import block to
add `"context"`, `"net"`, and `"github.com/slam0504/go-ddd-core/pkg/errorsx"`
(keep the import groups: stdlib together, then third-party including
`go-ddd-core`):

```go
func TestMapErrorCtxVerbatim(t *testing.T) {
	if got := mapError(context.Canceled); !errors.Is(got, context.Canceled) {
		t.Fatalf("mapError(Canceled) = %v, want errors.Is(context.Canceled)", got)
	}
	if got := mapError(context.DeadlineExceeded); !errors.Is(got, context.DeadlineExceeded) {
		t.Fatalf("mapError(DeadlineExceeded) = %v, want errors.Is(context.DeadlineExceeded)", got)
	}
	// A ctx error must pass through verbatim, NOT be re-coded.
	if code := errorsx.CodeOf(mapError(context.Canceled)); code == errorsx.CodeUnavailable || code == errorsx.CodeInternal {
		t.Fatalf("ctx error must pass through verbatim, got coded %v", code)
	}
}

func TestClassifyBackendErr(t *testing.T) {
	if got := classifyBackendErr(&net.OpError{Op: "dial", Err: errors.New("connection refused")}); got != errorsx.CodeUnavailable {
		t.Fatalf("net.Error → %v, want CodeUnavailable", got)
	}
	if got := classifyBackendErr(errors.New("dial tcp 127.0.0.1:6379: connect: connection refused")); got != errorsx.CodeUnavailable {
		t.Fatalf("connection-refused string → %v, want CodeUnavailable", got)
	}
	if got := classifyBackendErr(errors.New("some weird logical failure")); got != errorsx.CodeInternal {
		t.Fatalf("unclassifiable → %v, want CodeInternal (never CodeUnknown)", got)
	}
	if got := classifyBackendErr(errors.New("anything at all")); got == errorsx.CodeUnknown {
		t.Fatal("classifyBackendErr must never return CodeUnknown")
	}
}

func TestMapErrorNeverUnknown(t *testing.T) {
	if code := errorsx.CodeOf(mapError(errors.New("boom"))); code == errorsx.CodeUnknown {
		t.Fatal("mapError must produce a coded error (CodeOf != CodeUnknown) for non-ctx backend errors")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ratelimit/redisrate/ -run 'TestMapError|TestClassifyBackendErr' -v`
Expected: FAIL — build error `undefined: mapError` / `classifyBackendErr`.

- [ ] **Step 3: Write minimal implementation**

Extend `limiter.go`'s import block to:

```go
import (
	"context"
	"errors"
	"net"
	"strings"

	"github.com/go-redis/redis_rate/v10"
	"github.com/redis/go-redis/v9"
	"github.com/slam0504/go-ddd-core/pkg/errorsx"
	"github.com/slam0504/go-ddd-core/ports/ratelimit"
)
```

Append to `limiter.go`:

```go
// mapError maps a redis_rate.Allow error to the contract's CLASS-2 shape. A ctx
// error (cancellation/expiry observed DURING the call) is returned verbatim; any
// other backend error gets a coded errorsx surface whose CodeOf is never
// CodeUnknown.
func mapError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return errorsx.Wrap(classifyBackendErr(err), "redisratelimit: allow", err)
}

// classifyBackendErr maps a non-ctx backend error to CodeUnavailable (transport
// / reachability failure) or CodeInternal (everything else) — never
// CodeUnknown. Mirrors jobs/asynq.classifyBackendErr.
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./ratelimit/redisrate/ -run 'TestMapError|TestClassifyBackendErr' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add ratelimit/redisrate/limiter.go ratelimit/redisrate/limiter_test.go
git commit -m "feat(ratelimit/redisrate): backend error mapping (ctx verbatim, coded otherwise, never Unknown)"
```

---

### Task 7: `Allow` — fixed precedence + backend call

**Files:**
- Modify: `ratelimit/redisrate/limiter.go` (add `Allow` method)
- Modify: `ratelimit/redisrate/limiter_test.go` (append precedence tests + helper)

**Interfaces:**
- Consumes: `encode` (Task 2), `Limiter`/`New` (Task 4), `mapResult` (Task 5),
  `mapError` (Task 6).
- Produces: `func (l *Limiter) Allow(ctx context.Context, key string) (ratelimit.Result, error)`
  — completes the `ratelimit.Limiter` interface. Consumed by Task 8's
  `RunContract`.

- [ ] **Step 1: Write the failing test**

Append to `ratelimit/redisrate/limiter_test.go`. The import block already has
`context`, `errors`, `time`, `redis`, `redis_rate`, `errorsx`. The tests below
use an unreachable client whose backend is NEVER contacted — the precedence
short-circuit returns before any Redis call (a real backend call would surface
`CodeUnavailable`, not the ctx error / `CodeInvalidArgument` asserted here):

```go
// newUnreachableLimiter builds a Limiter whose client points at a closed port.
// The precedence tests never reach the backend, so the unreachable addr proves
// the short-circuit.
func newUnreachableLimiter(t *testing.T) *Limiter {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:0"})
	t.Cleanup(func() { _ = client.Close() })
	l, err := New(client, redis_rate.Limit{Rate: 1, Burst: 1, Period: time.Hour})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return l
}

func TestAllowEmptyKeyInvalidArgument(t *testing.T) {
	l := newUnreachableLimiter(t)
	res, err := l.Allow(context.Background(), "")
	if code := errorsx.CodeOf(err); code != errorsx.CodeInvalidArgument {
		t.Fatalf("empty key: code = %v, want CodeInvalidArgument (err=%v)", code, err)
	}
	if res.Allowed {
		t.Fatal("empty key returned Allowed=true; an error return must not also allow")
	}
}

func TestAllowEmptyKeyPrecedesCancelledCtx(t *testing.T) {
	l := newUnreachableLimiter(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := l.Allow(ctx, "")
	if code := errorsx.CodeOf(err); code != errorsx.CodeInvalidArgument {
		t.Fatalf("empty key + cancelled ctx: code = %v, want CodeInvalidArgument (precedence)", code)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatal("empty key must precede ctx observation")
	}
}

func TestAllowPreCancelledCtxNoBackend(t *testing.T) {
	l := newUnreachableLimiter(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := l.Allow(ctx, "k")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled ctx: err = %v, want errors.Is(context.Canceled) with no backend contact", err)
	}
}

func TestAllowPreExpiredCtxNoBackend(t *testing.T) {
	l := newUnreachableLimiter(t)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, err := l.Allow(ctx, "k")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pre-expired ctx: err = %v, want errors.Is(context.DeadlineExceeded)", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ratelimit/redisrate/ -run TestAllow -v`
Expected: FAIL — build error `l.Allow undefined (type *Limiter has no field or method Allow)`.

- [ ] **Step 3: Write minimal implementation**

Append the `Allow` method to `limiter.go`:

```go
// Allow reports whether key may proceed right now, as Result data. Ordinary
// quota exhaustion is NOT an error — it returns Result{Allowed:false}, nil with
// a RetryAfter hint. Fixed precedence (contract): empty key → pre-cancelled or
// expired ctx → backend. The first two short-circuit before any Redis contact.
func (l *Limiter) Allow(ctx context.Context, key string) (ratelimit.Result, error) {
	if key == "" {
		return ratelimit.Result{}, errorsx.New(errorsx.CodeInvalidArgument,
			"redisratelimit: empty key (a missing partition key, not an anonymous caller)")
	}
	if err := ctx.Err(); err != nil {
		return ratelimit.Result{}, err
	}
	res, err := l.limiter.Allow(ctx, encode(l.keyPrefix, key), l.limit)
	if err != nil {
		return ratelimit.Result{}, mapError(err)
	}
	return mapResult(res, l.limit), nil
}
```

- [ ] **Step 4: Run test + compile-time interface check**

Run: `go test ./ratelimit/redisrate/ -run TestAllow -v`
Expected: PASS.

Then add a compile-time assertion that `*Limiter` satisfies the interface.
Append to `limiter.go` (after the imports, before the type — or at file end):

```go
// Compile-time assertion that *Limiter implements core's ratelimit.Limiter.
var _ ratelimit.Limiter = (*Limiter)(nil)
```

Run: `go build ./ratelimit/...`
Expected: clean (the assertion compiles only if `Allow` matches the interface).

- [ ] **Step 5: Commit**

```bash
git add ratelimit/redisrate/limiter.go ratelimit/redisrate/limiter_test.go
git commit -m "feat(ratelimit/redisrate): Allow with fixed empty-key/ctx/backend precedence"
```

---

### Task 8: Integration suite — `RunContract` + adapter-level Redis tests

**Files:**
- Create: `ratelimit/redisrate/integration_test.go`

**Interfaces:**
- Consumes: `New`, `WithKeyPrefix` (Tasks 3–4); `redistest.StartContainer`
  (existing: `func StartContainer(ctx context.Context) (*redis.Client, func(), error)`);
  `ratelimittest.RunContract(t, factory)` (core).
- Produces: nothing (terminal test file).

- [ ] **Step 1: Write the integration test file**

Create `ratelimit/redisrate/integration_test.go` (black-box `_test` package,
mirroring `idempotency/redis/integration_test.go`; note the three import
groups — stdlib, third-party incl. `go-ddd-core`, then `go-ddd-adapters/...`):

```go
//go:build integration

package redisratelimit_test

import (
	"context"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-redis/redis_rate/v10"
	"github.com/redis/go-redis/v9"
	"github.com/slam0504/go-ddd-core/pkg/errorsx"
	"github.com/slam0504/go-ddd-core/ports/ratelimit"
	"github.com/slam0504/go-ddd-core/ports/ratelimit/ratelimittest"

	redisratelimit "github.com/slam0504/go-ddd-adapters/ratelimit/redisrate"
	"github.com/slam0504/go-ddd-adapters/internal/redistest"
)

var sharedClient *redis.Client

func TestMain(m *testing.M) {
	ctx := context.Background()
	client, cleanup, err := redistest.StartContainer(ctx)
	if err != nil {
		panic(err)
	}
	sharedClient = client
	code := m.Run()
	cleanup()
	os.Exit(code)
}

// factoryCounter guarantees a UNIQUE namespace per factory call. RunContract
// reuses fixed keys (ratelimittest:a / :b) across subtests and TestMain boots
// the container ONCE; under -count>1 / -race reruns t.Name() repeats, so an
// atomic counter (not t.Name() alone, not the long Period) is what isolates
// state across calls.
var factoryCounter atomic.Uint64

// TestRateLimitContract runs core's deterministic conformance suite. Profile:
// Burst 1 → first same-key Allow allowed, second denied; the long Period keeps
// GCRA refill from racing the suite's no-sleep invariants.
func TestRateLimitContract(t *testing.T) {
	ratelimittest.RunContract(t, func(t *testing.T) ratelimit.Limiter {
		n := factoryCounter.Add(1)
		l, err := redisratelimit.New(
			sharedClient,
			redis_rate.Limit{Rate: 1, Burst: 1, Period: time.Hour},
			redisratelimit.WithKeyPrefix("ratelimittest:"+t.Name()+":"+strconv.FormatUint(n, 10)),
		)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return l
	})
}

// TestAllowRedisUnavailable: a valid key + live ctx against an unreachable Redis
// must surface CodeUnavailable (never CodeUnknown). Port 1 refuses immediately.
func TestAllowRedisUnavailable(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = client.Close() })
	l, err := redisratelimit.New(client, redis_rate.Limit{Rate: 1, Burst: 1, Period: time.Hour})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = l.Allow(context.Background(), "k")
	if code := errorsx.CodeOf(err); code != errorsx.CodeUnavailable {
		t.Fatalf("unreachable Redis: code = %v, want CodeUnavailable (err=%v)", code, err)
	}
}

// TestAllowRecoversAfterRefill exercises the timing-dependent behaviour
// RunContract deliberately omits: deplete the bucket, wait past RetryAfter, and
// the key is allowed again (GCRA refill).
func TestAllowRecoversAfterRefill(t *testing.T) {
	n := factoryCounter.Add(1)
	l, err := redisratelimit.New(sharedClient,
		redis_rate.Limit{Rate: 1, Burst: 1, Period: 500 * time.Millisecond},
		redisratelimit.WithKeyPrefix("recover:"+strconv.FormatUint(n, 10)),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	first, err := l.Allow(ctx, "k")
	if err != nil || !first.Allowed {
		t.Fatalf("first Allow: allowed=%v err=%v, want allowed", first.Allowed, err)
	}
	if first.RetryAfter != 0 {
		t.Fatalf("allowed RetryAfter = %v, want 0", first.RetryAfter)
	}
	denied, err := l.Allow(ctx, "k")
	if err != nil {
		t.Fatalf("second Allow: %v", err)
	}
	if denied.Allowed {
		t.Fatal("second Allow: want denied")
	}
	if denied.RetryAfter <= 0 {
		t.Fatalf("denied RetryAfter = %v, want > 0", denied.RetryAfter)
	}
	time.Sleep(denied.RetryAfter + 100*time.Millisecond)
	recovered, err := l.Allow(ctx, "k")
	if err != nil {
		t.Fatalf("recovery Allow: %v", err)
	}
	if !recovered.Allowed {
		t.Fatal("after waiting past RetryAfter the key must be allowed again (GCRA refill)")
	}
}

// TestKeyPrefixIsolationEndToEnd: two Limiters with different prefixes do not
// share a bucket for the same key (end-to-end check on top of the encode unit
// test).
func TestKeyPrefixIsolationEndToEnd(t *testing.T) {
	n := factoryCounter.Add(1)
	mk := func(prefix string) ratelimit.Limiter {
		l, err := redisratelimit.New(sharedClient,
			redis_rate.Limit{Rate: 1, Burst: 1, Period: time.Hour},
			redisratelimit.WithKeyPrefix(prefix+":"+strconv.FormatUint(n, 10)),
		)
		if err != nil {
			t.Fatalf("New(%s): %v", prefix, err)
		}
		return l
	}
	a := mk("tenant-a")
	b := mk("tenant-b")
	ctx := context.Background()
	if _, err := a.Allow(ctx, "same-key"); err != nil {
		t.Fatalf("a deplete #1: %v", err)
	}
	depleted, err := a.Allow(ctx, "same-key")
	if err != nil {
		t.Fatalf("a deplete #2: %v", err)
	}
	if depleted.Allowed {
		t.Fatal("a second Allow should be denied (Burst 1)")
	}
	res, err := b.Allow(ctx, "same-key")
	if err != nil {
		t.Fatalf("b Allow: %v", err)
	}
	if !res.Allowed {
		t.Fatal("tenant-b first Allow on the same key was denied; prefixes must isolate buckets")
	}
}
```

- [ ] **Step 2: Run the integration suite (requires Docker)**

Run: `go test -tags=integration -race ./ratelimit/redisrate/ -v`
Expected: PASS — `TestRateLimitContract` (all RunContract subtests),
`TestAllowRedisUnavailable`, `TestAllowRecoversAfterRefill`,
`TestKeyPrefixIsolationEndToEnd`. The container boots once via `TestMain`.

If Docker is unavailable in this environment, note that explicitly and defer
this leg to CI (Task 9 wires it); do NOT claim it passed.

- [ ] **Step 3: Commit**

```bash
git add ratelimit/redisrate/integration_test.go
git commit -m "test(ratelimit/redisrate): RunContract + unavailable/recovery/prefix-isolation integration suite"
```

---

### Task 9: Package doc, CI wiring, bookkeeping, and final verification

**Files:**
- Create: `ratelimit/redisrate/doc.go`
- Modify: `.github/workflows/ci.yml:77`
- Modify: `CHANGELOG.md` (`[Unreleased]`)
- Modify: `README.md` (adapter table, after the `jobs/asynq` row)

**Interfaces:**
- Consumes: the whole package (documents it).
- Produces: nothing.

- [ ] **Step 1: Write the package doc**

Create `ratelimit/redisrate/doc.go`:

```go
// Package redisratelimit is a distributed, Redis-backed implementation of core's
// ports/ratelimit.Limiter, wrapping github.com/go-redis/redis_rate/v10 (the
// Generic Cell Rate Algorithm, GCRA).
//
// # Why distributed
//
// Inbound throttling on a horizontally-scaled service needs all instances to
// share one quota. An in-process limiter would let each instance count
// independently (effective quota = N × the configured rate). This adapter keeps
// the counter in Redis, so the quota holds across instances. It matches the
// repo's other Redis-backed adapters (idempotency/redis, jobs/asynq).
//
// # Construction
//
//	l, err := redisratelimit.New(client, redis_rate.Limit{Rate: 100, Burst: 100, Period: time.Minute})
//
// client may be *redis.Client, *redis.ClusterClient, or *redis.Ring (any
// redis.UniversalClient — redis_rate's internal rediser needs Del, which
// redis.Scripter lacks). The limit is positional and required: there is no
// universal safe default, so a missing limit fails loud at New rather than
// silently throttling at some arbitrary rate. WithKeyPrefix overrides the key
// namespace (default "ratelimit"); two Limiters with different prefixes never
// share a bucket for the same key.
//
// # Result projection (accurate-or-absent)
//
// Allow returns the decision as data (Result.Allowed) — ordinary quota
// exhaustion is Result{Allowed:false}, nil, never an error and never
// errorsx.CodeRateLimited. The advisory metadata projects redis_rate's GCRA
// state: Limit is the instantaneous burst (Burst), Remaining is the
// instantaneous capacity (<= Burst). These describe the GCRA burst the key may
// consume right now, NOT a fixed-window "X requests per window" quota; an HTTP
// middleware whose header convention cannot faithfully express that projection
// SHOULD omit the headers rather than fabricate a window number. RetryAfter is 0
// when allowed (redis_rate returns -1 there; this adapter maps it to the
// contract-required 0) and the backend wait hint when denied. ResetAt is absent:
// projecting an absolute reset instant would require blending the client clock
// with a Redis-side duration (skew risk); a WithClock option can add it when a
// middleware consumer needs the header.
//
// # Errors
//
// A non-nil error means Allow could not reach a decision, in fixed precedence:
// an empty key is a missing partition key (errorsx.CodeInvalidArgument, no
// backend contact); a ctx already cancelled or past its deadline returns the
// matching ctx error verbatim (no backend contact); a backend failure is a coded
// errorsx whose CodeOf is never CodeUnknown (unreachable → CodeUnavailable;
// unclassifiable → CodeInternal).
//
// # Redis Cluster / Ring
//
// Every redis_rate operation is single-key, so it stays within one hash slot by
// construction — *redis.ClusterClient / *redis.Ring SHOULD work with no hash
// tag. This is an API-derived claim; CI exercises single-node redis:7-alpine.
package redisratelimit
```

- [ ] **Step 2: Wire the integration suite into CI**

Modify `.github/workflows/ci.yml` line 77. Change:

```yaml
        run: go test -tags=integration -race ./ports/database/pgx/... ./eventbus/outbox/pgx/... ./idempotency/redis/... ./jobs/asynq/...
```

to:

```yaml
        run: go test -tags=integration -race ./ports/database/pgx/... ./eventbus/outbox/pgx/... ./idempotency/redis/... ./jobs/asynq/... ./ratelimit/redisrate/...
```

- [ ] **Step 3: Add the CHANGELOG `[Unreleased]` entry**

In `CHANGELOG.md`, replace the empty `## [Unreleased]` section so it reads:

```markdown
## [Unreleased]

### Added

- `ratelimit/redisrate` (`redisratelimit`): the first concrete
  `ports/ratelimit.Limiter` — a distributed, Redis-backed inbound request
  limiter wrapping `github.com/go-redis/redis_rate/v10` (GCRA). `New(client,
  limit, opts...)` takes any `redis.UniversalClient` and a required positional
  `redis_rate.Limit`; `Allow` returns the decision as data (ordinary denial is
  `Result{Allowed:false}, nil`, never `CodeRateLimited`) with the fixed empty-key
  → ctx → backend precedence. `RetryAfter` is explicit-zero on allow (redis_rate
  returns `-1`); `Limit`/`Remaining` project the GCRA instantaneous burst;
  `ResetAt` is absent. Redis keys are prefix-free length-encoded so a
  client-supplied key cannot flatten into another namespace. `WithKeyPrefix`
  only. Bumps the core dependency to the untagged `ports/ratelimit` contract.
```

- [ ] **Step 4: Add the README adapter-table row**

In `README.md`, immediately after the `jobs/asynq` row (the line beginning
`| `jobs/asynq` |`), insert:

```markdown
| `ratelimit/redisrate` | `ratelimit.Limiter` | [go-redis/redis_rate v10][redisrate] (GCRA, Redis-backed); distributed inbound limiter, denial-is-data (never `CodeRateLimited`), `RetryAfter`-zero-on-allow, prefix-free length-encoded keys, GCRA-burst `Limit`/`Remaining` projection, `ResetAt` absent; `WithKeyPrefix` only. Redis 3.2+ |
```

Then add the link reference near the other link definitions at the bottom of
`README.md` (where `[asynq]`, `[goredis]`, etc. are defined):

```markdown
[redisrate]: https://github.com/go-redis/redis_rate
```

- [ ] **Step 5: Full verification matrix**

Run from the repo root:

```bash
gofmt -l ratelimit/redisrate/
go build ./...
go vet ./...
go test ./ratelimit/redisrate/
/usr/local/bin/golangci-lint run ./...
/usr/local/bin/golangci-lint run --build-tags=integration ./...
```

Expected: `gofmt -l` prints nothing; build/vet clean; unit tests PASS; both lint
runs report 0 issues. If Docker is available, also run
`go test -tags=integration -race ./ratelimit/redisrate/` and confirm PASS;
otherwise note it is deferred to CI.

- [ ] **Step 6: Commit**

```bash
git add ratelimit/redisrate/doc.go .github/workflows/ci.yml CHANGELOG.md README.md
git commit -m "docs(ratelimit/redisrate): package doc, CI integration leg, CHANGELOG + README row"
```

---

## Post-implementation (out of scope for the task list, for the human/PR)

- Open the impl PR from `feat/ratelimit-redisrate`; confirm CI is green
  including the new `./ratelimit/redisrate/...` integration leg, **at the core
  pseudo-version pin**.
- The cross-repo tag-gate continues AFTER merge (spec §10): core tags the
  ratelimit release → adapter dep-bump PR (pseudo → tag, README Status + compat
  matrix ride it) → adapter release tag + GitHub Release. None of these are in
  this plan.
- Update `.agent/state.md`, `.agent/decisions.md`, `.agent/review-log.md`, and
  `<workspace-root>/.agent-memory/go-ddd.md` after merge (commit `.agent/*.md`
  with a separate `git add` then `git commit` — the gitignore advisory).

## Self-Review

**Spec coverage:**
- §1 goal / distributed-first → Task 4 (`New`) + doc.go (Task 9). ✓
- §2 deps + package path + file list → Global Constraints + File Structure +
  Tasks 1, 4. ✓
- §3 `New` signature, `UniversalClient`, positional required limit, typed-nil
  guard, no public `WithLimiter`, `WithKeyPrefix` only → Tasks 3, 4. ✓
- §4 `Allow` precedence (empty key → ctx → backend) → Task 7. ✓
- §5 Result mapping incl. `RetryAfter` `-1` correction, `Limit=Burst`,
  `ResetAt` absent → Task 5. ✓
- §6 prefix-free length-prefixed key (P1) → Task 2. ✓
- §7 error mapping (ctx verbatim; Unavailable/Internal, never Unknown) → Task 6. ✓
- §8 testing: `RunContract` + mandatory per-factory unique namespace; adversarial
  key UNIT test; Redis-unavailable; recovery-after-wait; prefix isolation; ctx
  precedence → Tasks 2, 7, 8. ✓
- §9 design decisions → encoded across the relevant tasks + doc.go. ✓
- §10 tag-gate deferred past impl PR → Global Constraints + Post-implementation. ✓
- §11 verification (`gofmt`, build, vet, unit, integration, lint) → Task 9 + CI
  (Task 9 step 2). ✓

**Placeholder scan:** no `TBD` / "add error handling" / "write tests for the
above" — every code step carries complete code. ✓

**Type consistency:** `encode(keyPrefix, key string) string` (Tasks 2, 7);
`config{keyPrefix string}` + `WithKeyPrefix` + `defaultKeyPrefix` (Tasks 3, 4);
`New(client redis.UniversalClient, limit redis_rate.Limit, opts ...Option) (*Limiter, error)`
(Task 4, used in 8); `mapResult(*redis_rate.Result, redis_rate.Limit) ratelimit.Result`
(Tasks 5, 7); `mapError(error) error` + `classifyBackendErr(error) errorsx.Code`
(Tasks 6, 7); `Allow(context.Context, string) (ratelimit.Result, error)` (Task
7, used in 8). All names match across tasks. ✓
