# storage/minio — ObjectStorage Adapter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Mature core `ports/storage` (BREAKING token-paginated `List`, `ObjectInfo.Metadata`, pinned semantics, `storagetest.RunContract`) and ship `storage/minio` (`miniostorage`), the first ObjectStorage adapter, through the full four-step tag gate.

**Architecture:** Phase A changes the core contract first (zero consumers = cheapest BREAKING moment) and proves it with a conformance suite + in-memory reference impl. Phase B implements the adapter over minio-go's ctx-aware public API with an adapter-owned `StartAfter` list cursor, proven by running the same suite against a real MinIO container in CI.

**Tech Stack:** Go, `github.com/minio/minio-go/v7 v7.2.1`, `github.com/testcontainers/testcontainers-go/modules/minio v0.42.0`, core `pkg/errorsx`.

**Spec:** `docs/superpowers/specs/2026-07-03-storage-minio-adapter-design.md` (all semantics pinned there; §8 decisions log is binding).

## Global Constraints

- Core repo: `/Users/eason_tseng/playground/project/go-ddd-core` (module `github.com/slam0504/go-ddd-core`, go 1.26). Adapters repo: `/Users/eason_tseng/playground/project/go-ddd-adapters` (module `github.com/slam0504/go-ddd-adapters`, go 1.25.0 — do NOT bump in impl tasks; see Task 9 for the go-1.26 interaction).
- Core Phase A branch `feat/storage-maturation` MUST be cut from core `main` (`666a4bb`), NOT from the in-flight `chore/bump-go-1.26` branch (open core PR #38).
- Adapters work continues on the existing branch `feat/storage-minio` (spec already committed there).
- `storage.ErrNotFound` is an UNCODED domain sentinel (`errors.Is` only; `CodeOf` may be `CodeUnknown`). Every OTHER backend error must be errorsx-coded, never `CodeUnknown`.
- Validation precedence everywhere: malformed argument (`CodeInvalidArgument`) BEFORE ctx BEFORE backend.
- Portable metadata keys are lowercase ASCII; adapters canonicalize returned keys to lowercase.
- `List` ordering: backend-defined; suite compares SETS, never order. Pages never exceed `Limit`.
- Adapter list token: adapter-owned `StartAfter` key cursor + `Limit+1` lookahead — NEVER `minio.Core.ListObjectsV2` (no ctx parameter; violates ctx responsibility).
- errorsx API: `errorsx.New(code, msg)`, `errorsx.Wrap(code, msg, cause)`, `errorsx.CodeOf(err)`.
- Lint with the v2 binary `/usr/local/bin/golangci-lint`; `go-ddd-adapters/...` imports form their own goimports group (core is third-party).
- Conventional commits. `.agent/*.md` commits need `git add` and `git commit` as separate Bash calls.

---

## Phase A — core `ports/storage` maturation

### Task 1: Matured contract — `storage.go` rewrite + core CHANGELOG

**Files:**
- Modify: `/Users/eason_tseng/playground/project/go-ddd-core/ports/storage/storage.go` (full replacement below)
- Modify: `/Users/eason_tseng/playground/project/go-ddd-core/CHANGELOG.md` (`[Unreleased]` section)

**Interfaces:**
- Consumes: `pkg/errorsx` codes (referenced in godoc only — no import needed in the contract file).
- Produces: `storage.ObjectStorage` (5 methods, `List(ctx, ListOptions) (ListPage, error)`), `storage.ListOptions{Prefix, Token string; Limit int}`, `storage.ListPage{Objects []ObjectInfo; NextToken string}`, `storage.ObjectInfo{Key, Size, ContentType, ETag, UpdatedAt, Metadata}`, `storage.PutOptions{ContentType, Metadata}`, `storage.PresignedURLer`, `storage.ErrNotFound`. Tasks 2, 5–7 depend on these exact names.

- [ ] **Step 1: Create the branch from core `main`**

```bash
cd /Users/eason_tseng/playground/project/go-ddd-core
git checkout main && git pull
git checkout -b feat/storage-maturation
```

- [ ] **Step 2: Replace `ports/storage/storage.go` with the matured contract**

```go
// Package storage defines the object storage contract for blob data (S3,
// GCS, MinIO, local filesystem, etc.).
//
// # Validation precedence
//
// On every method, a malformed argument — an empty key, or List with
// Limit <= 0 — is rejected with an errorsx.CodeInvalidArgument error BEFORE
// the context is consulted and BEFORE the backend is contacted. A cancelled
// or expired context is reported next; only then do backend errors surface.
//
// # Sentinel vs coded errors
//
// A missing object on Get or Stat is reported as an error wrapping
// ErrNotFound. ErrNotFound is a domain sentinel, never a coded error:
// errorsx.CodeOf on such an error is NOT guaranteed to be CodeNotFound (it
// may be CodeUnknown). Callers detect missing objects with errors.Is, never
// with CodeOf. Any OTHER non-nil error (backend, configuration, transport)
// MUST carry an errorsx code and is never CodeUnknown.
package storage

import (
	"context"
	"errors"
	"io"
	"time"
)

// ObjectInfo describes a stored object.
//
// ETag is an opaque backend validator: it may be empty (some backends have
// none), and when non-empty the contract promises no format — it is not
// required to be a content hash, nor to change on overwrite. Metadata holds
// user metadata with keys canonicalized to lowercase ASCII by the adapter;
// portable callers use lowercase ASCII keys only (see PutOptions). Metadata
// is populated by Get and Stat; List implementations are not required to
// populate it.
type ObjectInfo struct {
	Key         string
	Size        int64
	ContentType string
	ETag        string
	UpdatedAt   time.Time
	Metadata    map[string]string
}

// PutOptions configures a Put operation.
//
// ContentType left empty lets the adapter or backend apply a default (for
// example application/octet-stream); empty-string round-trip is not
// promised — ObjectInfo.ContentType reports the backend's effective value.
// Portable Metadata keys are lowercase ASCII: S3-class backends
// canonicalize metadata keys, so mixed-case keys are not guaranteed to
// round-trip byte-for-byte.
type PutOptions struct {
	ContentType string
	Metadata    map[string]string
}

// ListOptions configures a List call.
//
// Token is opaque and adapter-owned: callers must not parse or construct
// it — only pass back the previous page's NextToken (or "" for the first
// page). Limit is the maximum page size and must be positive: Limit <= 0
// is rejected with CodeInvalidArgument (there is no unbounded default).
// Backends may cap the effective page size below Limit; adapters document
// their cap.
type ListOptions struct {
	Prefix string
	Token  string
	Limit  int
}

// ListPage is one page of a List walk.
//
// NextToken == "" is the ONLY termination signal: a page may hold fewer
// than Limit objects while NextToken is non-empty, and callers must not
// treat a short page as terminal.
type ListPage struct {
	Objects   []ObjectInfo
	NextToken string
}

// ObjectStorage stores and retrieves opaque byte streams keyed by string.
type ObjectStorage interface {
	// Put stores body under key, unconditionally overwriting any existing
	// object (last-writer-wins). size is the exact byte length of body and
	// must be >= 0: a negative size is rejected with CodeInvalidArgument
	// (unknown-length streaming is not part of this contract — callers
	// buffer first). The declared size is handed to the backend as-is; if
	// the backend or the reader reports a mismatch the implementation
	// surfaces that error, and it must never intentionally truncate or pad
	// silently. Implementations are not required to pre-validate the
	// reader's length.
	Put(ctx context.Context, key string, body io.Reader, size int64, opts PutOptions) error

	// Get returns the object's content and its ObjectInfo. The caller owns
	// the returned io.ReadCloser and MUST close it. The returned ObjectInfo
	// is consistent with what Stat reports for the same object. A missing
	// key returns an error wrapping ErrNotFound.
	Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error)

	// Stat returns the object's ObjectInfo without its content. A missing
	// key returns an error wrapping ErrNotFound.
	Stat(ctx context.Context, key string) (ObjectInfo, error)

	// Delete removes the object. Delete is idempotent: deleting an absent
	// key succeeds and never returns ErrNotFound.
	Delete(ctx context.Context, key string) error

	// List returns one page of objects whose keys start with opts.Prefix
	// (an empty Prefix lists everything). Ordering is backend-defined and
	// not promised globally; the only requirement is a stable walk: under
	// no concurrent mutation, walking pages via NextToken until it is ""
	// yields every matching object exactly once. An invalid Token returns
	// a coded error, never CodeUnknown: a malformed adapter-owned token
	// may be CodeInvalidArgument, while a backend-rejected token surfaces
	// as a backend-class coded error.
	List(ctx context.Context, opts ListOptions) (ListPage, error)
}

// PresignedURLer is an optional capability for adapters that support
// pre-signed URLs (S3-compatible services, GCS, etc.). expires must be
// positive: expires <= 0 is rejected with CodeInvalidArgument. A presigned
// URL grants unauthenticated access until it expires — callers treat it as
// a secret.
type PresignedURLer interface {
	PresignGet(ctx context.Context, key string, expires time.Duration) (string, error)
	PresignPut(ctx context.Context, key string, expires time.Duration) (string, error)
}

// ErrNotFound is returned (wrapped) by Get and Stat when a key does not
// exist. It is a domain sentinel: match with errors.Is. It is deliberately
// NOT an errorsx-coded error — see the package doc.
var ErrNotFound = errors.New("storage: object not found")
```

- [ ] **Step 3: Compile-gate**

Run: `cd /Users/eason_tseng/playground/project/go-ddd-core && go build ./... && go vet ./...`
Expected: clean (nothing else in core imports `ports/storage` — verify with `grep -rn "ports/storage" --include="*.go" . | grep -v ports/storage/`, expected no hits outside the package).

- [ ] **Step 4: CHANGELOG entry under `[Unreleased]`**

Add under the existing `## [Unreleased]` heading (keep the go-1.26 entry if PR #38 merged first; otherwise this is the first entry):

```markdown
### Changed

- **BREAKING** `ports/storage`: `ObjectStorage.List` is now token-paginated
  — `List(ctx, ListOptions{Prefix, Token, Limit}) (ListPage, error)` with an
  adapter-owned opaque `NextToken` (`""` = walk complete; `Limit <= 0` is
  `CodeInvalidArgument`). `ObjectInfo` gains `Metadata map[string]string`
  (lowercase-ASCII portable keys). Operation semantics pinned in godoc:
  unconditional overwrite Put (negative size rejected), idempotent Delete,
  caller-closes Get, `ErrNotFound` as uncoded domain sentinel (errors.Is,
  never CodeOf), empty-key/limit validation precedence before ctx before
  backend, ETag demoted to opaque validator.

### Added

- `ports/storage/storagetest.RunContract`: deterministic conformance suite
  for `ObjectStorage` implementations, with an in-memory reference
  implementation. Presign is deliberately out of suite scope (proven
  adapter-side against real backends).
```

- [ ] **Step 5: Commit**

```bash
cd /Users/eason_tseng/playground/project/go-ddd-core
git add ports/storage/storage.go CHANGELOG.md
git commit -m "feat(storage)!: token-paginated List, ObjectInfo.Metadata, pinned contract semantics"
```

### Task 2: `storagetest` conformance suite + reference impl

**Files:**
- Create: `/Users/eason_tseng/playground/project/go-ddd-core/ports/storage/storagetest/storagetest.go`
- Create: `/Users/eason_tseng/playground/project/go-ddd-core/ports/storage/storagetest/reference.go`
- Create: `/Users/eason_tseng/playground/project/go-ddd-core/ports/storage/storagetest/reference_test.go`

**Interfaces:**
- Consumes: Task 1's `storage.*` types verbatim; `pkg/errorsx.CodeOf`/`CodeInvalidArgument`.
- Produces: `storagetest.Factory func(t *testing.T) storage.ObjectStorage` and `storagetest.RunContract(t *testing.T, newStorage Factory)`. Task 7's integration harness calls exactly these.

- [ ] **Step 1: Write the reference in-memory implementation (`reference.go`)**

```go
package storagetest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/slam0504/go-ddd-core/pkg/errorsx"
	"github.com/slam0504/go-ddd-core/ports/storage"
)

// memStorage is the test-only reference implementation the suite is proven
// against. It exists so RunContract is demonstrably satisfiable and
// non-vacuous; it is not exported for production use.
type memStorage struct {
	mu      sync.Mutex
	objects map[string]memObject
}

type memObject struct {
	data        []byte
	contentType string
	metadata    map[string]string
	updatedAt   time.Time
}

func newMemStorage() *memStorage {
	return &memStorage{objects: map[string]memObject{}}
}

func errEmptyKey() error {
	return errorsx.New(errorsx.CodeInvalidArgument, "storagetest: empty key")
}

func (m *memStorage) Put(ctx context.Context, key string, body io.Reader, size int64, opts storage.PutOptions) error {
	if key == "" {
		return errEmptyKey()
	}
	if size < 0 {
		return errorsx.New(errorsx.CodeInvalidArgument, "storagetest: negative size")
	}
	if err := ctx.Err(); err != nil {
		return errorsx.Wrap(errorsx.CodeUnavailable, "storagetest: context done", err)
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return errorsx.Wrap(errorsx.CodeUnavailable, "storagetest: read body", err)
	}
	if int64(len(data)) != size {
		return errorsx.New(errorsx.CodeInvalidArgument,
			fmt.Sprintf("storagetest: declared size %d != actual %d", size, len(data)))
	}
	ct := opts.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = memObject{
		data:        data,
		contentType: ct,
		metadata:    maps.Clone(opts.Metadata),
		updatedAt:   time.Now(),
	}
	return nil
}

func (m *memStorage) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error) {
	info, err := m.Stat(ctx, key)
	if err != nil {
		return nil, storage.ObjectInfo{}, err
	}
	m.mu.Lock()
	data := bytes.Clone(m.objects[key].data)
	m.mu.Unlock()
	return io.NopCloser(bytes.NewReader(data)), info, nil
}

func (m *memStorage) Stat(ctx context.Context, key string) (storage.ObjectInfo, error) {
	if key == "" {
		return storage.ObjectInfo{}, errEmptyKey()
	}
	if err := ctx.Err(); err != nil {
		return storage.ObjectInfo{}, errorsx.Wrap(errorsx.CodeUnavailable, "storagetest: context done", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	obj, ok := m.objects[key]
	if !ok {
		return storage.ObjectInfo{}, fmt.Errorf("storagetest: %q: %w", key, storage.ErrNotFound)
	}
	return storage.ObjectInfo{
		Key:         key,
		Size:        int64(len(obj.data)),
		ContentType: obj.contentType,
		ETag:        "", // empty ETag is contractually allowed
		UpdatedAt:   obj.updatedAt,
		Metadata:    maps.Clone(obj.metadata),
	}, nil
}

func (m *memStorage) Delete(ctx context.Context, key string) error {
	if key == "" {
		return errEmptyKey()
	}
	if err := ctx.Err(); err != nil {
		return errorsx.Wrap(errorsx.CodeUnavailable, "storagetest: context done", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key) // idempotent: absent key is a no-op success
	return nil
}

func (m *memStorage) List(ctx context.Context, opts storage.ListOptions) (storage.ListPage, error) {
	if opts.Limit <= 0 {
		return storage.ListPage{}, errorsx.New(errorsx.CodeInvalidArgument, "storagetest: non-positive limit")
	}
	if err := ctx.Err(); err != nil {
		return storage.ListPage{}, errorsx.Wrap(errorsx.CodeUnavailable, "storagetest: context done", err)
	}
	m.mu.Lock()
	keys := make([]string, 0, len(m.objects))
	for k := range m.objects {
		if strings.HasPrefix(k, opts.Prefix) {
			keys = append(keys, k)
		}
	}
	m.mu.Unlock()
	sort.Strings(keys) // lexical order = this backend's stable walk order

	page := storage.ListPage{}
	for _, k := range keys {
		if opts.Token != "" && k <= opts.Token {
			continue // resume strictly after the cursor
		}
		if len(page.Objects) == opts.Limit {
			page.NextToken = page.Objects[opts.Limit-1].Key
			return page, nil
		}
		info, err := m.Stat(ctx, k)
		if err != nil {
			return storage.ListPage{}, err
		}
		info.Metadata = nil // List is not required to populate Metadata
		page.Objects = append(page.Objects, info)
	}
	return page, nil
}
```

- [ ] **Step 2: Write the suite (`storagetest.go`)**

```go
// Package storagetest is the exported conformance suite for
// ports/storage.ObjectStorage. Any adapter runs RunContract against its own
// implementation to prove it honours the object-storage contract. The suite
// is deterministic: it asserts invariants visible through the interface,
// compares List results as SETS (ordering is backend-defined), and never
// asserts ETag behaviour beyond "empty allowed". Missing-object assertions
// use errors.Is(err, storage.ErrNotFound), deliberately NEVER
// errorsx.CodeOf (ErrNotFound is an uncoded domain sentinel). Imports only
// testing + the contract + errorsx so adapters inherit no transport
// dependency. PresignedURLer is deliberately out of scope: an in-memory
// reference cannot sign meaningful URLs; adapters prove presign against a
// real backend.
package storagetest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/slam0504/go-ddd-core/pkg/errorsx"
	"github.com/slam0504/go-ddd-core/ports/storage"
)

// Factory returns a fresh, keyspace-isolated storage.ObjectStorage for one
// subtest. It receives the subtest's *testing.T so an adapter can key
// isolation on t.Name() and register t.Cleanup (matches cachetest /
// inboxtest). Each call MUST return an implementation whose keyspace does
// not overlap any other call's (e.g. a new in-memory map, or a fresh
// bucket per call).
type Factory func(t *testing.T) storage.ObjectStorage

// RunContract runs the full deterministic ObjectStorage conformance suite.
func RunContract(t *testing.T, newStorage Factory) {
	t.Helper()
	ctx := context.Background()

	put := func(t *testing.T, s storage.ObjectStorage, key, body string, opts storage.PutOptions) {
		t.Helper()
		if err := s.Put(ctx, key, strings.NewReader(body), int64(len(body)), opts); err != nil {
			t.Fatalf("Put(%q): %v", key, err)
		}
	}

	t.Run("PutGetRoundtrip", func(t *testing.T) {
		s := newStorage(t)
		put(t, s, "k", "hello", storage.PutOptions{ContentType: "text/plain"})
		rc, info, err := s.Get(ctx, "k")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		defer rc.Close()
		got, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		if !bytes.Equal(got, []byte("hello")) {
			t.Fatalf("body = %q, want %q", got, "hello")
		}
		if info.Key != "k" || info.Size != 5 || info.ContentType != "text/plain" {
			t.Fatalf("info = %+v, want Key=k Size=5 ContentType=text/plain", info)
		}
	})

	t.Run("PutOverwriteWins", func(t *testing.T) {
		s := newStorage(t)
		put(t, s, "k", "first", storage.PutOptions{ContentType: "text/plain"})
		put(t, s, "k", "second-longer", storage.PutOptions{ContentType: "application/json"})
		rc, info, err := s.Get(ctx, "k")
		if err != nil {
			t.Fatalf("Get after overwrite: %v", err)
		}
		defer rc.Close()
		got, _ := io.ReadAll(rc)
		if string(got) != "second-longer" {
			t.Fatalf("body = %q, want the second write (last-writer-wins)", got)
		}
		if info.Size != int64(len("second-longer")) || info.ContentType != "application/json" {
			t.Fatalf("info = %+v: overwrite must fully replace size and content type", info)
		}
		// Deliberately NO ETag assertion: ETag is an opaque validator.
	})

	t.Run("PutNegativeSizeInvalidArgument", func(t *testing.T) {
		s := newStorage(t)
		err := s.Put(ctx, "k", strings.NewReader("x"), -1, storage.PutOptions{})
		if errorsx.CodeOf(err) != errorsx.CodeInvalidArgument {
			t.Fatalf("Put(size=-1) code = %v, want CodeInvalidArgument", errorsx.CodeOf(err))
		}
	})

	t.Run("MetadataRoundtripLowercaseKeys", func(t *testing.T) {
		s := newStorage(t)
		md := map[string]string{"request-id": "r-42", "owner": "orders"}
		put(t, s, "k", "v", storage.PutOptions{ContentType: "text/plain", Metadata: md})
		info, err := s.Stat(ctx, "k")
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		for wantK, wantV := range md {
			if info.Metadata[wantK] != wantV {
				t.Fatalf("Stat Metadata[%q] = %q, want %q (full: %v)", wantK, info.Metadata[wantK], wantV, info.Metadata)
			}
		}
		rc, ginfo, err := s.Get(ctx, "k")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		rc.Close()
		for wantK, wantV := range md {
			if ginfo.Metadata[wantK] != wantV {
				t.Fatalf("Get Metadata[%q] = %q, want %q", wantK, ginfo.Metadata[wantK], wantV)
			}
		}
	})

	t.Run("GetMissingReturnsErrNotFound", func(t *testing.T) {
		s := newStorage(t)
		_, _, err := s.Get(ctx, "absent")
		if !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("Get(absent) err = %v, want errors.Is(_, storage.ErrNotFound)", err)
		}
		// Deliberately NO CodeOf assertion: ErrNotFound is an uncoded sentinel.
	})

	t.Run("StatMissingReturnsErrNotFound", func(t *testing.T) {
		s := newStorage(t)
		_, err := s.Stat(ctx, "absent")
		if !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("Stat(absent) err = %v, want errors.Is(_, storage.ErrNotFound)", err)
		}
	})

	t.Run("DeleteIsIdempotent", func(t *testing.T) {
		s := newStorage(t)
		if err := s.Delete(ctx, "absent"); err != nil {
			t.Fatalf("Delete(absent) = %v, want nil (idempotent delete)", err)
		}
		put(t, s, "k", "v", storage.PutOptions{})
		if err := s.Delete(ctx, "k"); err != nil {
			t.Fatalf("Delete(existing): %v", err)
		}
		if _, err := s.Stat(ctx, "k"); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("Stat after Delete err = %v, want ErrNotFound", err)
		}
		if err := s.Delete(ctx, "k"); err != nil {
			t.Fatalf("second Delete = %v, want nil", err)
		}
	})

	t.Run("EmptyKeyPrecedesCancelledCtx", func(t *testing.T) {
		s := newStorage(t)
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		if code := errorsx.CodeOf(s.Put(cancelled, "", strings.NewReader(""), 0, storage.PutOptions{})); code != errorsx.CodeInvalidArgument {
			t.Fatalf("Put empty key code = %v, want CodeInvalidArgument before ctx", code)
		}
		_, _, err := s.Get(cancelled, "")
		if code := errorsx.CodeOf(err); code != errorsx.CodeInvalidArgument {
			t.Fatalf("Get empty key code = %v, want CodeInvalidArgument before ctx", code)
		}
		_, serr := s.Stat(cancelled, "")
		if code := errorsx.CodeOf(serr); code != errorsx.CodeInvalidArgument {
			t.Fatalf("Stat empty key code = %v, want CodeInvalidArgument before ctx", code)
		}
		if code := errorsx.CodeOf(s.Delete(cancelled, "")); code != errorsx.CodeInvalidArgument {
			t.Fatalf("Delete empty key code = %v, want CodeInvalidArgument before ctx", code)
		}
	})

	t.Run("ListLimitNonPositivePrecedesCancelledCtx", func(t *testing.T) {
		s := newStorage(t)
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		for _, limit := range []int{0, -1} {
			_, err := s.List(cancelled, storage.ListOptions{Limit: limit})
			if code := errorsx.CodeOf(err); code != errorsx.CodeInvalidArgument {
				t.Fatalf("List(Limit=%d) code = %v, want CodeInvalidArgument before ctx", limit, code)
			}
		}
	})

	t.Run("ListTokenWalkCompleteNoDupes", func(t *testing.T) {
		s := newStorage(t)
		want := map[string]bool{}
		for _, k := range []string{"w/a", "w/b", "w/c", "w/d", "w/e", "w/f", "w/g"} {
			put(t, s, k, "v", storage.PutOptions{})
			want[k] = true
		}
		got := map[string]bool{}
		token := ""
		for pages := 0; ; pages++ {
			if pages > 20 {
				t.Fatal("walk did not terminate within 20 pages")
			}
			page, err := s.List(ctx, storage.ListOptions{Prefix: "w/", Token: token, Limit: 3})
			if err != nil {
				t.Fatalf("List page %d: %v", pages, err)
			}
			if len(page.Objects) > 3 {
				t.Fatalf("page %d has %d objects, exceeds Limit 3", pages, len(page.Objects))
			}
			for _, o := range page.Objects {
				if got[o.Key] {
					t.Fatalf("duplicate key %q in walk", o.Key)
				}
				got[o.Key] = true
			}
			if page.NextToken == "" {
				break
			}
			token = page.NextToken
		}
		if len(got) != len(want) {
			t.Fatalf("walk yielded %d keys, want %d: got %v", len(got), len(want), got)
		}
		for k := range want {
			if !got[k] {
				t.Fatalf("walk omitted key %q", k)
			}
		}
	})

	t.Run("ListPrefixFilters", func(t *testing.T) {
		s := newStorage(t)
		for _, k := range []string{"a/1", "a/2", "b/1"} {
			put(t, s, k, "v", storage.PutOptions{})
		}
		page, err := s.List(ctx, storage.ListOptions{Prefix: "a/", Limit: 10})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		got := map[string]bool{}
		for _, o := range page.Objects {
			got[o.Key] = true
		}
		if page.NextToken != "" || len(got) != 2 || !got["a/1"] || !got["a/2"] {
			t.Fatalf("List(a/) = %v (next %q), want exactly {a/1, a/2}", got, page.NextToken)
		}
	})

	t.Run("ListEmptyPrefixListsAll", func(t *testing.T) {
		s := newStorage(t)
		for _, k := range []string{"a/1", "b/1", "c"} {
			put(t, s, k, "v", storage.PutOptions{})
		}
		page, err := s.List(ctx, storage.ListOptions{Limit: 10})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(page.Objects) != 3 || page.NextToken != "" {
			t.Fatalf("List(\"\") returned %d objects (next %q), want all 3", len(page.Objects), page.NextToken)
		}
	})

	t.Run("ListNoMatchReturnsEmptyPage", func(t *testing.T) {
		s := newStorage(t)
		put(t, s, "a/1", "v", storage.PutOptions{})
		page, err := s.List(ctx, storage.ListOptions{Prefix: "zzz/", Limit: 5})
		if err != nil {
			t.Fatalf("List(no match) err = %v, want nil", err)
		}
		if len(page.Objects) != 0 || page.NextToken != "" {
			t.Fatalf("List(no match) = %+v, want empty page with empty NextToken", page)
		}
	})
}
```

- [ ] **Step 3: Write `reference_test.go`**

```go
package storagetest

import (
	"testing"

	"github.com/slam0504/go-ddd-core/ports/storage"
)

// TestReferenceImplementationPassesContract proves RunContract is
// satisfiable: the suite is only trustworthy if a known-good implementation
// passes it.
func TestReferenceImplementationPassesContract(t *testing.T) {
	RunContract(t, func(t *testing.T) storage.ObjectStorage {
		return newMemStorage()
	})
}
```

- [ ] **Step 4: Run the suite — expect PASS**

Run: `cd /Users/eason_tseng/playground/project/go-ddd-core && go test -race ./ports/storage/...`
Expected: PASS, 13 subtests.

- [ ] **Step 5: Non-vacuity red-proof (×3 temporary breakages, then restore)**

Sequentially break the reference impl, run, confirm the suite CATCHES it, revert (do not commit breakage):
1. In `Delete`, return `fmt.Errorf("...: %w", storage.ErrNotFound)` when the key is absent → `DeleteIsIdempotent` must FAIL.
2. In `List`, drop the `k <= opts.Token` resume filter → `ListTokenWalkCompleteNoDupes` must FAIL (duplicates).
3. In `Put`, skip storing `opts.Metadata` → `MetadataRoundtripLowercaseKeys` must FAIL.

After each: `go test ./ports/storage/storagetest/ -run TestReference -count=1` → FAIL on the named subtest. `git checkout ports/storage/storagetest/reference.go` restores. Final run: PASS.

- [ ] **Step 6: Lint + commit**

```bash
cd /Users/eason_tseng/playground/project/go-ddd-core
/usr/local/bin/golangci-lint run ./ports/storage/...
git add ports/storage/storagetest/
git commit -m "feat(storage): storagetest.RunContract conformance suite + reference impl"
```

### Task 3: Core PR — open, CI, merge (UNTAGGED)

**Files:** none (ops task).

**Interfaces:**
- Produces: core `main` merge SHA carrying the matured contract — Task 4 pseudo-pins it.

- [ ] **Step 1: Push + open PR**

```bash
cd /Users/eason_tseng/playground/project/go-ddd-core
git push -u origin feat/storage-maturation
gh pr create --title "feat(storage)!: mature ports/storage — token-paginated List + storagetest" \
  --body "$(cat <<'EOF'
BREAKING ports/storage maturation ahead of its first adapter (spec in
go-ddd-adapters docs/superpowers/specs/2026-07-03-storage-minio-adapter-design.md):
token-paginated List, ObjectInfo.Metadata, pinned operation semantics,
storagetest.RunContract + in-memory reference impl. Zero consumers today;
merge UNTAGGED — the tag rides the release-prep PR after the adapter impl
PR proves the contract (four-step gate).

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 2: Wait for CI green, then merge**

Run: `gh pr checks --watch` then `gh pr merge --merge --delete-branch`
Expected: all checks green; record the merge SHA for Task 4. Note: if core PR #38 (go 1.26 bump) merged first, rebase on `main` before merging and re-run CI.

---

## Phase B — `storage/minio` adapter (repo: go-ddd-adapters, branch `feat/storage-minio`)

### Task 4: Pseudo-pin core + add minio-go dependency

**Files:**
- Modify: `/Users/eason_tseng/playground/project/go-ddd-adapters/go.mod` (+ `go.sum`)
- Modify: `/Users/eason_tseng/playground/project/go-ddd-adapters/examples/orders/go.mod` (+ `go.sum`)

**Interfaces:**
- Produces: resolvable `storage.ListOptions`/`storagetest.RunContract` symbols + `minio-go v7.2.1` for Tasks 5–7.

- [ ] **Step 1: Bump core to the Task 3 merge SHA pseudo-version (root + examples)**

```bash
cd /Users/eason_tseng/playground/project/go-ddd-adapters
go get github.com/slam0504/go-ddd-core@<TASK3-MERGE-SHA>
go get github.com/minio/minio-go/v7@v7.2.1
go get github.com/testcontainers/testcontainers-go/modules/minio@v0.42.0
go mod tidy
cd examples/orders && go get github.com/slam0504/go-ddd-core@<TASK3-MERGE-SHA> && go mod tidy
```

- [ ] **Step 2: Verify symbols resolve + build**

Run: `cd /Users/eason_tseng/playground/project/go-ddd-adapters && go build ./... && go doc github.com/slam0504/go-ddd-core/ports/storage.ListOptions | head -5`
Expected: build clean; `ListOptions` doc shows `Prefix`/`Token`/`Limit`.
NOTE: if core PR #38 merged before Task 3, the pseudo-version carries core's go 1.26 directive and this repo's `go 1.25.0` will refuse the require — in that case bump root + examples `go` directives to `1.26` in THIS task and record it for the Task 9 CHANGELOG.

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum examples/orders/go.mod examples/orders/go.sum
git commit -m "chore(deps): pin core at storage-maturation pseudo-version; add minio-go v7.2.1"
```

### Task 5: `miniostorage` scaffold — constructor, guards, error mapping

**Files:**
- Create: `/Users/eason_tseng/playground/project/go-ddd-adapters/storage/minio/doc.go`
- Create: `/Users/eason_tseng/playground/project/go-ddd-adapters/storage/minio/storage.go`
- Create: `/Users/eason_tseng/playground/project/go-ddd-adapters/storage/minio/errors.go`
- Test: `/Users/eason_tseng/playground/project/go-ddd-adapters/storage/minio/storage_test.go`

**Interfaces:**
- Consumes: `storage.*` contract types, `errorsx`, `minio.Client`.
- Produces: `miniostorage.New(client *minio.Client, bucket string, opts ...Option) (*Storage, error)`, `type Option func(*Storage)`, `mapError(op string, err error) error`, `canonicalMetadata(header http.Header, userMeta map[string]string) map[string]string`, `validateKey(key string) error`. Tasks 6–7 call these exact names.

- [ ] **Step 1: Write failing constructor-guard tests (`storage_test.go`)**

```go
package miniostorage

import (
	"testing"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/slam0504/go-ddd-core/pkg/errorsx"
)

// testClient returns a real *minio.Client pointing at a dead endpoint. It
// never gets dialed by guard tests: argument validation must fire before
// any network use (contract precedence).
func testClient(t *testing.T) *minio.Client {
	t.Helper()
	c, err := minio.New("127.0.0.1:1", &minio.Options{
		Creds: credentials.NewStaticV4("x", "y", ""), Secure: false,
	})
	if err != nil {
		t.Fatalf("minio.New: %v", err)
	}
	return c
}

func TestNew_NilClientRejected(t *testing.T) {
	_, err := New(nil, "bucket")
	if errorsx.CodeOf(err) != errorsx.CodeInvalidArgument {
		t.Fatalf("New(nil client) code = %v, want CodeInvalidArgument", errorsx.CodeOf(err))
	}
}

func TestNew_EmptyBucketRejected(t *testing.T) {
	_, err := New(testClient(t), "")
	if errorsx.CodeOf(err) != errorsx.CodeInvalidArgument {
		t.Fatalf("New(empty bucket) code = %v, want CodeInvalidArgument", errorsx.CodeOf(err))
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd /Users/eason_tseng/playground/project/go-ddd-adapters && go test ./storage/minio/`
Expected: FAIL — `New` undefined.

- [ ] **Step 3: Write `doc.go`, `storage.go`, `errors.go`**

`doc.go`:

```go
// Package miniostorage implements ports/storage.ObjectStorage and
// storage.PresignedURLer over an S3-compatible backend via minio-go v7
// (MinIO, AWS S3, and other S3-compatible services).
//
// The bucket is the isolation unit and must already exist: New does not
// create it (bucket provisioning is a deployment concern). A missing bucket
// surfaces as errorsx.CodeUnavailable — deliberately NOT storage.ErrNotFound,
// which is reserved for missing objects.
//
// List pagination: ListPage.NextToken is an adapter-owned cursor (the last
// returned key, resumed via the backend's StartAfter listing), not an S3
// continuation token. Pages are assembled with a Limit+1 lookahead. The
// backend page cap does not limit callers: the adapter iterates internally.
package miniostorage
```

`storage.go`:

```go
package miniostorage

import (
	minio "github.com/minio/minio-go/v7"

	"github.com/slam0504/go-ddd-core/pkg/errorsx"
)

// Storage implements storage.ObjectStorage + storage.PresignedURLer over a
// caller-configured *minio.Client and a single existing bucket.
type Storage struct {
	client *minio.Client
	bucket string
}

// Option customises a Storage. Reserved: v1 ships no options.
type Option func(*Storage)

// New wraps client and bucket. The bucket must already exist — New does not
// create it and does not contact the backend.
func New(client *minio.Client, bucket string, opts ...Option) (*Storage, error) {
	if client == nil {
		return nil, errorsx.New(errorsx.CodeInvalidArgument, "miniostorage: nil client")
	}
	if bucket == "" {
		return nil, errorsx.New(errorsx.CodeInvalidArgument, "miniostorage: empty bucket")
	}
	s := &Storage{client: client, bucket: bucket}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

func validateKey(key string) error {
	if key == "" {
		return errorsx.New(errorsx.CodeInvalidArgument, "miniostorage: empty key")
	}
	return nil
}
```

`errors.go`:

```go
package miniostorage

import (
	"fmt"
	"net/http"
	"strings"

	minio "github.com/minio/minio-go/v7"

	"github.com/slam0504/go-ddd-core/pkg/errorsx"
	"github.com/slam0504/go-ddd-core/ports/storage"
)

// mapError translates a minio-go error into the contract's error model:
// NoSuchKey wraps the uncoded storage.ErrNotFound sentinel; NoSuchBucket is
// CodeUnavailable (a deployment/config failure, never ErrNotFound); every
// other failure is errorsx-coded and never CodeUnknown.
func mapError(op string, err error) error {
	if err == nil {
		return nil
	}
	resp := minio.ToErrorResponse(err)
	switch resp.Code {
	case "NoSuchKey":
		return fmt.Errorf("miniostorage: %s: %w", op, storage.ErrNotFound)
	case "NoSuchBucket":
		return errorsx.Wrap(errorsx.CodeUnavailable, "miniostorage: "+op+": bucket does not exist", err)
	}
	return errorsx.Wrap(errorsx.CodeUnavailable, "miniostorage: "+op, err)
}

// canonicalMetadata merges user metadata from the x-amz-meta-* response
// headers (portable S3 surface) and minio-go's UserMetadata map (MinIO
// servers), canonicalizing keys to lowercase per the contract's portable
// metadata rule. Returns nil when no user metadata is present.
func canonicalMetadata(header http.Header, userMeta map[string]string) map[string]string {
	out := map[string]string{}
	const prefix = "X-Amz-Meta-"
	for k, vs := range header {
		if len(vs) > 0 && strings.HasPrefix(k, prefix) {
			out[strings.ToLower(strings.TrimPrefix(k, prefix))] = vs[0]
		}
	}
	for k, v := range userMeta {
		out[strings.ToLower(k)] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
```

- [ ] **Step 4: Run tests — PASS; lint**

Run: `go test ./storage/minio/ && /usr/local/bin/golangci-lint run ./storage/minio/...`
Expected: PASS, 0 lint issues.

- [ ] **Step 5: Commit**

```bash
git add storage/minio/
git commit -m "feat(storage/minio): scaffold — New guards, error mapping, metadata canonicalization"
```

### Task 6: Method implementations — Put/Get/Stat/Delete/List/Presign

**Files:**
- Create: `/Users/eason_tseng/playground/project/go-ddd-adapters/storage/minio/objectstorage.go`
- Create: `/Users/eason_tseng/playground/project/go-ddd-adapters/storage/minio/presign.go`
- Modify (extend): `/Users/eason_tseng/playground/project/go-ddd-adapters/storage/minio/storage_test.go`

**Interfaces:**
- Consumes: Task 5's `validateKey` / `mapError` / `canonicalMetadata`; `storage.ObjectStorage` + `storage.PresignedURLer` signatures from Task 1.
- Produces: `*Storage` satisfying both interfaces (compile-time asserted). Task 7 proves behaviour.

- [ ] **Step 1: Write failing pre-network guard tests (append to `storage_test.go`)**

```go
func TestGuards_FireBeforeNetwork(t *testing.T) {
	// The client points at 127.0.0.1:1; if a guard is missing, the call
	// dials and fails with a transport error instead of CodeInvalidArgument.
	s, err := New(testClient(t), "bucket")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	if code := errorsx.CodeOf(s.Put(ctx, "", strings.NewReader(""), 0, storage.PutOptions{})); code != errorsx.CodeInvalidArgument {
		t.Fatalf("Put empty key code = %v", code)
	}
	if code := errorsx.CodeOf(s.Put(ctx, "k", strings.NewReader("x"), -1, storage.PutOptions{})); code != errorsx.CodeInvalidArgument {
		t.Fatalf("Put negative size code = %v", code)
	}
	if _, _, err := s.Get(ctx, ""); errorsx.CodeOf(err) != errorsx.CodeInvalidArgument {
		t.Fatalf("Get empty key code = %v", errorsx.CodeOf(err))
	}
	if _, err := s.Stat(ctx, ""); errorsx.CodeOf(err) != errorsx.CodeInvalidArgument {
		t.Fatalf("Stat empty key code = %v", errorsx.CodeOf(err))
	}
	if code := errorsx.CodeOf(s.Delete(ctx, "")); code != errorsx.CodeInvalidArgument {
		t.Fatalf("Delete empty key code = %v", code)
	}
	if _, err := s.List(ctx, storage.ListOptions{Limit: 0}); errorsx.CodeOf(err) != errorsx.CodeInvalidArgument {
		t.Fatalf("List limit 0 code = %v", errorsx.CodeOf(err))
	}
	if _, err := s.PresignGet(ctx, "k", 0); errorsx.CodeOf(err) != errorsx.CodeInvalidArgument {
		t.Fatalf("PresignGet expires 0 code = %v", errorsx.CodeOf(err))
	}
	if _, err := s.PresignPut(ctx, "k", -time.Second); errorsx.CodeOf(err) != errorsx.CodeInvalidArgument {
		t.Fatalf("PresignPut negative expires code = %v", errorsx.CodeOf(err))
	}
}
```

(add imports: `context`, `strings`, `time`, `github.com/slam0504/go-ddd-core/ports/storage`)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./storage/minio/`
Expected: FAIL — methods undefined.

- [ ] **Step 3: Implement `objectstorage.go`**

```go
package miniostorage

import (
	"context"
	"io"

	minio "github.com/minio/minio-go/v7"

	"github.com/slam0504/go-ddd-core/pkg/errorsx"
	"github.com/slam0504/go-ddd-core/ports/storage"
)

var _ storage.ObjectStorage = (*Storage)(nil)

// Put stores body under key, unconditionally overwriting (last-writer-wins).
// The declared size is handed to the backend as-is; a backend-reported
// mismatch surfaces as an error (never silent truncation).
func (s *Storage) Put(ctx context.Context, key string, body io.Reader, size int64, opts storage.PutOptions) error {
	if err := validateKey(key); err != nil {
		return err
	}
	if size < 0 {
		return errorsx.New(errorsx.CodeInvalidArgument, "miniostorage: negative size")
	}
	_, err := s.client.PutObject(ctx, s.bucket, key, body, size, minio.PutObjectOptions{
		ContentType:  opts.ContentType,
		UserMetadata: opts.Metadata,
	})
	return mapError("put "+key, err)
}

// Get returns the object's content and info; the caller must close the
// reader. minio-go's GetObject is lazy, so Stat() is forced here to make a
// missing key surface as ErrNotFound at call time, not at first Read.
func (s *Storage) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error) {
	if err := validateKey(key); err != nil {
		return nil, storage.ObjectInfo{}, err
	}
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, storage.ObjectInfo{}, mapError("get "+key, err)
	}
	st, err := obj.Stat()
	if err != nil {
		_ = obj.Close()
		return nil, storage.ObjectInfo{}, mapError("get "+key, err)
	}
	return obj, toObjectInfo(st), nil
}

