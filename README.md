# go-ddd-adapters

Concrete adapter implementations for the [`go-ddd-core`][core] port
interfaces. The core ships interfaces only; this repository hosts the
infra-bound implementations so downstream services can wire a real stack
without re-inventing the plumbing.

[core]: https://github.com/slam0504/go-ddd-core

## Status

`v0.12.0` adds the HTTP client adapter. The `httpclient/std` adapter
is the first concrete consumer of core's `ports/httpclient` contract,
wrapping `*net/http.Client` with stdlib-parity defaults (no client-level
timeout, no tracing) and opt-in explicit-provider OTel tracing via
`WithTracing(tp)`. Port published since core v0.1.0, so no core tag or
dep-bump PR; adapter tags `v0.12.0` at merge. The same release gives the
`cache/redis` adapter a `HealthCheck` export (a PING-based core `health.Check`).

`v0.11.0` is the latest tagged release — the cache slice. The
`cache/redis` adapter implements core's `cache.Cache` over go-redis v9
(`redis.Cmdable`). Key design: prefix-free length-encoded key encoding so a
client-supplied key cannot collide with another namespace; `redis.Nil` maps to
`cache.ErrMiss` (never a coded error); ttl==0 means no expiry (native go-redis
semantics); ttl<0 is rejected with `CodeInvalidArgument` before backend
contact; all backend errors are coded (never `CodeUnknown`). Configurable via
`WithKeyPrefix`. Passes core's `cachetest.RunContract` against a real Redis via
testcontainers. Requires `go-ddd-core v0.12.0` (`ports/cache` maturation).

`v0.10.0` adds the inbound rate-limiting slice. The
`ratelimit/redisrate` adapter implements core's `ratelimit.Limiter` over
[go-redis/redis_rate][redisrate] (GCRA, Redis-backed). It is a distributed
inbound limiter: the throttling decision is data (ordinary denial is
`Result{Allowed:false}, nil`, never `CodeRateLimited`), with the fixed
empty-key → ctx → backend error precedence, `RetryAfter`-zero-on-allow,
prefix-free length-encoded keys, and GCRA-burst `Limit`/`Remaining` projection
(`ResetAt` absent). Configurable via `WithKeyPrefix`. Passes core's
`ratelimittest.RunContract` against a real Redis via testcontainers plus
real-backend integration under `-race`. Requires `go-ddd-core v0.10.0`
(`ports/ratelimit`). Redis 3.2+.

`v0.9.0` adds the background-jobs slice. The `jobs/asynq` adapter
implements core's `jobs.Enqueuer` + `jobs.Worker` over [hibiken/asynq][asynq]
(Redis-backed). Dispatch is exact-type-match (not `asynq.ServeMux`), Enqueue
maps failures into two classes, a 30-day default scheduling horizon rejects an
out-of-horizon `ProcessAt` at Enqueue, completed tasks are retained 1h by
default, and delivery is at-least-once with retry→archive dead-lettering.
Configurable via `WithQueue` / `WithSchedulingHorizon` / `WithRetention` /
`WithMaxRetry` / `WithRetryDelay` / `WithTaskTimeout` / `WithConcurrency` /
`WithShutdownTimeout` / `WithLogger`. Passes core's `jobstest.RunContract`
against a real Redis via testcontainers. Requires `go-ddd-core v0.9.0`
(`ports/jobs`). Redis 4.0+.

`v0.8.0` adds the idempotency slice. The
`idempotency/redis` adapter implements core's `idempotency.Store` over
Redis: `Begin`/`Finish`/`Cancel` are single-key Lua scripts (atomic
without a Go-side lock), ownership is a `crypto/rand` lease token, an
in-progress reservation auto-expires via `PEXPIRE` (this expiry IS the
reclaim mechanism), and a completed record carries a separate
non-sliding retention TTL. Accepts any `redis.Scripter` (`*redis.Client`
/ `*redis.ClusterClient` / `*redis.Ring`); configurable via
`WithKeyPrefix` / `WithLeaseTTL` / `WithRetention` (both durations must
be `>= 1ms`). Passes core's `RunStoreContract` and `RunReclaimContract`
against a real Redis via testcontainers. Requires `go-ddd-core v0.8.0`
(`ports/idempotency.Store`).

