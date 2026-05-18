# go-ddd-adapters State

Last verified: 2026-05-18 Asia/Taipei
Source: verified via `git status --short`, `git log --oneline -6`, `go.mod`
read, `git tag -l`, and a cross-check against
`<workspace-root>/.agent-memory/go-ddd.md`.

## Current Branch

- working branch: `feat/inbox-memory-adapter` (v0.4.0 inbox memory move
  in progress — see Current Plan below)
- `main` HEAD: `a42b938` `Merge pull request #6 from
  slam0504/docs/v0.3.0-alignment`
- tags present in repo: `v0.1.0`, `v0.2.0`. No `v0.3.0` tag cut on
  adapters yet, even though `main` already depends on
  `go-ddd-core v0.3.0`.
- latest commits on `main`:
  - `a42b938` `Merge pull request #6 from slam0504/docs/v0.3.0-alignment`
  - `c85d838` `docs: use workspace-root placeholder instead of absolute paths`
  - `f0f01fc` `docs: add AGENTS.md and CLAUDE.md agent protocol entry points`
  - `d873350` `docs: record go-ddd-core v0.3.0 alignment in README and .agent memory`
  - `ab92ea3` `Merge pull request #5 from slam0504/release/v0.3.0-bump`
  - `732e4c5` `chore(deps): bump go-ddd-core to v0.3.0`

## Current Status

- v0.3.0 dependency-bump cycle is **CLOSED**. Both root `go.mod` and
  `examples/orders/go.mod` require `github.com/slam0504/go-ddd-core v0.3.0`.
- Doc / agent-memory alignment cycle is **CLOSED** (PR #6 merged
  2026-05-18). `.agent/`, `AGENTS.md`, `CLAUDE.md` and README are now
  in tree; absolute paths under `/Users/...` were symbolified to
  `<workspace-root>/...`.
- **No adapter migration was needed for v0.3.0.** Grep audit on
  `Inbox|Outbox|TxManager|UnitOfWork|UoW|WithinTx` found zero usage in
  adapter code.
- Kafka and OTel bootstrap module helpers are on `main`.
- `ConsumerGroup` is a single `bootstrap.ModuleFunc` with shared context
  and WaitGroup, so Stop cancels all topic consumers atomically.

## Current Plan (v0.4.0 inbox memory move)

Cross-repo memory `<workspace-root>/.agent-memory/go-ddd.md` lists this
as the primary v0.4.0 item.

- `go-ddd-core` head (`d9c8e5c`, v0.3.0 release) still contains
  `eventbus/inbox/memory.go` and its `_test.go`, but core CHANGELOG
  `### Deprecated` and `docs/anti-patterns.md` have pre-announced that
  the file will move out into this repo.
- Correct sequencing: provide the new home in `go-ddd-adapters` **now**
  (so downstream services have a target to migrate to), then core
  removes its copy in a later release.
- Open decisions before coding (need maintainer input):
  - Target package path in this repo (candidates:
    `eventbus/inbox/memory`, `eventbus/inbox`, ...).
  - Whether to re-export `eventbus.InboxKey` / `eventbus.Inbox` from
    the new package or expect callers to import core directly.
  - Whether the new adapter additionally exposes an
    `inbox.NewMemoryWithCapacity` or similar non-core-equivalent
    surface, or stays strictly a 1:1 relocation.

## Verification After This Branch Lands

```sh
go test ./...
cd examples/orders && go test ./...
```

## Other Open Items

- Decide if/when to cut an adapters `v0.3.0` tag to give downstream
  services a pinnable version aligned with `core v0.3.0`.
