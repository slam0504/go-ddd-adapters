# go-ddd-adapters Review Log

Last verified: 2026-05-20 Asia/Taipei (post PR #14 merge at `2e9e96d`)

## Recent Findings

- `719c425`: `ConsumerGroup` originally returned independent modules, so Stop
  remained serial by topic. Status: fixed by `5ead403`.
- `5ead403`: test timing was initially wall-clock sensitive. Status: addressed
  by `08caee9` with channel-sync test style.
- v0.3.0 dependency bump (`732e4c5`, merged via `ab92ea3` PR #5): grep
  audit on `Inbox|Outbox|TxManager|UnitOfWork|UoW|WithinTx` confirmed no
  adapter code touches any of the v0.3.0 changed APIs, so the bump
  landed without migration. Cross-checked against
  `<workspace-root>/.agent-memory/go-ddd.md` which records the same
  audit outcome.
- v0.4.0 inbox-memory relocation (PR #7 merged via `2ae4b52`): first
  CI run failed on `golangci-lint (.)` because the 1:1-copied test
  file kept core's two-group import shape but this repo's
  `.golangci.yml` configures `goimports.local-prefixes` to put
  `go-ddd-adapters/...` in its own (fourth) group. Fixed by
  `7e2a096` (one-line regroup via `golangci-lint fmt`). Sibling
  `golangci-lint (examples/orders)` showed as "fail" in the same run
  but `gh api repos/.../actions/jobs/<id>` revealed its conclusion
  was actually `cancelled` (cancel-on-failure cascade), not a real
  lint issue — captured here so future debugging starts from raw
  job conclusion rather than the `gh pr checks` summary.
- `40fe9aa` (pgx TxManager tests) initially landed with no CI
  coverage: the new `ports/database/pgx/*_integration_test.go` files
  are guarded by `//go:build integration` but the `integration-test`
  job only ran `examples/orders`. Closed by this commit — the
  `integration-test` job now also runs
  `go test -tags=integration -race ./ports/database/pgx/...
  ./eventbus/outbox/pgx/...` at the repo root, and the same change
  bumps the CI runner to Go 1.25 (matching the adapter root
  `go 1.25.0` already in `go.mod`). The original plan scheduled this
  CI work as commit 8; it was pulled forward to land before the
  upcoming Store implementation / integration test commits so they
  do not accumulate on top of unverified test infrastructure.

- `9f610ce` (pgx Store implementation): `fetchSQL` selected due rows
  with `ORDER BY available_at, id` inside the CTE, but the outer
  `UPDATE ... RETURNING` has no guaranteed row order — package
  comments and the planned integration tests treat Fetch as ordered,
  so without a fix the order would be nondeterministic in practice.
  Closed by this commit: `fetchSQL` is now a three-stage CTE (`due`
  → `updated` → outer `SELECT ... ORDER BY available_at, id`). The
  outer SELECT re-applies the lock order because UPDATE does not
  modify `available_at`, so `updated.available_at` equals the value
  `due` selected on. Simpler than the JOIN-back form because
  `updated` already RETURNs every column the Store needs.
- `0655989` (pgx docs sweep): README `### Postgres Outbox + Relay`
  operational-properties bullet said "concurrent Relays partition the
  active backlog row-by-row", which can read as load-balancing even
  with the subsequent fairness disclaimer. Closed by `d1db48c` —
  README now says "concurrent Relays claim disjoint rows" matching
  the no-overlap invariant verbatim; `sql.go` fetchSQL header comment
  follows the same tighten. CHANGELOG's "no-overlap, not fair
  partitioning" kept as a negative-form disavowal.
- **`feat/outbox-pgx-adapter` branch merged via PR #14 at
  `2e9e96d` (2026-05-20)**: pgx-Postgres Outbox + pgx TxManager +
  integration tests + CI extension + docs sweep. 13 commits landed
  (10 plan commits, 2 mid-flight review-driven fixes, 1 pre-push
  go-mod-tidy chore). Implements v0.4.0 plan locked at
  `~/.claude/plans/outbox-relay-agile-orbit.md` revision 6
  (revision 6 records the PR #14 merge). Three Codex review
  findings closed in-band:
  - `c028ef3` — CI coverage gap raised against `40fe9aa`: new
    `ports/database/pgx/*_integration_test.go` files were
    `//go:build integration`-guarded but `integration-test` job
    only ran `examples/orders`. Closed by pulling forward plan
    commit 8 to extend the job with a root-module pgx step + bump
    runner to Go 1.25.
  - `fde15ce` — `fetchSQL` row-order bug raised against `9f610ce`:
    `UPDATE ... RETURNING` has no guaranteed row order, so the
    documented Fetch ordering would have been honoured only by
    physical-layout accident. Closed by three-stage CTE
    (`due` → `updated` → outer `SELECT ... ORDER BY available_at,
    id`).
  - `d1db48c` — SKIP LOCKED wording slip raised against
    `0655989`: README's "concurrent Relays partition the active
    backlog" read as load-balancing even with the fairness
    disclaimer. Closed by tightening to "claim disjoint rows"
    across README and `sql.go`.

  CI 5/5 green first try at merge tip including the first ever pgx
  testcontainers run against real Postgres (1m10s).
- v0.7.0 `auth/casbin` (Phase A, `feat/authz-casbin-v0.7.0`): spec/plan
  reviewed and approved after two refinements — (1) typed-nil guard
  mechanism made explicit (concrete type switch on `*casbin.Enforcer` /
  `*casbin.SyncedEnforcer`, no generic reflect); (2) race-test claim
  narrowed to concurrent read-only `Allow` (reload safety delegated to
  Casbin, not asserted by the adapter). Two added review notes folded
  in-band during execution: Task 1's casbin `// indirect` line resolved
  by running `go mod tidy` after the first import lands in Task 2; the
  `defaultRequestBuilder` `Type:ID` colon-collision risk documented in
  godoc (commit `b7d1cad`) with `WithRequestBuilder` as the escape
  hatch. Implementation verified: root build/vet/test, `-race` on
  auth/casbin, integration (real casbin), `examples/orders`
  build/vet/test all green; golangci-lint via CI.

## Current Open Review Items

- None.
