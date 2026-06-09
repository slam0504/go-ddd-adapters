# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [v0.8.0] - 2026-06-09

The idempotency adapter slice: a Redis-backed `Store` implementing core's
`ports/idempotency.Store`. Bumps the core dependency to `v0.8.0`, which
publishes the `idempotency` contract. No breaking changes to the v0.7.0
surface; HTTP enforcement middleware (key/header extraction, scope/fingerprint
builders, response capture/replay) and `examples/orders` idempotency wiring are
deferred to a later cycle.

### Added

- `idempotency/redis` (`redisidempotency`): a Redis-backed `Store` adapter
  wrapping any `redis.Scripter` (`*redis.Client`, `*redis.ClusterClient`,
  `*redis.Ring`) as `go-ddd-core` `ports/idempotency.Store`. Each of
  `Begin`/`Finish`/`Cancel` is a single-key Lua script, so the reserve/
  finish/cancel transition is atomic without a Go-side lock. Ownership is a
  `crypto/rand` lease token minted in Go; an in-progress reservation carries a
  `PEXPIRE` lease whose expiry IS the reclaim/liveness mechanism, and a
  completed record carries a separate non-sliding retention TTL. Configurable
  via `WithKeyPrefix`, `WithLeaseTTL`, and `WithRetention` (both durations must
  be `>= 1ms` or `New` fails loud). `Finish`/`Cancel` map a stale/forged token,
  an already-completed record, or a vanished record to `errorsx.CodeConflict`
  (NOT applied), and a transport failure to `errorsx.CodeUnavailable`
  (INDETERMINATE). Passes core's `RunStoreContract` and `RunReclaimContract`
  against a real Redis via testcontainers.

## [v0.7.0] - 2026-06-05

The authorization (AuthZ) adapter slice: a Casbin-backed `Authorizer`
implementing core's `ports/auth.Authorizer`. Bumps the core dependency
to `v0.7.0`, which publishes the `Authorizer` contract. No breaking
changes to the v0.6.0 surface; HTTP enforcement middleware and
`examples/orders` AuthZ wiring are deferred to later cycles.

### Added

- `auth/casbin` (`casbinauth`): an `Authorizer` adapter wrapping a
  caller-built Casbin enforcer as `go-ddd-core` `ports/auth.Authorizer`.
  Depends on a one-method `Enforcer` interface (both `*casbin.Enforcer`
  and `*casbin.SyncedEnforcer` satisfy it); default `(sub, obj, act)`
  request builder with `Type:ID` object encoding, overridable via
  `WithRequestBuilder`. Deny → `ErrForbidden`, malformed input →
  `ErrInvalidAuthorizationRequest`; engine/ctx/builder errors are passed
  through un-disguised.

## [v0.6.0] - 2026-06-05

The authentication (AuthN) adapter slice: a static-key JWT verifier and
the paired net/http bearer middleware, both implementing core's
`ports/auth` contract (`auth.TokenVerifier`, `auth.Identity`, and the
three auth sentinels). Bumps the core dependency to `v0.6.0`, which
publishes that contract. No breaking changes to the v0.5.0 surface;
JWKS-backed keys and authorization (`Authorizer`) are deferred to a
later cycle.

### Added

#### JWT verifier (`auth/jwt`)
- New `auth/jwt` adapter (package `authjwt`) implementing core's
  `auth.TokenVerifier` for signed JWTs against a single static key
  family per `Verifier`, built on `golang-jwt/jwt v5`. Configure
  exactly one of `WithHMACSecret` (HS256/384/512), `WithRSAPublicKey`
  (RS256/384/512), or `WithECDSAPublicKey` (ES256/384/512 fixed by the
  curve); zero, duplicate, or mixed key options fail at `New`.
- Algorithm locking via `jwt.WithValidMethods`: the accepted set is the
  key family's algorithms, optionally narrowed by `WithAllowedAlgorithms`
  (intersected with the family, never widened, never cross-family), so
  `alg:none` and RS256<->HS256 confusion are rejected at construction or
  parse time. Production callers SHOULD pin the exact issuer algorithm.
- Secure-by-default, enforced at `New` (deploy time) rather than
  `Verify`: `exp` required (`jwt.WithExpirationRequired`), RFC 7518 §3.2
  HMAC secret minimum length (>= the hash size of the largest accepted
  HMAC algorithm), RSA modulus >= 2048 bits (NIST SP 800-57) with an odd
  exponent >= 3, and an ECDSA on-curve check via `(*ecdsa.PublicKey).ECDH()`.