// Stat returns the object's info without content.
func (s *Storage) Stat(ctx context.Context, key string) (storage.ObjectInfo, error) {
	if err := validateKey(key); err != nil {
		return storage.ObjectInfo{}, err
	}
	st, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return storage.ObjectInfo{}, mapError("stat "+key, err)
	}
	return toObjectInfo(st), nil
}

// Delete removes the object; deleting an absent key succeeds (S3 semantics
// pass the contract's idempotency requirement through unchanged).
func (s *Storage) Delete(ctx context.Context, key string) error {
	if err := validateKey(key); err != nil {
		return err
	}
	return mapError("delete "+key, s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}))
}

// List returns one lexically-ordered page. NextToken is the last returned
// key (adapter-owned cursor); the next page resumes via StartAfter. A
// Limit+1 lookahead decides whether a next page exists. Any string is a
// structurally valid cursor, so no malformed-token branch exists here.
func (s *Storage) List(ctx context.Context, opts storage.ListOptions) (storage.ListPage, error) {
	if opts.Limit <= 0 {
		return storage.ListPage{}, errorsx.New(errorsx.CodeInvalidArgument, "miniostorage: non-positive limit")
	}
	// Own the iteration lifetime: minio-go stops listing when ctx ends.
	lctx, cancel := context.WithCancel(ctx)
	defer cancel()

	page := storage.ListPage{}
	for oi := range s.client.ListObjectsIter(lctx, s.bucket, minio.ListObjectsOptions{
		Prefix:     opts.Prefix,
		Recursive:  true,
		StartAfter: opts.Token,
	}) {
		if oi.Err != nil {
			return storage.ListPage{}, mapError("list", oi.Err)
		}
		if len(page.Objects) == opts.Limit { // Limit+1th object: lookahead hit
			page.NextToken = page.Objects[opts.Limit-1].Key
			return page, nil
		}
		page.Objects = append(page.Objects, storage.ObjectInfo{
			Key:         oi.Key,
			Size:        oi.Size,
			ContentType: oi.ContentType,
			ETag:        oi.ETag,
			UpdatedAt:   oi.LastModified,
			// Metadata deliberately nil: the contract populates it on
			// Get/Stat only.
		})
	}
	if err := ctx.Err(); err != nil && len(page.Objects) < opts.Limit {
		// Iterator may end early because ctx was cancelled mid-walk.
		return storage.ListPage{}, errorsx.Wrap(errorsx.CodeUnavailable, "miniostorage: list interrupted", err)
	}
	return page, nil
}

