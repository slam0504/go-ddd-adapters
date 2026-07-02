# httpclient/std Adapter (+ cache/redis Health Export) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the `httpclient/std` (`stdhttp`) adapter as adapters v0.12.0, plus two prerequisites: a `cache/redis` HealthCheck export (own small PR) and a core `ports/httpclient` godoc fix (merge untagged).

**Architecture:** Three sequenced deliverables per the spec (`docs/superpowers/specs/2026-07-02-httpclient-std-adapter-design.md`). Part 1 is an additive method on `rediscache.Cache` returning a core `health.Check`. Part 2 is comment-only in go-ddd-core. Part 3 wraps `*http.Client` behind core's `ports/httpclient.Client` with stdlib-parity defaults; tracing is opt-in via explicit `trace.TracerProvider` injection; `ContextualClient` is a wrapper type (method-name collision makes single-type dual-implementation impossible).

**Tech Stack:** Go 1.25, go-redis v9.20.0, go-ddd-core v0.12.0 (pin unchanged), otelhttp v0.61.0 (promoted indirect→direct), OTel SDK trace + tracetest for span assertions.

## Global Constraints

- Adapter repo pins `go-ddd-core v0.12.0` — do NOT bump the pin in any task.
- `stdhttp` defaults MUST equal `net/http.Client` zero-value behavior: timeout 0 (none), `http.DefaultTransport`, no tracing. No safe-default overrides.
- Error style: constructor sentinels are errorsx-coded `CodeInvalidArgument` with `"stdhttp: "` / `"rediscache: "` message prefix (mirrors `rediscache.ErrNilClient`).
- `stdhttp.Do` errors are stdlib passthrough — never errorsx-coded (port contract is net/http-shaped).
- No new CI integration leg for Part 3 (all tests are `httptest`-based unit tests). Part 1's integration test rides the EXISTING cache/redis integration step.
- Local lint binary: `/usr/local/bin/golangci-lint` (the `~/go/bin` one is a stale v1.64.8 that chokes on the v2 config). Import grouping: `go-ddd-adapters/...` imports get their own group (core is third-party).
- Branches: Part 1 on `feat/cache-redis-healthcheck` (cut from `origin/main`), own PR. Part 2 in the go-ddd-core repo on `docs/httpclient-godoc`. Part 3 on the existing `feat/httpclient-std` (already carries spec + this plan).
- Commit trailer: `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>` on every commit.

---

## Part 1 — cache/redis HealthCheck export (branch `feat/cache-redis-healthcheck`)

### Task 1: `HealthCheck` method — unit tests + implementation

**Files:**
- Create: `cache/redis/health.go`
- Test: `cache/redis/health_test.go` (unit, NO build tag)

**Interfaces:**
- Consumes: existing `rediscache.New(client redis.Cmdable, opts ...Option) (*Cache, error)`; core `health.NewCheck(name string, fn func(ctx context.Context) error) health.Check`; `redis.Cmdable.Ping(ctx) *redis.StatusCmd`.
- Produces: `func (c *Cache) HealthCheck(name string) health.Check` — Task 2's integration test and any registry wiring rely on this exact signature.

- [ ] **Step 1: Cut the branch**

```bash
cd /Users/eason_tseng/playground/project/go-ddd-adapters
git fetch origin
git switch -c feat/cache-redis-healthcheck origin/main
```

- [ ] **Step 2: Write the failing unit tests**

Create `cache/redis/health_test.go`:

```go
package rediscache_test

import (
	"testing"

	"github.com/redis/go-redis/v9"

	rediscache "github.com/slam0504/go-ddd-adapters/cache/redis"
)

// newUnitCache builds a Cache whose client is never dialed — Name() must not
// touch the network.
func newUnitCache(t *testing.T) *rediscache.Cache {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	c, err := rediscache.New(client)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// An empty name must fall back to the documented default so the probe never
// reaches a registry (which rejects empty names) unnamed.
func TestHealthCheck_EmptyNameDefaultsToCacheRedis(t *testing.T) {
	if got := newUnitCache(t).HealthCheck("").Name(); got != "cache-redis" {
		t.Fatalf("Name() = %q, want %q", got, "cache-redis")
	}
}

// A custom name must pass through verbatim — multiple Cache instances in one
// process disambiguate their probes by name.
func TestHealthCheck_CustomNamePassesThrough(t *testing.T) {
	if got := newUnitCache(t).HealthCheck("redis-session").Name(); got != "redis-session" {
		t.Fatalf("Name() = %q, want %q", got, "redis-session")
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./cache/redis/... -run TestHealthCheck -v`
Expected: FAIL — `c.HealthCheck undefined (type *rediscache.Cache has no field or method HealthCheck)`

- [ ] **Step 4: Write the implementation**

Create `cache/redis/health.go`:

