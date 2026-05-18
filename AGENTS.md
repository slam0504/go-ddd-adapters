# go-ddd-adapters Agent Memory Rules

Paths written as `<workspace-root>/...` in this file and in `.agent/`
refer to the directory that contains both `go-ddd-core` and
`go-ddd-adapters` as sibling repos. Substitute your own layout when
reading.

Read `<workspace-root>/AGENTS.md` first for the shared Claude/Codex protocol.

## Project Role

`go-ddd-adapters` contains concrete implementations for `go-ddd-core` ports and
examples showing how services wire the core contracts to real infrastructure.
Keep technology-specific dependencies here, not in `go-ddd-core`.

## Required Startup

1. Read `.agent/state.md`.
2. Read `.agent/decisions.md`.
3. Read `.agent/review-log.md`.
4. If work affects core contracts or releases, read
   `<workspace-root>/.agent-memory/go-ddd.md`.
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
- `<workspace-root>/.agent-memory/go-ddd.md` for cross-repo changes