func toObjectInfo(oi minio.ObjectInfo) storage.ObjectInfo {
	return storage.ObjectInfo{
		Key:         oi.Key,
		Size:        oi.Size,
		ContentType: oi.ContentType,
		ETag:        oi.ETag,
		UpdatedAt:   oi.LastModified,
		Metadata:    canonicalMetadata(oi.Metadata, oi.UserMetadata),
	}
}
```

- [ ] **Step 4: Implement `presign.go`**

```go
package miniostorage

import (
	"context"
	"time"

	"github.com/slam0504/go-ddd-core/pkg/errorsx"
	"github.com/slam0504/go-ddd-core/ports/storage"
)

var _ storage.PresignedURLer = (*Storage)(nil)

// PresignGet returns a time-limited unauthenticated download URL.
func (s *Storage) PresignGet(ctx context.Context, key string, expires time.Duration) (string, error) {
	if err := validatePresign(key, expires); err != nil {
		return "", err
	}
	u, err := s.client.PresignedGetObject(ctx, s.bucket, key, expires, nil)
	if err != nil {
		return "", mapError("presign get "+key, err)
	}
	return u.String(), nil
}

// PresignPut returns a time-limited unauthenticated upload URL.
func (s *Storage) PresignPut(ctx context.Context, key string, expires time.Duration) (string, error) {
	if err := validatePresign(key, expires); err != nil {
		return "", err
	}
	u, err := s.client.PresignedPutObject(ctx, s.bucket, key, expires)
	if err != nil {
		return "", mapError("presign put "+key, err)
	}
	return u.String(), nil
}