```go
package rediscache

import (
	"context"

	"github.com/slam0504/go-ddd-core/ports/health"
)

// defaultHealthCheckName names the probe when the caller passes "".
const defaultHealthCheckName = "cache-redis"

// HealthCheck returns a core ports/health.Check that probes the underlying
// Redis client with PING. An empty name defaults to "cache-redis"; processes
// wiring several Caches into one registry should pass distinct names
// (registries reject duplicates). Ping errors pass through as-is — the health
// contract only distinguishes nil from non-nil, so no errorsx coding is
// applied. The client was already nil-guarded by New; HealthCheck adds no
// further validation.
func (c *Cache) HealthCheck(name string) health.Check {
	if name == "" {
		name = defaultHealthCheckName
	}
	return health.NewCheck(name, func(ctx context.Context) error {
		return c.client.Ping(ctx).Err()
	})
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./cache/redis/... -run TestHealthCheck -v`
Expected: PASS (2 tests)

- [ ] **Step 6: Full unit sweep + vet**

Run: `go build ./... && go vet ./... && go test ./cache/redis/...`
Expected: all PASS, no output from build/vet

- [ ] **Step 7: Commit**

```bash
git add cache/redis/health.go cache/redis/health_test.go
git commit -m "feat(cache/redis): export HealthCheck as core health.Check

PING-based probe reusing the client already validated by New. Empty
name defaults to \"cache-redis\" (registries reject empty names);
custom names pass through verbatim. Ping errors pass through uncoded —
the health contract only distinguishes nil from non-nil.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

### Task 2: Integration test + bookkeeping + PR

**Files:**
- Modify: `cache/redis/integration_test.go` (append test)
- Modify: `CHANGELOG.md` (`[Unreleased]` section)
- Modify: `README.md` (cache/redis adapter-table row)

**Interfaces:**
- Consumes: `(*Cache).HealthCheck(name string) health.Check` from Task 1; existing integration harness package vars `testClient`, helper `nextPrefix(tag string) string`.
- Produces: merged PR on `main` whose `[Unreleased]` CHANGELOG entry Task 9 folds into `[v0.12.0]`.

- [ ] **Step 1: Write the integration test**

Append to `cache/redis/integration_test.go` (file already has `//go:build integration` and imports `context`, `rediscache`; add nothing to imports):

```go
// TestHealthCheck_Ping verifies the exported probe against a live container:
// nil on healthy PING, non-nil under a cancelled ctx (deterministic error
// path — no container stop needed).
func TestHealthCheck_Ping(t *testing.T) {
	c, err := rediscache.New(testClient, rediscache.WithKeyPrefix(nextPrefix("health")))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.HealthCheck("").Check(context.Background()); err != nil {
		t.Fatalf("Check on live container: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.HealthCheck("").Check(ctx); err == nil {
		t.Fatal("Check with cancelled ctx: want non-nil error, got nil")
	}
}
```

- [ ] **Step 2: Run the integration suite (Docker required; else defer to CI)**

Run: `go test -tags=integration -race ./cache/redis/... -run TestHealthCheck_Ping -v`
Expected: PASS. If Docker is unavailable locally, note it and rely on the CI `integration (testcontainers)` leg — do NOT claim local verification.

- [ ] **Step 3: CHANGELOG entry**

In `CHANGELOG.md`, under `## [Unreleased]`, add:

```markdown
### Added
- `cache/redis`: `(*Cache).HealthCheck(name)` exports a PING-based core
  `ports/health.Check` (empty name defaults to `"cache-redis"`). Closes one
  deferred item from the cache/redis spec §10.
```

- [ ] **Step 4: README row**

In `README.md`, find the adapter-table row for `cache/redis` and extend its Notes cell with `; \`HealthCheck\` exports a PING-based core \`health.Check\``. Keep the rest of the row byte-identical.

- [ ] **Step 5: Lint + commit**

Run: `/usr/local/bin/golangci-lint run ./... && /usr/local/bin/golangci-lint run --build-tags=integration ./...`
Expected: 0 issues.

```bash
git add cache/redis/integration_test.go CHANGELOG.md README.md
git commit -m "test(cache/redis): HealthCheck integration probe + bookkeeping

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

- [ ] **Step 6: Push + PR + merge**

```bash
git push -u origin feat/cache-redis-healthcheck
gh pr create --title "feat(cache/redis): export HealthCheck as core health.Check" \
  --body "Additive helper closing one deferred item recorded in docs/superpowers/specs/2026-07-01-cache-redis-adapter-design.md §10 (already on main). Full design (spec lives on feat/httpclient-std): https://github.com/slam0504/go-ddd-adapters/blob/feat/httpclient-std/docs/superpowers/specs/2026-07-02-httpclient-std-adapter-design.md (§3). No tag — rides into v0.12.0.

