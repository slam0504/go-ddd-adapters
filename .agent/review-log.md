# go-ddd-adapters Review Log

Last verified: 2026-05-20 Asia/Taipei (afternoon, mid-flight on `feat/outbox-pgx-adapter`)

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

## Current Open Review Items

- None.
