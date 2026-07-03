# storage/minio — first ObjectStorage adapter (core maturation + storagetest + adapter)

Date: 2026-07-03. Status: draft (pending user review).
First consumer of core `ports/storage` — the last published core port with
zero adapters. Closes the "zero-consumer port" gap in the roadmap's B
quadrant.

## 1. Goal & context

Two phases, full cross-repo tag-gate (core contract changes this cycle):

- **Phase A (core)**: mature the `ports/storage` contract — `List` becomes
  token-paginated (**BREAKING**), `ObjectInfo` gains `Metadata`, operation
  semantics pinned (overwrite, idempotent delete, validation precedence,
  error-coding rules), plus `ports/storage/storagetest.RunContract`.
- **Phase B (adapters)**: `storage/minio` (`miniostorage`) — S3-compatible
  adapter over minio-go v7, implementing `ObjectStorage` +
  `PresignedURLer`, proven by the conformance suite against a real MinIO
  container in CI.

Candidate evaluation (recorded to avoid re-litigation): chosen over the
reader pgx projection (an `examples/orders` deepening, not a new adapter
investment — scheduled as the next examples-focused cycle) and over
`storage/fs` / `storage/s3` as first driver. `storage/fs` would hide the
object-store-shaped problems (pagination tokens, metadata canonicalization,
presign, remote error mapping); `storage/s3` would front-load the
aws-sdk-go-v2 dependency and credential/config surface before the contract
is pressure-tested. MinIO exercises real S3 semantics in CI without an AWS
account. `fs` and `s3` are sibling follow-up cycles.

### Why the contract must change first (Phase A before B)

`ports/storage` has never been touched since its scaffold commit (core
`0b5b3fd`, "phase 5: infra ports"): a one-line package godoc, no operation
semantics, no validation precedence, no conformance suite. The current
`List(ctx, prefix) ([]ObjectInfo, error)` is an unbounded return that
directly conflicts with S3's paginated `ListObjectsV2` (1000-object page
cap): a faithful adapter would be forced to accumulate an entire bucket in
memory or invent its own limit. With zero consumers, this is the cheapest
moment the signature will ever be fixable (same reasoning as the v0.13.0
inbox `Memory` BREAKING).

## 2. Versions & tag-gate (full four-step)

1. Adapter impl PR merges at a core **pseudo-version pin** (CI green).
2. Core tags its next version (expected **v0.14.0**), publishing the matured
   `ports/storage` + `storagetest`.
3. Adapters dep-bump PR: pseudo → the core tag (root + `examples/orders`) +
   CHANGELOG flip + README compat row.
4. Adapters tag their next version (expected **v0.14.0**) + GitHub Release
   Latest. Any core/adapter version coincidence is accidental; the alignment
   policy remains dropped (v0.11.0 incident).

Dependencies (verified via Go module proxy 2026-07-03):

- `github.com/minio/minio-go/v7 v7.2.1` (latest).
- `github.com/testcontainers/testcontainers-go/modules/minio v0.42.0` —
  deliberately pinned to match the repo's existing
  `testcontainers-go v0.42.0`, mirroring the redis-module precedent.

## 3. Phase A — core `ports/storage` maturation (BREAKING)

### 3.1 `List` → token pagination (BREAKING signature change)

```go
// ListOptions configures a List call. Token is opaque: callers must not
// parse or construct it — only pass back the previous page's NextToken.
type ListOptions struct {
        Prefix string
        Token  string
        Limit  int
}

// ListPage is one page of a List walk. NextToken == "" means the walk is
// complete.
type ListPage struct {
        Objects   []ObjectInfo
        NextToken string
}

List(ctx context.Context, opts ListOptions) (ListPage, error)
```

Pinned semantics:

- `Limit <= 0` → `errorsx.CodeInvalidArgument` (no unbounded default; every
  caller states a page size).
- `NextToken == ""` is the only termination signal. A page MAY be shorter
  than `Limit` while `NextToken` is non-empty (S3 `ListObjectsV2`
  semantics) — callers must not treat a short page as terminal.