🤖 Generated with [Claude Code](https://claude.com/claude-code)"
```

Wait for CI 5/5 green, then merge (squash/merge per repo habit — merge commit), delete branch. Record the merge SHA for `.agent/state.md` (Task 9).

---

## Part 2 — core `ports/httpclient` godoc PR (go-ddd-core repo, branch `docs/httpclient-godoc`)

### Task 3: Godoc rewrite + core CHANGELOG + PR (merge untagged)

**Files:**
- Modify: `/Users/eason_tseng/playground/project/go-ddd-core/ports/httpclient/httpclient.go` (comments ONLY)
- Modify: `/Users/eason_tseng/playground/project/go-ddd-core/CHANGELOG.md` (`[Unreleased]`)

**Interfaces:**
- Consumes: nothing.
- Produces: corrected contract prose that Part 3's package doc references instead of restating. ZERO API change — `gofmt` diff must show only comment lines.

- [ ] **Step 1: Cut the branch**

```bash
cd /Users/eason_tseng/playground/project/go-ddd-core
git fetch origin && git switch -c docs/httpclient-godoc origin/main
```

- [ ] **Step 2: Rewrite the godoc**

Replace the entire contents of `ports/httpclient/httpclient.go` with (only comments differ from the current file):

```go
// Package httpclient defines the outbound HTTP client contract. The Client
// interface matches net/http.Client.Do so adapters can wrap the stdlib
// client, resty, retryablehttp, or a traced variant.
//
// Responsibility boundary: implementations MUST honour the request context
// (req.Context()) so callers can cancel in-flight requests. Closing a
// non-nil resp.Body after a nil error is the CALLER's responsibility,
// exactly as with net/http.Client.Do. Redirect policy, retries, timeouts,
// and instrumentation are adapter policy — this contract does not
// prescribe them.
package httpclient

import (
	"context"
	"net/http"
)

// Client sends HTTP requests and returns responses. The signature and
// semantics are those of net/http.Client.Do: implementations must honour
// req.Context() cancellation; when the returned error is nil the caller
// owns resp.Body and must close it; when the error is non-nil the
// response may be ignored, per stdlib Do semantics.
type Client interface {
	Do(req *http.Request) (*http.Response, error)
}

// ContextualClient is an optional context-first variant of Client for
// adapters that prefer an explicit ctx parameter over
// req.WithContext(ctx). Do(ctx, req) must behave exactly like
// Client.Do(req.WithContext(ctx)) — the same cancellation and body-
// ownership rules apply. Note that a single concrete type cannot
// implement both Client and ContextualClient (same method name,
// different signatures); adapters typically expose a small wrapper type
// for this interface.
type ContextualClient interface {
	Do(ctx context.Context, req *http.Request) (*http.Response, error)
}
```

- [ ] **Step 3: Verify comment-only diff + build**

Run: `git diff --stat && go build ./... && go vet ./ports/httpclient/...`
Expected: 1 file changed; `git diff` shows ONLY comment lines changed; build/vet clean.

- [ ] **Step 4: core CHANGELOG entry**

In core `CHANGELOG.md` under `## [Unreleased]`, add:

```markdown
### Changed
- `ports/httpclient`: godoc rewritten to state the responsibility boundary
  (ctx cancellation is the implementation's duty; closing `resp.Body` is the
  caller's; redirects/retries/timeouts are adapter policy) and to note that
  `Client` and `ContextualClient` cannot be implemented by one concrete type.
  No API change.
```

- [ ] **Step 5: Commit + PR + merge (NO tag)**

```bash
git add ports/httpclient/httpclient.go CHANGELOG.md
git commit -m "docs(ports/httpclient): state the responsibility boundary in godoc

Fixes the broken sentence in the Client godoc and spells out: ctx
cancellation is the implementation's duty, closing resp.Body is the
caller's, redirect/retry/timeout/instrumentation are adapter policy.
Notes the Client/ContextualClient method-name collision. No API change;
merge untagged — rides the next core release.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
git push -u origin docs/httpclient-godoc
gh pr create --title "docs(ports/httpclient): state the responsibility boundary in godoc" \
  --body "Comment-only; no API change; merge untagged. Design (adapters repo): https://github.com/slam0504/go-ddd-adapters/blob/feat/httpclient-std/docs/superpowers/specs/2026-07-02-httpclient-std-adapter-design.md (§4).

🤖 Generated with [Claude Code](https://claude.com/claude-code)"
```

Wait for core CI green, merge, delete branch. Do NOT tag.

---

## Part 3 — `httpclient/std` (`stdhttp`) adapter (branch `feat/httpclient-std`)

All Part 3 work happens on the existing `feat/httpclient-std` branch:

```bash
cd /Users/eason_tseng/playground/project/go-ddd-adapters
git switch feat/httpclient-std
```

### Task 4: Options + `New` validation

**Files:**
- Create: `httpclient/std/options.go`
- Create: `httpclient/std/client.go`
- Test: `httpclient/std/options_test.go`

**Interfaces:**
- Consumes: core `pkg/errorsx` (`errorsx.New`, `errorsx.CodeInvalidArgument`); core `ports/httpclient.Client`; `go.opentelemetry.io/otel/trace.TracerProvider`.
- Produces (later tasks depend on these exact names):
  - `type Client struct` with unexported field `hc *http.Client`
  - `func New(opts ...Option) (*Client, error)`
  - `func (c *Client) Do(req *http.Request) (*http.Response, error)`
  - `type Option func(*config)`
  - `func WithTimeout(d time.Duration) Option`
  - `func WithTransport(rt http.RoundTripper) Option`
  - `func WithTracing(tp trace.TracerProvider) Option`
  - Sentinels: `ErrNegativeTimeout`, `ErrNilTransport`, `ErrNilTracerProvider`
  - config fields: `timeout time.Duration`, `transport http.RoundTripper`, `transportSet bool`, `tracerProvider trace.TracerProvider`, `tracingSet bool`

- [ ] **Step 1: Write the failing constructor tests**

Create `httpclient/std/options_test.go`:

```go
package stdhttp_test

import (
	"errors"
	"testing"
	"time"

	"github.com/slam0504/go-ddd-core/pkg/errorsx"
	"go.opentelemetry.io/otel/trace/noop"

	stdhttp "github.com/slam0504/go-ddd-adapters/httpclient/std"
)

// requireInvalidArgument pins the coded invariant from the plan's Global
// Constraints: constructor sentinels are errorsx-coded CodeInvalidArgument,
// so errorsx.CodeOf never reports CodeUnknown for them.
func requireInvalidArgument(t *testing.T, err error) {
	t.Helper()
	if got := errorsx.CodeOf(err); got != errorsx.CodeInvalidArgument {
		t.Fatalf("errorsx.CodeOf(%v) = %v, want CodeInvalidArgument", err, got)
	}
}

// The zero-option client must construct — its behavior (stdlib parity) is
// asserted behaviorally in client_test.go.
func TestNew_Defaults(t *testing.T) {
	c, err := stdhttp.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c == nil {
		t.Fatal("New returned nil client")
	}
}

// d < 0 is a caller bug and must fail loud at construction, not surface as
// weird stdlib behavior later.
func TestNew_NegativeTimeoutRejected(t *testing.T) {
	_, err := stdhttp.New(stdhttp.WithTimeout(-time.Second))
	if !errors.Is(err, stdhttp.ErrNegativeTimeout) {
		t.Fatalf("err = %v, want ErrNegativeTimeout", err)
	}
	requireInvalidArgument(t, err)
}

// d == 0 explicitly means "no client-level timeout" (stdlib parity) and must
// be accepted.
func TestNew_ZeroTimeoutAccepted(t *testing.T) {
	if _, err := stdhttp.New(stdhttp.WithTimeout(0)); err != nil {
		t.Fatalf("New(WithTimeout(0)): %v", err)
	}
}

// A nil transport passed explicitly is a caller bug — fail loud instead of
// silently falling back to http.DefaultTransport.
func TestNew_NilTransportRejected(t *testing.T) {
	_, err := stdhttp.New(stdhttp.WithTransport(nil))
	if !errors.Is(err, stdhttp.ErrNilTransport) {
		t.Fatalf("err = %v, want ErrNilTransport", err)
	}
	requireInvalidArgument(t, err)
}

// Tracing is explicit-injection only; a nil provider would silently no-op via
// the otel global, which this repo's wiring convention forbids.
func TestNew_NilTracerProviderRejected(t *testing.T) {
	_, err := stdhttp.New(stdhttp.WithTracing(nil))
	if !errors.Is(err, stdhttp.ErrNilTracerProvider) {
		t.Fatalf("err = %v, want ErrNilTracerProvider", err)
	}
	requireInvalidArgument(t, err)
}

// A non-nil provider must be accepted (span emission is asserted in
// tracing_test.go).
func TestNew_TracingAccepted(t *testing.T) {
	if _, err := stdhttp.New(stdhttp.WithTracing(noop.NewTracerProvider())); err != nil {
		t.Fatalf("New(WithTracing): %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./httpclient/std/... -v`
Expected: FAIL to build — package does not exist yet.

- [ ] **Step 3: Write the implementation**

Create `httpclient/std/options.go`:

```go
package stdhttp

import (
	"net/http"
	"time"

	"github.com/slam0504/go-ddd-core/pkg/errorsx"
	"go.opentelemetry.io/otel/trace"
)

// Constructor sentinels. errorsx-coded CodeInvalidArgument (mirrors
// rediscache.ErrNilClient) so errorsx.CodeOf never reports CodeUnknown.
var (
	// ErrNegativeTimeout is returned by New when WithTimeout got d < 0.
	ErrNegativeTimeout = errorsx.New(errorsx.CodeInvalidArgument, "stdhttp: negative timeout")
	// ErrNilTransport is returned by New when WithTransport got nil.
	ErrNilTransport = errorsx.New(errorsx.CodeInvalidArgument, "stdhttp: nil transport")
	// ErrNilTracerProvider is returned by New when WithTracing got nil.
	ErrNilTracerProvider = errorsx.New(errorsx.CodeInvalidArgument, "stdhttp: nil tracer provider")
)

type config struct {
	timeout        time.Duration
	transport      http.RoundTripper
	transportSet   bool
	tracerProvider trace.TracerProvider
	tracingSet     bool
}

// Option configures a Client at construction time.
type Option func(*config)

// WithTimeout sets the client-level timeout. d == 0 (the default) means no
// client-level timeout, identical to net/http.Client; d < 0 is rejected by
// New. Production callers are strongly encouraged to set a timeout — the
// adapter deliberately does not choose one for them.
func WithTimeout(d time.Duration) Option {
	return func(c *config) { c.timeout = d }
}

// WithTransport replaces the base http.RoundTripper (default
// http.DefaultTransport). nil is rejected by New.
func WithTransport(rt http.RoundTripper) Option {
	return func(c *config) { c.transport = rt; c.transportSet = true }
}

// WithTracing wraps the base transport with otelhttp using the EXPLICITLY
// injected provider. There is deliberately no fallback to the otel global
// provider — this repo wires observability explicitly (observability/otel
// registers no globals). nil is rejected by New.
func WithTracing(tp trace.TracerProvider) Option {
	return func(c *config) { c.tracerProvider = tp; c.tracingSet = true }
}
```

Create `httpclient/std/client.go`:

```go
package stdhttp

import (
	"net/http"

	"github.com/slam0504/go-ddd-core/ports/httpclient"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Client is a stdlib-backed implementation of core's ports/httpclient.Client.
// Defaults match net/http.Client exactly: no client-level timeout,
// http.DefaultTransport, no tracing.
type Client struct {
	hc *http.Client
}

// New builds a Client. With no options the client behaves exactly like a
// zero-value net/http.Client.
func New(opts ...Option) (*Client, error) {
	var cfg config
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.timeout < 0 {
		return nil, ErrNegativeTimeout
	}
	if cfg.transportSet && cfg.transport == nil {
		return nil, ErrNilTransport
	}
	if cfg.tracingSet && cfg.tracerProvider == nil {
		return nil, ErrNilTracerProvider
	}
	transport := cfg.transport
	if cfg.tracingSet {
		base := transport
		if base == nil {
			base = http.DefaultTransport
		}
		transport = otelhttp.NewTransport(base, otelhttp.WithTracerProvider(cfg.tracerProvider))
	}
	return &Client{hc: &http.Client{Timeout: cfg.timeout, Transport: transport}}, nil
}

// Do sends req with net/http.Client.Do semantics: req.Context() cancellation
// is honoured; a nil error means the caller owns closing resp.Body; errors
// are stdlib passthrough (*url.Error etc.), never re-coded — the port
// contract is explicitly net/http-shaped.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	return c.hc.Do(req)
}

// Compile-time assertion that *Client implements core's httpclient.Client.
var _ httpclient.Client = (*Client)(nil)
```

- [ ] **Step 4: go mod tidy (otelhttp promotes indirect→direct) + run tests**

Run: `go mod tidy && go test ./httpclient/std/... -v`
Expected: PASS (6 tests). `git diff go.mod` shows `otelhttp v0.61.0` moved out of the `// indirect` block; no version changes.

- [ ] **Step 5: Commit**

```bash
git add httpclient/std/options.go httpclient/std/client.go httpclient/std/options_test.go go.mod go.sum
git commit -m "feat(httpclient/std): stdhttp Client with stdlib-parity defaults

New(opts...) over *http.Client: default timeout 0 (= stdlib, no
client-level timeout), WithTimeout (d<0 rejected), WithTransport (nil
rejected), WithTracing with explicit TracerProvider injection (nil
rejected; no otel-global fallback per repo wiring convention).

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

### Task 5: `Do` behavior — timeout + ctx cancellation (httptest)

**Files:**
- Test: `httpclient/std/client_test.go`

**Interfaces:**
- Consumes: `stdhttp.New`, `WithTimeout`, `(*Client).Do` from Task 4.
- Produces: nothing new — this task pins behavior with tests only. If a test exposes a bug, the fix belongs in `client.go` within this task.

- [ ] **Step 1: Write the behavior tests**

Create `httpclient/std/client_test.go`:

```go
package stdhttp_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	stdhttp "github.com/slam0504/go-ddd-adapters/httpclient/std"
)

