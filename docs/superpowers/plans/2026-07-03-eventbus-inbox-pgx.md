# eventbus/inbox/pgx Durable Inbox Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the durable Postgres inbox (`eventbus/inbox/pgx`) as adapters v0.13.0, after maturing core's `eventbus.Inbox` contract (duplicate-Record = error) and before wiring exactly-once consumption into the examples/orders worker.

**Architecture:** Three phases per the spec (`docs/superpowers/specs/2026-07-02-eventbus-inbox-pgx-design.md`). Phase A (core): `ErrAlreadyRecorded` sentinel + godoc semantics + `eventbus/inboxtest.RunContract`. Phase B (adapters): `pgxinbox` with plain-INSERT + 23505→`ErrAlreadyRecorded` transactional guard; `Memory` aligned (BREAKING). Phase C (examples): joining UnitOfWork so the worker's single per-message transaction covers Seen → handler side effects → Record.

**Tech Stack:** Go 1.25, go-ddd-core (pseudo-pin → v0.13.0), pgx/v5 + pgxpool, testcontainers Postgres, golang-migrate SQL files (embedded).

## Global Constraints

- Full four-step tag-gate (spec §2): impl PR at core PSEUDO-pin → core tag `v0.13.0` → dep-bump PR → adapters tag `v0.13.0` + Release Latest.
- `Record` duplicate is NEVER silent success; pgx `Record` is a plain `INSERT` — NOT `ON CONFLICT DO NOTHING` (spec §4.1).
- Validation precedence (both methods): empty `Consumer` OR `EventID` → `errorsx.CodeInvalidArgument` BEFORE ctx check BEFORE backend; valid key + cancelled ctx → ctx error verbatim (spec §3.2).
- `Memory` change is BREAKING (behavior): CHANGELOG must say so explicitly. `WithTTL`: within window duplicate → `ErrAlreadyRecorded`; expired → key re-recordable (spec §4.2).
- Core is NOT modified in Phase C — the joining UoW lives in examples (spec §5.2). `application.UnitOfWorkFunc` EXISTS in core (verified) — use it.
- No retention/cleanup in v1; `recorded_at` column ships unused (documented limitation, spec §7).
- Branches: core `feat/inbox-maturation` (cut from core origin/main); adapters `feat/inbox-pgx` (EXISTS — carries spec + this plan).
- Local lint binary: `/usr/local/bin/golangci-lint` (PATH one is stale v1.64.8). Import grouping: `go-ddd-adapters/...` in its own group.
- Commit trailer on every commit: `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`.

---

## Phase A — core (repo `/Users/eason_tseng/playground/project/go-ddd-core`, branch `feat/inbox-maturation`)

### Task 1: `ErrAlreadyRecorded` + Inbox godoc semantics

**Files:**
- Modify: `eventbus/inbox.go` (full replacement below)

**Interfaces:**
- Consumes: nothing.
- Produces: `eventbus.ErrAlreadyRecorded` (plain sentinel) and the semantic contract every later task implements. Interface METHOD SET is unchanged (`Seen`/`Record`).

- [ ] **Step 1: Cut the branch**

```bash
cd /Users/eason_tseng/playground/project/go-ddd-core
git fetch origin && git switch -c feat/inbox-maturation origin/main
```

- [ ] **Step 2: Replace `eventbus/inbox.go`**

```go
package eventbus

import (
	"context"
	"errors"
)

// InboxKey scopes idempotency to a specific consumer plus event. Dedup must
// be per-consumer because the same event is often delivered to several
// independent consumers — a projector, a reactor, a downstream saga — and
// each must record its own progress. Sharing a single global eventID-keyed
// inbox would let one consumer's record silently skip another consumer's
// handler.
//
// Consumer is a logical name chosen by the caller: a consumer group, a
// projector name, a handler tag — whatever uniquely identifies the
// processing position. EventID is the domain event id (DomainEvent.EventID()).
type InboxKey struct {
	Consumer string
	EventID  string
}

// ErrAlreadyRecorded is returned by Inbox.Record when the key is already
// recorded. A caller running Record inside the handler's transaction treats
// it as "duplicate delivery": roll the transaction back and acknowledge the
// message without re-running side effects. Implementations may wrap it;
// callers test with errors.Is.
var ErrAlreadyRecorded = errors.New("eventbus: inbox key already recorded")

// Inbox deduplicates incoming messages so at-least-once delivery becomes
// effectively-once per consumer.
//
// Semantics:
//
//   - Record MUST return ErrAlreadyRecorded (possibly wrapped) when key is
//     already recorded — including when the conflict only surfaces at
//     commit time under concurrent duplicate delivery. Duplicate Record is
//     never a silent success: silent success cannot roll the caller's
//     transaction back, which defeats the exactly-once guard.
//   - Seen is an advisory fast-path to skip handler work cheaply; it may be
//     stale under concurrent delivery. Correctness rests on Record running
//     in the same transaction as the handler's side effects and the caller
//     rolling back on ErrAlreadyRecorded.
//   - Validation precedence (both methods): an empty Consumer or EventID is
//     rejected with an errorsx.CodeInvalidArgument-coded error BEFORE the
//     ctx is consulted and BEFORE any backend call; a valid key under a
//     cancelled or expired ctx returns the ctx error verbatim.
type Inbox interface {
	// Seen reports whether key has been recorded. When true, the consumer
	// should ack without re-running the handler. Advisory under
	// concurrency — see the interface comment.
	Seen(ctx context.Context, key InboxKey) (bool, error)
	// Record marks key as processed, typically within the same transaction
	// as the handler's side effects. Returns ErrAlreadyRecorded (possibly
	// wrapped) when key was already recorded.
	Record(ctx context.Context, key InboxKey) error
}
```

- [ ] **Step 3: Build + vet**

Run: `go build ./... && go vet ./eventbus/...`
Expected: clean. `git diff` shows only `eventbus/inbox.go`; method signatures unchanged.

- [ ] **Step 4: Commit**

```bash
git add eventbus/inbox.go
git commit -m "feat(eventbus): ErrAlreadyRecorded + Inbox duplicate/validation semantics

Record on an already-recorded key MUST return ErrAlreadyRecorded (never
silent success — silent success cannot roll the caller's tx back). Seen
is demoted to advisory fast-path. Validation precedence pinned: empty
Consumer/EventID -> CodeInvalidArgument before ctx before backend.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

### Task 2: `eventbus/inboxtest` conformance suite + core PR

**Files:**
- Create: `eventbus/inboxtest/inboxtest.go`
- Create: `eventbus/inboxtest/reference_test.go`
- Modify: `CHANGELOG.md` (`[Unreleased]`)

**Interfaces:**
- Consumes: `eventbus.Inbox`, `eventbus.InboxKey`, `eventbus.ErrAlreadyRecorded` (Task 1); `errorsx.CodeOf` / `errorsx.CodeInvalidArgument`.
- Produces: `inboxtest.Factory` = `func(t *testing.T) eventbus.Inbox`; `inboxtest.RunContract(t *testing.T, newInbox Factory)`. Tasks 4 and 6 run this suite verbatim.

- [ ] **Step 1: Write the suite**

Create `eventbus/inboxtest/inboxtest.go`:

```go
// Package inboxtest provides a deterministic conformance suite for
// implementations of eventbus.Inbox. Adapters call RunContract from their
// own tests; every subtest is deterministic (no sleeps, no containers).
//
// The suite pins the matured contract: duplicate Record returns
// ErrAlreadyRecorded (never silent success), Seen is an advisory fast-path,
// and both methods validate the key (CodeInvalidArgument) before consulting
// ctx and before any backend call. Concurrency-race behavior (blocking
// INSERT, loser rollback) is deliberately NOT here — it depends on backend
// locking and lives in adapter-specific integration tests.
package inboxtest