func validatePresign(key string, expires time.Duration) error {
	if err := validateKey(key); err != nil {
		return err
	}
	if expires <= 0 {
		return errorsx.New(errorsx.CodeInvalidArgument, "miniostorage: non-positive expires")
	}
	return nil
}
```

- [ ] **Step 5: Run tests — PASS; vet + lint**

Run: `go test ./storage/minio/ && go vet ./storage/minio/ && /usr/local/bin/golangci-lint run ./storage/minio/...`
Expected: PASS, 0 issues. NOTE: if `oi.UserMetadata` (type `minio.StringMap`) does not assign directly to `map[string]string` in `canonicalMetadata`'s caller, insert the conversion `map[string]string(oi.UserMetadata)` — check with `go doc github.com/minio/minio-go/v7.StringMap`.

- [ ] **Step 6: Commit**

```bash
git add storage/minio/
git commit -m "feat(storage/minio): ObjectStorage + PresignedURLer implementations"
```

### Task 7: Integration harness — RunContract vs real MinIO + adapter-specific tests + CI

**Files:**
- Create: `/Users/eason_tseng/playground/project/go-ddd-adapters/storage/minio/integration_test.go`
- Modify: `/Users/eason_tseng/playground/project/go-ddd-adapters/.github/workflows/ci.yml:77` (append `./storage/minio/...` to the existing integration-test path list)

**Interfaces:**
- Consumes: `storagetest.RunContract` (Task 2), `miniostorage.New` (Task 5), methods (Task 6).

- [ ] **Step 1: Verify the testcontainers minio module API before writing**

Run: `go doc github.com/testcontainers/testcontainers-go/modules/minio | head -20` and `go doc github.com/testcontainers/testcontainers-go/modules/minio.MinioContainer`
Expected: a `Run(ctx, img, opts...)` constructor and a `ConnectionString(ctx)` method plus default root credentials (fields or `minio.DefaultUser`/`DefaultPassword` constants). Adapt the harness below to the ACTUAL surface — the shape below is best-evidence, not gospel.

- [ ] **Step 2: Write `integration_test.go`**

```go
//go:build integration