- `Token` is opaque and **adapter-owned**: the contract does not promise it
  maps to any backend-native continuation token. An invalid token returns a
  coded error, never `CodeUnknown`: a malformed adapter-owned token MAY be
  `CodeInvalidArgument`; a backend-rejected token surfaces as a
  backend/config/transport-class coded error.
- Empty `Prefix` lists everything.
- Backends may cap the effective page size below `Limit` (e.g. S3's 1000);
  each adapter documents its cap.
- **Ordering: backend-defined, NOT promised globally by the contract.** The
  only requirement: stable within a token walk under no concurrent
  mutation (no duplicates, no omissions across the whole walk).

### 3.2 `ObjectInfo` gains `Metadata` (additive)

`Metadata map[string]string`, populated by `Stat` and `Get`. Fixes the
current write-only asymmetry (`PutOptions.Metadata` exists but nothing
returns it).

Key normalization (pinned — S3-class backends canonicalize/lowercase
metadata keys, so mixed-case exact roundtrip is not portable):

- Contract godoc: **portable metadata keys are lowercase ASCII**.
- Adapters canonicalize returned `ObjectInfo.Metadata` keys to lowercase.
- `storagetest` asserts exact roundtrip with lowercase keys ONLY; mixed-case
  roundtrip is explicitly not asserted (and not promised).

### 3.3 `Put` semantics

- **Unconditional overwrite** (last-writer-wins, S3 model). Overwriting an
  existing key succeeds; no conditional-put in v1.
- `size < 0` → `CodeInvalidArgument`. Unknown-length streaming (`-1`) is
  deferred until a consumer demonstrates the need; callers buffer first.
- The adapter passes the declared `size` to the backend as-is. If the
  backend or the reader reports a size mismatch, the adapter surfaces that
  error; the adapter MUST NOT intentionally truncate or pad silently.
  There is NO requirement to pre-validate the reader's length (that would
  force buffering).
- Empty `PutOptions.ContentType`: the adapter/backend may apply a default
  (e.g. `application/octet-stream`); the contract does not promise
  empty-string roundtrip. `ObjectInfo.ContentType` reports the backend's
  effective value.

### 3.4 `Get` / `Stat` / `Delete` semantics

- `Get`: the caller owns closing the returned `io.ReadCloser`
  (responsibility-boundary godoc, httpclient precedent). The returned
  `ObjectInfo` is consistent with what `Stat` reports.
- `Get`/`Stat` on a missing key: return an error **wrapping
  `storage.ErrNotFound`**; callers match with
  `errors.Is(err, storage.ErrNotFound)`.
- `Delete` is **idempotent**: deleting an absent key succeeds (S3 model;
  the conformance suite asserts this). `Delete` never returns
  `ErrNotFound`.

### 3.5 Sentinel vs coded errors (aligned with `cache.ErrMiss`)