import (
	"context"
	"errors"
	"testing"

	"github.com/slam0504/go-ddd-core/eventbus"
	"github.com/slam0504/go-ddd-core/pkg/errorsx"
)

// Factory returns a fresh, empty Inbox for one subtest. Implementations
// backed by shared state must isolate each call (fresh keyspace or store).
type Factory func(t *testing.T) eventbus.Inbox

// RunContract runs the Inbox conformance suite against newInbox.
func RunContract(t *testing.T, newInbox Factory) {
	t.Helper()
	ctx := context.Background()
	key := eventbus.InboxKey{Consumer: "consumer-a", EventID: "evt-1"}

	t.Run("SeenUnrecordedReturnsFalse", func(t *testing.T) {
		ibx := newInbox(t)
		seen, err := ibx.Seen(ctx, key)
		if err != nil {
			t.Fatalf("Seen: %v", err)
		}
		if seen {
			t.Fatal("Seen on unrecorded key = true, want false")
		}
	})

	t.Run("RecordThenSeenReturnsTrue", func(t *testing.T) {
		ibx := newInbox(t)
		if err := ibx.Record(ctx, key); err != nil {
			t.Fatalf("Record: %v", err)
		}
		seen, err := ibx.Seen(ctx, key)
		if err != nil {
			t.Fatalf("Seen: %v", err)
		}
		if !seen {
			t.Fatal("Seen after Record = false, want true")
		}
	})

	t.Run("DuplicateRecordReturnsErrAlreadyRecorded", func(t *testing.T) {
		ibx := newInbox(t)
		if err := ibx.Record(ctx, key); err != nil {
			t.Fatalf("first Record: %v", err)
		}
		err := ibx.Record(ctx, key)
		if !errors.Is(err, eventbus.ErrAlreadyRecorded) {
			t.Fatalf("duplicate Record err = %v, want ErrAlreadyRecorded", err)
		}
	})

	t.Run("PerConsumerIsolation", func(t *testing.T) {
		ibx := newInbox(t)
		a := eventbus.InboxKey{Consumer: "consumer-a", EventID: "evt-shared"}
		b := eventbus.InboxKey{Consumer: "consumer-b", EventID: "evt-shared"}
		if err := ibx.Record(ctx, a); err != nil {
			t.Fatalf("Record a: %v", err)
		}
		if err := ibx.Record(ctx, b); err != nil {
			t.Fatalf("Record b (same event, different consumer): %v", err)
		}
		seenB, err := ibx.Seen(ctx, b)
		if err != nil || !seenB {
			t.Fatalf("Seen b = (%v, %v), want (true, nil)", seenB, err)
		}
	})

	t.Run("PerEventIsolation", func(t *testing.T) {
		ibx := newInbox(t)
		e1 := eventbus.InboxKey{Consumer: "consumer-a", EventID: "evt-1"}
		e2 := eventbus.InboxKey{Consumer: "consumer-a", EventID: "evt-2"}
		if err := ibx.Record(ctx, e1); err != nil {
			t.Fatalf("Record e1: %v", err)
		}
		if err := ibx.Record(ctx, e2); err != nil {
			t.Fatalf("Record e2 (same consumer, different event): %v", err)
		}
	})

	t.Run("EmptyConsumerInvalidArgument", func(t *testing.T) {
		ibx := newInbox(t)
		bad := eventbus.InboxKey{Consumer: "", EventID: "evt-1"}
		if _, err := ibx.Seen(ctx, bad); errorsx.CodeOf(err) != errorsx.CodeInvalidArgument {
			t.Fatalf("Seen empty consumer: CodeOf = %v, want CodeInvalidArgument", errorsx.CodeOf(err))
		}
		if err := ibx.Record(ctx, bad); errorsx.CodeOf(err) != errorsx.CodeInvalidArgument {
			t.Fatalf("Record empty consumer: CodeOf = %v, want CodeInvalidArgument", errorsx.CodeOf(err))
		}
	})

	t.Run("EmptyEventIDInvalidArgument", func(t *testing.T) {
		ibx := newInbox(t)
		bad := eventbus.InboxKey{Consumer: "consumer-a", EventID: ""}
		if _, err := ibx.Seen(ctx, bad); errorsx.CodeOf(err) != errorsx.CodeInvalidArgument {
			t.Fatalf("Seen empty event id: CodeOf = %v, want CodeInvalidArgument", errorsx.CodeOf(err))
		}
		if err := ibx.Record(ctx, bad); errorsx.CodeOf(err) != errorsx.CodeInvalidArgument {
			t.Fatalf("Record empty event id: CodeOf = %v, want CodeInvalidArgument", errorsx.CodeOf(err))
		}
	})

	t.Run("EmptyKeyPrecedesCancelledCtx", func(t *testing.T) {
		ibx := newInbox(t)
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		bad := eventbus.InboxKey{Consumer: "", EventID: "evt-1"}
		if err := ibx.Record(cancelled, bad); errorsx.CodeOf(err) != errorsx.CodeInvalidArgument {
			t.Fatalf("empty key + cancelled ctx: CodeOf = %v, want CodeInvalidArgument (key precedes ctx)", errorsx.CodeOf(err))
		}
	})

	t.Run("ValidKeyCancelledCtxReturnsCtxError", func(t *testing.T) {
		ibx := newInbox(t)
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := ibx.Seen(cancelled, key); !errors.Is(err, context.Canceled) {
			t.Fatalf("Seen cancelled ctx err = %v, want context.Canceled", err)
		}
		if err := ibx.Record(cancelled, key); !errors.Is(err, context.Canceled) {
			t.Fatalf("Record cancelled ctx err = %v, want context.Canceled", err)
		}
	})
}
```

- [ ] **Step 2: Write the reference impl + suite run**

Create `eventbus/inboxtest/reference_test.go`:

```go
package inboxtest_test

import (
	"context"
	"sync"
	"testing"

	"github.com/slam0504/go-ddd-core/eventbus"
	"github.com/slam0504/go-ddd-core/eventbus/inboxtest"
	"github.com/slam0504/go-ddd-core/pkg/errorsx"
)

// referenceInbox is the minimal in-memory conformer used to author and
// red-prove the suite. It is test-only — production in-memory inboxes live
// in go-ddd-adapters.
type referenceInbox struct {
	mu   sync.Mutex
	seen map[eventbus.InboxKey]struct{}
}

func newReference() *referenceInbox {
	return &referenceInbox{seen: make(map[eventbus.InboxKey]struct{})}
}