func TestDo_RoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c, err := stdhttp.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("got %d %q, want 200 \"ok\"", resp.StatusCode, body)
	}
}

// WithTimeout(d>0) must abort a slow request with a stdlib timeout error —
// passthrough, not an errorsx-coded error.
func TestDo_TimeoutAbortsSlowRequest(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	c, err := stdhttp.New(stdhttp.WithTimeout(50 * time.Millisecond))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	_, err = c.Do(req) //nolint:bodyclose // error path: no body on non-nil err
	var uerr *url.Error
	if !errors.As(err, &uerr) || !uerr.Timeout() {
		t.Fatalf("err = %v, want *url.Error with Timeout()==true", err)
	}
}

// The default client has NO client-level timeout (stdlib parity): a request
// slower than any would-be "safe default" must still succeed.
func TestDo_DefaultHasNoClientTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c, err := stdhttp.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
}

// Cancelling req.Context() mid-flight must abort the request with
// context.Canceled surfaced through the stdlib error chain.
func TestDo_HonoursRequestContextCancellation(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()

	c, err := stdhttp.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	go func() {
		<-started
		cancel()
	}()
	_, err = c.Do(req) //nolint:bodyclose // error path: no body on non-nil err
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled in chain", err)
	}
}
```

- [ ] **Step 2: Run tests**

Run: `go test ./httpclient/std/... -race -v`
Expected: PASS (all). These pin existing Task 4 behavior; a failure means a `client.go` bug — fix it here.

- [ ] **Step 3: Commit**

```bash
git add httpclient/std/client_test.go
git commit -m "test(httpclient/std): Do behavior — roundtrip, timeout, stdlib-parity default, ctx cancellation

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