- Claim mapping to `auth.Identity`: `Subject` <- `sub` (missing/empty/
  non-string -> `ErrTokenInvalid`); `TenantID` <- `WithTenantClaim(name)`
  (off by default); `Roles` <- `WithRolesClaim(name)` (default `roles`);
  `Claims` exposes the raw claim map as-is. Optional `WithIssuer`,
  `WithAudience`, and `WithLeeway` gates. Empty security-gate options
  (`WithIssuer("")`, `WithAudience("")`, empty `WithAllowedAlgorithms`)
  fail loud at `New`; empty extraction-name options keep their default.
- Error mapping to core sentinels: empty token -> `ErrTokenMissing`;
  expired `exp` -> `ErrTokenExpired`; everything else (malformed, bad
  signature, disallowed algorithm, `nbf` not-yet-valid, missing `exp`/
  `iss`/`aud`, invalid `sub`) -> `ErrTokenInvalid`. A cancelled context
  returns the raw `ctx.Err()` (checked after the empty-token guard), and
  401 surfaces never leak token or claim content.

#### HTTP bearer middleware (`transport/http/stdlib/authmw`)
- New `transport/http/stdlib/authmw` sub-package (package `authmw`):
  net/http bearer-authentication middleware wiring a core
  `auth.TokenVerifier` into the request pipeline.
  `New(verifier, opts...) (Middleware, error)` returns a standard
  `func(http.Handler) http.Handler` decorator applied to the handler
  passed to `httpstdlib.New` (the Server is unchanged). It fails loud at
  construction on a nil verifier — a literal-nil interface or a nil
  `auth.TokenVerifierFunc` — via the `ErrNilVerifier` sentinel.
- On success the verified `auth.Identity` is stored in the request
  context (readable downstream via `auth.IdentityFromContext`) before
  `next` runs; any extraction or verification failure is written by the
  responder and `next` is not called.
- Default extractor is strict: exactly one `Authorization: Bearer
  <token>` header (multiple headers are ambiguous and rejected),
  case-insensitive scheme, no trimming, any whitespace in the token
  rejected. Overridable via `WithTokenExtractor`; an extractor returning
  `("", nil)` is treated as a missing token and `Verify` is not called.
- Default responder sanitizes failures: coded auth sentinels keep their
  401 and clean message, while any uncoded error collapses to a fixed
  500 `"internal error"` so a custom verifier cannot leak token or claim
  content. On a 401 it sets the RFC 6750 `WWW-Authenticate` challenge
  (bare `Bearer` for a missing token, `Bearer error="invalid_token"` for
  invalid/expired); non-401 responses carry no challenge. Overridable
  via `WithErrorResponder`. There is deliberately no logging option, to
  avoid a surface that could record token or claim content.

## [v0.5.0] - 2026-05-26

The net/http transport adapter. Wraps Go's `net/http` server as a
`bootstrap.Module` with a synchronous bind (port-in-use surfaces as a
startup error) and timeout-bounded graceful shutdown, plus a health
sub-package that aggregates `ports/health.Check` probes into
Kubernetes-style liveness and readiness endpoints. No breaking changes
to the v0.4.0 surface.

### Added

#### net/http transport (`transport/http/stdlib`)
- New `transport/http/stdlib` adapter (package `httpstdlib`) wrapping
  `net/http` as a `bootstrap.Module`. `New(addr, handler, opts...)`
  returns a `*Server` whose `Module()` binds the listener synchronously
  inside Start — a port-in-use surfaces as a startup error, not a
  goroutine-only log line — and whose `Addr()` reports the resolved
  address after Start (useful with an ephemeral `":0"` port). The
  package-level `Module(addr, handler, opts...)` is a convenience
  wrapper for when the resolved address is not needed.
- `Stop` runs `server.Shutdown` under `WithShutdownTimeout` (default
  15s), returning `context.DeadlineExceeded` if in-flight requests do
  not drain in time. Options: `WithReadHeaderTimeout`,
  `WithShutdownTimeout`, `WithLogger`, `WithModuleName` (distinguishes
  multiple servers in lifecycle logs), and `WithBaseContext`.

#### Health probes (`transport/http/stdlib/health`)
- New `transport/http/stdlib/health` sub-package aggregating
  `go-ddd-core` `ports/health.Check` probes into liveness / readiness
  HTTP handlers. The zero-value `Registry` is ready to use;
  `Register` / `MustRegister` are safe for concurrent startup calls.