func validate(key eventbus.InboxKey) error {
	if key.Consumer == "" || key.EventID == "" {
		return errorsx.New(errorsx.CodeInvalidArgument, "inboxtest: empty consumer or event id")
	}
	return nil
}

func (r *referenceInbox) Seen(ctx context.Context, key eventbus.InboxKey) (bool, error) {
	if err := validate(key); err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.seen[key]
	return ok, nil
}

func (r *referenceInbox) Record(ctx context.Context, key eventbus.InboxKey) error {
	if err := validate(key); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.seen[key]; ok {
		return eventbus.ErrAlreadyRecorded
	}
	r.seen[key] = struct{}{}
	return nil
}

func TestRunContract_Reference(t *testing.T) {
	inboxtest.RunContract(t, func(t *testing.T) eventbus.Inbox {
		return newReference()
	})
}
```

- [ ] **Step 3: Run the suite green**

Run: `go test ./eventbus/inboxtest/... -race -v`
Expected: PASS, 9 subtests.

- [ ] **Step 4: Red-prove (non-vacuity), then restore**

Temporarily break `referenceInbox` three ways — ONE at a time, run, record the failing subtest name verbatim in your report, restore:
1. duplicate `Record` returns `nil` → `DuplicateRecordReturnsErrAlreadyRecorded` must FAIL.
2. delete the `validate` calls → `EmptyConsumerInvalidArgument` / `EmptyEventIDInvalidArgument` / `EmptyKeyPrecedesCancelledCtx` must FAIL.
3. delete the `ctx.Err()` checks → `ValidKeyCancelledCtxReturnsCtxError` must FAIL.

After restoring: `go test ./eventbus/inboxtest/... -race` → PASS.

- [ ] **Step 5: core CHANGELOG**

Under `## [Unreleased]` in core `CHANGELOG.md`:

```markdown
### Added
- `eventbus.ErrAlreadyRecorded` sentinel + `eventbus/inboxtest.RunContract`
  (9 deterministic subtests): duplicate `Record` is now contractually an
  error (never silent success), `Seen` is an advisory fast-path, and both
  methods validate empty `Consumer`/`EventID` (`CodeInvalidArgument`,
  before ctx, before backend).

### Changed
- **BREAKING (semantics):** `eventbus.Inbox.Record` on an already-recorded
  key must return `ErrAlreadyRecorded`; previously the contract was silent
  and the reference in-process implementation treated it as a no-op.
```

- [ ] **Step 6: Commit + PR (merge UNTAGGED — tag lands at gate step 2)**

```bash
git add eventbus/inboxtest/ CHANGELOG.md
git commit -m "feat(eventbus): inboxtest.RunContract conformance suite

9 deterministic subtests pinning the matured Inbox contract; authored
against a test-only reference impl and red-proven via 3 temporary
breakages (duplicate-silent, no-validation, no-ctx-check).

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
git push -u origin feat/inbox-maturation
gh pr create --title "feat(eventbus): Inbox duplicate-Record semantics + inboxtest suite" \
  --body "Phase A of the durable-inbox cycle. Design (adapters repo): https://github.com/slam0504/go-ddd-adapters/blob/feat/inbox-pgx/docs/superpowers/specs/2026-07-02-eventbus-inbox-pgx-design.md (§3). Merges untagged; v0.13.0 tags after the adapter PR merges at the pseudo-pin (four-step gate).

🤖 Generated with [Claude Code](https://claude.com/claude-code)"
```

Controller merges after core CI green. Record the merge SHA — Task 3 pins it.

---

## Phase B — adapters (repo `/Users/eason_tseng/playground/project/go-ddd-adapters`, branch `feat/inbox-pgx`)

### Task 3: Pin core pseudo-version

**Files:**
- Modify: `go.mod`, `go.sum`, `examples/orders/go.mod`, `examples/orders/go.sum`

**Interfaces:**
- Consumes: core merge SHA from Task 2 (call it `<CORE_SHA>`).
- Produces: both modules resolve `eventbus.ErrAlreadyRecorded` + `eventbus/inboxtest`.

- [ ] **Step 1: Pin**

```bash
cd /Users/eason_tseng/playground/project/go-ddd-adapters && git switch feat/inbox-pgx
go get github.com/slam0504/go-ddd-core@<CORE_SHA> && go mod tidy
cd examples/orders && go get github.com/slam0504/go-ddd-core@<CORE_SHA> && go mod tidy && cd ../..
go build ./... && (cd examples/orders && go build ./...)
```

Expected: both go.mod show the same `v0.12.1-0.<timestamp>-<sha12>` pseudo-version; builds clean.

- [ ] **Step 2: Commit**

```bash
git add go.mod go.sum examples/orders/go.mod examples/orders/go.sum
git commit -m "chore(deps): pin go-ddd-core to inbox-maturation pseudo-version

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

### Task 4: `Memory` alignment (BREAKING) — RunContract + TTL semantics

**Files:**
- Modify: `eventbus/inbox/memory.go`
- Modify: `eventbus/inbox/memory_test.go` (existing duplicate-no-op assertions will need updating; keep every other existing test's intent)
- Test: add `TestMemory_RunContract` + TTL re-record intent tests

**Interfaces:**
- Consumes: `inboxtest.RunContract`, `eventbus.ErrAlreadyRecorded`, `errorsx`.
- Produces: `Memory` conforming to the matured contract; `WithTTL`-expired keys re-recordable (spec §4.2).

- [ ] **Step 1: Write the failing conformance + TTL tests**

Append to `eventbus/inbox/memory_test.go` (adjust imports to add `inboxtest` + `errors` as needed):

```go
func TestMemory_RunContract(t *testing.T) {
	inboxtest.RunContract(t, func(t *testing.T) eventbus.Inbox {
		return inbox.NewMemory()
	})
}

