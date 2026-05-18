# go-ddd-adapters Review Log

Last verified: 2026-05-18 Asia/Taipei (afternoon, post PR #7 merge)

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

## Current Open Review Items

- None. Next review trigger is the next material change to adapters
  (e.g., a new inbox/outbox SQL adapter, or core's removal of its
  `eventbus/inbox/memory.go`).