- `LivenessHandler` serves `/healthz` always 200 and runs no Check (a
  failing dependency must not trigger a pod restart). `ReadinessHandler`
  serves `/readyz`, running every Check sequentially in registration
  order under `SetProbeTimeout` (default 2s each): 200 when all pass,
  503 when any fails, with the body always listing every Check —
  including passing ones on the 503 path — so operators see
  partial-failure state. `Handler()` combines both on the exact paths
  `/healthz` + `/readyz` via a method-aware mux (GET only; other methods
  return 405); mount under a prefix with `http.StripPrefix`.

### Changed

- Bumped `github.com/slam0504/go-ddd-core` from `v0.3.0` → `v0.5.0`
  in both root and `examples/orders` modules. Core's v0.4.0 removed
  the in-process `eventbus/inbox/memory.go` Memory Inbox; the
  `Inbox` interface in core's parent `eventbus` package is
  unchanged, and adapters' import graph touches only that interface,
  so the bump is non-breaking for adapter consumers. The relocated
  Memory Inbox in `eventbus/inbox` (this repo, shipped in v0.3.0)
  is now the canonical implementation; downstream services that
  had not yet migrated their import path from
  `go-ddd-core/eventbus/inbox` to
  `go-ddd-adapters/eventbus/inbox` must do so before pinning
  `go-ddd-core@v0.4.0`+ transitively through this version.
- `examples/orders` upgraded to demo transactional outbox end-to-end.
  `cmd/api` and `cmd/worker` now share Postgres for the write model;
  aggregate `Save` and outbox `Stage` commit in one transaction.
  Neither binary publishes to Kafka anymore — a new standalone
  `cmd/relay` binary drains `outbox_records` to Kafka under
  `FOR UPDATE SKIP LOCKED`. `kafka.RestoreCoreHeaders` propagates
  trace / causation / correlation headers across the process
  boundary; the worker also restores headers from the inbound Kafka
  envelope before dispatching `ShipOrderCommand` and sets
  `causation_id` to the consumed `OrderPlaced` event id.
  `docker-compose.yml` gains a Postgres service, two migrate init
  services (`outbox-migrate` + `orders-migrate` using the official
  `migrate/migrate:v4.19.1` image with `x-migrations-table` per
  source so adapter and example migrations track independently),
  and an `orders-relay` service. The `cmd/reader` projection
  remains in-memory; durable inbox, exactly-once delivery, and the
  in-memory-reader shortcut remain intentional and are documented
  in `examples/orders/README.md`. `Item` gained `json:"sku"`,
  `json:"quantity"`, `json:"price_cents"` tags — the existing
  curl flow previously decoded `price_cents` to 0 due to
  encoding/json case-insensitive matching not normalising
  underscores.
- `examples/orders` enables optimistic locking in the pgx repository
  and maps core's `ErrConcurrencyConflict` to HTTP 409 in `cmd/api`.
  The in-memory repository intentionally does not enforce optimistic
  locking; the gap is documented in `examples/orders/README.md`.

## [v0.4.0] - 2026-05-20

The production-shaped Outbox. Closes all five limitations the
in-process `Memory` outbox shipped with in `v0.3.0`: real transactional
`Stage`, durable `last_error`, multi-Relay safety via `FOR UPDATE SKIP
LOCKED`, separate `outbox_dead_letters` DLQ table, and a `claim_token`
UUID guard that eliminates the stale-worker write window. Paired with a
new `ports/database/pgx` `TxManager` so `Stage` participates in the
caller's database transaction the way the core contract requires. No
breaking changes to the v0.3.0 surface; the in-process `Memory` outbox
remains available alongside the pgx successor.

### Added

#### Postgres Outbox (`eventbus/outbox/pgx`)
- New production-ready Outbox adapter implementing
  `eventbus.Outbox` + `eventbus.OutboxStore` + the local
  `outbox.DeadLetterRecorder` extension over pgx/v5 and Postgres 12+.
  Closes the five limitations the in-process `Memory` adapter shipped
  with (non-transactional `Stage`, no durable `last_error`, no
  multi-Relay safety, no separate DLQ table, the
  `WithMaxSize`/`WithIDGenerator`/`WithClock` knobs that do not carry
  over to a SQL store).
- `Stage` requires a `pgx.Tx` in ctx via `ports/database/pgx` —
  returns `pgxoutbox.ErrNoTx` otherwise. No silent autocommit.
- `Fetch` claims due rows under `FOR UPDATE SKIP LOCKED`, stamps a
  fresh `claim_token` (`gen_random_uuid()`) and a lease window per
  row in a single statement. Safe for multiple Relay instances; the
  invariant is no-overlap, not fair partitioning.
