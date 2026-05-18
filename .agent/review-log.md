# go-ddd-adapters Review Log

Last verified: 2026-05-18 Asia/Taipei

## Recent Findings

- `719c425`: `ConsumerGroup` originally returned independent modules, so Stop
  remained serial by topic. Status: fixed by `5ead403`.
- `5ead403`: test timing was initially wall-clock sensitive. Status: addressed
  by `08caee9` with channel-sync test style.
- v0.3.0 dependency bump (`732e4c5`, merged via `ab92ea3` PR #5): grep
  audit on `Inbox|Outbox|TxManager|UnitOfWork|UoW|WithinTx` confirmed no
  adapter code touches any of the v0.3.0 changed APIs, so the bump
  landed without migration. Cross-checked against
  `/Users/eason_tseng/playground/project/.agent-memory/go-ddd.md` which
  records the same audit outcome.

## Current Open Review Items

- None. Next review trigger is v0.4.0 work (inbox memory move from core
  into this repo) — re-open this section when that PR opens.