package miniostorage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	minio "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	tcminio "github.com/testcontainers/testcontainers-go/modules/minio"

	"github.com/slam0504/go-ddd-core/pkg/errorsx"
	"github.com/slam0504/go-ddd-core/ports/storage"
	"github.com/slam0504/go-ddd-core/ports/storage/storagetest"
)

var bucketSeq atomic.Int64

// startMinio launches one MinIO container per test function and returns a
// connected client.
func startMinio(t *testing.T) *minio.Client {
	t.Helper()
	ctx := context.Background()
	c, err := tcminio.Run(ctx, "minio/minio:RELEASE.2024-01-16T16-07-38Z")
	if err != nil {
		t.Fatalf("start minio container: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })
	endpoint, err := c.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(c.Username, c.Password, ""),
		Secure: false,
	})
	if err != nil {
		t.Fatalf("minio client: %v", err)
	}
	return client
}

// freshBucket creates a new bucket (the adapter's isolation unit — New does
// not create buckets, the harness owns provisioning).
func freshBucket(t *testing.T, client *minio.Client) string {
	t.Helper()
	name := fmt.Sprintf("t%d", bucketSeq.Add(1))
	if err := client.MakeBucket(context.Background(), name, minio.MakeBucketOptions{}); err != nil {
		t.Fatalf("make bucket %s: %v", name, err)
	}
	return name
}

