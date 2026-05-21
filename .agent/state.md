# go-ddd-adapters State

Last verified: 2026-05-21 Asia/Taipei (post core v0.4.0 release, on `chore/bump-core-v0.4.0` branch)
Source: verified via `git log main --oneline -10`, `git tag -l -n0`,
`gh release view v0.4.0`, `go test ./...` + `golangci-lint run
./...` (root + examples/orders + `--build-tags=integration`) post
dep bump.

## Current Branch

- in-flight branch: `chore/bump-core-v0.4.0` (this commit) — bumps
  `github.com/slam0504/go-ddd-core` `v0.3.0` → `v0.4.0` in both
  modules + CHANGELOG `[Unreleased]` note + this state.md update
  + the prior Option-2 inventory-closure update.
- main: `d438c0a` (HEAD as of 2026-05-20, post PR #17 merge)
- previous main: `bc9b041` (post PR #16 merge, v0.4.0 tag cut)
- tags present in repo: `v0.1.0`, `v0.2.0`, `v0.3.0`, **`v0.4.0`**
  (annotated, pushed 2026-05-20; GitHub Release marked Latest at
  https://github.com/slam0504/go-ddd-adapters/releases/tag/v0.4.0).
  `v0.4.0` points at merge commit `bc9b041`; `v0.3.0` points at
  `3ce2a23`.
- latest commits on `main`:
  - `bc9b041` `Merge pull request #16 from slam0504/release/v0.4.0`
  - `0e2edcc` `release(v0.4.0): pgx Outbox + pgx TxManager`
  - `55c3bea` `Merge pull request #15 from slam0504/chore/post-v0.4.0-pgx-outbox-bookkeeping`
  - `bd9e191` `chore(agent-memory): record v0.4.0 pgx outbox cycle CLOSED post PR #14`
  - `2e9e96d` `Merge pull request #14 from slam0504/feat/outbox-pgx-adapter`
  - `81a45bf` `chore(examples/orders): go mod tidy to follow adapter root dep bumps`
  - `b1ce6f1` `chore(agent-memory): mark pgx outbox cycle ready for review`
  - `d1db48c` `docs(outbox/pgx): tighten SKIP LOCKED wording ("partition" → "disjoint")`
  - `0655989` `docs(outbox/pgx): README adapter row + migration guide + CHANGELOG`
  - `698548a` `test(eventbus/outbox/pgx): integration tests`

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
  published). First tagged release on the v0.3.x line; aligned
  adapters with `go-ddd-core v0.3.0`. Downstream services can pin
  via `go get github.com/slam0504/go-ddd-adapters@v0.3.0`.
- **Core v0.4.0 shipped 2026-05-21** at core `main` head `aadde89`
  (merge of core PR #4); annotated tag `v0.4.0` pushed to origin,
  GitHub Release Latest at
  https://github.com/slam0504/go-ddd-core/releases/tag/v0.4.0.
  Core's v0.4.0 physically removed `eventbus/inbox/memory.go` +
  `memory_test.go` (breaking; `Inbox` interface in parent
  `eventbus` package preserved). Adapters' `go.mod` (root +
  `examples/orders`) bumped `go-ddd-core` v0.3.0 → v0.4.0 on this
  branch; non-breaking for adapters' import graph since no `.go`
  file references the removed sub-package. Both inbox-memory
  removal gating conditions now resolved AND delivered.
- **`v0.4.0` release cycle is CLOSED** (PR #16 merged 2026-05-20,
  tag `v0.4.0` annotated at `bc9b041`, GitHub Release published as
  Latest at https://github.com/slam0504/go-ddd-adapters/releases/tag/v0.4.0).
  Ships pgx-Postgres Outbox + pgx TxManager (PR #14). Closes all
  five memory-adapter limitations recorded in `.agent/decisions.md`
  (real tx Stage, durable last_error, multi-Relay safety, separate
  DLQ table, claim_token UUID stale-worker guard). Downstream
  services can now pin via `go get
  github.com/slam0504/go-ddd-adapters@v0.4.0`. Go floor bumped
  1.24 → 1.25 on the adapter root; tagged v0.3.x and v0.2.x are
  unaffected. The `[Unreleased]` CHANGELOG section is now the
  accumulator for the next cycle.
- **v0.4.0 pgx-Postgres outbox cycle is CLOSED** (PR #14 merged
  2026-05-20 at merge commit `2e9e96d`). New packages
  `eventbus/outbox/pgx` (transactional Outbox + OutboxStore + DLQ
  via `claim_token` UUID + `FOR UPDATE SKIP LOCKED` + atomic CTE
  Terminate) and `ports/database/pgx` (TxManager + ctx helpers +
  Executor) closed the five memory-adapter limitations recorded in
  `.agent/decisions.md`. Plan locked at
  `~/.claude/plans/outbox-relay-agile-orbit.md` revision 6 (commit
  8 pulled forward to land CI coverage before Store impl). Three
  Codex review findings closed in-band: `c028ef3` (CI coverage
  gap), `fde15ce` (`UPDATE ... RETURNING` row order via outer
  SELECT), `d1db48c` (SKIP LOCKED wording "partition" → "claim
  disjoint rows"). CI 5/5 green first try at merge tip including
  the first ever pgx testcontainers run against real Postgres
  (1m10s). **Go floor bumped 1.24 → 1.25** on the adapter root
  module (required by pgx/v5 v5.9.2 + testcontainers v0.42.0 +
  golang-migrate v4.19.1 + current OTel releases); CI runner and
  `examples/orders/Dockerfile` followed.
- Kafka and OTel bootstrap module helpers are on `main`.
- `ConsumerGroup` is a single `bootstrap.ModuleFunc` with shared context
  and WaitGroup, so Stop cancels all topic consumers atomically.

## Adapter Surface (current)

| Adapter | Port | Notes |
| --- | --- | --- |
| `eventbus/kafka` | `eventbus.Publisher` / `Subscriber` / `Codec` | watermill-kafka v3 (Sarama); `RestoreCoreHeaders` outbox bridge |
| `eventbus/inbox` | `eventbus.Inbox` | in-process `Memory`; `WithMaxSize` + `WithTTL` |
| `eventbus/outbox` | `eventbus.Outbox` / `OutboxStore` / `Relay` | in-process `Memory` + polling `Relay` — **non-transactional test/dev only**; DLQ via local `DeadLetterRecorder`; `HeaderRestorer` callback |
| `eventbus/outbox/pgx` | `eventbus.Outbox` / `OutboxStore` / `outbox.DeadLetterRecorder` | pgx/v5 + Postgres 12+; lease-based claim with `FOR UPDATE SKIP LOCKED`, `claim_token` UUID guard, separate `outbox_dead_letters` table, safe for multiple Relay instances |
| `ports/database/pgx` | `database.TxManager` | pgx/v5 pool + ctx-bound transaction handle (`pgxdb.WithTx` / `pgxdb.TxFromContext` / `pgxdb.Executor`) |
| `logger/slogger` | `logger.Logger` | stdlib `log/slog` |
| `observability/otel` | `observability.Provider` | OTel SDK v1.32 |

## Open Items

- ~~**v0.4.0 core-side removal**~~ ✅ resolved + delivered
  2026-05-21. Both gating conditions satisfied (Gate (i) adapters'
  `v0.4.0` tag at `bc9b041` on 2026-05-20; Gate (ii) downstream
  consumer inventory formally closed as **empty** under Option 2
  on 2026-05-21, verified via workspace grep of
  `go-ddd-core/eventbus/inbox` returning zero Go `import`
  statements across `/Users/eason_tseng/playground/project`).
  Core PR #4 (`release/v0.4.0-prep` → `main`) merged 2026-05-21
  at `aadde89`; physically removed `eventbus/inbox/memory.go` +
  `memory_test.go`; core `v0.4.0` tag pushed + GitHub Release
  Latest. Adapters' dependency bumped on this branch
  (`chore/bump-core-v0.4.0`).
- **pgx outbox cycle scoped-out follow-ups** (each its own future
  cycle, not gating the v0.4.0 cycle close):
  - `examples/orders` outbox wiring against the pgx Store
    (needs docker-compose Postgres service, pgxrepo for aggregate
    persistence, end-to-end integration test).
  - `LISTEN/NOTIFY` push-based delivery variant.
  - `claim_id`-based worker attribution — current lease model
    answers "is this row claimed?" via wall-clock `claimed_until`
    only, NOT "WHICH worker holds it". Multi-Relay across many
    hosts is supported (SKIP LOCKED), operator visibility into
    worker identity is the scoped-out piece.
  - `eventbus/inbox/pgx` adapter.
- **`examples/orders` outbox wiring** (optional follow-up): closes the
  "no outbox" shortcut documented in the example; gated on whether the
  team wants the memory adapter on the demo path or prefers waiting
  for pgx.
- ~~adapters tag~~ ✅ done 2026-05-19 (`v0.3.0` at `3ce2a23`) +
  2026-05-20 (`v0.4.0` at `bc9b041`, marked Latest at
  https://github.com/slam0504/go-ddd-adapters/releases/tag/v0.4.0).

## Verification

Last green CI run (PR #11, 2026-05-19 — release prep):

- `go test ./...` PASS (root + `examples/orders`).
- `go test -race ./eventbus/outbox/...` PASS locally before push.
- `golangci-lint run ./...` 0 issues.
- `integration (testcontainers)` PASS.
- Tag `v0.3.0` cut at `3ce2a23` (same tree as PR #11 merge); no
  separate CI on the tag itself, but the underlying commit is the
  green-CI artifact of PR #11.
- Tag `v0.4.0` cut at `bc9b041` (same tree as PR #16 merge); no
  separate CI on the tag itself. PR #16 itself is a prose-only
  release-prep diff (CHANGELOG + README) on top of `55c3bea`
  which already carried the v0.4.0 feature surface (PR #14 + PR
  #15); CI on PR #16 green at merge.

PR #14 CI verdict at merge tip (`2e9e96d`, 2026-05-20):

- 5/5 checks green first try, including the first ever pgx
  testcontainers run against real Postgres (1m10s).
- `integration-test` job's new root-module step
  (`go test -tags=integration -race ./ports/database/pgx/...
  ./eventbus/outbox/pgx/...`, added in `c028ef3`) PASS.
- `examples/orders` integration leg PASS.
- `golangci-lint run ./...` and `golangci-lint run
  --build-tags=integration ./...` 0 issues.

Default verification before any release-related work:

```sh
go test ./...
go test -race ./eventbus/outbox/...
cd examples/orders && go test ./...
golangci-lint run ./...
```

Pre-push verification on `chore/bump-core-v0.4.0` (2026-05-21):

- `go build ./...` clean (root + `examples/orders`).
- `go vet ./...` clean (root + `examples/orders`).
- `go test ./...` PASS (root: 5 cached + 3 fresh; eventbus/outbox
  3.5s; eventbus/kafka 1.0s; `examples/orders` no test files).
- `go test -race ./eventbus/outbox/...` PASS (3.7s).
- `golangci-lint run ./...` 0 issues (root + `examples/orders`).
- `golangci-lint run --build-tags=integration ./...` 0 issues.
- Container-driven integration tests (pgx outbox testcontainer)
  deferred to CI per cycle convention.
