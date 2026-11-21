# go-ddd-adapters Agent Memory Rules

Durable agent memory for this repo lives entirely under `.agent/`
(state, decisions, review log). Cross-repo coordination with
`go-ddd-core` is recorded in each repo's own `.agent/state.md`
and synchronised by the operator across sessions; there is no
shared file outside either repository.

## Project Role

`go-ddd-adapters` contains concrete implementations for `go-ddd-core` ports and
examples showing how services wire the core contracts to real infrastructure.
Keep technology-specific dependencies here, not in `go-ddd-core`.

## Required Startup

1. Read `.agent/state.md`.
2. Read `.agent/decisions.md`.
3. Read `.agent/review-log.md`.
4. Run:

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

For cross-repo changes (release coordination with `go-ddd-core`),
mirror the relevant state into the other repo's `.agent/state.md`
during the same session — there is no shared file outside the repos.
