# go-ddd-adapters State

Last verified: 2026-05-18 Asia/Taipei
Source: verified via `git status --short`, `git log --oneline -5`, `go.mod`
read, `git tag -l`, and a grep audit of v0.3.0 changed APIs across the repo.

## Current Branch

- branch: `main` (clean working tree against `origin/main`)
- tags present in repo: `v0.1.0`, `v0.2.0`. No `v0.3.0` tag cut on adapters
  yet, even though `main` already depends on `go-ddd-core v0.3.0`.
- latest commits:
  - `ab92ea3` `Merge pull request #5 from slam0504/release/v0.3.0-bump`
  - `732e4c5` `chore(deps): bump go-ddd-core to v0.3.0`
  - `815f82a` `Merge pull request #4 from slam0504/step-5-module-helpers`
  - `41b9307` `style(examples/orders): gofmt strip trailing blank line in cmd/api/main.go`
  - `08caee9` `test(eventbus/kafka): swap wall-clock timing for channel sync`

## Current Status

- v0.3.0 dependency-bump cycle is **CLOSED**. Both root `go.mod` and
  `examples/orders/go.mod` require `github.com/slam0504/go-ddd-core v0.3.0`.
- **No adapter migration was needed for v0.3.0.** Grep audit on
  `Inbox|Outbox|TxManager|UnitOfWork|UoW|WithinTx` found zero usage in
  adapter code; the only remaining mention was a forward-looking sentence
  in `README.md`, which has now been updated to reflect the closed cycle.
- Kafka and OTel bootstrap module helpers are merged on `main`.
- `ConsumerGroup` is a single `bootstrap.ModuleFunc` with shared context
  and WaitGroup, so Stop cancels all topic consumers atomically.

## Next Steps

- Track v0.4.0 planning from `/Users/eason_tseng/playground/project/.agent-memory/go-ddd.md`.
  Primary item is moving `eventbus/inbox/memory.go` from `go-ddd-core` into
  this repo (code-level breaking change pre-announced in core's CHANGELOG
  `### Deprecated` and `docs/anti-patterns.md`).
- Decide if/when to cut an adapters `v0.3.0` tag to give downstream
  services a pinnable version aligned with `core v0.3.0`.
- Re-run default verification before any release-related work:

  ```sh
  go test ./...
  cd examples/orders && go test ./...
  ```