### Task 6: `Contextual()` wrapper

**Files:**
- Create: `httpclient/std/contextual.go`
- Test: `httpclient/std/contextual_test.go`

**Interfaces:**
- Consumes: `(*Client).Do` from Task 4; core `ports/httpclient.ContextualClient`.
- Produces: `func (c *Client) Contextual() httpclient.ContextualClient`.

- [ ] **Step 1: Write the failing test**

Create `httpclient/std/contextual_test.go`:

```go
package stdhttp_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	stdhttp "github.com/slam0504/go-ddd-adapters/httpclient/std"
)

// Contextual().Do(ctx, req) must attach ctx to the request — cancelling ctx
// aborts a request that was built WITHOUT a context.
func TestContextual_AttachesContext(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()

	c, err := stdhttp.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequest(http.MethodGet, srv.URL, nil) // deliberately no ctx
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	go func() {
		<-started
		cancel()
	}()
	_, err = c.Contextual().Do(ctx, req) //nolint:bodyclose // error path
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled in chain", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./httpclient/std/... -run TestContextual -v`
Expected: FAIL to build — `c.Contextual undefined`.

- [ ] **Step 3: Write the implementation**

Create `httpclient/std/contextual.go`:

```go
package stdhttp

import (
	"context"
	"net/http"

	"github.com/slam0504/go-ddd-core/ports/httpclient"
)

// Contextual returns a context-first view of c implementing core's
// httpclient.ContextualClient. Do(ctx, req) is exactly
// c.Do(req.WithContext(ctx)) — no added semantics. A separate wrapper type
// is required because Client.Do(req) and ContextualClient.Do(ctx, req)
// share a method name with different signatures, so one concrete type
// cannot implement both interfaces.
func (c *Client) Contextual() httpclient.ContextualClient {
	return contextualClient{c: c}
}

type contextualClient struct{ c *Client }

func (w contextualClient) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	return w.c.Do(req.WithContext(ctx))
}

// Compile-time assertion that the wrapper implements ContextualClient.
var _ httpclient.ContextualClient = contextualClient{}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./httpclient/std/... -race -v`
Expected: PASS (all, including TestContextual_AttachesContext).