- `MarkSent` / `MarkFailed` / `Terminate` all guard on
  `claim_token`. Stale workers (lease expired and re-claimed by
  another Relay) hit zero rows and return nil silently. Terminate is
  atomic via a single `DELETE ... RETURNING` CTE that feeds the
  `outbox_dead_letters` table.
- `OutboxRecord.ID` is `"<dbid>:<UUID>"`; malformed input to Mark* /
  Terminate returns `pgxoutbox.ErrMalformedID` (programmer-error
  path).
- `WithClaimLease(d)` option, default `5s`, lower bound `100ms`.
- **At-least-once delivery contract.** Lease expiry / slow publisher
  / worker crash between Publish and MarkSent can cause duplicate
  publishes; consumers must dedup via `eventbus/inbox` (or
  equivalent) keyed on `OutboxRecord.EventID`. Tuning the lease
  reduces the window but does not eliminate it.
- `migrations.FS` exposes the schema as an `embed.FS` for tests;
  production callers run the SQL via their existing tooling
  (golang-migrate / goose / atlas / flyway).

#### Postgres TxManager (`ports/database/pgx`)
- New package providing `pgxdb.TxManager`, satisfying
  `go-ddd-core/ports/database.TxManager` on top of a `*pgxpool.Pool`.
  `WithinTx(ctx, fn)` opens a tx, injects it into ctx under a
  package-private key, commits on `fn` nil return, rolls back on
  non-nil error or panic (and re-panics).
- `WithTxOptions(pgx.TxOptions{...})` sets the TxManager-level
  default isolation / access mode. Per-call override is intentionally
  not supported (core's `TxManager.WithinTx` contract has no options
  slot).
- `pgxdb.WithTx` / `pgxdb.TxFromContext` / `pgxdb.Executor` helpers
  let adapter code read or fall through to the pool without caring
  whether the caller is currently inside a tx. `pgxdb.ErrNoTx` is
  the sentinel returned by code that requires a tx in ctx.

### Changed

- **Go floor bumped from 1.24 → 1.25** for the adapter root module.
  Required by the pgx adapter's dependency tree (`pgx/v5 v5.9.2`,
  `testcontainers-go v0.42.0`, `golang-migrate/v4 v4.19.1`, current
  OpenTelemetry releases). Tagged `v0.3.x` and `v0.2.x` are
  unaffected. CI's `actions/setup-go` and the `examples/orders`
  Dockerfile (`golang:1.25-alpine`) follow the bump.

## [v0.3.0] - 2026-05-19

First tagged release on the v0.3.x line. Aligns adapters with
`go-ddd-core v0.3.0` and lands the inbox + outbox adapter packages
plus the kafka outbox-relay header bridge. No breaking changes to
adapters that existed in `v0.2.0` (Kafka publisher / subscriber /
codec, slog logger, OpenTelemetry provider) — all v0.3.0 additions
are new packages or new exported symbols.

### Added

#### Kafka eventbus (`eventbus/kafka`)
- `JSONCodec` with type registry — Marshal populates the six identity
  headers (`event_id`, `event_name`, `aggregate_id`, `aggregate_type`,
  `occurred_at` as RFC3339Nano, `event_version`) and pass-through reads
  `trace_id` / `causation_id` / `correlation_id` from context if set via
  `WithTraceID` / `WithCausationID` / `WithCorrelationID`.
- `Publisher` wrapping `watermill-kafka` v3 with optional
  `PartitionByAggregate` for per-aggregate ordering.
- `Subscriber` wrapping `watermill-kafka` v3, decoding into
  `eventbus.Envelope` and forwarding raw `*message.Message` so callers
  retain ack/nack control.
- Public context-key helpers (`WithTraceID`, `TraceIDFrom`, …) for
  trace/correlation pass-through.
- `RestoreCoreHeaders` — an `outbox.HeaderRestorer` callback that
  re-injects the three core well-known propagation headers
  (`trace_id` / `causation_id` / `correlation_id`) from a stored
  `OutboxRecord.Headers` map back into ctx via this package's
  `WithTraceID` / `WithCausationID` / `WithCorrelationID` helpers.

#### Inbox eventbus (`eventbus/inbox`)
- New package providing the in-process `Memory` Inbox previously
  shipped at `go-ddd-core/eventbus/inbox/memory.go` (v0.2.0 / v0.3.0).
  Downstream services migrate by changing the import path from
  `go-ddd-core/eventbus/inbox` to
  `go-ddd-adapters/eventbus/inbox`; call sites
  (`inbox.NewMemory(...)`) stay identical.