`storage.ErrNotFound` is a **domain sentinel, not a coded backend error**
— the same split `ports/cache` pins for `ErrMiss` ("a miss … is reported
as the ErrMiss sentinel, never a coded error"):

- `errorsx.CodeOf(err)` on an `ErrNotFound`-wrapping error is NOT
  guaranteed to be `CodeNotFound`; it may be `CodeUnknown`. Callers use
  `errors.Is`, never `CodeOf`, to detect missing objects.
- Any OTHER non-nil error (backend/config/transport failures) MUST be
  errorsx-coded and never `CodeUnknown`. The "never CodeUnknown" rule
  explicitly excludes the contract sentinel.

### 3.6 Validation precedence

Mirrors the cache/ratelimit/inbox pattern:

- Empty `key` ("") on `Put`/`Get`/`Stat`/`Delete` → `CodeInvalidArgument`
  BEFORE consulting ctx, BEFORE contacting the backend.
- `List` with `Limit <= 0` → same tier (`CodeInvalidArgument` before
  ctx/backend).
- Then cancelled/expired ctx, then backend errors.

### 3.7 `ETag` — opaque validator (weak promise)

- `ETag` is an **opaque backend validator**: empty string allowed (backend
  may have none); when available, the adapter exposes the backend's value
  as-is.
- The contract promises NO format, does NOT require a content hash, and
  does NOT require change-on-overwrite (S3 multipart/encryption make that
  non-portable). `storagetest` does not assert ETag behaviour beyond
  passthrough presence being allowed to be empty.

### 3.8 `PresignedURLer`

Stays an optional capability interface. godoc additions only:
`expires <= 0` → `CodeInvalidArgument`; URLs grant unauthenticated access
until expiry (caller treats them as secrets).

## 4. Phase A — `ports/storage/storagetest`

Shape mirrors `cachetest`/`inboxtest`:

```go
type Factory func(t *testing.T) storage.ObjectStorage
func RunContract(t *testing.T, newStorage Factory)
```

- Factory contract: each call returns a fresh, keyspace-isolated
  implementation (adapters may namespace on `t.Name()` and register
  `t.Cleanup`).
- Imports only `testing` + the contract + `pkg/errorsx` (never httpx) —
  adapters inherit no transport dependency.
- Deterministic and synchronous-only; a test-only in-memory reference
  implementation lives in the package and passes the suite (non-vacuity
  red-proven via temporary breakages during development, inboxtest
  precedent).

Subtests (names indicative):

1. `PutGetRoundtrip` — body, size, content type.
2. `PutOverwriteWins` — second Put replaces content; NO ETag assertion.
3. `MetadataRoundtripLowercaseKeys` — lowercase keys only; exact roundtrip
   via both `Stat` and `Get`.
4. `GetMissingReturnsErrNotFound` / `StatMissingReturnsErrNotFound` —
   `errors.Is` match; deliberately NO `CodeOf` assertion (§3.5).
5. `DeleteIsIdempotent` — delete absent key → nil; delete existing →
   subsequent `Get` is `ErrNotFound`.
6. `EmptyKeyInvalidArgument` — every key-taking method; also proven to
   precede a cancelled ctx (precedence leg).
7. `ListLimitNonPositiveInvalidArgument` — `0` and `-1`.
8. `ListTokenWalkCompleteNoDupes` — N objects, `Limit` smaller than N,
   walk to `NextToken == ""`; **set comparison, no order assertion**
   (§3.1: ordering is backend-defined).
9. `ListPrefixFilters` / `ListEmptyPrefixListsAll`.
10. `ListNoMatchReturnsEmptyPage` — empty `Objects`, `NextToken == ""`,
    no error.

`PresignedURLer` is NOT covered by the suite in v1: the in-memory
reference implementation cannot sign meaningful URLs, and a presign
assertion against it would be vacuous. Presign correctness is proven
adapter-side with real HTTP (§6).

## 5. Phase B — `storage/minio` adapter

- Path `storage/minio`, package **`miniostorage`** (naming convention:
  `rediscache`, `pgxinbox`).
- `New(client *minio.Client, bucket string, opts ...Option) (*Storage, error)`:
  - nil client / empty bucket name → constructor error
    (`CodeInvalidArgument`).
  - Does NOT auto-create the bucket — bucket provisioning is a
    deployment/caller responsibility; the CI harness creates it.
  - `opts` variadic reserved; v1 ships no options (`WithKeyPrefix`
    deliberately excluded — the bucket is the isolation unit; YAGNI until
    a consumer shows the need).
- Implements `storage.ObjectStorage` + `storage.PresignedURLer`.
- Error mapping (minio `ErrorResponse.Code`):
  - `NoSuchKey` (object missing) → wrap `storage.ErrNotFound`.
  - `NoSuchBucket` → **coded config/backend error, `CodeUnavailable` —
    explicitly NOT `ErrNotFound`** (a missing bucket is a
    deployment/configuration failure, not a missing object; conflating
    them would mask misconfiguration as cache-miss-style flow). Pinned to
    `CodeUnavailable` (the backend as deployed is unusable); the
    `CodeInternal` alternative was considered and rejected because it
    connotes an adapter bug rather than an environment precondition.
  - All other backend errors → errorsx-coded, never `CodeUnknown`.
- `List`: `ListPage.NextToken` is an **adapter-owned opaque token — NOT a
  MinIO/S3 continuation token**. Rationale (verified against minio-go
  v7.2.1 source): `minio.Core.ListObjectsV2` — the only surface exposing
  the backend continuation token — takes no ctx parameter and internally
  calls `listObjectsV2Query(context.Background(), ...)`, which would
  violate the contract's ctx-cancellation responsibility (§3.6). v1
  therefore uses the ctx-aware public API (`Client.ListObjectsIter` or
  `Client.ListObjects(ctx, bucket, ListObjectsOptions{Prefix,
  Recursive: true, MaxKeys, StartAfter})`; exact pick at plan time):
  - The adapter token encodes the `StartAfter` key; the next page resumes
    from it.
  - To decide whether a next page exists, the implementation consumes up
    to `Limit + 1` objects, returns the first `Limit`, and on lookahead
    sets `NextToken` to the last RETURNED key.
  - A malformed adapter token → `CodeInvalidArgument` (§3.1).
- Metadata: sent as user metadata on Put; on Stat/Get the adapter strips
  the transport prefix (`X-Amz-Meta-*`) and canonicalizes keys to
  lowercase (§3.2).
- ETag: backend passthrough, opaque (§3.7).
- Presign: `PresignedGetObject` / `PresignedPutObject`;
  `expires <= 0` → `CodeInvalidArgument` before contacting anything.

## 6. Testing & CI

- **Primary proof**: `storagetest.RunContract` against a real MinIO
  container (`testcontainers-go/modules/minio v0.42.0`) under
  `//go:build integration` — same pattern as cache/redis running
  `cachetest` vs `redis:7-alpine`.
- Adapter-specific integration tests:
  - Presign end-to-end: `net/http` PUT through a presigned URL, then GET
    through a presigned URL, bytes compared.
  - Pagination walk with more objects than one page (exercises a
    multi-page walk through the adapter token against a real backend).
  - `NoSuchBucket` mapping: operations against a non-existent bucket →
    `CodeUnavailable`, NOT `errors.Is(_, ErrNotFound)`.
  - Metadata canonicalization intent test: mixed-case keys on Put come
    back lowercase.
- Unit tests: constructor guards, presign expires guard, empty-key/limit
  precedence (no backend needed — guards fire pre-network).
- CI: **add/extend the existing integration step** to include
  `./storage/minio/...` — align with the repo's current workflow shape at
  implementation time; a separate job is not mandated by this spec.
- Local verification: `go build/vet/test ./...`,
  `go test -tags=integration -race ./storage/minio/...` (Docker),
  `golangci-lint run ./...` + `--build-tags=integration` (v2 binary at
  `/usr/local/bin/golangci-lint`).

## 7. Out of scope / deferred (do not re-litigate without new evidence)

- `storage/fs` and `storage/s3` sibling adapters (after MinIO
  pressure-tests the contract).
- Unknown-length (`size = -1`) streaming Put.
- `WithKeyPrefix` / key namespacing options.
- Conditional put (If-Match / If-None-Match), object versioning.
- Multipart tuning, server-side encryption options.
- Mixed-case metadata roundtrip guarantees.
- Presign coverage inside `storagetest`.
- `examples/orders` storage wiring.

## 8. Pinned decisions log (user-reviewed in-session, 2026-07-03)

1. `List` token pagination over `iter.Seq2` (core is a published module;
   remote list APIs are natively token-paged) and over document-the-limit.
2. Driver: MinIO first (real object-store semantics in CI without AWS).
3. `ErrNotFound` is an uncoded domain sentinel (cache.ErrMiss alignment);
   "never CodeUnknown" excludes it.
4. Metadata keys: lowercase-ASCII portable rule; suite asserts lowercase
   only.
5. ETag: opaque validator, empty allowed, no change-on-overwrite promise.
6. Put size: reject negative, pass declared size through, surface backend
   mismatch, never silently truncate/pad; no pre-validation requirement.
7. List ordering: backend-defined; stable walk under no concurrent
   mutation; suite compares sets.
8. `NoSuchBucket` → `CodeUnavailable` (not `ErrNotFound`, not
   `CodeInternal`).
9. `List` token is adapter-owned (`StartAfter`-encoded, `Limit + 1`
   lookahead), NOT a backend continuation token — pinned because
   minio-go v7.2.1 `Core.ListObjectsV2` takes no ctx (internally
   `context.Background()`), conflicting with the contract's ctx
   responsibility; ctx-aware public list API used instead.