// Within the TTL window a duplicate Record must surface ErrAlreadyRecorded;
// once the entry expires the key is treated as not recorded and a fresh
// Record succeeds — anything else would nullify WithTTL.
func TestMemory_TTL_DuplicateWithinWindowThenReRecordAfterExpiry(t *testing.T) {
	now := time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	m := inbox.NewMemory(inbox.WithTTL(time.Minute), inbox.WithClock(clock))
	key := eventbus.InboxKey{Consumer: "c", EventID: "e"}
	ctx := context.Background()

	if err := m.Record(ctx, key); err != nil {
		t.Fatalf("first Record: %v", err)
	}
	if err := m.Record(ctx, key); !errors.Is(err, eventbus.ErrAlreadyRecorded) {
		t.Fatalf("duplicate within TTL: err = %v, want ErrAlreadyRecorded", err)
	}

	now = now.Add(2 * time.Minute) // expire the entry
	seen, err := m.Seen(ctx, key)
	if err != nil || seen {
		t.Fatalf("Seen after expiry = (%v, %v), want (false, nil)", seen, err)
	}
	if err := m.Record(ctx, key); err != nil {
		t.Fatalf("re-Record after expiry: %v, want nil (fresh recording)", err)
	}
}
```

- [ ] **Step 2: Run to verify failures**

Run: `go test ./eventbus/inbox/... -run 'TestMemory_RunContract|TestMemory_TTL' -v`
Expected: FAIL — `DuplicateRecordReturnsErrAlreadyRecorded`, the two empty-field subtests, `EmptyKeyPrecedesCancelledCtx`, `ValidKeyCancelledCtxReturnsCtxError`, and the TTL duplicate assertion (current Record no-ops).

- [ ] **Step 3: Align `memory.go`**

In `eventbus/inbox/memory.go`: add imports `"github.com/slam0504/go-ddd-core/pkg/errorsx"`. Add:

```go
func errInvalidKey() error {
	return errorsx.New(errorsx.CodeInvalidArgument, "inbox: empty consumer or event id")
}
```

Replace `Seen` and `Record` (keep eviction and everything else):

```go
// Seen reports whether key has been recorded. When WithTTL was set, an
// entry recorded more than ttl ago reports false (the caller's handler
// will be invoked again and may re-Record the key). Precedence: empty
// key -> ctx -> store.
func (m *Memory) Seen(ctx context.Context, key eventbus.InboxKey) (bool, error) {
	if key.Consumer == "" || key.EventID == "" {
		return false, errInvalidKey()
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	seenAt, ok := m.seenAt[key]
	if !ok {
		return false, nil
	}
	if m.ttl > 0 && m.now().Sub(seenAt) > m.ttl {
		return false, nil
	}
	return true, nil
}

// Record marks key as processed. On an already-recorded key it returns
// eventbus.ErrAlreadyRecorded — unless WithTTL is set and the existing
// entry has expired, in which case the key is treated as not recorded and
// is overwritten with a fresh timestamp (restarting the dedup window).
// Precedence: empty key -> ctx -> store.
//
// BREAKING (vs pre-v0.13.0): duplicate Record used to be a silent no-op;
// the matured eventbus.Inbox contract requires the error so transactional
// callers can roll back.
func (m *Memory) Record(ctx context.Context, key eventbus.InboxKey) error {
	if key.Consumer == "" || key.EventID == "" {
		return errInvalidKey()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if seenAt, ok := m.seenAt[key]; ok {
		if m.ttl == 0 || m.now().Sub(seenAt) <= m.ttl {
			return eventbus.ErrAlreadyRecorded
		}
		// expired — fall through to overwrite below
	}
	m.seenAt[key] = m.now()
	if m.maxSize > 0 && len(m.seenAt) > m.maxSize {
		m.evictOldestHalf()
	}
	return nil
}
```

Also update the `WithTTL` option godoc's sentence "Record overwrites the entry with a fresh timestamp" — it stays true; and any existing `memory_test.go` test that asserts duplicate-Record `== nil` must be updated to expect `ErrAlreadyRecorded` (update the assertion AND the test name/comment so intent matches).

- [ ] **Step 4: Run green**

Run: `go test ./eventbus/inbox/... -race -count=1 -v 2>&1 | tail -20`
Expected: ALL pass, including `TestMemory_RunContract` (9 subtests) and the pre-existing eviction/TTL tests.

- [ ] **Step 5: Commit**

```bash
git add eventbus/inbox/memory.go eventbus/inbox/memory_test.go
git commit -m "feat(eventbus/inbox)!: Memory conforms to matured Inbox contract

BREAKING: duplicate Record now returns eventbus.ErrAlreadyRecorded
(was silent no-op). TTL semantics preserved precisely: within-window
duplicate errors; expired entry is re-recordable. Empty-key validation
(CodeInvalidArgument) and ctx precedence added. Passes
inboxtest.RunContract.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

### Task 5: `pgxinbox` scaffold — migrations + New + validation (unit-testable paths)

**Files:**
- Create: `eventbus/inbox/pgx/migrations/001_create_inbox_records.up.sql`
- Create: `eventbus/inbox/pgx/migrations/001_create_inbox_records.down.sql`
- Create: `eventbus/inbox/pgx/migrations/embed.go`
- Create: `eventbus/inbox/pgx/inbox.go`
- Test: `eventbus/inbox/pgx/inbox_test.go` (unit, no build tag)

**Interfaces:**
- Consumes: `pgxdb.Executor(ctx, pool)` / `pgxdb.TxFromContext` (read `ports/database/pgx/executor.go` for the exact `PgxExecutor` method set before writing SQL calls); `eventbus.ErrAlreadyRecorded`; `errorsx`.
- Produces: `pgxinbox.New(pool *pgxpool.Pool) (*Inbox, error)`; `(*Inbox).Seen` / `(*Inbox).Record` implementing `eventbus.Inbox`; `pgxinbox.Migrations` embed.FS. Tasks 6/7/9/10 depend on these exact names.

- [ ] **Step 1: Migrations**

`001_create_inbox_records.up.sql`:

```sql
CREATE TABLE inbox_records (
    consumer    TEXT        NOT NULL,
    event_id    TEXT        NOT NULL,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (consumer, event_id)
);
```

`001_create_inbox_records.down.sql`:

```sql
DROP TABLE inbox_records;
```

`embed.go`: mirror `eventbus/outbox/pgx/migrations/embed.go` exactly (same package doc style, `//go:embed *.sql`, exported FS var) — read that file first and adapt only the names/comments to inbox.

- [ ] **Step 2: Write the failing unit tests**

Create `eventbus/inbox/pgx/inbox_test.go`:

```go
package pgxinbox_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/slam0504/go-ddd-core/eventbus"
	"github.com/slam0504/go-ddd-core/pkg/errorsx"

	pgxinbox "github.com/slam0504/go-ddd-adapters/eventbus/inbox/pgx"
)

// lazyPool returns a pool that never dials — pgxpool.New is lazy, and every
// path under test here must fail BEFORE any backend call.
func lazyPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), "postgres://127.0.0.1:1/never?sslmode=disable")
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestNew_NilPoolRejected(t *testing.T) {
	_, err := pgxinbox.New(nil)
	if errorsx.CodeOf(err) != errorsx.CodeInvalidArgument {
		t.Fatalf("New(nil): CodeOf = %v, want CodeInvalidArgument", errorsx.CodeOf(err))
	}
}

// Empty-field and ctx precedence run pre-backend: with a never-dialed pool,
// any attempt to reach Postgres would hang/error differently — a passing
// test here proves the check ordering.
func TestValidationPrecedence_PreBackend(t *testing.T) {
	ibx, err := pgxinbox.New(lazyPool(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	bad := eventbus.InboxKey{Consumer: "", EventID: "e"}
	if _, err := ibx.Seen(ctx, bad); errorsx.CodeOf(err) != errorsx.CodeInvalidArgument {
		t.Fatalf("Seen empty consumer: CodeOf = %v, want CodeInvalidArgument", errorsx.CodeOf(err))
	}
	if err := ibx.Record(ctx, bad); errorsx.CodeOf(err) != errorsx.CodeInvalidArgument {
		t.Fatalf("Record empty consumer: CodeOf = %v, want CodeInvalidArgument", errorsx.CodeOf(err))
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	good := eventbus.InboxKey{Consumer: "c", EventID: "e"}
	if _, err := ibx.Seen(cancelled, good); !errors.Is(err, context.Canceled) {
		t.Fatalf("Seen cancelled ctx: err = %v, want context.Canceled", err)
	}
	if err := ibx.Record(cancelled, good); !errors.Is(err, context.Canceled) {
		t.Fatalf("Record cancelled ctx: err = %v, want context.Canceled", err)
	}
}
```

- [ ] **Step 3: Run to verify build failure**

Run: `go test ./eventbus/inbox/pgx/... -v`
Expected: FAIL to build — package does not exist.

- [ ] **Step 4: Implement `inbox.go`**

```go
// Package pgxinbox is the Postgres-backed durable implementation of core's
// eventbus.Inbox over pgx/v5. Record is a plain INSERT guarded by the
// (consumer, event_id) primary key — deliberately NOT ON CONFLICT DO
// NOTHING: a duplicate surfaces as eventbus.ErrAlreadyRecorded so the
// caller's transaction rolls back. Under concurrent duplicate delivery the
// second INSERT blocks on the first transaction's lock and raises the
// unique violation only after the winner commits — that blocking INSERT is
// the transactional exactly-once guard.
//
// Both methods join an ambient pgx transaction when ctx carries one
// (pgxdb.WithTx / TxFromContext), else they run on the pool — the same
// shape as outbox Stage.
//
// Limitation (v1): inbox_records grows without bound; recorded_at exists to
// keep a future retention/cleanup migration possible. See the design spec
// 2026-07-02 §7.
package pgxinbox

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/slam0504/go-ddd-core/eventbus"
	"github.com/slam0504/go-ddd-core/pkg/errorsx"

	pgxdb "github.com/slam0504/go-ddd-adapters/ports/database/pgx"
)

// Inbox is a durable Postgres eventbus.Inbox. Safe for concurrent use.
type Inbox struct {
	pool *pgxpool.Pool
}

// New builds an Inbox over pool. The inbox_records table must exist (apply
// Migrations).
func New(pool *pgxpool.Pool) (*Inbox, error) {
	if pool == nil {
		return nil, errorsx.New(errorsx.CodeInvalidArgument, "pgxinbox: nil pool")
	}
	return &Inbox{pool: pool}, nil
}

func errInvalidKey() error {
	return errorsx.New(errorsx.CodeInvalidArgument, "pgxinbox: empty consumer or event id")
}

// Seen reports whether key has been recorded. Advisory under concurrency —
// see the core eventbus.Inbox contract. Precedence: empty key -> ctx ->
// backend.
func (i *Inbox) Seen(ctx context.Context, key eventbus.InboxKey) (bool, error) {
	if key.Consumer == "" || key.EventID == "" {
		return false, errInvalidKey()
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	q := pgxdb.Executor(ctx, i.pool)
	var seen bool
	err := q.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM inbox_records WHERE consumer = $1 AND event_id = $2)`,
		key.Consumer, key.EventID,
	).Scan(&seen)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return false, err
		}
		return false, fmt.Errorf("pgxinbox: seen: %w", err)
	}
	return seen, nil
}