`v0.7.0` adds the authorization (AuthZ)
slice. The `auth/casbin` adapter implements core's `auth.Authorizer`
by wrapping a caller-built Casbin enforcer behind a one-method
`Enforcer` interface (both `*casbin.Enforcer` and
`*casbin.SyncedEnforcer` satisfy it); the default request builder maps
a call to the `(sub, obj, act)` tuple with `Type:ID` object encoding,
overridable via `WithRequestBuilder`. Denied decisions map to
`ErrForbidden`, malformed input to `ErrInvalidAuthorizationRequest`,
and engine/context/builder errors pass through un-disguised. Requires
`go-ddd-core v0.7.0` (`ports/auth.Authorizer`).

`v0.6.0` adds the authentication (AuthN)
slice. The `auth/jwt` adapter implements core's `auth.TokenVerifier`
against static keys (HMAC secret / RSA / ECDSA public keys) using
`golang-jwt/jwt v5`, with algorithm locking and secure-by-default
validation (`exp` required, RFC 7518 §3.2 HMAC secret length, RSA
modulus >= 2048, ECDSA on-curve). The paired
`transport/http/stdlib/authmw` middleware extracts a bearer token,
calls the verifier, and stores the resulting `auth.Identity` in the
request context; it sanitizes failures (coded auth sentinels keep their
401, uncoded errors collapse to a fixed 500) and sets the RFC 6750
`WWW-Authenticate` challenge. Requires `go-ddd-core v0.6.0`
(`ports/auth`).

`v0.5.0` adds the HTTP transport adapter `transport/http/stdlib` — a
`net/http` server wrapped as a `bootstrap.Module` (synchronous
listen-bind so a port-in-use fails at `Start`, graceful `Shutdown`
under a configurable timeout) — plus the `transport/http/stdlib/health`
sub-package that aggregates `ports/health.Check` probes into `/healthz`
(liveness) and `/readyz` (readiness) handlers.

