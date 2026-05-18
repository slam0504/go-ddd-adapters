# go-ddd-adapters Agent Memory Rules

Read `/Users/eason_tseng/playground/project/AGENTS.md` first for the shared
Claude/Codex protocol.

## Project Role

`go-ddd-adapters` contains concrete implementations for `go-ddd-core` ports and
examples showing how services wire the core contracts to real infrastructure.
Keep technology-specific dependencies here, not in `go-ddd-core`.

## Required Startup

1. Read `.agent/state.md`.
2. Read `.agent/decisions.md`.
3. Read `.agent/review-log.md`.
4. If work affects core contracts or releases, read
   `/Users/eason_tseng/playground/project/.agent-memory/go-ddd.md`.
5. Run:

   ```sh
   git status --short
   git log --oneline -5
   ```

## Verification

Default verification:

```sh
go test ./...
cd examples/orders && go test ./...
```

Integration tests are build-tagged and may require Docker/Redpanda.

## Durable Memory Update

At the end of meaningful work, update:

- `.agent/state.md`
- `.agent/decisions.md` if a design is accepted or changed
- `.agent/review-log.md` if CR findings were added or resolved
- `/Users/eason_tseng/playground/project/.agent-memory/go-ddd.md` for
  cross-repo changes
