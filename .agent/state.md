# go-ddd-adapters State

Last verified: 2026-05-18 Asia/Taipei (afternoon, post PR #7 merge)
Source: verified via `git status --short`, `git log --oneline -8`, `go.mod`
read, `git tag -l`, and a cross-check against
`<workspace-root>/.agent-memory/go-ddd.md`.

## Current Branch

- branch: `main` (clean working tree against `origin/main`)
- tags present in repo: `v0.1.0`, `v0.2.0`. No `v0.3.x`/`v0.4.0-pre` tag
  cut on adapters yet, even though `main` already depends on
  `go-ddd-core v0.3.0` and now hosts the v0.4.0 inbox adapter.
- latest commits on `main`:
  - `2ae4b52` `Merge pull request #7 from slam0504/feat/inbox-memory-adapter`
  - `7e2a096` `style(eventbus/inbox): regroup test imports per goimports local-prefixes`
  - `5276d14` `docs(inbox): register adapter in README and record design decisions`
  - `54bcff7` `test(eventbus/inbox): table-driven TTL boundary and compose-with-MaxSize cases`
  - `fe5f5c8` `feat(eventbus/inbox): add WithTTL option for time-based dedup expiry`
  - `a5cd19d` `feat(eventbus/inbox): relocate in-process Memory inbox from go-ddd-core`
  - `6b85c84` `chore(agent-memory): sync state.md to post-v0.3.0-alignment baseline`
  - `a42b938` `Merge pull request #6 from slam0504/docs/v0.3.0-alignment`

## Current Status

- v0.3.0 dependency-bump cycle is **CLOSED**. Both root `go.mod` and
  `examples/orders/go.mod` require `github.com/slam0504/go-ddd-core v0.3.0`.
- Doc / agent-memory alignment cycle is **CLOSED** (PR #6 merged
  2026-05-18 morning).
- v0.4.0 inbox-memory relocation, adapters side, is **CLOSED** (PR #7
  merged 2026-05-18 afternoon). The new home `eventbus/inbox` (flat
  package `inbox`) hosts the in-process `Memory` Inbox, with a new
  `WithTTL` option on top of the 1:1 relocation. Design rationale is
  in `.agent/decisions.md` "Inbox Adapter Package".
- Kafka and OTel bootstrap module helpers are on `main`.
- `ConsumerGroup` is a single `bootstrap.ModuleFunc` with shared context
  and WaitGroup, so Stop cancels all topic consumers atomically.

## Adapter Surface (current)

| Adapter | Port | Notes |
| --- | --- | --- |
| `eventbus/kafka` | `eventbus.Publisher` / `Subscriber` / `Codec` | watermill-kafka v3 (Sarama) |
| `eventbus/inbox` | `eventbus.Inbox` | in-process `Memory`; `WithMaxSize` + `WithTTL` |
| `logger/slogger` | `logger.Logger` | stdlib `log/slog` |
| `observability/otel` | `observability.Provider` | OTel SDK v1.32 |

## Open Items

- **v0.4.0 core-side removal** (on `go-ddd-core`): once enough
  downstream services have migrated to
  `go-ddd-adapters/eventbus/inbox`, core can physically remove
  `eventbus/inbox/memory.go`. Not in adapters' scope, but tracked here
  because the migration story is shared.
- **adapters tag** (optional): cut a `v0.3.x` or `v0.4.0-pre` tag so
  downstream services have a pinnable version aligned with core v0.3.0
  AND the new inbox package.

## Verification

Last green run (PR #7 CI, 2026-05-18):

- `go test ./...` PASS (root + `examples/orders`).
- `golangci-lint run ./...` 0 issues.
- `integration (testcontainers)` PASS.

Default verification before any release-related work:

```sh
go test ./...
cd examples/orders && go test ./...
golangci-lint run ./...
```