- [ ] **Step 5: Commit**

```bash
git add httpclient/std/contextual.go httpclient/std/contextual_test.go
git commit -m "feat(httpclient/std): Contextual() wrapper for httpclient.ContextualClient

Client.Do(req) and ContextualClient.Do(ctx, req) share a method name
with different signatures — one concrete type cannot implement both, so
the context-first interface is served by a thin wrapper doing
req.WithContext(ctx) with no added semantics.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

### Task 7: Tracing span emission

**Files:**
- Test: `httpclient/std/tracing_test.go`

**Interfaces:**
- Consumes: `stdhttp.New`, `WithTracing` from Task 4; `go.opentelemetry.io/otel/sdk/trace` (`sdktrace.NewTracerProvider`, `sdktrace.WithSpanProcessor`) + `sdk/trace/tracetest.NewSpanRecorder` (SDK already a direct dep).
- Produces: nothing new — asserts the otelhttp wiring done in Task 4 actually emits spans, and that the default path emits none.

- [ ] **Step 1: Write the span tests**

Create `httpclient/std/tracing_test.go`:

```go
package stdhttp_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	stdhttp "github.com/slam0504/go-ddd-adapters/httpclient/std"
)

func doOK(t *testing.T, c *stdhttp.Client, url string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	// otelhttp ends the client span when the body is fully consumed + closed.
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

// WithTracing(tp) must emit a client span per request through the injected
// provider — proving otelhttp is wired to the EXPLICIT provider, not the
// otel global.
func TestTracing_EmitsSpanThroughInjectedProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	c, err := stdhttp.New(stdhttp.WithTracing(tp))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	doOK(t, c, srv.URL)
	if got := len(rec.Ended()); got != 1 {
		t.Fatalf("ended spans = %d, want 1", got)
	}
}