func TestRunContract(t *testing.T) {
	client := startMinio(t)
	storagetest.RunContract(t, func(t *testing.T) storage.ObjectStorage {
		s, err := New(client, freshBucket(t, client))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return s
	})
}

func TestPresign_EndToEnd(t *testing.T) {
	client := startMinio(t)
	s, err := New(client, freshBucket(t, client))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	body := []byte("presigned payload")

	putURL, err := s.PresignPut(ctx, "p/obj", time.Minute)
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, putURL, bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HTTP PUT via presigned URL: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("presigned PUT status = %d, want 200", resp.StatusCode)
	}

	getURL, err := s.PresignGet(ctx, "p/obj", time.Minute)
	if err != nil {
		t.Fatalf("PresignGet: %v", err)
	}
	gresp, err := http.Get(getURL)
	if err != nil {
		t.Fatalf("HTTP GET via presigned URL: %v", err)
	}
	defer gresp.Body.Close()
	got, _ := io.ReadAll(gresp.Body)
	if !bytes.Equal(got, body) {
		t.Fatalf("presigned GET body = %q, want %q", got, body)
	}
}

func TestList_MultiPageWalkAgainstRealBackend(t *testing.T) {
	client := startMinio(t)
	s, err := New(client, freshBucket(t, client))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	const n = 25
	want := map[string]bool{}
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("walk/%03d", i)
		if err := s.Put(ctx, key, strings.NewReader("v"), 1, storage.PutOptions{}); err != nil {
			t.Fatalf("Put %s: %v", key, err)
		}
		want[key] = true
	}
	got := map[string]bool{}
	token, pages := "", 0
	for {
		pages++
		if pages > 10 {
			t.Fatal("walk did not terminate")
		}
		page, err := s.List(ctx, storage.ListOptions{Prefix: "walk/", Token: token, Limit: 10})
		if err != nil {
			t.Fatalf("List page %d: %v", pages, err)
		}
		for _, o := range page.Objects {
			if got[o.Key] {
				t.Fatalf("duplicate %q", o.Key)
			}
			got[o.Key] = true
		}
		if page.NextToken == "" {
			break
		}
		token = page.NextToken
	}
	if len(got) != n {
		t.Fatalf("walk yielded %d keys, want %d", len(got), n)
	}
	if pages != 3 {
		t.Fatalf("pages = %d, want 3 (25 objects / Limit 10)", pages)
	}
}