- `WithMaxSize(limit int)` bounds the dedup map by evicting the
  oldest half whenever the map reaches `limit`. Default is
  unbounded.
- `WithTTL(d time.Duration)` adds time-based dedup expiry. Lazy
  filtering at read time (no write-side contention); `Record`
  overwrites an expired entry in place; expired entries are
  reclaimed only when `WithMaxSize`-triggered eviction fires.
  Strict-`>` boundary: `age == ttl` is still fresh,
  `age == ttl + 1ns` is expired.
- `WithClock(now func() time.Time)` for deterministic tests.
- Core retains its copy of `eventbus/inbox/memory.go` for one more
  release cycle; the planned removal there is tracked as an open
  follow-up (see `.agent/state.md`).

#### Outbox eventbus (`eventbus/outbox`)
- New package providing in-process implementations of core's outbox
  contracts. **`Memory` is a non-transactional test/dev adapter only.**
  It implements `eventbus.Outbox` for shape compatibility but does NOT
  participate in the caller's database transaction (only in-process
  mutex safety inside its own store). Use it for tests, single-instance
  examples, and exercising Relay/backoff/DLQ behaviour — not for
  production. The forthcoming SQL/pgx successor will deliver a real
  transactional Outbox.
- `Memory` satisfies `eventbus.Outbox`, `eventbus.OutboxStore`, and
  the local `DeadLetterRecorder` extension. Options:
  `WithMaxSize` (returns `ErrOutboxFull` on overflow; never evicts
  unsent records), `WithClock`, `WithIDGenerator`. Stage is
  all-or-nothing: marshal failures abort the batch; canonical
  `EventName / AggregateID / AggregateType` come from the
  `domain.DomainEvent` interface methods, not from codec metadata.
- `Relay` is a driver-agnostic polling drainer. Options:
  `WithPollInterval`, `WithBatchSize`, `WithBackoff` (default
  exponential with jitter, capped at 60s), `WithMaxAttempts` (default
  10; 0 = unlimited), `WithRelayClock`, `WithHeaderRestorer`. Per-
  record `defer recover` routes panics through the same `fail()` path
  that handles publish errors, so MaxAttempts / DLQ still apply.
  `ErrRelayAlreadyRunning` guards same-`*Relay` reentry; multi-Relay
  against one Memory is the user's responsibility.
- `DeadLetterRecord` carries `Attempts` (terminal count =
  `Record.Attempts + 1`), `Reason`, and `FailedAt`. `Memory.DeadLettered()`
  returns a defensive-copy snapshot for inspection.
- `RelayModule(*Relay, logger.Logger)` wraps Relay into a
  `bootstrap.ModuleFunc` with the kafka-consumer-style detach-then-
  cancel lifecycle: Start spawns one goroutine; Stop cancels and
  waits bounded by `stopCtx`; `context.Canceled` is a silent clean
  shutdown.

#### Slog logger (`logger/slogger`)
- `Logger` implementing `logger.Logger` over `log/slog`.
- Config-driven `New` (writer, level, format `json`/`text`, source
  attribution) plus `NewWithHandler` escape hatch.

#### OpenTelemetry provider (`observability/otel`)
- `Provider` implementing `observability.Provider`. Caller supplies
  `SpanExporter` and `MetricReader`; the adapter does not depend on any
  exporter package, leaving exporter choice (OTLP, stdout, Jaeger…) up
  to the consumer.
- Default propagator is the W3C composite of TraceContext + Baggage.
- `Shutdown` flushes both providers and reports the first error
  encountered.

### Notes

- Single Go module. Per-adapter `go.mod` splitting is deferred until
  dependency footprint becomes a real concern.
- Adapters are constructors only; no global registration. A bootstrap
  registry will arrive in a later release alongside the realistic
  example service.

[Unreleased]: https://github.com/slam0504/go-ddd-adapters/compare/v0.8.0...HEAD
[v0.8.0]: https://github.com/slam0504/go-ddd-adapters/compare/v0.7.0...v0.8.0
[v0.7.0]: https://github.com/slam0504/go-ddd-adapters/compare/v0.6.0...v0.7.0
[v0.6.0]: https://github.com/slam0504/go-ddd-adapters/compare/v0.5.0...v0.6.0
[v0.5.0]: https://github.com/slam0504/go-ddd-adapters/compare/v0.4.0...v0.5.0
[v0.4.0]: https://github.com/slam0504/go-ddd-adapters/compare/v0.3.0...v0.4.0
[v0.3.0]: https://github.com/slam0504/go-ddd-adapters/compare/v0.2.0...v0.3.0