// Without WithTracing the request path is pure stdlib — zero spans anywhere.
func TestTracing_OffByDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	rec := tracetest.NewSpanRecorder()
	_ = sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)) // provider exists but is NOT injected
	c, err := stdhttp.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	doOK(t, c, srv.URL)
	if got := len(rec.Ended()); got != 0 {
		t.Fatalf("ended spans = %d, want 0", got)
	}
}
```

- [ ] **Step 2: Run tests**

Run: `go test ./httpclient/std/... -race -run TestTracing -v`
Expected: PASS (2 tests). If `EmitsSpan` fails with 0 spans, the otelhttp wrap in `New` (Task 4) is broken — fix `client.go` here.

- [ ] **Step 3: Full package sweep + commit**

Run: `go test ./httpclient/std/... -race && go vet ./...`
Expected: PASS / clean.

```bash
git add httpclient/std/tracing_test.go
git commit -m "test(httpclient/std): span emitted via injected provider; zero spans by default

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

### Task 8: Package doc + CHANGELOG + README + lint

**Files:**
- Create: `httpclient/std/doc.go`
- Modify: `CHANGELOG.md` (`[Unreleased]`)
- Modify: `README.md` (adapter table row + Status paragraph)

**Interfaces:**
- Consumes: everything shipped in Tasks 4–7.
- Produces: the reviewable PR surface; Task 9 flips `[Unreleased]` to `[v0.12.0]`.

- [ ] **Step 1: Write doc.go**

Create `httpclient/std/doc.go`:

```go
// Package stdhttp is the stdlib-backed adapter for core's
// ports/httpclient contract. It wraps *net/http.Client behind
// httpclient.Client, with a Contextual() view for
// httpclient.ContextualClient.
//
// Defaults deliberately match a zero-value net/http.Client: NO
// client-level timeout, http.DefaultTransport, no tracing. This adapter
// is named "std" — its defaults must not silently diverge from the
// stdlib. Production callers are strongly encouraged to set
// WithTimeout; the adapter does not choose one for them.
//
// Tracing is opt-in via WithTracing(tp) with an EXPLICITLY injected
// trace.TracerProvider (no otel-global fallback — this repo wires
// observability explicitly). Errors from Do are stdlib passthrough
// (*url.Error etc.), never errorsx-coded: the port contract is
// net/http-shaped. Retries and circuit breaking are deliberately out of
// scope (see the 2026-07-02 design spec §8).
package stdhttp
```

- [ ] **Step 2: CHANGELOG entry**