`v0.4.0` is the production-shaped Outbox milestone. It adds
`eventbus/outbox/pgx` (transactional Outbox + OutboxStore + DLQ backed
by Postgres 12+ via pgx/v5) paired with `ports/database/pgx` (the
`database.TxManager` adapter that lets `Stage` participate in the
caller's database transaction). Closes all five limitations the
in-process `Memory` outbox shipped with in v0.3.0; the `Memory` outbox
remains available for tests and demos. `v0.4.0` bumps the Go floor from
1.24 to 1.25 (required by the pgx dependency tree).

`v0.3.0` remains available on the v0.3.x line and aligns this repo
with `go-ddd-core v0.3.0`. It brings the in-process `Memory` Inbox
(relocated from core) and the new in-process Outbox + Relay (with
adapter-private DLQ and a kafka header-restorer bridge), plus the
unchanged Kafka publisher/subscriber/codec, slog logger, and
OpenTelemetry provider that already shipped in `v0.2.0`.

| Adapter | Port | Backing tech |
| --- | --- | --- |
| `eventbus/kafka` | `eventbus.Publisher`, `eventbus.Subscriber`, `eventbus.Codec` | [watermill-kafka v3][wmk] (Sarama) |
| `eventbus/inbox` | `eventbus.Inbox` | in-process map (`Memory`) with optional `WithMaxSize` and `WithTTL` eviction |
| `eventbus/outbox` | `eventbus.Outbox`, `eventbus.OutboxStore`, `eventbus.Relay` | in-process `Memory` + polling `Relay` — **non-transactional test/dev adapter, not for production** |
| `eventbus/outbox/pgx` | `eventbus.Outbox`, `eventbus.OutboxStore`, `outbox.DeadLetterRecorder` | [pgx/v5][pgx] + Postgres 12+; lease-based claim with `FOR UPDATE SKIP LOCKED`, separate `outbox_dead_letters` table, safe for multiple Relay instances |
| `ports/database/pgx` | `database.TxManager` | [pgx/v5][pgx] pool + ctx-bound transaction handle (`pgxdb.WithTx` / `pgxdb.TxFromContext` / `pgxdb.Executor`) |
| `transport/http/stdlib` | `bootstrap.Module` | stdlib `net/http` server; synchronous listen-bind (port-in-use fails at `Start`), graceful `Shutdown` under a configurable timeout |
| `transport/http/stdlib/health` | `health.Check` | stdlib `net/http.ServeMux`; aggregates `ports/health.Check` probes into `/healthz` (liveness, always 200) + `/readyz` (readiness, 200/503) |
| `auth/jwt` | `auth.TokenVerifier` | [golang-jwt v5][gjwt]; static keys (HMAC / RSA / ECDSA), algorithm-locked, secure-by-default (`exp` required, RFC 7518 §3.2 HMAC length, RSA >= 2048, ECDSA on-curve) |
| `transport/http/stdlib/authmw` | `auth.TokenVerifier` (consumed) | stdlib `net/http` bearer middleware; strict single-header extraction (case-insensitive scheme, no trimming, whitespace rejected), stores verified `auth.Identity` in the request context, sanitizes failures (401 sentinels kept, uncoded -> fixed 500) + RFC 6750 `WWW-Authenticate` |
| `auth/casbin` | `auth.Authorizer` | [Casbin v3][casbin]; wraps a caller-built enforcer behind a one-method `Enforcer` interface (`*casbin.Enforcer` / `*casbin.SyncedEnforcer`), default `(sub, obj, act)` request builder with `Type:ID` object encoding, overridable via `WithRequestBuilder`; deny -> `ErrForbidden`, malformed -> `ErrInvalidAuthorizationRequest`, engine/ctx/builder errors passed through |
| `idempotency/redis` | `idempotency.Store` | [go-redis v9][goredis]; atomic single-key Lua reserve/finish/cancel, `crypto/rand` lease-token ownership, `PEXPIRE`-based reclaim, configurable lease TTL + completed-record retention (see the Redis-version note under [Compatibility matrix](#compatibility-matrix) for the cluster/ring caveat) |
| `jobs/asynq` | `jobs.Enqueuer` / `jobs.Worker` | [hibiken/asynq][asynq] v0.24.1 (Redis-backed); exact-type-match dispatch, 30-day default scheduling horizon, at-least-once delivery with retry→archive; `WithQueue` / `WithSchedulingHorizon` / `WithRetention` / `WithMaxRetry` / `WithRetryDelay` / `WithTaskTimeout` / `WithConcurrency` / `WithShutdownTimeout` / `WithLogger`. Redis 4.0+ |
| `httpclient/std` | `httpclient.Client` / `ContextualClient` | stdlib `net/http.Client` wrapper; stdlib-parity defaults (no client timeout); `WithTimeout` / `WithTransport` / opt-in `WithTracing(tp)` explicit OTel provider; `Contextual()` context-first view |
| `ratelimit/redisrate` | `ratelimit.Limiter` | [go-redis/redis_rate v10][redisrate] (GCRA, Redis-backed); distributed inbound limiter, denial-is-data (never `CodeRateLimited`), `RetryAfter`-zero-on-allow, prefix-free length-encoded keys, GCRA-burst `Limit`/`Remaining` projection, `ResetAt` absent; `WithKeyPrefix` only. Redis 3.2+ |
| `cache/redis` | `cache.Cache` | go-redis v9 (`redis.Cmdable`); prefix-free key, `redis.Nil`→`ErrMiss`, `WithKeyPrefix`, ttl==0 no-expiry / ttl<0 rejected, coded backend errors (never `CodeUnknown`). Redis any version |
| `logger/slogger` | `logger.Logger` | `log/slog` (stdlib) |
| `observability/otel` | `observability.Provider` | OpenTelemetry SDK v1.32 |

[wmk]: https://github.com/ThreeDotsLabs/watermill-kafka
[pgx]: https://github.com/jackc/pgx
[gjwt]: https://github.com/golang-jwt/jwt
[casbin]: https://github.com/casbin/casbin
[goredis]: https://github.com/redis/go-redis
[asynq]: https://github.com/hibiken/asynq
[redisrate]: https://github.com/go-redis/redis_rate

## Compatibility matrix

| `go-ddd-adapters` | `go-ddd-core` | Go |
| --- | --- | --- |
| `v0.11.0` | `v0.12.0` | `>= 1.25` |
| `v0.10.0` | `v0.10.0` | `>= 1.25` |
| `v0.9.0` | `v0.9.0` | `>= 1.25` |
| `v0.8.0` | `v0.8.0` | `>= 1.25` |
| `v0.7.0` | `v0.7.0` | `>= 1.25` |
| `v0.6.0` | `v0.6.0` | `>= 1.25` |
| `v0.5.0` | `v0.5.0` | `>= 1.25` |
| `v0.4.0` | `v0.3.0` | `>= 1.25` |
| `v0.3.0` | `v0.3.0` | `>= 1.24` |
| `v0.2.x` | `v0.2.x` | `>= 1.24` |

`v0.4.0` bumped the Go floor from 1.24 to 1.25 when adding the
`eventbus/outbox/pgx` adapter — its dependency tree
(`pgx/v5 v5.9.2`, `testcontainers-go v0.42.0`,
`golang-migrate/v4 v4.19.1`, current OpenTelemetry releases) requires
`go 1.25.0`. Tagged `v0.3.x` and `v0.2.x` are unaffected.

The `idempotency/redis` adapter needs a Redis server with
`EVAL`, `PEXPIRE`, and multi-field `HSET`. The binding requirement is
multi-field `HSET` (the begin/finish scripts set several hash fields in
one call), which Redis added in **4.0** — `EVAL` + `PEXPIRE` go back to
2.6, so the effective floor is **Redis 4.0+**. Tested against single-node
`redis:7-alpine`. `*redis.ClusterClient` / `*redis.Ring` are accepted
(the constructor takes any `redis.Scripter`) and SHOULD work because
every script touches exactly one key, so all keys land in one hash slot
by construction; this is derived from the single-key design and the
go-redis `Scripter` API, and is NOT exercised by a live multi-node test
in CI.

## Install

```sh
go get github.com/slam0504/go-ddd-adapters@latest
```

You only pay for what you import — each adapter lives under its own
package, so e.g. importing `logger/slog` does not pull Kafka or OTel SDK
dependencies into your binary.

## Usage sketches

### Kafka publisher

```go
codec := kafka.NewJSONCodec()
codec.Register("game.submitted.v1", func() domain.DomainEvent { return &GameSubmitted{} })

pub, err := kafka.NewPublisher(kafka.PublisherConfig{
    Brokers:              []string{"kafka:9092"},
    Codec:                codec,
    PartitionByAggregate: true,
})
if err != nil { /* handle */ }
defer pub.Close()

_ = pub.Publish(ctx, "game-events", evt)
```

### In-process Inbox

```go
in := inbox.NewMemory(
    inbox.WithMaxSize(10_000),       // bound the dedup map
    inbox.WithTTL(24*time.Hour),     // and/or time-based expiry
)

k := eventbus.InboxKey{Consumer: "projector", EventID: evt.EventID()}
seen, _ := in.Seen(ctx, k)
if seen {
    return nil // already processed
}
if err := handler(ctx, evt); err != nil {
    return err
}
return in.Record(ctx, k)
```

The `Inbox` interface itself lives in
`github.com/slam0504/go-ddd-core/eventbus`; this adapter ships the
in-process default that previously lived in `go-ddd-core` v0.2.0–v0.3.0.

### In-process Outbox + Relay

> **Warning — non-transactional.** This `Memory` outbox implements the
> `eventbus.Outbox` interface but does **not** participate in the
> caller's database transaction. It only provides in-process mutex
> safety inside its own store. If aggregate persistence succeeds but
> `Stage` is not called, or the process crashes between domain `Save`
> and `Stage`, the corresponding event is lost. Use this adapter for
> tests, single-instance examples, and local development — not for
> production at-least-once delivery.

```go
codec := kafka.NewJSONCodec()
codec.Register("order.placed.v1", func() domain.DomainEvent { return &OrderPlaced{} })

// Producer side — Stage runs inside (or alongside) the aggregate save.
ob, _ := outbox.NewMemory(outbox.MemoryConfig{Codec: codec},
    outbox.WithMaxSize(10_000), // ErrOutboxFull when backlog exceeds limit
)
_ = ob.Stage(ctx, "orders", evt)

// Relay side — drains the store and publishes via Kafka.
pub, _ := kafka.NewPublisher(kafka.PublisherConfig{Brokers: []string{"kafka:9092"}, Codec: codec})
relay, _ := outbox.NewRelay(outbox.RelayConfig{
    Store: ob, Publisher: pub, Codec: codec, Logger: log,
},
    outbox.WithMaxAttempts(10),
    // Re-promote stored trace_id / causation_id / correlation_id back
    // into the publish ctx so the kafka codec re-emits them.
    outbox.WithHeaderRestorer(kafka.RestoreCoreHeaders),
)

// Wire into bootstrap so Stop drains the relay before shutdown.
app.Use(kafka.PublisherModule(pub))
app.Use(outbox.RelayModule(relay, log))
```

Records that exceed `WithMaxAttempts` are moved to an adapter-private
dead-letter quarantine; inspect via `ob.DeadLettered()`.

### Postgres Outbox + Relay (`eventbus/outbox/pgx`)

The pgx adapter is the production-shaped Outbox: `Stage` runs inside
the caller's database transaction, so aggregate persistence and event
staging commit (or roll back) atomically. The same `outbox.Relay`
drains the active table.

```go
pool, err := pgxpool.New(ctx, "postgres://user:pass@host:5432/db")
if err != nil { /* handle */ }
defer pool.Close()

tm := pgxdb.NewTxManager(pool)

codec := kafka.NewJSONCodec()
codec.Register("order.placed.v1", func() domain.DomainEvent { return &OrderPlaced{} })

store, _ := pgxoutbox.NewStore(pgxoutbox.Config{Pool: pool, Codec: codec},
    pgxoutbox.WithClaimLease(10*time.Second),
)

// Producer side — aggregate save AND Stage commit together. Stage
// reads the *pgx.Tx out of ctx and refuses (ErrNoTx) if there is
// none. No silent autocommit.
err = tm.WithinTx(ctx, func(ctx context.Context) error {
    if err := repo.Save(ctx, agg); err != nil { return err }
    return store.Stage(ctx, "orders", agg.PullEvents()...)
})

// Relay side — drains the active table; identical wiring to the
// memory Relay (decision: Relay is driver-agnostic).
pub, _ := kafka.NewPublisher(kafka.PublisherConfig{Brokers: []string{"kafka:9092"}, Codec: codec})
relay, _ := outbox.NewRelay(outbox.RelayConfig{
    Store: store, Publisher: pub, Codec: codec, Logger: log,
},
    outbox.WithMaxAttempts(10),
    outbox.WithHeaderRestorer(kafka.RestoreCoreHeaders),
)
```

Operational properties:

- **Transactional Stage.** `Stage` requires a `pgx.Tx` in ctx (put
  there by `pgxdb.TxManager.WithinTx`); otherwise it returns
  `pgxoutbox.ErrNoTx`. Aggregate save + event stage are atomic.
- **Safe for multiple Relay instances.** `Fetch` uses `FOR UPDATE
  SKIP LOCKED`; concurrent Relays claim disjoint rows. There is no
  fairness guarantee — one Relay can claim more rows than another in
  any given drain pass — only the no-overlap invariant.
- **At-least-once delivery.** Lease expiry, slow `Publisher.Publish`,
  or a worker crash between Publish and MarkSent can produce
  duplicate publishes. Downstream consumers MUST deduplicate via
  `eventbus/inbox` (or equivalent) keyed on `OutboxRecord.EventID`.
  Tuning `WithClaimLease` reduces the duplicate window but does NOT
  eliminate it — never claim exactly-once.
- **Postgres baseline 12+.** Migration `001` includes
  `CREATE EXTENSION IF NOT EXISTS pgcrypto` so `gen_random_uuid()` is
  available on 12 (no-op on 13+).
- **Schema is yours.** The adapter ships the SQL files and an
  `embed.FS` for tests, but does NOT run migrations at runtime — see
  the [Migrations](#migrations) section.

### Redis idempotency Store (`idempotency/redis`)

Guards inbound-request idempotency (e.g. POST retries): reserve a
`(scope, key)` for a request `fingerprint`, do the work, then finish or
cancel. `Begin` is atomic, so concurrent retries of the same request see
exactly one `StatusNew`; the rest observe `StatusInProgress`,
`StatusCompleted` (with the replayable response), or `StatusMismatch`.

```go
client := redis.NewClient(&redis.Options{Addr: "redis:6379"})
defer client.Close()

store, err := redisidempotency.New(client,
    redisidempotency.WithKeyPrefix("orders-idem"),
    redisidempotency.WithLeaseTTL(30*time.Second), // in-progress reclaim bound
    redisidempotency.WithRetention(24*time.Hour),  // completed-record TTL
)
if err != nil { /* handle */ }

res, err := store.Begin(ctx, "tenantA:placeOrder", idemKey, fingerprint)
if err != nil { /* INDETERMINATE (not CodeConflict) — caller retries Begin */ }

switch res.Status {
case idempotency.StatusNew:
    body, appErr := handle(ctx, req)
    if appErr != nil {
        // Release the reservation so a later retry can re-run.
        _ = store.Cancel(ctx, res)
        return appErr
    }
    if err := store.Finish(ctx, res, body); err != nil {
        // CodeConflict => the reservation was reclaimed/forged; otherwise
        // INDETERMINATE. Either way, recover by re-issuing Begin.
    }
case idempotency.StatusCompleted:
    return res.Response // replay the stored response
case idempotency.StatusInProgress:
    return errTryAgainLater // another worker holds the lease
case idempotency.StatusMismatch:
    return errKeyReusedWithDifferentBody // HTTP 409: same key, new fingerprint
}
```

`Finish`/`Cancel` read only `Scope`, `Key`, and `LeaseToken` from the
reservation. An in-progress lease auto-expires after `WithLeaseTTL`,
which is the reclaim mechanism; completed-record retention
(`WithRetention`) is measured from `Finish` and does not slide on replay.

### Background jobs (`jobs/asynq`)

Enqueue work to run asynchronously (immediately or at a scheduled time) and
process it with a homogeneous worker pool. Delivery is at-least-once, so
handlers must be idempotent. `Run` blocks until its `ctx` is cancelled, then
drains within the configured shutdown timeout and returns `nil`.

```go
enq, err := jobsasynq.NewEnqueuer(asynq.RedisClientOpt{Addr: "redis:6379"})
if err != nil { /* handle */ }
defer enq.Close()

// info.ID correlates logs/traces. A ProcessAt beyond the 30-day default
// horizon is rejected here with CodeInvalidArgument, never silently dropped.
info, err := enq.Enqueue(ctx, jobs.Job{Type: "email:welcome", Payload: body})

w, err := jobsasynq.NewWorker(asynq.RedisClientOpt{Addr: "redis:6379"})
if err != nil { /* handle */ }
_ = w.Register("email:welcome", jobs.HandlerFunc(func(ctx context.Context, t jobs.Task) error {
    return send(ctx, t.Payload) // exact-type match; unhandled types are retried then archived
}))
if err := w.Run(ctx); err != nil { /* coded fatal, e.g. CodeUnavailable */ }
```

### Slog logger

```go
log := slogger.New(slogger.Config{Level: slog.LevelInfo})
log.Log(ctx, logger.LevelInfo, "service started", logger.Attr{Key: "addr", Value: ":8080"})
```

### OTel provider

```go
prov, err := otelad.New(ctx, otelad.Config{
    ServiceName:    "billing",
    ServiceVersion: "1.4.2",
    SpanExporter:   exp, // your own otlptracegrpc / stdout / jaeger exporter
})
if err != nil { /* handle */ }
defer prov.Shutdown(ctx)

tr := prov.Tracer("billing")
```

## Migrations

The `eventbus/outbox/pgx` adapter ships its Postgres schema as
versioned `.up.sql` / `.down.sql` files under
`eventbus/outbox/pgx/migrations/`. The adapter does NOT run
migrations at runtime — schema management is the caller's
responsibility.

Canonical command using [golang-migrate][gm]:

```sh
migrate -path eventbus/outbox/pgx/migrations \
        -database "postgres://user:pass@host:5432/db?sslmode=disable" \
        up
```

The same SQL files work with [goose][goose], [atlas][atlas],
[flyway][flyway], or any tool that consumes numerically-prefixed
versioned SQL — pick whatever already exists in your stack.

For tests and example wiring, the adapter exposes the embedded files
via `eventbus/outbox/pgx/migrations.FS` (an `embed.FS`), usable with
golang-migrate's `iofs` source driver.

[gm]: https://github.com/golang-migrate/migrate
[goose]: https://github.com/pressly/goose
[atlas]: https://atlasgo.io
[flyway]: https://flywaydb.org

## License

MIT — see [LICENSE](LICENSE).
