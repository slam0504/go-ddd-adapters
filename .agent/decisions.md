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

## Pre-announced v0.4.0 Migration

Core has pre-announced that `eventbus/inbox/memory.go` will be moved out
of `go-ddd-core` into this repo (see core CHANGELOG `### Deprecated` and
`docs/anti-patterns.md` "Note on `eventbus/inbox/memory.go`"). When that
lands, this repo absorbs the in-memory inbox implementation under a
package path TBD at planning time.