func TestNoSuchBucket_IsUnavailableNotErrNotFound(t *testing.T) {
	client := startMinio(t)
	s, err := New(client, "no-such-bucket-xyz") // deliberately not created
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = s.Stat(context.Background(), "k")
	if errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("missing BUCKET must not read as missing OBJECT: %v", err)
	}
	if errorsx.CodeOf(err) != errorsx.CodeUnavailable {
		t.Fatalf("Stat vs missing bucket code = %v, want CodeUnavailable", errorsx.CodeOf(err))
	}
}

func TestMetadata_MixedCaseKeysComeBackLowercase(t *testing.T) {
	client := startMinio(t)
	s, err := New(client, freshBucket(t, client))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	err = s.Put(ctx, "m", strings.NewReader("v"), 1, storage.PutOptions{
		Metadata: map[string]string{"X-Custom-Key": "val"},
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	info, err := s.Stat(ctx, "m")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Metadata["x-custom-key"] != "val" {
		t.Fatalf("Metadata = %v, want lowercase key x-custom-key=val", info.Metadata)
	}
	if _, mixed := info.Metadata["X-Custom-Key"]; mixed {
		t.Fatalf("Metadata leaked a non-canonicalized mixed-case key: %v", info.Metadata)
	}
}
```

- [ ] **Step 3: Run locally (needs Docker)**

Run: `go test -tags=integration -race ./storage/minio/ -v`
Expected: PASS — 13 RunContract subtests + 4 adapter-specific tests. If the module API differs from Step 1's expectation, adapt the harness (verify-then-adapt) and note the delta in the commit message.

- [ ] **Step 4: Extend the CI integration step**

In `.github/workflows/ci.yml` line 77, append `./storage/minio/...`:

```yaml
      - name: pgx adapter integration tests
        run: go test -tags=integration -race ./ports/database/pgx/... ./eventbus/outbox/pgx/... ./idempotency/redis/... ./jobs/asynq/... ./ratelimit/redisrate/... ./cache/redis/... ./eventbus/inbox/pgx/... ./storage/minio/...
```

- [ ] **Step 5: Lint (with integration tag) + commit**

```bash
/usr/local/bin/golangci-lint run --build-tags=integration ./storage/minio/...
git add storage/minio/integration_test.go .github/workflows/ci.yml
git commit -m "test(storage/minio): integration harness — RunContract vs real MinIO, presign e2e, multi-page walk, NoSuchBucket, metadata canonicalization; CI leg"
```

### Task 8: Bookkeeping — CHANGELOG + README

**Files:**
- Modify: `/Users/eason_tseng/playground/project/go-ddd-adapters/CHANGELOG.md` (`[Unreleased]`)
- Modify: `/Users/eason_tseng/playground/project/go-ddd-adapters/README.md` (adapter table row; follow the exact row format of existing entries)

- [ ] **Step 1: CHANGELOG `[Unreleased]` entry**

```markdown
### Added

- `storage/minio` (`miniostorage`): first `ports/storage.ObjectStorage`
  consumer, over minio-go v7.2.1 (MinIO / AWS S3 / S3-compatible).
  Implements `ObjectStorage` + `PresignedURLer`. Adapter-owned `StartAfter`
  list cursor with `Limit+1` lookahead (NOT an S3 continuation token —
  minio-go's token-level API is not ctx-aware); `NoSuchKey` →
  `storage.ErrNotFound` (uncoded sentinel), `NoSuchBucket` →
  `CodeUnavailable` (never `ErrNotFound`); metadata keys canonicalized to
  lowercase; bucket must pre-exist (`New` never creates it). Proven by
  core's `storagetest.RunContract` against a real MinIO container in CI
  plus presign/pagination/misconfiguration integration tests.
```

Also note the core BREAKING dependency: this release consumes core's matured `ports/storage` (token-paginated `List`).

- [ ] **Step 2: README adapter-table row**

Add to the adapter table (match existing style):

```markdown
| `storage/minio` | `storage.ObjectStorage` / `storage.PresignedURLer` | minio-go v7.2.1 (MinIO / S3-compatible); adapter-owned StartAfter list cursor (+1 lookahead), lowercase metadata canonicalization, `NoSuchBucket` → `CodeUnavailable`; bucket must pre-exist |
```

- [ ] **Step 3: Verify + commit**

Run: `go build ./... && go test ./storage/minio/`
Expected: clean.

```bash
git add CHANGELOG.md README.md
git commit -m "docs(storage/minio): CHANGELOG entry + README adapter row"
```

### Task 9: Four-step tag gate (ops checklist — main session, not a subagent)

**Files:** release-prep diffs only (core CHANGELOG flip, adapters go.mod bump + CHANGELOG flip + README compat row).

- [ ] **Step 1**: Push `feat/storage-minio`, open adapters impl PR at the core pseudo-pin; CI must be green (integration leg includes the new MinIO testcontainer run); merge.
- [ ] **Step 2**: Core release-prep PR (CHANGELOG `[Unreleased]` → `[0.14.0]`, compare links) → merge → annotated tag `v0.14.0` at core `main` → GitHub Release Latest → verify `go list -m github.com/slam0504/go-ddd-core@v0.14.0` resolves via proxy. INTERACTION: if core PR #38 (go 1.26 floor) merged into this release, the tag ships the floor bump too — the dep-bump below must then bump the adapters `go` directives and the CHANGELOG must say so.
- [ ] **Step 3**: Adapters dep-bump PR `chore/bump-core-v0.14.0`: pseudo → `v0.14.0` (root + `examples/orders` + `go mod tidy`), CHANGELOG `[Unreleased]` → `[v0.14.0] - <date>` + compare links, README Status paragraph + compat matrix row; CI 5/5 green; merge.
- [ ] **Step 4**: Annotated adapter tag `v0.14.0` at the dep-bump merge; push; GitHub Release Latest (verify via `releases/latest` API). Update `.agent/state.md`, `.agent/decisions.md`, cross-repo `.agent-memory/go-ddd.md` (git add and git commit as SEPARATE Bash calls); write project auto-memory.

---

## Verification strategy (whole-plan)

- Core: `go build ./... && go vet ./... && go test -race ./ports/storage/...` + non-vacuity red-proof ×3 + `/usr/local/bin/golangci-lint run ./...`.
- Adapters unit: `go test ./storage/minio/` (guards fire pre-network against a dead endpoint — deterministic, no Docker).
- Adapters integration (Docker): `go test -tags=integration -race ./storage/minio/ -v` — RunContract (12 subtests) + presign e2e + 3-page walk + NoSuchBucket + metadata canonicalization.
- CI is the authoritative integration proof (testcontainers), same as every prior adapter cycle.
- Not locally verifiable: GitHub Release/proxy resolution steps (Task 9) — verified via `gh` + `go list -m` after the fact.
