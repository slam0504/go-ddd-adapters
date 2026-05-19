# go-ddd-adapters State

Last verified: 2026-05-19 Asia/Taipei (evening, post v0.3.0 tag cut)
Source: verified via `git status --short`, `git log --oneline -10`, `go.mod`
read, `git tag -l`, GitHub Release inspection, and a cross-check against
`<workspace-root>/.agent-memory/go-ddd.md`.

## Current Branch

- branch: `main` (clean working tree against `origin/main`)
- tags present in repo: `v0.1.0`, `v0.2.0`, **`v0.3.0`** (annotated,
  pushed 2026-05-19 evening; GitHub Release marked Latest at
  https://github.com/slam0504/go-ddd-adapters/releases/tag/v0.3.0).
  `v0.3.0` points at merge commit `3ce2a23`.
- latest commits on `main`:
  - `3ce2a23` `Merge pull request #11 from slam0504/release/v0.3.0`
  - `d603984` `docs(release): finalize v0.3.0 — Inbox + Outbox + kafka header bridge`
  - `d9342ef` `Merge pull request #10 from slam0504/chore/post-v0.4.0-outbox-bookkeeping`
  - `7c5eeb9` `chore(agent-memory): record v0.4.0 outbox cycle CLOSED post PR #9`
  - `6abeafd` `Merge pull request #9 from slam0504/feat/outbox-relay-memory`
  - `bd79e73` `style(eventbus/outbox): gofmt + suppress two gosec false positives`
  - `2f86e2e` `docs(outbox): register adapter in README + CHANGELOG entry + usage sketch`
  - `fb7e9ec` `test(eventbus/outbox): table-driven Memory + Relay + module tests`
  - `9ae090d` `feat(eventbus/kafka): RestoreCoreHeaders helper for outbox relay wiring`
  - `d326952` `feat(eventbus/outbox): polling Relay with backoff, DLQ, header restorer, bootstrap module`

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
- **`v0.3.0` release cycle is CLOSED** (PR #11 merged 2026-05-19
  evening, tag `v0.3.0` annotated at `3ce2a23`, GitHub Release
  published as Latest). First tagged release on the v0.3.x line;
  aligns adapters with `go-ddd-core v0.3.0`. Downstream services
  can now pin via `go get
  github.com/slam0504/go-ddd-adapters@v0.3.0`. The `[Unreleased]`
  CHANGELOG section is now empty and ready to accumulate the next
  cycle.
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
- ~~adapters tag~~ ✅ done 2026-05-19: `v0.3.0` annotated tag pushed
  to origin at `3ce2a23` with a GitHub Release marked Latest.

## Verification

Last green run (PR #11 CI, 2026-05-19 — release prep):

- `go test ./...` PASS (root + `examples/orders`).
- `go test -race ./eventbus/outbox/...` PASS locally before push.
- `golangci-lint run ./...` 0 issues.
- `integration (testcontainers)` PASS.
- Tag `v0.3.0` cut at `3ce2a23` (same tree as PR #11 merge); no
  separate CI on the tag itself, but the underlying commit is the
  green-CI artifact of PR #11.

Default verification before any release-related work:

```sh
go test ./...
go test -race ./eventbus/outbox/...
cd examples/orders && go test ./...
golangci-lint run ./...
```
