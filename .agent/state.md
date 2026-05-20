# go-ddd-adapters State

Last verified: 2026-05-20 Asia/Taipei (evening, end of `feat/outbox-pgx-adapter` branch, PR-ready before push)
Source: verified via `git log origin/main --oneline`, `git tag -l`,
`git rev-list --count main..HEAD` (feat branch is 11 commits ahead of
main at `d1db48c`), and the in-flight memory file
`pgx_outbox_pr_in_flight.md`.

## Current Branch

- main: `b69104d` (HEAD as of 2026-05-20 evening; not yet bumped by
  this branch — `feat/outbox-pgx-adapter` is 11 commits ahead but
  unpushed)
- in-flight branch: `feat/outbox-pgx-adapter` at `d1db48c`, PR-ready
- tags present in repo: `v0.1.0`, `v0.2.0`, **`v0.3.0`** (annotated,
  pushed 2026-05-19 evening; GitHub Release marked Latest at
  https://github.com/slam0504/go-ddd-adapters/releases/tag/v0.3.0).
  `v0.3.0` points at merge commit `3ce2a23`.
- latest commits on `main`:
  - `b69104d` `Merge pull request #13 from slam0504/chore/state-v0.4.0-gating-followup`
  - `8ad04c6` `chore(state): record second gating condition for core v0.4.0 inbox removal`
  - `17d6323` `Merge pull request #12 from slam0504/chore/post-v0.3.0-tag-bookkeeping`
  - `3ce2a23` `Merge pull request #11 from slam0504/release/v0.3.0`
  - `d603984` `docs(release): finalize v0.3.0 — Inbox + Outbox + kafka header bridge`
  - `d9342ef` `Merge pull request #10 from slam0504/chore/post-v0.4.0-outbox-bookkeeping`
  - `7c5eeb9` `chore(agent-memory): record v0.4.0 outbox cycle CLOSED post PR #9`
  - `6abeafd` `Merge pull request #9 from slam0504/feat/outbox-relay-memory`
  - `bd79e73` `style(eventbus/outbox): gofmt + suppress two gosec false positives`
  - `2f86e2e` `docs(outbox): register adapter in README + CHANGELOG entry + usage sketch`

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
  CHANGELOG section is the accumulator for the next cycle (v0.4.0
  pgx outbox below).
- **v0.4.0 pgx-Postgres outbox cycle: PR-ready, awaiting push +
  review** (`feat/outbox-pgx-adapter` branch at `d1db48c`, 11
  commits ahead of `main` at `b69104d`). New packages
  `eventbus/outbox/pgx` (transactional Outbox + OutboxStore + DLQ
  via `claim_token` UUID + `FOR UPDATE SKIP LOCKED` + atomic CTE
  Terminate) and `ports/database/pgx` (TxManager + ctx helpers +
  Executor). Closes the five memory-adapter limitations recorded in
  `.agent/decisions.md`. Plan-locked at `~/.claude/plans/outbox-relay-agile-orbit.md`
  revision 5 (commit 8 pulled forward to land CI coverage before
  Store impl). Two in-band Codex review rounds closed: `fde15ce`
  fixed `UPDATE ... RETURNING` row order via outer SELECT;
  `d1db48c` tightened SKIP LOCKED wording from "partition" to
  "claim disjoint rows". Local `golangci-lint --build-tags=integration
  ./...` 0 issues; `go test -race ./...` green. Container-driven
  integration tests run in CI via the pgx step added in `c028ef3`.
  **Go floor bumped 1.24 → 1.25** on the adapter root module
  (required by pgx/v5 v5.9.2 + testcontainers v0.42.0 +
  golang-migrate v4.19.1 + current OTel releases); CI runner and
  `examples/orders/Dockerfile` follow.
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

- **v0.4.0 core-side removal** (on `go-ddd-core`): physically removing
  `eventbus/inbox/memory.go` is gated on two conditions, not one:
  1. downstream services have migrated their import path from
     `go-ddd-core/eventbus/inbox` to `go-ddd-adapters/eventbus/inbox`
     (no inventory of consumers exists yet — user must surface the
     list before migration can be planned), AND
  2. **adapters has cut its next release after `v0.3.0`** — the
     CHANGELOG promises `eventbus/inbox/memory.go` stays on core for
     "one more release cycle", so the overlap window closes when the
     adapters tag advances (likely the pgx outbox cycle). Until that
     tag exists, removing the core copy would break the published
     guarantee even if all known consumers had already migrated.

  Not in adapters' scope to execute, but tracked here because both
  gating conditions touch this repo's release cadence.
- **pgx-Postgres Outbox** (`eventbus/outbox/pgx`):
  **PR-ready, branch `feat/outbox-pgx-adapter` at `d1db48c`,
  awaiting push + GitHub PR creation + CI green + review**. See
  Current Status above for the cycle summary. Open follow-ups
  intentionally NOT in this PR's scope:
  - `examples/orders` outbox wiring against the pgx Store
    (separate cycle; needs docker-compose Postgres service, pgxrepo
    for aggregate persistence, end-to-end integration test).
  - `LISTEN/NOTIFY` push-based delivery variant (future cycle if
    needed).
  - `claim_id`-based worker attribution — current lease model
    answers "is this row claimed?" via wall-clock `claimed_until`
    only, NOT "WHICH worker holds it". Multi-Relay across many
    hosts is supported (SKIP LOCKED), operator visibility into
    worker identity is the scoped-out piece.
  - `eventbus/inbox/pgx` adapter (separate cycle).
- **`examples/orders` outbox wiring** (optional follow-up): closes the
  "no outbox" shortcut documented in the example; gated on whether the
  team wants the memory adapter on the demo path or prefers waiting
  for pgx.
- ~~adapters tag~~ ✅ done 2026-05-19: `v0.3.0` annotated tag pushed
  to origin at `3ce2a23` with a GitHub Release marked Latest.

## Verification

Last green CI run (PR #11, 2026-05-19 — release prep):

- `go test ./...` PASS (root + `examples/orders`).
- `go test -race ./eventbus/outbox/...` PASS locally before push.
- `golangci-lint run ./...` 0 issues.
- `integration (testcontainers)` PASS.
- Tag `v0.3.0` cut at `3ce2a23` (same tree as PR #11 merge); no
  separate CI on the tag itself, but the underlying commit is the
  green-CI artifact of PR #11.

Pre-push local verification on `feat/outbox-pgx-adapter` at `d1db48c`
(2026-05-20 evening):

- `go test -race ./...` PASS.
- `go build -tags=integration ./...` clean.
- `golangci-lint run --build-tags=integration ./...` 0 issues.
- Container-driven integration tests (testcontainers Postgres + redpanda)
  not run locally (no Docker provider available in this environment);
  deferred to CI's `integration-test` job which now also runs the pgx
  step added in `c028ef3`. CI verdict will be the green-CI artifact
  once the branch is pushed and PR is opened.

Default verification before any release-related work:

```sh
go test ./...
go test -race ./eventbus/outbox/...
cd examples/orders && go test ./...
golangci-lint run ./...
```
