# httpclient/std adapter (+ cache/redis health export) — design

Date: 2026-07-02. Status: accepted (user-reviewed in-session).
Supersedes nothing; picks up the remaining half of the roadmap slot
"v0.5.x — Cache + Outbound HTTP client" (`cache/redis` half shipped as
adapter v0.11.0).

## 1. Goal & context

Three sequenced work items, smallest first:

1. **Small PR (adapters): `cache/redis` health.Check export** — additive
   helper closing one deferred item from the cache/redis spec §10. No own
   tag; rides into the v0.12.0 release.
2. **core PR: `ports/httpclient` godoc-only fix** — merge WITHOUT tag;
   rides the next core release. Zero API change.
3. **Main cycle (adapters): `httpclient/std`** — first consumer of core's
   `ports/httpclient` contract → adapter **v0.12.0** tag + GitHub Release.

Candidate evaluation (recorded so future sessions don't re-litigate):
`httpclient/std` chosen over `cache/ristretto` (optional L1; wait until
byte-ownership/cachetest/Redis adapter field experience accumulates; also
ristretto's async Set visibility vs cachetest determinism is UNVERIFIED)
and over health-export-only (too small to be a cycle).

## 2. Tag plan — simplified gate

`ports/httpclient` has been published since core **v0.1.0** (commit
`0b5b3fd`, zero changes since) and the adapter already pins core v0.12.0.
This cycle changes no core API, so the four-step cross-repo tag-gate does
NOT apply:

- No new core tag required (Part 2 merges untagged).
- No dep-bump PR.
- Gate: impl PR merged with CI green → adapter `v0.12.0` annotated tag +
  GitHub Release Latest. Part 1's health export ships in the same v0.12.0.

## 3. Part 1 — cache/redis health.Check export

New file `cache/redis/health.go`:

```go
func (c *Cache) HealthCheck(name string) health.Check
```

- Method form (not package func): reuses the client already validated by
  `New` (nil + typed-nil guard happen there; `HealthCheck` adds none).
- `name == ""` → default `"cache-redis"` (core's Registry rejects empty
  names; never let an empty name reach it). Custom name passes through
  verbatim — multiple Cache instances in one process disambiguate by name.
- Implementation: `health.NewCheck(name, func(ctx) error { return
  c.client.Ping(ctx).Err() })`. `redis.Cmdable` includes
  `Ping(ctx) *StatusCmd` (verified go-redis v9.20.0 `commands.go:186`).
- Ping errors pass through **as-is** — no errorsx wrapping. The health
  contract only distinguishes nil / non-nil.

Tests:
- Unit: name semantics — empty name yields `Name() == "cache-redis"`;
  custom name returned verbatim.
- Integration (existing testcontainers harness): Ping success → nil.
- Error path: cancelled ctx → non-nil error (deterministic; no container
  stop needed).

## 4. Part 2 — core `ports/httpclient` godoc PR

Comment-only changes to `ports/httpclient/httpclient.go`:

- Fix the broken sentence ("close the response body is the caller's
  responsibility") and spell out the responsibility boundary:
  - Implementations MUST honour `req.Context()` cancellation.
  - Closing `resp.Body` is the **caller's** responsibility.
  - When `err != nil`, response handling follows stdlib
    `net/http.Client.Do` semantics.
  - Redirects, retries, timeouts are adapter policy — the contract does
    not prescribe them.
- `ContextualClient` godoc: clarify it is the context-first convenience
  equivalent of `req.WithContext(ctx)`.
- core CHANGELOG `[Unreleased]` gets a docs entry. **No tag** — this is
  unlike the cache cycle's TypedCache deletion: no published content /
  CHANGELOG contradiction exists, so no patch tag is warranted.

## 5. Part 3 — httpclient/std adapter

Path `httpclient/std`, **package `stdhttp`** (dir already lives in the
`httpclient` namespace; `stdhttp.New(...)` reads clean where
`stdhttpclient.New(...)` would repeat itself).

### 5.1 Surface

- `type Client struct` wrapping `*http.Client`.
- `func New(opts ...Option) (*Client, error)`.
- `WithTimeout(d time.Duration)`:
  - default **0** — identical to stdlib (no client-level timeout). This
    adapter is named `std`; its defaults MUST NOT silently diverge from
    `net/http.Client`. No consumer has asked for a safe-default override.
  - `d < 0` → constructor error (fail loud).
  - `d == 0` → explicit "no timeout".
  - `d > 0` → sets `http.Client.Timeout`.
  - Package doc RECOMMENDS setting a timeout in production, but the
    adapter does not decide for the caller.
- `WithTransport(rt http.RoundTripper)`: custom base transport.
- `WithTracing(tp trace.TracerProvider)`: **opt-in**, explicit provider
  injection; wraps the base transport with
  `otelhttp.NewTransport(base, otelhttp.WithTracerProvider(tp))`.
  `nil` provider → constructor error. Deliberately does NOT fall back to
  the otelhttp global provider — repo convention is explicit wiring
  (observability/otel registers no globals).
- Implements `httpclient.Client` (`Do(req) (*http.Response, error)`).
- `func (c *Client) Contextual() httpclient.ContextualClient`: thin
  wrapper doing `c.Do(req.WithContext(ctx))`, no added semantics.
  **Constraint**: `Client.Do(req)` and `ContextualClient.Do(ctx, req)`
  share a method name with different signatures — one concrete type
  cannot implement both interfaces. The wrapper is the only shape.

### 5.2 Error model

stdlib passthrough (`*url.Error` etc.). **No errorsx coding** — the port
contract is explicitly shaped like `net/http.Client.Do`; coded errors
would violate it.

### 5.3 Tests

All `httptest.Server`-based, deterministic, no containers — **no new CI
integration leg**:

- Timeout: `d > 0` against a slow handler → deadline error; `d == 0`
  passthrough (no client timeout).
- Option guards: `d < 0` error; nil TracerProvider error; nil transport
  behavior (self-decided at impl: nil `WithTransport` arg → error).
- ctx cancellation honoured through `Do`.
- Tracing: in-memory span exporter (`sdktrace` + `tracetest`) asserts a
  span is emitted when `WithTracing` is on, and none when off.
- `Contextual()`: ctx actually attached (server observes cancellation).

### 5.4 Dependencies

Adds `go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp`
(+ transitive). OTel SDK v1.32 already in the module via
observability/otel.

## 6. Bookkeeping

- CHANGELOG `[Unreleased]` entries ride each PR; release flip to
  `[v0.12.0]` + compare links at tag time.
- README: adapter table row (`httpclient/std`), Status paragraph, compat
  matrix `v0.12.0` row.
- `.agent/state.md` + `.agent/decisions.md` + cross-repo
  `.agent-memory/go-ddd.md` updated at cycle close.

## 7. Success criteria

- Part 1: unit name tests + integration Ping test green; `HealthCheck`
  usable against the existing health Registry.
- Part 3: `go build/vet/test ./...` + `golangci-lint` green (root +
  examples/orders); all §5.3 behaviors covered by tests that fail when
  the behavior breaks; CI green on the PR.
- Adapter `v0.12.0` annotated tag at the merged head, GitHub Release
  Latest; downstream pins via
  `go get github.com/slam0504/go-ddd-adapters@v0.12.0`.

## 8. Deferred / explicitly out (do not re-litigate without new evidence)

- retry (replay/backoff/idempotency policy — needs a real consumer's
  rules), circuit breaker (state machine + dep selection),
  `ports/resilience` extraction, per-request options.
- `httpclienttest` conformance suite: NOT added. The port is a thin
  stdlib-shaped contract; the testable invariants are adapter policy,
  not cross-driver contract. Revisit only if a second driver
  (resty/retryablehttp) surfaces shared deterministic invariants.
- `cache/ristretto`: still deferred; before any future pick-up, FIRST
  verify ristretto v2 Set visibility (async/buffered, `Wait()`),
  cost-based admission rejection, and reference-storage vs cachetest's
  byte-ownership aliasing invariants.

## Self-decided (open to veto during plan review)

- `cache/redis/health.go` file name; default check name `"cache-redis"`.
- `stdhttp` option-validation error style follows the existing adapter
  convention (errorsx sentinels at constructor, as rediscache does).
- Nil `WithTransport(nil)` → constructor error (consistent fail-loud).
