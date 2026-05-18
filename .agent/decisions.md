# go-ddd-adapters Decisions

Last verified: 2026-05-18 Asia/Taipei

## Adapter Boundary

- Concrete Kafka/Sarama, slog, and OTel SDK dependencies belong in this repo.
- Core package imports should remain contract-level.
- Examples can use concrete adapters because they are wiring demonstrations.

## Kafka Consumer Lifecycle

- `ConsumerModule` takes an `eventbus.Subscriber` interface, not a concrete
  `*kafka.Subscriber`, so tests and alternate backends can substitute fakes.
- Runtime context uses `context.WithCancel(context.WithoutCancel(startCtx))`.
- Handler signature is:

  ```go
  func(ctx context.Context, env eventbus.Envelope) error
  ```

- Module owns Ack/Nack:
  - nil error -> Ack
  - non-nil error -> Nack
  - panic -> recover, log stack, Nack
- `ConsumerGroup` is a single `bootstrap.ModuleFunc`, not `[]bootstrap.Module`.
  It shares one context and WaitGroup across topics so Stop cancels all topics
  atomically.

## Observability Lifecycle

- `observability/otel.Module` wraps `Provider.Shutdown` for bootstrap stop.

## Upstream Contract Notes (go-ddd-core v0.3.0)

This repo does **not** currently implement these ports, but the contracts
are documented here so future adapter work (SQL/Redis inbox, GORM/pgx
outbox, UoW-aware repositories) starts from the right shape without
re-reading core source.

- **Inbox key shape**: `eventbus.InboxKey{Consumer, EventID}`. The
  `eventbus.Inbox` port is **best-effort dedup**: `Inbox.Record` returns
  only `error`, and the reference `Memory` implementation no-ops on
  duplicate, so a caller cannot distinguish first-write from
  duplicate-write. Under concurrent redelivery two consumers can both
  pass `Seen` and run the handler before either calls `Record`.
  **Handlers in any adapter — SQL inbox, Redis inbox, in-memory inbox —
  must be idempotent on at-least-once delivery.**
- **Outbox record split**: `OutboxRecord.ID` identifies the outbox row;
  `OutboxRecord.EventID` carries the domain event id used as broker
  message id and as the inbox dedup key. `DomainEvent.EventID()` is the
  canonical event id source.
- **Unit-of-Work bridge**: use cases depend on `application.UnitOfWork`.
  Adapters can supply a `ports/database.TxManager` and bridge it via
  `application.UnitOfWorkFromTxManager`. Use cases must **not** import
  `TxManager` directly — that boundary is what keeps application code
  driver-agnostic.

## Inbox Adapter Package (`eventbus/inbox`)

The in-process `Memory` Inbox originally shipped in `go-ddd-core` v0.2.0
and was relocated to this repo in PR #N (TBD on merge). Key design
choices recorded for future SQL/Redis adapter work:

- **Path is `eventbus/inbox` (flat package `inbox`), not
  `eventbus/inbox/memory`.** Three reasons:
  - Mirrors `go-ddd-core/eventbus/inbox` so existing users only change
    their import path; the call site (`inbox.NewMemory(...)`) stays
    identical.
  - Treats "in-process Inbox implementations" as one tech-cluster —
    `Memory` today, hypothetical future in-process variants (a buffered
    sliding-window dedup, an LRU cache) live next to it under the same
    package. This matches how `eventbus/kafka` groups Publisher /
    Subscriber / Codec under one tech-cluster package.
  - Future out-of-process implementations sit at sibling paths
    (`eventbus/inbox/sql`, `eventbus/inbox/redis`, ...) — different
    tech clusters with different transitive deps, so they get their
    own sub-packages.
- **No re-export of `eventbus.InboxKey` / `eventbus.Inbox` from this
  package.** Callers import the contract from `go-ddd-core/eventbus`
  and the implementation from `go-ddd-adapters/eventbus/inbox`. Two
  imports is the explicit boundary the adapter repo is built around.
- **`WithTTL` uses lazy filtering at read time, not eager removal.**
  - `Seen` returns false for entries whose age exceeds `ttl`, but does
    not delete from the map (stays under RLock, no write-side
    contention).
  - `Record` overwrites an expired existing entry in place so Size
    does not grow.
  - Actual memory reclamation falls to `WithMaxSize`-triggered
    `evictOldestHalf`. Long-running services with high event
    cardinality must pair `WithTTL` with `WithMaxSize`.
  - Boundary semantics: `age == ttl` is still fresh (strict `>`),
    `age == ttl + 1ns` is expired. Pinned in
    `TestTTL_BehaviorTable`.

## Pre-announced v0.4.0 Migration (status)

Core has pre-announced that `eventbus/inbox/memory.go` will be removed
from `go-ddd-core` (see core CHANGELOG `### Deprecated` and
`docs/anti-patterns.md` "Note on `eventbus/inbox/memory.go`"). The new
home in this repo is now in place under `eventbus/inbox/` (this PR).
Core can remove its copy in a subsequent core release; downstream
services migrate by changing the import path from
`go-ddd-core/eventbus/inbox` to `go-ddd-adapters/eventbus/inbox`.