// Record marks key as processed via a plain INSERT. A duplicate key raises
// Postgres unique_violation (23505), returned as a wrapped
// eventbus.ErrAlreadyRecorded — run Record inside the handler's transaction
// and roll back on it. Precedence: empty key -> ctx -> backend.
func (i *Inbox) Record(ctx context.Context, key eventbus.InboxKey) error {
	if key.Consumer == "" || key.EventID == "" {
		return errInvalidKey()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	q := pgxdb.Executor(ctx, i.pool)
	_, err := q.Exec(ctx,
		`INSERT INTO inbox_records (consumer, event_id) VALUES ($1, $2)`,
		key.Consumer, key.EventID,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return fmt.Errorf("pgxinbox: %w", eventbus.ErrAlreadyRecorded)
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return fmt.Errorf("pgxinbox: record: %w", err)
	}
	return nil
}

// Compile-time assertion that *Inbox implements core's eventbus.Inbox.
var _ eventbus.Inbox = (*Inbox)(nil)
```

If `PgxExecutor` lacks `QueryRow`/`Exec` under those names, adapt the calls to the actual method set in `ports/database/pgx/executor.go` (outbox's `store.go` shows working usage) — do NOT change the executor.

- [ ] **Step 5: Run unit tests green + commit**

Run: `go test ./eventbus/inbox/pgx/... -race -v` → PASS (2 tests). `go vet ./...` clean.

```bash
git add eventbus/inbox/pgx/
git commit -m "feat(eventbus/inbox/pgx): pgxinbox scaffold — schema, New, validation precedence

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

### Task 6: `pgxinbox` integration — RunContract vs real Postgres + CI leg

**Files:**
- Create: `eventbus/inbox/pgx/integration_test.go` (`//go:build integration`)
- Modify: `.github/workflows/` CI — append `./eventbus/inbox/pgx/...` to the existing "pgx adapter integration tests" step's package list (pure append, line currently ending `./cache/redis/...`)

**Interfaces:**
- Consumes: Task 5's `pgxinbox.New` + `Migrations`; `inboxtest.RunContract`.
- Produces: CI-proven conformance.

- [ ] **Step 1: Write the integration harness + suite run**

Mirror `eventbus/outbox/pgx`'s testcontainers harness (read its `postgres_test.go` / `store_integration_test.go` first: container start, migration apply via golang-migrate against `Migrations`, shared pool in `TestMain` with `os.Exit(m.Run())`). Then the suite + isolation:

```go
// TestRunContract runs core's inbox conformance suite against real
// Postgres. Factory isolation: each subtest gets a fresh keyspace by
// truncating inbox_records (the suite uses fixed key values).
func TestRunContract(t *testing.T) {
	inboxtest.RunContract(t, func(t *testing.T) eventbus.Inbox {
		if _, err := sharedPool.Exec(context.Background(), `TRUNCATE inbox_records`); err != nil {
			t.Fatalf("truncate: %v", err)
		}
		ibx, err := pgxinbox.New(sharedPool)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return ibx
	})
}
```

(Adapt `sharedPool` to the harness's actual pool variable name.)

- [ ] **Step 2: Run locally (Docker available)**

Run: `go test -tags=integration -race ./eventbus/inbox/pgx/... -v 2>&1 | tail -15`
Expected: PASS — 9 RunContract subtests vs real Postgres.

- [ ] **Step 3: CI append + lint + commit**

Append ` ./eventbus/inbox/pgx/...` to the CI step. Run both lint invocations (0 issues).

```bash
git add eventbus/inbox/pgx/integration_test.go .github/workflows/
git commit -m "test(eventbus/inbox/pgx): RunContract vs real Postgres + CI integration leg

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

### Task 7: Concurrent-duplicate race test (the transactional guard)

**Files:**
- Modify: `eventbus/inbox/pgx/integration_test.go` (append)

**Interfaces:**
- Consumes: Task 6's harness; `pgxdb.WithTx`.
- Produces: proof that the blocking-INSERT guard works: exactly one of two concurrent transactions commits.

- [ ] **Step 1: Write the race test**

```go
// TestRecord_ConcurrentDuplicate_ExactlyOneWins proves the transactional
// guard: two transactions Record the same key concurrently; the second
// INSERT blocks on the first tx's lock and raises 23505 only after the
// winner commits — the loser observes ErrAlreadyRecorded and rolls back.
func TestRecord_ConcurrentDuplicate_ExactlyOneWins(t *testing.T) {
	ctx := context.Background()
	if _, err := sharedPool.Exec(ctx, `TRUNCATE inbox_records`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	ibx, err := pgxinbox.New(sharedPool)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	key := eventbus.InboxKey{Consumer: "race-consumer", EventID: "race-evt"}

	tx1, err := sharedPool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx1: %v", err)
	}
	if err := ibx.Record(pgxdb.WithTx(ctx, tx1), key); err != nil {
		t.Fatalf("tx1 Record: %v", err)
	}

	// tx2 Record must BLOCK until tx1 commits, then observe 23505.
	loserErr := make(chan error, 1)
	go func() {
		tx2, err := sharedPool.Begin(ctx)
		if err != nil {
			loserErr <- fmt.Errorf("begin tx2: %w", err)
			return
		}
		defer func() { _ = tx2.Rollback(ctx) }()
		loserErr <- ibx.Record(pgxdb.WithTx(ctx, tx2), key)
	}()

	select {
	case err := <-loserErr:
		t.Fatalf("tx2 Record returned %v before tx1 committed — INSERT did not block", err)
	case <-time.After(300 * time.Millisecond):
		// still blocked: expected
	}

	if err := tx1.Commit(ctx); err != nil {
		t.Fatalf("tx1 commit: %v", err)
	}
	select {
	case err := <-loserErr:
		if !errors.Is(err, eventbus.ErrAlreadyRecorded) {
			t.Fatalf("loser err = %v, want ErrAlreadyRecorded", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("tx2 Record still blocked 5s after tx1 commit")
	}

	var n int
	if err := sharedPool.QueryRow(ctx, `SELECT count(*) FROM inbox_records`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("inbox_records count = %d, want exactly 1", n)
	}
}
```

- [ ] **Step 2: Run + commit**

Run: `go test -tags=integration -race ./eventbus/inbox/pgx/... -run TestRecord_Concurrent -v` → PASS.

```bash
git add eventbus/inbox/pgx/integration_test.go
git commit -m "test(eventbus/inbox/pgx): concurrent-duplicate race — blocking INSERT, exactly one wins

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

### Task 8: Adapters bookkeeping (CHANGELOG BREAKING + README)

**Files:**
- Modify: `CHANGELOG.md` (`[Unreleased]`), `README.md` (adapter table: update `eventbus/inbox` row + add `eventbus/inbox/pgx` row)

- [ ] **Step 1: CHANGELOG**

Under `## [Unreleased]`:

```markdown
### Added
- `eventbus/inbox/pgx` (`pgxinbox`): durable Postgres inbox — first adapter
  proving core's matured `eventbus.Inbox` contract. Plain `INSERT` +
  `(consumer, event_id)` primary key; duplicate surfaces as
  `eventbus.ErrAlreadyRecorded` (never `ON CONFLICT DO NOTHING`); joins an
  ambient pgx transaction via `pgxdb` ctx helpers. v1 limitation:
  `inbox_records` grows without bound (`recorded_at` reserves the cleanup
  path).
- `examples/orders`: worker consumes `OrderPlaced` exactly-once per consumer
  via a single per-message transaction (Seen → ship + outbox.Stage →
  Record) using an examples-local joining UnitOfWork.

### Changed
- **BREAKING:** `eventbus/inbox` `Memory.Record` on an already-recorded key
  now returns `eventbus.ErrAlreadyRecorded` (was silent no-op), per core's
  matured Inbox contract. `WithTTL` is preserved precisely: within-window
  duplicates error; an expired entry is re-recordable. Both methods now
  validate empty `Consumer`/`EventID` (`CodeInvalidArgument`) and honour
  ctx cancellation.
```

- [ ] **Step 2: README adapter table**

Update the `eventbus/inbox` row's notes to mention the matured duplicate semantics, and add:

```markdown
| `eventbus/inbox/pgx` | `eventbus.Inbox` | [pgx/v5][pgx] + Postgres; plain INSERT + PK guard (duplicate → `ErrAlreadyRecorded`), joins ambient tx via `pgxdb`; durable per-consumer dedup for multi-instance workers |
```

- [ ] **Step 3: Lint sweep + commit**

Run the full verification block (build/vet/test root + examples, both lint invocations). Commit:

```bash
git add CHANGELOG.md README.md
git commit -m "docs(eventbus/inbox): CHANGELOG (BREAKING Memory semantics) + README rows

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

## Phase C — examples/orders (same branch `feat/inbox-pgx`)

### Task 9: Joining UoW + exactly-once worker path + compose migration

**Files:**
- Create: `examples/orders/workerflow/inboxflow.go`
- Modify: `examples/orders/cmd/worker/main.go` (uow construction + handle closure + inbox wiring)
- Modify: `examples/orders/docker-compose.yml` (new `inbox-migrate` service + worker `depends_on`)
- Test: `examples/orders/workerflow/inboxflow_test.go` (unit — joining behavior)

**Interfaces:**
- Consumes: `pgxinbox.New`; `pgxdb.TxFromContext` / `TxManager.WithinTx(ctx, database.TxFunc)`; `application.UnitOfWorkFunc` (EXISTS in core — verified); existing `HandleOrderPlaced(ctx, log, cmdBus, env)`.
- Produces: `workerflow.ConsumerName` = `"orders-worker"`; `workerflow.JoiningUnitOfWork(txMgr database.TxManager) application.UnitOfWork`; `workerflow.HandleOrderPlacedOnce(ctx, log, txMgr, ibx, cmdBus, env) error`. Task 10 tests these exact names.

- [ ] **Step 1: Write the failing unit test for the joining UoW**

Create `examples/orders/workerflow/inboxflow_test.go`:

```go
package workerflow_test

import (
	"context"
	"testing"

	"github.com/slam0504/go-ddd-core/ports/database"

	pgxdb "github.com/slam0504/go-ddd-adapters/ports/database/pgx"
	"github.com/slam0504/go-ddd-adapters/examples/orders/workerflow"
)

// fakeTxManager records whether WithinTx was invoked — the joining UoW must
// NOT open a new transaction when ctx already carries one.
type fakeTxManager struct{ opened bool }

func (f *fakeTxManager) WithinTx(ctx context.Context, fn database.TxFunc) error {
	f.opened = true
	return fn(ctx)
}

func TestJoiningUnitOfWork_JoinsAmbientTx(t *testing.T) {
	f := &fakeTxManager{}
	uow := workerflow.JoiningUnitOfWork(f)
	// nil pgx.Tx is fine for the marker — TxFromContext only checks presence.
	ctx := pgxdb.WithTx(context.Background(), nil)
	ran := false
	if err := uow.Do(ctx, func(context.Context) error { ran = true; return nil }); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if !ran {
		t.Fatal("fn did not run")
	}
	if f.opened {
		t.Fatal("WithinTx was called despite ambient tx — joining broken (would nest a second transaction)")
	}
}

func TestJoiningUnitOfWork_OpensTxWhenNoAmbient(t *testing.T) {
	f := &fakeTxManager{}
	uow := workerflow.JoiningUnitOfWork(f)
	if err := uow.Do(context.Background(), func(context.Context) error { return nil }); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if !f.opened {
		t.Fatal("WithinTx not called with no ambient tx")
	}
}
```

NOTE: if `pgxdb.WithTx(ctx, nil)` panics or `TxFromContext` treats a nil tx as absent, use a real `pgx.Tx` from a test double or restructure the marker check — verify against `ports/database/pgx/context.go` first and adjust; the intent (ambient → join, no ambient → open) must hold.

- [ ] **Step 2: Run to verify failure**

Run: `cd examples/orders && go test ./workerflow/... -run TestJoining -v`
Expected: FAIL to build — `JoiningUnitOfWork` undefined.

- [ ] **Step 3: Implement `inboxflow.go`**

```go
package workerflow

import (
	"context"
	"errors"

	"github.com/slam0504/go-ddd-core/application"
	"github.com/slam0504/go-ddd-core/application/command"
	"github.com/slam0504/go-ddd-core/eventbus"
	"github.com/slam0504/go-ddd-core/ports/database"
	"github.com/slam0504/go-ddd-core/ports/logger"

	pgxdb "github.com/slam0504/go-ddd-adapters/ports/database/pgx"
)

// ConsumerName is the inbox consumer identity for the orders worker. One
// stable name per logical consumer — see eventbus.InboxKey.
const ConsumerName = "orders-worker"

// JoiningUnitOfWork adapts txMgr into an application.UnitOfWork that JOINS
// an ambient pgx transaction when ctx carries one (pgxdb.TxFromContext) and
// only opens a new transaction otherwise. The worker wraps each message in
// one outer transaction; ShipOrderHandler's internal uow.Do then joins that
// transaction instead of nesting a second one — pgxdb.TxManager.WithinTx
// always calls pool.BeginTx and never joins by itself (confirmed constraint,
// spec §5.1). Core is deliberately untouched.
func JoiningUnitOfWork(txMgr database.TxManager) application.UnitOfWork {
	return application.UnitOfWorkFunc(func(ctx context.Context, fn func(ctx context.Context) error) error {
		if _, ok := pgxdb.TxFromContext(ctx); ok {
			return fn(ctx)
		}
		return txMgr.WithinTx(ctx, database.TxFunc(fn))
	})
}

// HandleOrderPlacedOnce wraps HandleOrderPlaced in ONE per-message
// transaction with durable inbox dedup:
//
//	Seen (true → ack, skip handler)
//	  → HandleOrderPlaced (ship side effects + outbox.Stage join via
//	    JoiningUnitOfWork)
//	  → Record (duplicate → ErrAlreadyRecorded → whole tx rolls back)
//
// A rolled-back duplicate and a Seen fast-path both return nil so the
// broker acks: the event was already fully processed once.
func HandleOrderPlacedOnce(
	ctx context.Context,
	log logger.Logger,
	txMgr database.TxManager,
	ibx eventbus.Inbox,
	cmdBus *command.InMemoryBus,
	env eventbus.Envelope,
) error {
	key := eventbus.InboxKey{Consumer: ConsumerName, EventID: env.Event.EventID()}
	var skipped bool
	err := txMgr.WithinTx(ctx, func(txCtx context.Context) error {
		seen, err := ibx.Seen(txCtx, key)
		if err != nil {
			return err
		}
		if seen {
			skipped = true
			return nil
		}
		if err := HandleOrderPlaced(txCtx, log, cmdBus, env); err != nil {
			return err
		}
		return ibx.Record(txCtx, key)
	})
	if err != nil && errors.Is(err, eventbus.ErrAlreadyRecorded) {
		log.Info("duplicate delivery acked", logger.F("event_id", key.EventID))
		return nil
	}
	if err == nil && skipped {
		log.Info("already-seen delivery acked", logger.F("event_id", key.EventID))
	}
	return err
}
```

If `env.Event`'s static type does not expose `EventID()` directly, extract it the way `HandleOrderPlaced` accesses the event (check `eventbus.Envelope`'s field type in core) — the key MUST be the domain event id per `InboxKey` godoc.

- [ ] **Step 4: Wire the worker**

In `examples/orders/cmd/worker/main.go`:
- Replace `uow := application.UnitOfWorkFromTxManager(txMgr)` with `uow := workerflow.JoiningUnitOfWork(txMgr)`.
- After the pool is built: `ibx, err := pgxinbox.New(pool)` (handle err; add import `pgxinbox "github.com/slam0504/go-ddd-adapters/eventbus/inbox/pgx"`).
- Replace the handle closure body with `return workerflow.HandleOrderPlacedOnce(hctx, log, txMgr, ibx, cmdBus, env)` and update its comment to mention inbox dedup.
- Drop the now-unused `application` import if nothing else uses it.

- [ ] **Step 5: docker-compose migration**

In `examples/orders/docker-compose.yml`: add an `inbox-migrate` service mirroring `outbox-migrate` byte-for-byte except: service name, the volume mount pointing at `../../eventbus/inbox/pgx/migrations`, and `x-migrations-table=inbox_schema_migrations` in its command. Add `inbox-migrate: {condition: service_completed_successfully}` to the worker's `depends_on` (matching the existing `orders-migrate` entry style).

- [ ] **Step 6: Run green + commit**

Run: `cd examples/orders && go build ./... && go vet ./... && go test ./workerflow/... -race -v` → PASS.

```bash
git add examples/orders/workerflow/inboxflow.go examples/orders/workerflow/inboxflow_test.go examples/orders/cmd/worker/main.go examples/orders/docker-compose.yml
git commit -m "feat(examples/orders): exactly-once worker via joining UoW + pgx inbox

One per-message tx: Seen -> ship+Stage (handler uow JOINS the outer tx;
pgxdb WithinTx never joins by itself) -> Record; ErrAlreadyRecorded
rolls back and acks. inbox-migrate compose service added.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

### Task 10: Duplicate-delivery integration test + README

**Files:**
- Modify: `examples/orders/integration/main_test.go` (apply inbox migrations in harness; extend `truncate` to include `inbox_records`)
- Create: `examples/orders/integration/inbox_dedup_test.go` (`//go:build integration`)
- Modify: `examples/orders/README.md` (shortcut rewrite)

**Interfaces:**
- Consumes: Task 9's `HandleOrderPlacedOnce` / `JoiningUnitOfWork` / `ConsumerName`; existing harness (`truncate`, `seedPlacedOrder`, `sharedPool`, `testLogger`, envelope pattern from `TestWorker_HandleOrderPlaced_PropagatesHeaders` at `ship_order_test.go:285`).
- Produces: end-to-end exactly-once proof; README accuracy.

- [ ] **Step 1: Harness — migrations + truncate**

In `main_test.go`: where outbox/orders migrations are applied, also apply `pgxinbox.Migrations` with its own migrations table (mirror the existing per-source pattern). Extend the `truncate` helper's table list with `inbox_records`.

- [ ] **Step 2: Write the test**

Create `examples/orders/integration/inbox_dedup_test.go` (imports mirror `ship_order_test.go` plus `pgxinbox` + `workerflow`):

```go
// TestWorker_DuplicateDelivery_ExactlyOnce proves the wired guarantee:
// delivering the same OrderPlaced envelope twice ships the order once,
// stages exactly one OrderShipped outbox row, and records exactly one
// inbox key — the duplicate is acked without side effects.
func TestWorker_DuplicateDelivery_ExactlyOnce(t *testing.T) {
	truncate(t)
	const (
		orderID         = "ord-inbox-dedup"
		consumedEventID = "evt-placed-dup"
	)
	seedPlacedOrder(t, orderID, "dana",
		[]orderdom.Item{{SKU: "D", Quantity: 1, PriceCents: 100}}, 100)

	codec := eventcodec.New()
	txMgr := pgxdb.NewTxManager(sharedPool)
	uow := workerflow.JoiningUnitOfWork(txMgr)
	repo := pgxrepo.New(sharedPool)
	store, err := pgxoutbox.NewStore(pgxoutbox.Config{Pool: sharedPool, Codec: codec})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	ibx, err := pgxinbox.New(sharedPool)
	if err != nil {
		t.Fatalf("pgxinbox: %v", err)
	}

	bus := command.NewInMemoryBus()
	topic := "orders.shipped.inbox-dedup.test-" + uuid.NewString()
	command.Register[apporder.ShipOrderCommand, apporder.ShipOrderResult](
		bus,
		apporder.NewShipOrderHandler(uow, repo, store, topic, uuid.NewString),
	)

	placed := orderdom.NewOrderPlaced(consumedEventID, orderID, 1, "dana", 100)
	env := eventbus.Envelope{Name: orderdom.EventNamePlaced, Event: &placed}

	ctx := context.Background()
	if err := workerflow.HandleOrderPlacedOnce(ctx, testLogger(), txMgr, ibx, bus, env); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	if err := workerflow.HandleOrderPlacedOnce(ctx, testLogger(), txMgr, ibx, bus, env); err != nil {
		t.Fatalf("duplicate delivery: want nil (ack), got %v", err)
	}

	var outboxRows int
	if err := sharedPool.QueryRow(ctx,
		`SELECT count(*) FROM outbox_records WHERE aggregate_id = $1`, orderID,
	).Scan(&outboxRows); err != nil {
		t.Fatalf("outbox count: %v", err)
	}
	if outboxRows != 1 {
		t.Fatalf("outbox rows = %d, want exactly 1 (duplicate must not re-stage)", outboxRows)
	}

	var inboxRows int
	if err := sharedPool.QueryRow(ctx,
		`SELECT count(*) FROM inbox_records WHERE consumer = $1 AND event_id = $2`,
		workerflow.ConsumerName, consumedEventID,
	).Scan(&inboxRows); err != nil {
		t.Fatalf("inbox count: %v", err)
	}
	if inboxRows != 1 {
		t.Fatalf("inbox rows = %d, want exactly 1", inboxRows)
	}
}
```

If the second delivery would fail inside the ship handler BEFORE the Seen fast-path could matter (e.g. optimistic-lock conflict on the already-shipped aggregate), that would mask the inbox guard — the Seen fast-path runs FIRST inside the tx, so the handler must never execute on the duplicate. If the test fails with a ship/version error instead of passing, the Seen ordering in `HandleOrderPlacedOnce` is broken — fix Task 9, don't bend the test.

- [ ] **Step 3: Run + README + commit**

Run: `cd examples/orders && go test -tags=integration -race ./integration/... -run 'TestWorker_DuplicateDelivery|TestWorker_HandleOrderPlaced' -v` → PASS (new test + pre-existing header test both green; the full integration suite runs in CI).

README: delete the "no durable Inbox / consumer-side dedup" shortcut bullet; add under remaining limitations: reader projection still in-memory; `inbox_records` grows without bound (retention deferred; `recorded_at` reserves the cleanup path).

```bash
git add examples/orders/integration/ examples/orders/README.md
git commit -m "test(examples/orders): duplicate delivery ships exactly once; README shortcut closed

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

## Task 11: Release — four-step gate

**Files:** `CHANGELOG.md`, `README.md` (flip + compat row, in the dep-bump PR); `.agent/*.md` + `<workspace-root>/.agent-memory/go-ddd.md` (post-tag, direct on main).

- [ ] **Step 1**: Whole-branch final review (most capable model) on `feat/inbox-pgx` (merge-base..HEAD) AND a review pass over the core branch's merged diff. Fix findings; re-review until READY.
- [ ] **Step 2**: Push + adapters impl PR (title `feat(eventbus/inbox/pgx): durable Postgres inbox — matured Inbox contract (v0.13.0)`; body links the spec on this branch; note the pseudo-pin + BREAKING Memory change). CI 5/5 green → merge → delete branch.
- [ ] **Step 3**: Core: release-prep (CHANGELOG flip `[v0.13.0]`) → annotated tag `v0.13.0` → GitHub Release Latest → verify proxy (`go list -m go-ddd-core@v0.13.0`).
- [ ] **Step 4**: Adapters dep-bump PR `chore/bump-core-v0.13.0`: pseudo → `v0.13.0` (root + examples/orders + tidy); CHANGELOG `[Unreleased]` → `[v0.13.0] - <date>` + compare links; README Status promotion/demotion (v0.13.0 becomes "latest tagged release", v0.12.0 demoted to "adds the …" — follow the v0.11.0→v0.12.0 precedent EXACTLY) + compat matrix row `v0.13.0 ↔ v0.13.0`. CI green → merge.
- [ ] **Step 5**: Adapters annotated tag `v0.13.0` at the dep-bump merge + GitHub Release Latest (notes from CHANGELOG section); verify `releases/latest` + proxy.
- [ ] **Step 6**: Bookkeeping direct on main: `.agent/state.md` (new cycle section), `.agent/decisions.md` (duplicate-Record-is-error rationale; joining-UoW constraint + solution; plain-INSERT-not-ON-CONFLICT), `go-ddd.md` (durable-inbox open item closed). `.agent` add and commit as SEPARATE Bash calls. Ledger close.

---

## Verification strategy (spec §6)

- Phase A: 9 inboxtest subtests green vs reference impl; 3 red-proof breakages recorded with failing-subtest names.
- Phase B: RunContract green vs `Memory` (unit) AND vs real Postgres (integration, locally + CI); race test proves blocking-INSERT guard under `-race`; both lint invocations 0 issues.
- Phase C: joining-UoW unit tests (ambient join / no-ambient open); duplicate-delivery integration test (1 outbox row, 1 inbox row, duplicate acked); full pre-existing examples integration suite stays green in CI.
- Gate: core `v0.13.0` + adapters `v0.13.0` both resolve via proxy; `releases/latest` returns `v0.13.0`.
- Not locally verifiable: none expected (Docker available); if Docker is down, integration legs defer to CI and every report must say so.
