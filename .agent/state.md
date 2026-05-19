# go-ddd-adapters State

Last verified: 2026-05-19 Asia/Taipei (afternoon, post PR #9 merge)
Source: verified via `git status --short`, `git log --oneline -10`, `go.mod`
read, `git tag -l`, and a cross-check against
`<workspace-root>/.agent-memory/go-ddd.md`.

## Current Branch

- branch: `main` (clean working tree against `origin/main`)
- tags present in repo: `v0.1.0`, `v0.2.0`. Still no `v0.3.x` /
  `v0.4.0-pre` tag cut on adapters; `main` continues to be the
  pinning surface for downstream services.
- latest commits on `main`:
  - `6abeafd` `Merge pull request #9 from slam0504/feat/outbox-relay-memory`
  - `bd79e73` `style(eventbus/outbox): gofmt + suppress two gosec false positives`
  - `2f86e2e` `docs(outbox): register adapter in README + CHANGELOG entry + usage sketch`
  - `fb7e9ec` `test(eventbus/outbox): table-driven Memory + Relay + module tests`
  - `9ae090d` `feat(eventbus/kafka): RestoreCoreHeaders helper for outbox relay wiring`
  - `d326952` `feat(eventbus/outbox): polling Relay with backoff, DLQ, header restorer, bootstrap module`
  - `246277f` `feat(eventbus/outbox): in-process Memory implementing Outbox + OutboxStore + DeadLetterRecorder`
  - `f25d7f1` `chore(agent-memory): pre-flight decisions.md for outbox adapter`
  - `ff81048` `Merge pull request #8 from slam0504/chore/post-v0.4.0-bookkeeping`
  - `2ae4b52` `Merge pull request #7 from slam0504/feat/inbox-memory-adapter`

## Current Status

- v0.3.0 dependency-bump cycle is **CLOSED**. Both root `go.mod` and
  `examples/orders/go.mod` require `github.com/slam0504/go-ddd-core v0.3.0`.
- Doc / agent-memory alignment cycle is **CLOSED** (PR #6 merged
  2026-05-18 morning).
- v0.4.0 inbox-memory relocation cycle is **CLOSED** (PR #7 merged
  2026-05-18 afternoon; bookkeeping PR #8 2026-05-18 evening).
- **v0.4.0 outbox-adapter cycle is CLOSED** (PR #9 merged
  2026-05-19 afternoon, merge commit `6abeafd`). The new package
  `eventbus/outbox` (flat package `outbox`) hosts the in-process
  `Memory` store + driver-agnostic polling `Relay` +
  `RelayModule` bootstrap helper; plus `kafka.RestoreCoreHeaders`
  for the well-known-headers propagation pattern. **Memory is
  explicitly non-transactional** — labelled in package doc, README,
  CHANGELOG, and `.agent/decisions.md` "Outbox Adapter Package" —
  production users must wait for the pgx successor. All 23 design
  decisions recorded in decisions.md.
- Kafka and OTel bootstrap module helpers are on `main`.
- `ConsumerGroup` is a single `bootstrap.ModuleFunc` with shared context
  and WaitGroup, so Stop cancels all topic consumers atomically.

## Adapter Surface (current)

| Adapter | Port | Notes |
| --- | --- | --- |
| `eventbus/kafka` | `eventbus.Publisher` / `Subscriber` / `Codec` | watermill-kafka v3 (Sarama); `RestoreCoreHeaders` outbox bridge |
| `eventbus/inbox` | `eventbus.Inbox` | in-process `Memory`; `WithMaxSize` + `WithTTL` |
| `eventbus/outbox` | `eventbus.Outbox` / `OutboxStore` / `Relay` | in-process `Memory` + polling `Relay` — **non-transactional test/dev only**; DLQ via local `DeadLetterRecorder`; `HeaderRestorer` callback |
| `logger/slogger` | `logger.Logger` | stdlib `log/slog` |
| `observability/otel` | `observability.Provider` | OTel SDK v1.32 |

## Open Items

- **v0.4.0 core-side removal** (on `go-ddd-core`): once enough
  downstream services have migrated to
  `go-ddd-adapters/eventbus/inbox`, core can physically remove
  `eventbus/inbox/memory.go`. Not in adapters' scope, but tracked here
  because the migration story is shared.
- **pgx-Postgres Outbox** (`eventbus/outbox/pgx`, next cycle): real
  transactional `Stage` via `ports/database.TxManager` bridge,
  durable `last_error` column, `SELECT FOR UPDATE SKIP LOCKED`
  multi-Relay safety, DLQ table with the same `Attempts` terminal
  field. Adapter-private gaps the memory adapter intentionally
  leaves open are listed at the bottom of decisions.md's "Outbox
  Adapter Package" section.
- **`examples/orders` outbox wiring** (optional follow-up): closes the
  "no outbox" shortcut documented in the example; gated on whether the
  team wants the memory adapter on the demo path or prefers waiting
  for pgx.
- **adapters tag** (optional): cut a `v0.3.x` or `v0.4.0-pre` tag so
  downstream services have a pinnable version aligned with core v0.3.0
  AND the new inbox + outbox packages.

## Verification

Last green run (PR #9 CI, 2026-05-19):

- `go test ./...` PASS (root + `examples/orders`).
- `go test -race ./eventbus/outbox/...` PASS locally before push.
- `golangci-lint run ./...` 0 issues.
- `integration (testcontainers)` PASS.

Default verification before any release-related work:

```sh
go test ./...
go test -race ./eventbus/outbox/...
cd examples/orders && go test ./...
golangci-lint run ./...
```