In `CHANGELOG.md` under `## [Unreleased]` (below the Part 1 entry that merged via `main`; if this branch doesn't have it yet, just add this entry — the merge in Task 9 Step 1 reconciles):

```markdown
### Added
- `httpclient/std` (`stdhttp`): first adapter for core `ports/httpclient` —
  wraps `*net/http.Client` with stdlib-parity defaults (timeout 0, no
  tracing); `WithTimeout` / `WithTransport` / `WithTracing(tp)` (explicit
  provider injection, no otel-global fallback); `Contextual()` wrapper for
  `ContextualClient`; stdlib error passthrough. retry / breaker deferred.
```

- [ ] **Step 3: README updates**

In `README.md`:
1. Adapter table — add row (keep column alignment with neighbors):

```markdown
| `httpclient/std` | `httpclient.Client` / `ContextualClient` | stdlib `net/http.Client` wrapper; stdlib-parity defaults (no client timeout); `WithTimeout` / `WithTransport` / opt-in `WithTracing(tp)` explicit OTel provider; `Contextual()` context-first view |
```

2. Status section — add a short v0.12.0 paragraph mirroring the v0.11.0 one: `httpclient/std` is the first `ports/httpclient` consumer; simplified gate (port published since core v0.1.0, no core tag / no dep-bump).

- [ ] **Step 4: Lint + full-repo verification**

Run:
```bash
go build ./... && go vet ./... && go test ./... && \
/usr/local/bin/golangci-lint run ./... && \
/usr/local/bin/golangci-lint run --build-tags=integration ./... && \
cd examples/orders && go build ./... && go vet ./... && go test ./... && cd ../..
```
Expected: all PASS / 0 issues. Import grouping: `go-ddd-adapters/...` in its own group.

- [ ] **Step 5: Commit**

```bash
git add httpclient/std/doc.go CHANGELOG.md README.md
git commit -m "docs(httpclient/std): package doc + CHANGELOG + README rows

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

### Task 9: Release — PR, merge, tag v0.12.0

**Files:**
- Modify: `CHANGELOG.md` (flip `[Unreleased]` → `[v0.12.0] - <date>` + compare links)
- Modify: `README.md` (compat matrix `v0.12.0` row)
- Modify (post-merge, direct on `main`): `.agent/state.md`, `.agent/decisions.md`, `<workspace-root>/.agent-memory/go-ddd.md`

**Interfaces:**
- Consumes: merged Part 1 PR (its `[Unreleased]` entry must be present before the flip); Tasks 4–8 on this branch.
- Produces: adapters `v0.12.0` annotated tag + GitHub Release Latest.

- [ ] **Step 1: Sync main into the branch (pulls in Part 1's CHANGELOG entry)**

```bash
git fetch origin && git merge origin/main
```
Resolve any `CHANGELOG.md` conflict by keeping BOTH `[Unreleased]` entries (health export + stdhttp).

- [ ] **Step 2: Flip CHANGELOG + compat matrix**

- `CHANGELOG.md`: rename `## [Unreleased]` → `## [v0.12.0] - <today>` (fresh empty `## [Unreleased]` above it); update compare links: add `[v0.12.0]: .../compare/v0.11.0...v0.12.0`, reset `[Unreleased]` to `v0.12.0...HEAD`.
- `README.md`: compat matrix row — adapters `v0.12.0` ↔ core `v0.12.0` (pin unchanged).

```bash
git add CHANGELOG.md README.md
git commit -m "chore(release): prepare v0.12.0 (httpclient/std + cache/redis HealthCheck)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

- [ ] **Step 3: Push + PR**

```bash
git push
gh pr create --title "feat(httpclient/std): stdhttp adapter — first ports/httpclient consumer (v0.12.0)" \
  --body "Implements docs/superpowers/specs/2026-07-02-httpclient-std-adapter-design.md Part 3. Simplified gate: port published since core v0.1.0 → no core tag, no dep-bump PR; tag v0.12.0 lands at this merge. Spec + plan ride this branch.

🤖 Generated with [Claude Code](https://claude.com/claude-code)"
```

Wait for CI 5/5 green. Merge; delete branch.

- [ ] **Step 4: Tag + GitHub Release (only after merge, CI green)**

```bash
git switch main && git pull
git tag -a v0.12.0 -m "v0.12.0 — httpclient/std adapter + cache/redis HealthCheck"
git push origin v0.12.0
gh release create v0.12.0 --title "v0.12.0 — httpclient/std adapter" \
  --notes-from-tag --latest
```

Then fill release notes from the CHANGELOG `[v0.12.0]` section (edit via `gh release edit v0.12.0 --notes-file -`). Verify: `gh api repos/slam0504/go-ddd-adapters/releases/latest --jq .tag_name` → `v0.12.0`; `go list -m github.com/slam0504/go-ddd-adapters@v0.12.0` resolves via proxy.

- [ ] **Step 5: Bookkeeping (direct on main, two separate Bash calls for .agent add/commit)**

Update `.agent/state.md` (new v0.12.0 cycle section: PR numbers/SHAs, simplified-gate rationale), `.agent/decisions.md` (stdlib-parity default timeout decision; explicit-provider tracing; Contextual wrapper constraint), cross-repo `.agent-memory/go-ddd.md` (mark the v0.5.x roadmap slot fully closed). Note: `.agent/` is globally gitignored but the .md files are tracked — run `git add .agent/*.md` and `git commit` as SEPARATE Bash calls (the add exits 1 on the advisory).

---

## Verification strategy (spec §7 success criteria)

- Part 1: `go test ./cache/redis/... -run TestHealthCheck -v` (unit) + CI integration leg green on the PR.
- Part 3: `go build/vet/test ./...` + both lint invocations green locally before every push; CI 5/5 green on the PR; behavior matrix covered = roundtrip / timeout>0 / default-no-timeout / ctx-cancel / Contextual-attach / span-on / span-off / 4 constructor guards.
- Release: `releases/latest` API returns `v0.12.0`; module proxy resolves `@v0.12.0`.
- Not locally verifiable: the CI testcontainers leg (needs Docker); rely on PR CI and say so in any status report.
