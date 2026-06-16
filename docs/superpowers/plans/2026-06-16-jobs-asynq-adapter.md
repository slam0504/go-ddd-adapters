# jobs/asynq Background-Jobs Adapter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a production `jobs/asynq` (`jobsasynq`) adapter implementing core's `ports/jobs.Enqueuer` + `Worker` over `github.com/hibiken/asynq`, passing the full (0)+(a)–(v) tag-gate acceptance suite under `go test -race` + testcontainers Redis.

**Architecture:** Two concrete types — `Enqueuer` (over `*asynq.Client`) and `Worker` (over a lazily-built `asynq.Server` with a self-managed exact-match dispatch map). Functional-options + private-config constructors that fail loud on shape, never on reachability. Reachability surfaces from `EnqueueContext` (Enqueuer) and an explicit `Run`-time PING (Worker), so criterion (h)'s fatal-startup endpoint is preserved.

**Tech Stack:** Go 1.25, `hibiken/asynq v0.24.1`, `redis/go-redis/v9` (already present), `go-ddd-core/pkg/errorsx` + `ports/jobs`, testcontainers-go redis module v0.42.0.

**Spec:** `docs/superpowers/specs/2026-06-16-jobs-asynq-adapter-design.md`.

**Reference (do NOT merge):** spike branch `spike/jobs-asynq` has verbatim-reusable test scaffolding (recorderHook, captureLogger, JobState classifier, the 3 shutdown tests). Adapt to the new constructor signatures (constructors now return `error`, options replace the `Config` struct).

---

## File Structure

| File | Responsibility |
| --- | --- |
| `jobs/asynq/options.go` | `Option`, private `config`, defaults, exported declared values, validation, error sentinels |
| `jobs/asynq/enqueuer.go` | `Enqueuer` type, `NewEnqueuer`, `Enqueue`, `Close`, `snapshot`, `classifyBackendErr` |
| `jobs/asynq/worker.go` | `Worker` type, `NewWorker`, `Register`, `Run`, `ShutdownWithin`, `probeReachable`, connopt shape guard |
| `jobs/asynq/doc.go` | package doc declaring retry/backoff/dead-letter policy, fatal-code taxonomy, horizon, durability, at-least-once |
| `jobs/asynq/options_test.go` | pure unit tests (no build tag): option validation, nil conn opt, snapshot helper, classifyBackendErr |
| `jobs/asynq/harness_test.go` | `//go:build integration` test support: container, runWorker, JobState classifier, hooks, jobstest factory |
| `jobs/asynq/contract_test.go` | `//go:build integration`: (0) RunContract + (g)(m)(q)(p)(o)(k)(u) |
| `jobs/asynq/delivery_test.go` | `//go:build integration`: (a)(b)(d)(e)(n)(r) |
| `jobs/asynq/shutdown_test.go` | `//go:build integration`: (c)(h)(j)(l)(s1)(s2)(t)(f) |
| `.github/workflows/ci.yml:77` | add `./jobs/asynq/...` to the integration step |
| `README.md` | adapter-table row + usage sketch |
| `CHANGELOG.md` | `[Unreleased]` entry |

Phases: **P1** deps+scaffold, **P2** Enqueuer, **P3** Worker, **P4** harness, **P5** acceptance suite, **P6** CI/docs, **P7** verify+PR. Each task ends in a commit.

---

## Phase 1 — Dependencies & scaffold

### Task 1: Pin dependencies

**Files:** Modify `go.mod`, `go.sum`, `examples/orders/go.mod`, `examples/orders/go.sum`

- [ ] **Step 0: Create the feature branch off main**

Run: `git checkout main && git pull && git checkout -b feat/jobs-asynq-v0.9.0`
Expected: on a fresh branch off the latest `main` (which already carries the committed spec + this plan).

- [ ] **Step 1: Bump core to the jobs pseudo-version + add asynq**

Run (root module):
```bash
go get github.com/slam0504/go-ddd-core@784ef3e
go get github.com/hibiken/asynq@v0.24.1
go mod tidy
```
Then mirror the core bump in the example module:
```bash
cd examples/orders && go get github.com/slam0504/go-ddd-core@784ef3e && go mod tidy && cd ../..
```

- [ ] **Step 2: Verify the pins resolved**

Run: `go list -m github.com/slam0504/go-ddd-core github.com/hibiken/asynq`
Expected: core shows a `v0.8.1-0.2026...-784ef3e...` pseudo-version (NOT `v0.8.0`); asynq shows `v0.24.1`.

- [ ] **Step 3: Sanity build**

Run: `go build ./...`
Expected: clean (no jobsasynq package yet, so this just proves deps resolve).

- [ ] **Step 4: Commit**

```bash
git add go.mod go.sum examples/orders/go.mod examples/orders/go.sum
git commit -m "chore(jobs/asynq): pin core@784ef3e (ports/jobs) + asynq v0.24.1"
```

---

### Task 2: options.go — config, defaults, declared values, validation, sentinels

**Files:** Create `jobs/asynq/options.go`

- [ ] **Step 1: Write the file**

```go
package jobsasynq

import (
	"time"

	"github.com/hibiken/asynq"
	"github.com/slam0504/go-ddd-core/pkg/errorsx"
)

// Exported declared values. Per acceptance criterion (v) these are the single
// source the conformance fixtures assert against — never a second copy.
const (
	// DefaultSchedulingHorizon is the default "no later than" ceiling on a
	// Job.ProcessAt. Asynq has no useful native horizon, so the adapter declares
	// one; a ProcessAt beyond it is rejected at Enqueue (CodeInvalidArgument).
	DefaultSchedulingHorizon = 30 * 24 * time.Hour
	// DefaultRetention keeps a completed task observable (Inspector) as
	// completion evidence — NOT an idempotency replay window. Asynq stores
	// retention as integer seconds, so values below 1s are rejected.
	DefaultRetention = time.Hour
	// DefaultTaskTimeout matches Asynq's own implicit 30m handler timeout; the
	// adapter sets it explicitly so the value is user-visible and documented.
	DefaultTaskTimeout = 30 * time.Minute

	// RecoverWithin is the declared upper bound for an in-flight task to resolve
	// to completed or pending-retryable after an UNGRACEFUL stop, folding in
	// Asynq v0.24.1's 30s lease + ~1min recoverer poll interval + margin.
	RecoverWithin = 3 * time.Minute
	// RedeliverWithin is the declared upper bound for a fresh Worker to actually
	// redeliver a recovered task after RecoverWithin.
	RedeliverWithin = 3 * time.Minute

	defaultQueue           = "default"
	defaultMaxRetry        = 25 // == asynq's own default
	defaultConcurrency     = 10
	defaultShutdownTimeout = 8 * time.Second
	// shutdownReturnMargin is added to the configured shutdown timeout to form
	// the declared ShutdownWithin bound (Run's own return slack after drain).
	shutdownReturnMargin = 2 * time.Second
)

// Error sentinels for constructor validation (errors.Is-able in tests).
var (
	ErrNilRedisConnOpt           = errorsx.New(errorsx.CodeInvalidArgument, "jobs/asynq: nil RedisConnOpt")
	ErrInvalidRedisConnOpt       = errorsx.New(errorsx.CodeInvalidArgument, "jobs/asynq: RedisConnOpt.MakeRedisClient did not return a redis.UniversalClient")
	ErrEmptyQueue                = errorsx.New(errorsx.CodeInvalidArgument, "jobs/asynq: queue name is blank")
	ErrSchedulingHorizonNonPos   = errorsx.New(errorsx.CodeInvalidArgument, "jobs/asynq: scheduling horizon must be > 0")
	ErrRetentionTooSmall         = errorsx.New(errorsx.CodeInvalidArgument, "jobs/asynq: retention must be >= 1s (Asynq stores integer seconds)")
	ErrTaskTimeoutNonPos         = errorsx.New(errorsx.CodeInvalidArgument, "jobs/asynq: task timeout must be > 0")
	ErrMaxRetryNegative          = errorsx.New(errorsx.CodeInvalidArgument, "jobs/asynq: max retry must be >= 0")
	ErrConcurrencyNonPos         = errorsx.New(errorsx.CodeInvalidArgument, "jobs/asynq: concurrency must be > 0")
	ErrShutdownTimeoutNonPos     = errorsx.New(errorsx.CodeInvalidArgument, "jobs/asynq: shutdown timeout must be > 0")
	ErrNilRetryDelay             = errorsx.New(errorsx.CodeInvalidArgument, "jobs/asynq: retry delay func must not be nil")
)

// Option configures Enqueuer/Worker. Options are pure setters into the private
// config; all validation runs in the constructor AFTER every option is applied,
// so option order never changes the result (mirrors the redisidempotency /
// casbinauth pattern).
type Option func(*config)

type config struct {
	queue             string
	schedulingHorizon time.Duration
	retention         time.Duration
	maxRetry          int
	taskTimeout       time.Duration
	concurrency       int
	shutdownTimeout   time.Duration
	retryDelay        asynq.RetryDelayFunc
	logger            asynq.Logger // nil = asynq default (no exported default to inject)
}

func defaultConfig() config {
	return config{
		queue:             defaultQueue,
		schedulingHorizon: DefaultSchedulingHorizon,
		retention:         DefaultRetention,
		maxRetry:          defaultMaxRetry,
		taskTimeout:       DefaultTaskTimeout,
		concurrency:       defaultConcurrency,
		shutdownTimeout:   defaultShutdownTimeout,
		retryDelay:        asynq.DefaultRetryDelayFunc,
		logger:            nil,
	}
}

// WithQueue sets the queue both halves use (default "default"). A blank name is
// rejected: Asynq drops a blank queue on the worker side and silently falls
// back to the default, so we fail loud instead.
func WithQueue(name string) Option { return func(c *config) { c.queue = name } }

// WithSchedulingHorizon overrides the ProcessAt ceiling (default 30 days). Must
// be > 0.
func WithSchedulingHorizon(d time.Duration) Option {
	return func(c *config) { c.schedulingHorizon = d }
}

// WithRetention sets how long a completed task stays observable as completion
// evidence (default 1h). Must be >= 1s (Asynq stores int64(d.Seconds()); a
// sub-second value truncates to 0 and loses the evidence).
func WithRetention(d time.Duration) Option { return func(c *config) { c.retention = d } }

// WithMaxRetry caps attempts before Asynq archives a task (default 25). Must be
// >= 0. Pairs with WithRetryDelay to make retry/dead-letter policy fully tunable.
func WithMaxRetry(n int) Option { return func(c *config) { c.maxRetry = n } }

// WithTaskTimeout sets the per-attempt handler timeout (asynq.Timeout task
// option; default 30m, matching Asynq's implicit default). Must be > 0.
func WithTaskTimeout(d time.Duration) Option { return func(c *config) { c.taskTimeout = d } }

// WithConcurrency sets the worker's max concurrent handlers (default 10). Must
// be > 0.
func WithConcurrency(n int) Option { return func(c *config) { c.concurrency = n } }

// WithShutdownTimeout bounds the graceful drain on Run cancellation (default
// 8s). Must be > 0. The declared ShutdownWithin is this plus a fixed return
// margin (see Worker.ShutdownWithin).
func WithShutdownTimeout(d time.Duration) Option { return func(c *config) { c.shutdownTimeout = d } }

// WithRetryDelay sets the backoff schedule (default asynq.DefaultRetryDelayFunc).
// Must not be nil.
func WithRetryDelay(fn asynq.RetryDelayFunc) Option { return func(c *config) { c.retryDelay = fn } }

// WithLogger captures Asynq's internal logging (default: Asynq's own logger).
// Passing nil reverts to the Asynq default (no error).
func WithLogger(l asynq.Logger) Option { return func(c *config) { c.logger = l } }

// validateCommon validates fields both halves share.
func (c config) validateCommon() error {
	if strimEmpty(c.queue) {
		return ErrEmptyQueue
	}
	return nil
}

// validateEnqueuer validates Enqueuer-relevant fields.
func (c config) validateEnqueuer() error {
	if err := c.validateCommon(); err != nil {
		return err
	}
	if c.schedulingHorizon <= 0 {
		return ErrSchedulingHorizonNonPos
	}
	if c.retention < time.Second {
		return ErrRetentionTooSmall
	}
	if c.maxRetry < 0 {
		return ErrMaxRetryNegative
	}
	if c.taskTimeout <= 0 {
		return ErrTaskTimeoutNonPos
	}
	return nil
}

// validateWorker validates Worker-relevant fields.
func (c config) validateWorker() error {
	if err := c.validateCommon(); err != nil {
		return err
	}
	if c.concurrency <= 0 {
		return ErrConcurrencyNonPos
	}
	if c.shutdownTimeout <= 0 {
		return ErrShutdownTimeoutNonPos
	}
	if c.retryDelay == nil {
		return ErrNilRetryDelay
	}
	return nil
}
```

- [ ] **Step 2: Add the blank-string helper at the bottom of the file**

```go
import "strings" // add to the import block

func strimEmpty(s string) bool { return strings.TrimSpace(s) == "" }
```
(Place the `strings` import in the existing import block; the helper goes after `validateWorker`.)

- [ ] **Step 3: Build**

Run: `go build ./jobs/asynq/`
Expected: FAIL — `Enqueuer`/`Worker` referenced nowhere yet is fine, but the package has no other file; `go build` of a package with only options.go should succeed. Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add jobs/asynq/options.go
git commit -m "feat(jobs/asynq): options, defaults, declared values, validation"
```

---

## Phase 2 — Enqueuer

### Task 3: enqueuer.go — NewEnqueuer, Enqueue, snapshot, backend classifier

**Files:** Create `jobs/asynq/enqueuer.go`

- [ ] **Step 1: Write the file**

```go
package jobsasynq

import (
	"context"
	"errors"
	"net"
	"strings"

	"github.com/hibiken/asynq"
	goredis "github.com/redis/go-redis/v9"
	"github.com/slam0504/go-ddd-core/pkg/errorsx"
	"github.com/slam0504/go-ddd-core/ports/jobs"
)

// Enqueuer implements jobs.Enqueuer over an *asynq.Client. Safe for concurrent
// use (asynq.Client is).
type Enqueuer struct {
	client            *asynq.Client
	queue             string
	schedulingHorizon timeDuration
	retention         timeDuration
	maxRetry          int
	taskTimeout       timeDuration
}

// timeDuration aliases time.Duration to keep the struct compact in the plan;
// in the real file just use time.Duration directly (and import "time").
type timeDuration = durationAlias

// NewEnqueuer builds an Enqueuer. The required dependency (RedisConnOpt) is a
// positional param; optional policy is functional options. Validation is
// shape-only — NEVER a reachability probe (a New-time Ping would swallow the
// unreachable-backend signal that Enqueue must surface per the contract).
func NewEnqueuer(r asynq.RedisConnOpt, opts ...Option) (*Enqueuer, error) {
	if r == nil {
		return nil, ErrNilRedisConnOpt
	}
	if err := validateConnOptShape(r); err != nil {
		return nil, err
	}
	cfg := defaultConfig()
	for _, o := range opts {
		o(&cfg)
	}
	if err := cfg.validateEnqueuer(); err != nil {
		return nil, err
	}
	return &Enqueuer{
		client:            asynq.NewClient(r),
		queue:             cfg.queue,
		schedulingHorizon: cfg.schedulingHorizon,
		retention:         cfg.retention,
		maxRetry:          cfg.maxRetry,
		taskTimeout:       cfg.taskTimeout,
	}, nil
}

// Close releases the underlying asynq client.
func (e *Enqueuer) Close() error { return e.client.Close() }

// Enqueue snapshots the job (before any backend I/O) and submits it. Fixed
// precedence: (1a) empty Type → (1b) out-of-horizon ProcessAt → (2a) ctx entry
// check → (2b) backend.
func (e *Enqueuer) Enqueue(ctx context.Context, job jobs.Job) (jobs.JobInfo, error) {
	// Class 1a.
	if job.Type == "" {
		return jobs.JobInfo{}, errorsx.New(errorsx.CodeInvalidArgument, "jobs/asynq: empty job type")
	}
	// Class 1b. Evaluated before observing ctx or backend.
	if !job.ProcessAt.IsZero() && job.ProcessAt.After(timeNow().Add(e.schedulingHorizon)) {
		return jobs.JobInfo{}, errorsx.New(errorsx.CodeInvalidArgument, "jobs/asynq: ProcessAt beyond scheduling horizon")
	}
	// snapshot-before-submit.
	payload := snapshot(job.Payload)
	// Class 2a: observe ctx before touching the backend.
	if err := ctx.Err(); err != nil {
		return jobs.JobInfo{}, err
	}
	opts := []asynq.Option{
		asynq.Queue(e.queue),
		asynq.MaxRetry(e.maxRetry),
		asynq.Timeout(e.taskTimeout),
		asynq.Retention(e.retention),
	}
	if !job.ProcessAt.IsZero() {
		opts = append(opts, asynq.ProcessAt(job.ProcessAt))
	}
	info, err := e.client.EnqueueContext(ctx, asynq.NewTask(job.Type, payload), opts...)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return jobs.JobInfo{}, err
		}
		return jobs.JobInfo{}, errorsx.Wrap(classifyBackendErr(err), "jobs/asynq: enqueue failed", err)
	}
	return jobs.JobInfo{ID: info.ID}, nil
}

// snapshot returns a private copy of b. A nil and an empty slice are equivalent
// (both deliver a zero-length payload).
func snapshot(b []byte) []byte {
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

// classifyBackendErr maps a non-ctx backend error to a coded errorsx Code,
// NEVER CodeUnknown. Network/connection failures → CodeUnavailable; anything
// else unclassifiable → CodeInternal.
func classifyBackendErr(err error) errorsx.Code {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return errorsx.CodeUnavailable
	}
	msg := err.Error()
	for _, s := range []string{"connection refused", "no such host", "i/o timeout", "dial tcp", "connect:", "EOF", "broken pipe", "reset by peer"} {
		if strings.Contains(msg, s) {
			return errorsx.CodeUnavailable
		}
	}
	return errorsx.CodeInternal
}

// validateConnOptShape rejects a RedisConnOpt whose MakeRedisClient does not
// yield a redis.UniversalClient (the type Asynq would panic on). It builds and
// immediately closes a client; go-redis is lazy so this opens no connection.
func validateConnOptShape(r asynq.RedisConnOpt) error {
	c, ok := r.MakeRedisClient().(goredis.UniversalClient)
	if !ok {
		return ErrInvalidRedisConnOpt
	}
	_ = c.Close()
	return nil
}
```

- [ ] **Step 2: Replace the plan's `timeDuration`/`durationAlias`/`timeNow` placeholders**

In the real file, delete the `timeDuration`/`durationAlias` alias lines, import `"time"`, declare the struct fields as `time.Duration`, and add a package-level clock indirection so tests can stay on the real clock:
```go
import "time"
// fields: schedulingHorizon, retention, taskTimeout are time.Duration
var timeNow = time.Now // package-level for clarity; not overridden in tests
```
(No clock injection is needed — horizon/scheduling tests use real near/far times against the real clock. `timeNow` is just `time.Now` aliased for readability; you may inline `time.Now()` instead and drop the var.)

- [ ] **Step 3: Build**

Run: `go build ./jobs/asynq/`
Expected: PASS (Enqueuer compiles; Worker still absent but not referenced).

- [ ] **Step 4: Commit**

```bash
git add jobs/asynq/enqueuer.go
git commit -m "feat(jobs/asynq): Enqueuer with two-class errors, horizon, snapshot"
```

---

## Phase 3 — Worker

### Task 4: worker.go — NewWorker, Register, Run, ShutdownWithin, probeReachable

**Files:** Create `jobs/asynq/worker.go`

- [ ] **Step 1: Write the file**

```go
package jobsasynq

import (
	"context"
	"sync"
	"time"

	"github.com/hibiken/asynq"
	goredis "github.com/redis/go-redis/v9"
	"github.com/slam0504/go-ddd-core/pkg/errorsx"
	"github.com/slam0504/go-ddd-core/ports/jobs"
)

// Worker implements jobs.Worker over an asynq.Server built lazily in Run. The
// server is built per Run call (Run is once-per-instance). Dispatch uses a
// self-managed EXACT-match map, not asynq.ServeMux (whose longest-prefix
// matching would violate exact-type-match).
type Worker struct {
	redis           asynq.RedisConnOpt
	queue           string
	concurrency     int
	shutdownTimeout time.Duration
	retryDelay      asynq.RetryDelayFunc
	logger          asynq.Logger

	mu       sync.Mutex
	handlers map[string]jobs.Handler
}

// NewWorker builds a Worker. Shape-only validation (no reachability probe — see
// NewEnqueuer / criterion h rationale in the spec).
func NewWorker(r asynq.RedisConnOpt, opts ...Option) (*Worker, error) {
	if r == nil {
		return nil, ErrNilRedisConnOpt
	}
	if err := validateConnOptShape(r); err != nil {
		return nil, err
	}
	cfg := defaultConfig()
	for _, o := range opts {
		o(&cfg)
	}
	if err := cfg.validateWorker(); err != nil {
		return nil, err
	}
	return &Worker{
		redis:           r,
		queue:           cfg.queue,
		concurrency:     cfg.concurrency,
		shutdownTimeout: cfg.shutdownTimeout,
		retryDelay:      cfg.retryDelay,
		logger:          cfg.logger,
		handlers:        map[string]jobs.Handler{},
	}, nil
}

// ShutdownWithin is the declared bound (criterion v) the conformance fixtures
// assert Run returns within after cancellation: the configured graceful drain
// plus Run's own return margin.
func (w *Worker) ShutdownWithin() time.Duration { return w.shutdownTimeout + shutdownReturnMargin }

// Register routes tasks of jobType to h. Argument validation precedes the
// duplicate check (fixed precedence).
func (w *Worker) Register(jobType string, h jobs.Handler) error {
	if jobType == "" {
		return errorsx.New(errorsx.CodeInvalidArgument, "jobs/asynq: empty job type")
	}
	if h == nil {
		return errorsx.New(errorsx.CodeInvalidArgument, "jobs/asynq: nil handler")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.handlers[jobType]; ok {
		return errorsx.New(errorsx.CodeAlreadyExists, "jobs/asynq: handler already registered for type "+jobType)
	}
	w.handlers[jobType] = h
	return nil
}

// Run dispatches until ctx is cancelled, then returns nil. A pre-start fatal
// (unreachable backend) returns a coded errorsx, never a ctx error.
func (w *Worker) Run(ctx context.Context) error {
	if ctx.Err() != nil {
		return nil // already cancelled: return nil without starting
	}
	// Reachability probe BEFORE Start: asynq.Server.Start does not fail
	// synchronously on an unreachable Redis (it logs + retries in the
	// background), so the fatal-startup endpoint (criterion h) must come from
	// here.
	if err := w.probeReachable(ctx); err != nil {
		return err
	}
	srv := asynq.NewServer(w.redis, asynq.Config{
		Concurrency:     w.concurrency,
		Queues:          map[string]int{w.queue: 1},
		ShutdownTimeout: w.shutdownTimeout,
		RetryDelayFunc:  w.retryDelay,
		Logger:          w.logger,
		// Handler ctx derives from Run's ctx, so cancelling Run cancels every
		// in-flight handler ctx (criterion j). Asynq's own shutdown does NOT
		// cancel handler ctx; it only waits then aborts/requeues.
		BaseContext: func() context.Context { return ctx },
	})
	root := asynq.HandlerFunc(func(hctx context.Context, t *asynq.Task) error {
		w.mu.Lock()
		h, ok := w.handlers[t.Type()] // exact match only
		w.mu.Unlock()
		if !ok {
			// Unhandled type: never acked as success. Asynq's policy retries per
			// the schedule then archives (documented unhandled-job policy).
			return errorsx.New(errorsx.CodeInternal, "jobs/asynq: no handler registered for type "+t.Type())
		}
		id, _ := asynq.GetTaskID(hctx)
		return h.Handle(hctx, jobs.Task{ID: id, Type: t.Type(), Payload: snapshot(t.Payload())})
	})
	if err := srv.Start(root); err != nil {
		// Independent fatal, no cancellation: coded error, never a ctx error.
		return errorsx.Wrap(classifyBackendErr(err), "jobs/asynq: worker startup failed", err)
	}
	<-ctx.Done()
	// Cancellation endpoint: commit to nil. srv.Shutdown drains up to
	// ShutdownTimeout then aborts/requeues; any requeue/teardown failure is
	// Asynq-internal (logged via w.logger), never changes the return.
	srv.Shutdown()
	return nil
}

// probeReachable PINGs Redis via a short-lived client built from the conn opt.
// Unreachable → coded CodeUnavailable (criterion h). It does NOT reuse the
// server connection (the server is built only on success).
func (w *Worker) probeReachable(ctx context.Context) error {
	c, ok := w.redis.MakeRedisClient().(goredis.UniversalClient)
	if !ok {
		return ErrInvalidRedisConnOpt
	}
	defer func() { _ = c.Close() }()
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := c.Ping(pctx).Err(); err != nil {
		return errorsx.Wrap(errorsx.CodeUnavailable, "jobs/asynq: backend unreachable", err)
	}
	return nil
}
```

- [ ] **Step 2: Build + vet**

Run: `go build ./jobs/asynq/ && go vet ./jobs/asynq/`
Expected: PASS. Both `*Enqueuer` and `*Worker` now satisfy the core interfaces.

- [ ] **Step 3: Add a compile-time interface assertion at the end of worker.go**

```go
var (
	_ jobs.Enqueuer = (*Enqueuer)(nil)
	_ jobs.Worker   = (*Worker)(nil)
)
```
Run: `go build ./jobs/asynq/`
Expected: PASS (proves the contracts are satisfied).

- [ ] **Step 4: Commit**

```bash
git add jobs/asynq/worker.go
git commit -m "feat(jobs/asynq): Worker with exact-match dispatch, BaseContext, Run-time reachability probe"
```

---

### Task 5: doc.go — declared policy surface

**Files:** Create `jobs/asynq/doc.go`

- [ ] **Step 1: Write the file**

```go
// Package jobsasynq implements the go-ddd-core ports/jobs Enqueuer and Worker
// contracts over github.com/hibiken/asynq, a Redis-backed task queue.
//
// # Construction
//
// NewEnqueuer and NewWorker take the required asynq.RedisConnOpt positionally
// and optional policy as functional options (WithQueue, WithSchedulingHorizon,
// WithRetention, WithMaxRetry, WithTaskTimeout, WithConcurrency,
// WithShutdownTimeout, WithRetryDelay, WithLogger). All validation runs after
// options are applied; a bad RedisConnOpt SHAPE fails at construction, but
// backend REACHABILITY is never probed at construction — an unreachable backend
// surfaces from Enqueue (CodeUnavailable) and from Worker.Run (CodeUnavailable),
// preserving the contract's fatal-startup endpoint.
//
// # Declared policy (the contract delegates these to adapters)
//
//   - Retry/backoff: asynq.DefaultRetryDelayFunc unless WithRetryDelay overrides;
//     attempts capped by WithMaxRetry (default 25).
//   - Dead-letter: after the retry cap, Asynq ARCHIVES the task (it is retained,
//     inspectable, and manually retryable — not silently dropped).
//   - Unhandled-job policy: a task whose Type has no registered Handler is never
//     acked as success; the handler returns an error, so Asynq retries it per the
//     schedule and then archives it. Registering every enqueueable Type during
//     wiring (a homogeneous worker pool) is the caller's deployment precondition.
//   - Scheduling horizon: a Job.ProcessAt later than now+horizon (default 30
//     days, WithSchedulingHorizon) is rejected at Enqueue with
//     CodeInvalidArgument — never accepted then dropped.
//   - Per-attempt timeout: handlers get the asynq.Timeout deadline (default 30m,
//     WithTaskTimeout). Exceeding it expires the handler ctx; the contract treats
//     a ctx-error return from Handle as an ordinary failed attempt.
//   - Durability boundary: jobs are as durable as the backing Redis is
//     configured to be (RDB/AOF persistence is the operator's responsibility).
//     Loss beyond that boundary is the contract's prerequisite-(5) durable loss.
//
// # Fatal-code taxonomy (Worker.Run / Enqueue backend errors)
//
//   - Unreachable backend → errorsx.CodeUnavailable.
//   - Any other non-ctx backend failure → a coded errorsx whose CodeOf is never
//     CodeUnknown (unclassifiable → CodeInternal), so transport adapters can
//     always translate it.
//
// # Delivery guarantee
//
// At-least-once. A handler may run more than once (crash, lease expiry, shutdown
// before ack), possibly concurrently with a stalled earlier attempt, so handler
// effects must be idempotent. Asynq's lease is 30s; its recoverer reclaims
// leases expired at least 30s and polls about once a minute — folded into the
// exported RecoverWithin / RedeliverWithin bounds for conformance tests.
package jobsasynq
```

- [ ] **Step 2: Build**

Run: `go build ./jobs/asynq/`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add jobs/asynq/doc.go
git commit -m "docs(jobs/asynq): package doc declaring retry/dead-letter/horizon/durability policy"
```

---

## Phase 4 — Unit tests & integration harness

### Task 6: options_test.go — pure unit tests (no Docker)

**Files:** Create `jobs/asynq/options_test.go`

These run under plain `go test` (no build tag): option validation, nil/invalid conn opt, snapshot helper, classifyBackendErr. They use a fake `asynq.RedisConnOpt` so no Redis is touched.

- [ ] **Step 1: Write the file**

```go
package jobsasynq

import (
	"errors"
	"net"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	goredis "github.com/redis/go-redis/v9"
	"github.com/slam0504/go-ddd-core/pkg/errorsx"
)

// okConnOpt is a shape-valid RedisConnOpt that never connects (go-redis lazy).
var okConnOpt = asynq.RedisClientOpt{Addr: "127.0.0.1:6379"}

// badConnOpt returns a non-UniversalClient to exercise the shape guard.
type badConnOpt struct{}

func (badConnOpt) MakeRedisClient() interface{} { return "not a client" }

func TestNewEnqueuer_NilConnOpt(t *testing.T) {
	if _, err := NewEnqueuer(nil); !errors.Is(err, ErrNilRedisConnOpt) {
		t.Fatalf("nil conn opt: err = %v, want ErrNilRedisConnOpt", err)
	}
}

func TestNewWorker_BadConnOptShape(t *testing.T) {
	if _, err := NewWorker(badConnOpt{}); !errors.Is(err, ErrInvalidRedisConnOpt) {
		t.Fatalf("bad conn opt: err = %v, want ErrInvalidRedisConnOpt", err)
	}
}

func TestNewEnqueuer_OptionValidation(t *testing.T) {
	cases := []struct {
		name string
		opt  Option
		want error
	}{
		{"blank queue", WithQueue("  "), ErrEmptyQueue},
		{"non-positive horizon", WithSchedulingHorizon(0), ErrSchedulingHorizonNonPos},
		{"sub-second retention", WithRetention(999 * time.Millisecond), ErrRetentionTooSmall},
		{"negative max retry", WithMaxRetry(-1), ErrMaxRetryNegative},
		{"non-positive task timeout", WithTaskTimeout(0), ErrTaskTimeoutNonPos},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewEnqueuer(okConnOpt, tc.opt); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestNewWorker_OptionValidation(t *testing.T) {
	cases := []struct {
		name string
		opt  Option
		want error
	}{
		{"non-positive concurrency", WithConcurrency(0), ErrConcurrencyNonPos},
		{"non-positive shutdown timeout", WithShutdownTimeout(0), ErrShutdownTimeoutNonPos},
		{"nil retry delay", WithRetryDelay(nil), ErrNilRetryDelay},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewWorker(okConnOpt, tc.opt); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestNewEnqueuer_Defaults(t *testing.T) {
	e, err := NewEnqueuer(okConnOpt)
	if err != nil {
		t.Fatalf("NewEnqueuer: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	if e.schedulingHorizon != DefaultSchedulingHorizon {
		t.Fatalf("horizon = %v, want %v", e.schedulingHorizon, DefaultSchedulingHorizon)
	}
	if e.retention != DefaultRetention {
		t.Fatalf("retention = %v, want %v", e.retention, DefaultRetention)
	}
}

func TestSnapshot_IsolatesCaller(t *testing.T) {
	src := []byte("abc")
	got := snapshot(src)
	src[0] = 'X'
	if string(got) != "abc" {
		t.Fatalf("snapshot not isolated: got %q", got)
	}
	if got := snapshot(nil); len(got) != 0 {
		t.Fatalf("snapshot(nil) len = %d, want 0", len(got))
	}
}

func TestClassifyBackendErr(t *testing.T) {
	netErr := &net.OpError{Op: "dial", Err: errors.New("connection refused")}
	if got := classifyBackendErr(netErr); got != errorsx.CodeUnavailable {
		t.Fatalf("net.Error → %v, want CodeUnavailable", got)
	}
	if got := classifyBackendErr(errors.New("connection refused by host")); got != errorsx.CodeUnavailable {
		t.Fatalf("connection-refused string → %v, want CodeUnavailable", got)
	}
	if got := classifyBackendErr(errors.New("some weird logical failure")); got != errorsx.CodeInternal {
		t.Fatalf("unclassifiable → %v, want CodeInternal (never CodeUnknown)", got)
	}
	// guard: classify never yields CodeUnknown.
	if got := classifyBackendErr(goredis.Nil); got == errorsx.CodeUnknown {
		t.Fatal("classify yielded CodeUnknown")
	}
}
```

- [ ] **Step 2: Run unit tests**

Run: `go test -race ./jobs/asynq/`
Expected: PASS (all of the above; no Docker needed).

- [ ] **Step 3: Lint**

Run: `golangci-lint run ./jobs/asynq/` (use the repo's v2 binary at `/usr/local/bin/golangci-lint`; the `~/go/bin` v1.64.8 chokes on the v2 config).
Expected: 0 issues. If goimports groups are flagged, regroup so `go-ddd-core/...` and `go-ddd-adapters/...` are separate from third-party.

- [ ] **Step 4: Commit**

```bash
git add jobs/asynq/options_test.go
git commit -m "test(jobs/asynq): unit tests for option validation, snapshot, error classifier"
```

---

### Task 7: harness_test.go — integration test support

**Files:** Create `jobs/asynq/harness_test.go`

Adapted verbatim from the spike's validated scaffolding, updated for the new
constructors (which return `error`). Provides: container start, `runWorker`,
`assertRunNilWithin`, the `JobState` classifier, the go-redis hook recorder, the
asynq log capturer, the `hookedConnOpt`, and the `jobstest` Backend factory.

- [ ] **Step 1: Write the file**

```go
//go:build integration

package jobsasynq_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	goredis "github.com/redis/go-redis/v9"
	"github.com/slam0504/go-ddd-core/ports/jobs"
	"github.com/slam0504/go-ddd-core/ports/jobs/jobstest"
	"github.com/testcontainers/testcontainers-go"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	jobsasynq "github.com/slam0504/go-ddd-adapters/jobs/asynq"
)

// startRedisContainer starts a real Redis. Per the merge gate, Docker being
// unavailable is a FAILURE, never a skip.
func startRedisContainer(t *testing.T) (testcontainers.Container, string) {
	t.Helper()
	ctx := context.Background()
	rc, err := tcredis.Run(ctx, "redis:7-alpine")
	if err != nil {
		t.Fatalf("testcontainers redis (Docker unavailable counts as gate failure, not skip): %v", err)
	}
	t.Cleanup(func() { _ = rc.Terminate(context.Background()) })
	addr, err := rc.Endpoint(ctx, "")
	if err != nil {
		t.Fatalf("redis endpoint: %v", err)
	}
	return rc, addr
}

func mustEnqueuer(t *testing.T, addr string, opts ...jobsasynq.Option) *jobsasynq.Enqueuer {
	t.Helper()
	e, err := jobsasynq.NewEnqueuer(asynq.RedisClientOpt{Addr: addr}, opts...)
	if err != nil {
		t.Fatalf("NewEnqueuer: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

func mustWorker(t *testing.T, r asynq.RedisConnOpt, opts ...jobsasynq.Option) *jobsasynq.Worker {
	t.Helper()
	w, err := jobsasynq.NewWorker(r, opts...)
	if err != nil {
		t.Fatalf("NewWorker: %v", err)
	}
	return w
}

func runWorker(t *testing.T, w *jobsasynq.Worker) (cancel func(), done chan error) {
	t.Helper()
	ctx, c := context.WithCancel(context.Background())
	done = make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	return c, done
}

func assertRunNilWithin(t *testing.T, done chan error, bound time.Duration, what string) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%s: Run returned %v, want nil", what, err)
		}
	case <-time.After(bound):
		t.Fatalf("%s: Run did not return within declared bound %v", what, bound)
	}
}

// JobState classification for criterion (s).
type jobState int

const (
	stateCompleted jobState = iota
	statePendingRetryable
	stateActiveLeased
	stateLostDiscarded
	stateOther
)

func classifyJob(t *testing.T, insp *asynq.Inspector, queue, id string) jobState {
	t.Helper()
	info, err := insp.GetTaskInfo(queue, id)
	if err != nil {
		if errors.Is(err, asynq.ErrTaskNotFound) {
			return stateLostDiscarded // no completion evidence (retention gives it otherwise)
		}
		t.Fatalf("GetTaskInfo: %v", err)
	}
	switch info.State {
	case asynq.TaskStateCompleted:
		return stateCompleted
	case asynq.TaskStatePending, asynq.TaskStateRetry, asynq.TaskStateScheduled:
		return statePendingRetryable
	case asynq.TaskStateActive:
		return stateActiveLeased
	default:
		return stateOther
	}
}

// recorderHook records failed redis commands while armed (criterion-2 fault
// evidence). redis.Nil is a reply, not a failure.
type recorderHook struct {
	armed  atomic.Bool
	mu     sync.Mutex
	failed []string
}

func (h *recorderHook) record(cmd goredis.Cmder, err error) {
	if err == nil || errors.Is(err, goredis.Nil) || !h.armed.Load() {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failed = append(h.failed, fmt.Sprint(cmd.Args()))
}
func (h *recorderHook) DialHook(next goredis.DialHook) goredis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) { return next(ctx, network, addr) }
}
func (h *recorderHook) ProcessHook(next goredis.ProcessHook) goredis.ProcessHook {
	return func(ctx context.Context, cmd goredis.Cmder) error { err := next(ctx, cmd); h.record(cmd, err); return err }
}
func (h *recorderHook) ProcessPipelineHook(next goredis.ProcessPipelineHook) goredis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []goredis.Cmder) error {
		err := next(ctx, cmds)
		if err != nil {
			for _, cmd := range cmds {
				h.record(cmd, err)
			}
		}
		return err
	}
}
func (h *recorderHook) failedTouchingActive() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, s := range h.failed {
		if strings.Contains(s, ":active") {
			out = append(out, s)
		}
	}
	return out
}

// hookedConnOpt is a custom RedisConnOpt whose client carries a hook.
type hookedConnOpt struct {
	addr string
	hook goredis.Hook
}

func (o hookedConnOpt) MakeRedisClient() interface{} {
	c := goredis.NewClient(&goredis.Options{Addr: o.addr})
	c.AddHook(o.hook)
	return c
}

// captureLogger records asynq's internal log lines while armed.
type captureLogger struct {
	armed   atomic.Bool
	mu      sync.Mutex
	entries []string
}

func (l *captureLogger) log(args ...interface{}) {
	if !l.armed.Load() {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, fmt.Sprint(args...))
}
func (l *captureLogger) Debug(a ...interface{}) { l.log(a...) }
func (l *captureLogger) Info(a ...interface{})  { l.log(a...) }
func (l *captureLogger) Warn(a ...interface{})  { l.log(a...) }
func (l *captureLogger) Error(a ...interface{}) { l.log(a...) }
func (l *captureLogger) Fatal(a ...interface{}) { l.log(a...) }
func (l *captureLogger) matching(subs ...string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, e := range l.entries {
		low := strings.ToLower(e)
		for _, s := range subs {
			if strings.Contains(low, s) {
				out = append(out, e)
				break
			}
		}
	}
	return out
}

// newInspector builds an Inspector for a plain addr.
func newInspector(t *testing.T, addr string) *asynq.Inspector {
	t.Helper()
	insp := asynq.NewInspector(asynq.RedisClientOpt{Addr: addr})
	t.Cleanup(func() { _ = insp.Close() })
	return insp
}
```

- [ ] **Step 2: Build the integration test binary (compile only)**

Run: `go test -tags=integration -run=XXX_none ./jobs/asynq/`
Expected: compiles, runs 0 tests (`-run` matches nothing). If Docker is required by other files in the package, this still only compiles; PASS with "no tests to run".

- [ ] **Step 3: Commit**

```bash
git add jobs/asynq/harness_test.go
git commit -m "test(jobs/asynq): integration harness (container, JobState, hooks, logger)"
```

---

## Phase 5 — Acceptance suite (0)+(a)–(v)

All files `//go:build integration`, package `jobsasynq_test`, using the Task 7 harness.

### Task 8: contract_test.go — (0) RunContract + (g)(m)(q)(p)(o)(k)(u)

**Files:** Create `jobs/asynq/contract_test.go`

- [ ] **Step 1: Write the file**

```go
//go:build integration

package jobsasynq_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/slam0504/go-ddd-core/pkg/errorsx"
	"github.com/slam0504/go-ddd-core/ports/jobs"
	"github.com/slam0504/go-ddd-core/ports/jobs/jobstest"

	jobsasynq "github.com/slam0504/go-ddd-adapters/jobs/asynq"
)

// (0) jobstest.RunContract over a real backend. Each subtest gets an isolated
// queue (fresh container is overkill; a per-call unique queue isolates state).
func TestContract_RunContract(t *testing.T) {
	_, addr := startRedisContainer(t)
	var n int
	factory := func(t *testing.T) jobstest.Backend {
		n++
		q := fmt-ish(n) // see Step 2: replace with fmt.Sprintf("contract-%d", n)
		e := mustEnqueuer(t, addr, jobsasynq.WithQueue(q))
		w := mustWorker(t, asynq.RedisClientOpt{Addr: addr}, jobsasynq.WithQueue(q))
		return jobstest.Backend{Enqueuer: e, Worker: w}
	}
	jobstest.RunContract(t, factory)
}

// (g) malformed Enqueue writes nothing — verified via Inspector queue counts.
func TestContract_MalformedEnqueueWritesNothing(t *testing.T) {
	_, addr := startRedisContainer(t)
	insp := newInspector(t, addr)
	e := mustEnqueuer(t, addr, jobsasynq.WithQueue("g-queue"))

	if _, err := e.Enqueue(context.Background(), jobs.Job{Type: ""}); errorsx.CodeOf(err) != errorsx.CodeInvalidArgument {
		t.Fatalf("empty type: code = %v", errorsx.CodeOf(err))
	}
	qs, err := insp.GetQueueInfo("g-queue")
	if err == nil && qs.Size != 0 {
		t.Fatalf("queue size = %d after malformed enqueue, want 0", qs.Size)
	}
	// ErrQueueNotFound is also acceptable evidence nothing was written.
	if err != nil && !errors.Is(err, asynq.ErrQueueNotFound) {
		t.Fatalf("GetQueueInfo: %v", err)
	}
}

// (m) out-of-horizon ProcessAt rejected at Enqueue, precedence over cancelled ctx.
func TestContract_OutOfHorizonRejected(t *testing.T) {
	_, addr := startRedisContainer(t)
	e := mustEnqueuer(t, addr, jobsasynq.WithQueue("m-queue"))

	far := time.Now().Add(jobsasynq.DefaultSchedulingHorizon + time.Hour)
	info, err := e.Enqueue(context.Background(), jobs.Job{Type: "m:far", ProcessAt: far})
	if errorsx.CodeOf(err) != errorsx.CodeInvalidArgument {
		t.Fatalf("far ProcessAt: code = %v, want CodeInvalidArgument", errorsx.CodeOf(err))
	}
	if info.ID != "" {
		t.Fatalf("error carried non-zero JobInfo.ID %q", info.ID)
	}
	// Precedence: out-of-horizon (class 1b) beats a cancelled ctx (class 2a).
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = e.Enqueue(ctx, jobs.Job{Type: "m:far", ProcessAt: far})
	if errorsx.CodeOf(err) != errorsx.CodeInvalidArgument {
		t.Fatalf("far + cancelled: code = %v, want CodeInvalidArgument", errorsx.CodeOf(err))
	}
	if errors.Is(err, context.Canceled) {
		t.Fatal("far + cancelled returned ctx error; class-1b must win")
	}
}

// (p) past ProcessAt is immediately eligible (accepted, then delivered).
func TestContract_PastProcessAtEligible(t *testing.T) {
	_, addr := startRedisContainer(t)
	e := mustEnqueuer(t, addr, jobsasynq.WithQueue("p-queue"))
	w := mustWorker(t, asynq.RedisClientOpt{Addr: addr}, jobsasynq.WithQueue("p-queue"))

	got := make(chan struct{}, 1)
	if err := w.Register("p:past", jobs.HandlerFunc(func(context.Context, jobs.Task) error {
		got <- struct{}{}
		return nil
	})); err != nil {
		t.Fatalf("Register: %v", err)
	}
	cancel, done := runWorker(t, w)
	defer func() { cancel(); assertRunNilWithin(t, done, w.ShutdownWithin(), "p shutdown") }()

	if _, err := e.Enqueue(context.Background(), jobs.Job{Type: "p:past", ProcessAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	select {
	case <-got:
	case <-time.After(30 * time.Second):
		t.Fatal("past-ProcessAt job was not delivered promptly")
	}
}

// (q) within class 2, ctx is observed before the backend — both ctx-error kinds,
// even when the backend is unavailable (bad addr).
func TestContract_CtxPrecedesBackend(t *testing.T) {
	e, err := jobsasynq.NewEnqueuer(asynq.RedisClientOpt{Addr: "127.0.0.1:6390"}) // nothing listening
	if err != nil {
		t.Fatalf("NewEnqueuer: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })

	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.Enqueue(cctx, jobs.Job{Type: "q:c"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled + unavailable: err = %v, want Canceled (no backend contact)", err)
	}
	dctx, dcancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer dcancel()
	if _, err := e.Enqueue(dctx, jobs.Job{Type: "q:d"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pre-expired + unavailable: err = %v, want DeadlineExceeded", err)
	}
}

// (o) duplicate Register keeps the original handler: h1 receives, not h2.
func TestContract_DuplicateRegisterKeepsOriginal(t *testing.T) {
	_, addr := startRedisContainer(t)
	e := mustEnqueuer(t, addr, jobsasynq.WithQueue("o-queue"))
	w := mustWorker(t, asynq.RedisClientOpt{Addr: addr}, jobsasynq.WithQueue("o-queue"))

	h1Got := make(chan struct{}, 1)
	h2Got := make(chan struct{}, 1)
	if err := w.Register("o:dup", jobs.HandlerFunc(func(context.Context, jobs.Task) error { h1Got <- struct{}{}; return nil })); err != nil {
		t.Fatalf("Register h1: %v", err)
	}
	if err := w.Register("o:dup", jobs.HandlerFunc(func(context.Context, jobs.Task) error { h2Got <- struct{}{}; return nil })); errorsx.CodeOf(err) != errorsx.CodeAlreadyExists {
		t.Fatalf("duplicate Register: code = %v, want CodeAlreadyExists", errorsx.CodeOf(err))
	}
	cancel, done := runWorker(t, w)
	defer func() { cancel(); assertRunNilWithin(t, done, w.ShutdownWithin(), "o shutdown") }()

	if _, err := e.Enqueue(context.Background(), jobs.Job{Type: "o:dup"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	select {
	case <-h1Got:
	case <-h2Got:
		t.Fatal("h2 received; the original handler must stay installed")
	case <-time.After(30 * time.Second):
		t.Fatal("neither handler received")
	}
}

// (k) exact-type-match dispatch rejects prefixes: a handler for "k:email" must
// NOT receive "k:email:weekly".
func TestContract_ExactTypeMatch(t *testing.T) {
	_, addr := startRedisContainer(t)
	e := mustEnqueuer(t, addr, jobsasynq.WithQueue("k-queue"))
	w := mustWorker(t, asynq.RedisClientOpt{Addr: addr}, jobsasynq.WithQueue("k-queue"),
		jobsasynq.WithMaxRetry(0)) // archive fast; the prefix job is unhandled here

	exact := make(chan string, 1)
	if err := w.Register("k:email", jobs.HandlerFunc(func(_ context.Context, task jobs.Task) error { exact <- task.Type; return nil })); err != nil {
		t.Fatalf("Register: %v", err)
	}
	cancel, done := runWorker(t, w)
	defer func() { cancel(); assertRunNilWithin(t, done, w.ShutdownWithin(), "k shutdown") }()

	if _, err := e.Enqueue(context.Background(), jobs.Job{Type: "k:email:weekly"}); err != nil {
		t.Fatalf("Enqueue prefix: %v", err)
	}
	select {
	case got := <-exact:
		t.Fatalf("prefix job dispatched to k:email handler (got %q); exact match required", got)
	case <-time.After(5 * time.Second):
		// good: the prefix job did not match the shorter-typed handler.
	}
}

// (u) unhandled type is never acked as success and follows the documented
// policy (retry → archive). With MaxRetry(0) it lands in archived, never completed.
func TestContract_UnhandledTypeNeverAcked(t *testing.T) {
	_, addr := startRedisContainer(t)
	insp := newInspector(t, addr)
	e := mustEnqueuer(t, addr, jobsasynq.WithQueue("u-queue"), jobsasynq.WithMaxRetry(0))
	w := mustWorker(t, asynq.RedisClientOpt{Addr: addr}, jobsasynq.WithQueue("u-queue"))
	// Register an UNRELATED type so the worker runs but the enqueued type is unhandled.
	if err := w.Register("u:other", jobs.HandlerFunc(func(context.Context, jobs.Task) error { return nil })); err != nil {
		t.Fatalf("Register: %v", err)
	}
	cancel, done := runWorker(t, w)
	defer func() { cancel(); assertRunNilWithin(t, done, w.ShutdownWithin(), "u shutdown") }()

	info, err := e.Enqueue(context.Background(), jobs.Job{Type: "u:unhandled"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		ti, gerr := insp.GetTaskInfo("u-queue", info.ID)
		if gerr == nil {
			if ti.State == asynq.TaskStateCompleted {
				t.Fatal("unhandled type was acked as completed")
			}
			if ti.State == asynq.TaskStateArchived {
				return // documented policy reached: retried (0) then archived
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("unhandled job did not reach archived within bound (last err=%v)", gerr)
		}
		time.Sleep(300 * time.Millisecond)
	}
}
```

- [ ] **Step 2: Fix the `fmt-ish` placeholder**

In `TestContract_RunContract`, replace `q := fmt-ish(n)` with `q := fmt.Sprintf("contract-%d", n)` and add `"fmt"` to the imports.

- [ ] **Step 3: Run**

Run: `go test -tags=integration -race -run TestContract ./jobs/asynq/`
Expected: PASS (requires Docker). If `(u)` flakes on timing, raise its 30s loop bound; archived is deterministic with MaxRetry(0).

- [ ] **Step 4: Commit**

```bash
git add jobs/asynq/contract_test.go
git commit -m "test(jobs/asynq): RunContract + validation/dispatch criteria (0,g,m,q,p,o,k,u)"
```

---

### Task 9: delivery_test.go — (a)(b)(d)(e)(n)(r)

**Files:** Create `jobs/asynq/delivery_test.go`

- [ ] **Step 1: Write the file**

```go
//go:build integration

package jobsasynq_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/slam0504/go-ddd-core/ports/jobs"

	jobsasynq "github.com/slam0504/go-ddd-adapters/jobs/asynq"
)

// (a) at-least-once redelivery incl. concurrent-duplicate tolerance. A handler
// that fails its first attempt is redelivered; the second attempt succeeds.
func TestDelivery_AtLeastOnceRedelivery(t *testing.T) {
	_, addr := startRedisContainer(t)
	e := mustEnqueuer(t, addr, jobsasynq.WithQueue("a-queue"))
	w := mustWorker(t, asynq.RedisClientOpt{Addr: addr}, jobsasynq.WithQueue("a-queue"),
		jobsasynq.WithMaxRetry(3),
		jobsasynq.WithRetryDelay(func(int, error, *asynq.Task) time.Duration { return 200 * time.Millisecond }))

	var attempts atomic.Int32
	succeeded := make(chan struct{}, 1)
	if err := w.Register("a:retry", jobs.HandlerFunc(func(context.Context, jobs.Task) error {
		if attempts.Add(1) == 1 {
			return errTransient
		}
		succeeded <- struct{}{}
		return nil
	})); err != nil {
		t.Fatalf("Register: %v", err)
	}
	cancel, done := runWorker(t, w)
	defer func() { cancel(); assertRunNilWithin(t, done, w.ShutdownWithin(), "a shutdown") }()

	if _, err := e.Enqueue(context.Background(), jobs.Job{Type: "a:retry"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	select {
	case <-succeeded:
		if got := attempts.Load(); got < 2 {
			t.Fatalf("succeeded after %d attempts, want >= 2 (redelivery)", got)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("job was not redelivered+succeeded within bound")
	}
}

var errTransient = &transientErr{}

type transientErr struct{}

func (*transientErr) Error() string { return "transient" }

// (b) dispatch not before ProcessAt on the backend's own scheduling clock.
// Enqueue with ProcessAt = now+3s; assert the handler does not fire before then.
func TestDelivery_NotBeforeProcessAt(t *testing.T) {
	_, addr := startRedisContainer(t)
	e := mustEnqueuer(t, addr, jobsasynq.WithQueue("b-queue"))
	w := mustWorker(t, asynq.RedisClientOpt{Addr: addr}, jobsasynq.WithQueue("b-queue"))

	const delay = 3 * time.Second
	processAt := time.Now().Add(delay)
	fired := make(chan time.Time, 1)
	if err := w.Register("b:sched", jobs.HandlerFunc(func(context.Context, jobs.Task) error {
		fired <- time.Now()
		return nil
	})); err != nil {
		t.Fatalf("Register: %v", err)
	}
	cancel, done := runWorker(t, w)
	defer func() { cancel(); assertRunNilWithin(t, done, w.ShutdownWithin(), "b shutdown") }()

	if _, err := e.Enqueue(context.Background(), jobs.Job{Type: "b:sched", ProcessAt: processAt}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	select {
	case at := <-fired:
		if at.Before(processAt) {
			t.Fatalf("dispatched at %v, before ProcessAt %v", at, processAt)
		}
	case <-time.After(delay + 30*time.Second):
		t.Fatal("scheduled job never fired")
	}
}

// (d) handler payload mutation does not pollute redelivery: attempt 1 mutates
// its copy then fails; attempt 2 must see the original bytes.
func TestDelivery_PayloadMutationIsolated(t *testing.T) {
	_, addr := startRedisContainer(t)
	e := mustEnqueuer(t, addr, jobsasynq.WithQueue("d-queue"))
	w := mustWorker(t, asynq.RedisClientOpt{Addr: addr}, jobsasynq.WithQueue("d-queue"),
		jobsasynq.WithMaxRetry(3),
		jobsasynq.WithRetryDelay(func(int, error, *asynq.Task) time.Duration { return 200 * time.Millisecond }))

	var attempts atomic.Int32
	secondSaw := make(chan string, 1)
	if err := w.Register("d:mut", jobs.HandlerFunc(func(_ context.Context, task jobs.Task) error {
		if attempts.Add(1) == 1 {
			for i := range task.Payload {
				task.Payload[i] = 'X' // mutate the private copy
			}
			return errTransient
		}
		secondSaw <- string(task.Payload)
		return nil
	})); err != nil {
		t.Fatalf("Register: %v", err)
	}
	cancel, done := runWorker(t, w)
	defer func() { cancel(); assertRunNilWithin(t, done, w.ShutdownWithin(), "d shutdown") }()

	if _, err := e.Enqueue(context.Background(), jobs.Job{Type: "d:mut", Payload: []byte("orig")}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	select {
	case got := <-secondSaw:
		if got != "orig" {
			t.Fatalf("redelivery saw mutated payload %q, want \"orig\"", got)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("no redelivery")
	}
}

// (e) ID stable across redeliveries: Task.ID equals the JobInfo.ID on every
// delivery.
func TestDelivery_IDStableAcrossRedeliveries(t *testing.T) {
	_, addr := startRedisContainer(t)
	e := mustEnqueuer(t, addr, jobsasynq.WithQueue("e-queue"))
	w := mustWorker(t, asynq.RedisClientOpt{Addr: addr}, jobsasynq.WithQueue("e-queue"),
		jobsasynq.WithMaxRetry(3),
		jobsasynq.WithRetryDelay(func(int, error, *asynq.Task) time.Duration { return 200 * time.Millisecond }))

	var attempts atomic.Int32
	var mu sync.Mutex
	var seen []string
	twice := make(chan struct{}, 1)
	if err := w.Register("e:id", jobs.HandlerFunc(func(_ context.Context, task jobs.Task) error {
		mu.Lock()
		seen = append(seen, task.ID)
		mu.Unlock()
		if attempts.Add(1) == 1 {
			return errTransient
		}
		twice <- struct{}{}
		return nil
	})); err != nil {
		t.Fatalf("Register: %v", err)
	}
	cancel, done := runWorker(t, w)
	defer func() { cancel(); assertRunNilWithin(t, done, w.ShutdownWithin(), "e shutdown") }()

	info, err := e.Enqueue(context.Background(), jobs.Job{Type: "e:id"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	select {
	case <-twice:
	case <-time.After(30 * time.Second):
		t.Fatal("no redelivery")
	}
	mu.Lock()
	defer mu.Unlock()
	for i, id := range seen {
		if id != info.ID {
			t.Fatalf("delivery %d had ID %q, want stable %q", i, id, info.ID)
		}
	}
}

// (n) an accepted scheduled job is retained past ProcessAt with NO worker
// running (retain-until-dequeue). Enqueue ProcessAt=now+2s, run no worker, wait
// past it, assert the job is still present (scheduled/pending), not expired.
func TestDelivery_ScheduledRetainedWithoutWorker(t *testing.T) {
	_, addr := startRedisContainer(t)
	insp := newInspector(t, addr)
	e := mustEnqueuer(t, addr, jobsasynq.WithQueue("n-queue"))

	info, err := e.Enqueue(context.Background(), jobs.Job{Type: "n:sched", ProcessAt: time.Now().Add(2 * time.Second)})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	time.Sleep(6 * time.Second) // well past ProcessAt; still no worker
	ti, err := insp.GetTaskInfo("n-queue", info.ID)
	if err != nil {
		t.Fatalf("job vanished without a worker (want retained): %v", err)
	}
	switch ti.State {
	case asynq.TaskStatePending, asynq.TaskStateScheduled:
		// good: still deliverable, never expired pre-dequeue.
	default:
		t.Fatalf("job state = %v past ProcessAt with no worker; want pending/scheduled", ti.State)
	}
}

// (r) a worker that stops before dequeue does not consume the job; a NEW Worker
// instance over the same store delivers it (Run is once-per-instance).
func TestDelivery_NewWorkerDeliversAfterStop(t *testing.T) {
	_, addr := startRedisContainer(t)
	e := mustEnqueuer(t, addr, jobsasynq.WithQueue("r-queue"))

	// Enqueue first, then start w1 but cancel it immediately so it has no chance
	// to dequeue (ProcessAt slightly in the future guarantees it stays pending).
	if _, err := e.Enqueue(context.Background(), jobs.Job{Type: "r:job", ProcessAt: time.Now().Add(2 * time.Second)}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	w1 := mustWorker(t, asynq.RedisClientOpt{Addr: addr}, jobsasynq.WithQueue("r-queue"))
	if err := w1.Register("r:job", jobs.HandlerFunc(func(context.Context, jobs.Task) error { return nil })); err != nil {
		t.Fatalf("Register w1: %v", err)
	}
	cancel1, done1 := runWorker(t, w1)
	cancel1() // stop before the scheduled job becomes eligible
	assertRunNilWithin(t, done1, w1.ShutdownWithin(), "r w1 shutdown")

	// Fresh instance delivers it.
	w2 := mustWorker(t, asynq.RedisClientOpt{Addr: addr}, jobsasynq.WithQueue("r-queue"))
	got := make(chan struct{}, 1)
	if err := w2.Register("r:job", jobs.HandlerFunc(func(context.Context, jobs.Task) error { got <- struct{}{}; return nil })); err != nil {
		t.Fatalf("Register w2: %v", err)
	}
	cancel2, done2 := runWorker(t, w2)
	defer func() { cancel2(); assertRunNilWithin(t, done2, w2.ShutdownWithin(), "r w2 shutdown") }()
	select {
	case <-got:
	case <-time.After(30 * time.Second):
		t.Fatal("new Worker did not deliver the job left by the stopped worker")
	}
}
```

- [ ] **Step 2: Run**

Run: `go test -tags=integration -race -run TestDelivery ./jobs/asynq/`
Expected: PASS (Docker required). `(b)`/`(n)` are timing tests with generous bounds; if CI is slow, widen the post-ProcessAt sleeps, never the assertion direction.

- [ ] **Step 3: Commit**

```bash
git add jobs/asynq/delivery_test.go
git commit -m "test(jobs/asynq): delivery criteria (a,b,d,e,n,r)"
```

---

### Task 10: shutdown_test.go — (c)(h)(j)(l)(s1)(s2)(t)(f)

**Files:** Create `jobs/asynq/shutdown_test.go`

The three multi-criterion tests (stuck handler, redis-down-during-shutdown,
ack/shutdown race) are ported from the validated spike; the constructors now
return `error` (use `mustWorker`/`mustEnqueuer`). Plus standalone (h)(j)(l)(t)(f).

- [ ] **Step 1: Write the file**

```go
//go:build integration

package jobsasynq_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/slam0504/go-ddd-core/pkg/errorsx"
	"github.com/slam0504/go-ddd-core/ports/jobs"

	jobsasynq "github.com/slam0504/go-ddd-adapters/jobs/asynq"
)

const shutTO = 2 * time.Second // configured ShutdownTimeout for shutdown tests

// (f) unreachable backend at Enqueue → CodeUnavailable (and never CodeUnknown).
func TestShutdown_Enqueue_UnreachableBackend(t *testing.T) {
	e, err := jobsasynq.NewEnqueuer(asynq.RedisClientOpt{Addr: "127.0.0.1:6391"}) // nothing listening
	if err != nil {
		t.Fatalf("NewEnqueuer: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	_, err = e.Enqueue(context.Background(), jobs.Job{Type: "f:job"})
	if code := errorsx.CodeOf(err); code != errorsx.CodeUnavailable {
		t.Fatalf("unreachable backend: code = %v, want CodeUnavailable", code)
	}
	if errorsx.CodeOf(err) == errorsx.CodeUnknown {
		t.Fatal("backend failure yielded CodeUnknown")
	}
}

// (h) fatal startup (unreachable backend) → coded errorsx from Run, not a ctx
// error, not nil.
func TestShutdown_Run_FatalStartupUnreachable(t *testing.T) {
	w := mustWorker(t, asynq.RedisClientOpt{Addr: "127.0.0.1:6392"}) // nothing listening
	if err := w.Register("h:job", jobs.HandlerFunc(func(context.Context, jobs.Task) error { return nil })); err != nil {
		t.Fatalf("Register: %v", err)
	}
	err := w.Run(context.Background())
	if err == nil {
		t.Fatal("Run returned nil for an unreachable backend; want a coded fatal")
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run returned a ctx error %v; a fatal must be a coded errorsx", err)
	}
	if code := errorsx.CodeOf(err); code != errorsx.CodeUnavailable {
		t.Fatalf("fatal code = %v, want CodeUnavailable", code)
	}
}

// (j) handler ctx is cancelled when Run is cancelled (via BaseContext).
func TestShutdown_HandlerCtxCancelledOnRunCancel(t *testing.T) {
	_, addr := startRedisContainer(t)
	e := mustEnqueuer(t, addr, jobsasynq.WithQueue("j-queue"))
	w := mustWorker(t, asynq.RedisClientOpt{Addr: addr}, jobsasynq.WithQueue("j-queue"),
		jobsasynq.WithShutdownTimeout(shutTO))

	inHandler := make(chan struct{}, 1)
	ctxErr := make(chan error, 1)
	if err := w.Register("j:wait", jobs.HandlerFunc(func(hctx context.Context, _ jobs.Task) error {
		inHandler <- struct{}{}
		<-hctx.Done() // observes cancellation
		ctxErr <- hctx.Err()
		return hctx.Err()
	})); err != nil {
		t.Fatalf("Register: %v", err)
	}
	cancel, done := runWorker(t, w)
	if _, err := e.Enqueue(context.Background(), jobs.Job{Type: "j:wait"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	select {
	case <-inHandler:
	case <-time.After(30 * time.Second):
		t.Fatal("handler never started")
	}
	cancel() // cancelling Run must cancel the handler ctx
	select {
	case err := <-ctxErr:
		if err == nil {
			t.Fatal("handler ctx was not cancelled on Run cancel")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("handler ctx never observed cancellation")
	}
	assertRunNilWithin(t, done, w.ShutdownWithin(), "j shutdown")
}

// (l) concurrent Enqueue is clean under -race and the two Run endpoints hold.
func TestShutdown_ConcurrentEnqueueAndRunEndpoints(t *testing.T) {
	_, addr := startRedisContainer(t)
	e := mustEnqueuer(t, addr, jobsasynq.WithQueue("l-queue"))
	w := mustWorker(t, asynq.RedisClientOpt{Addr: addr}, jobsasynq.WithQueue("l-queue"),
		jobsasynq.WithShutdownTimeout(shutTO), jobsasynq.WithConcurrency(8))

	var processed atomic.Int32
	if err := w.Register("l:job", jobs.HandlerFunc(func(context.Context, jobs.Task) error {
		processed.Add(1)
		return nil
	})); err != nil {
		t.Fatalf("Register: %v", err)
	}
	cancel, done := runWorker(t, w)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, _ = e.Enqueue(context.Background(), jobs.Job{Type: "l:job", Payload: []byte(fmt.Sprintf("%d", n))})
		}(i)
	}
	wg.Wait()
	time.Sleep(2 * time.Second) // let some drain
	// Endpoint A: cancellation returns nil within the declared bound.
	cancel()
	assertRunNilWithin(t, done, w.ShutdownWithin(), "l shutdown")
}

// (t) accepted-but-ack-lost fault: an Enqueue that errors mid-flight follows
// class-2 rules (ctx error OR coded non-Unknown) + zero JobInfo, and caller
// mutation after the error does not corrupt an accepted job's payload
// (snapshot-before-submit). Modeled by a deadline that trips during the backend
// call against a reachable server.
func TestShutdown_AcceptedButAckLost_SnapshotIsolation(t *testing.T) {
	_, addr := startRedisContainer(t)
	insp := newInspector(t, addr)
	e := mustEnqueuer(t, addr, jobsasynq.WithQueue("t-queue"))

	// A very short deadline may surface a ctx error from EnqueueContext even if
	// the backend accepted the write — the class-2 indeterminate case.
	payload := []byte("origt")
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	info, err := e.Enqueue(ctx, jobs.Job{Type: "t:job", Payload: payload})
	// Mutate the caller's slice immediately after the call returns.
	for i := range payload {
		payload[i] = 'Z'
	}
	if err == nil {
		// Backend won the race: accepted. JobInfo is valid; the stored payload
		// must still be "origt" (snapshot isolated it from the mutation above).
		ti, gerr := insp.GetTaskInfo("t-queue", info.ID)
		if gerr != nil {
			t.Fatalf("accepted job not found: %v", gerr)
		}
		if string(ti.Payload) != "origt" {
			t.Fatalf("stored payload = %q, want \"origt\" (snapshot must isolate caller mutation)", ti.Payload)
		}
		return
	}
	// Error path: class-2 — ctx error OR coded non-Unknown; zero JobInfo.
	if !errors.Is(err, context.DeadlineExceeded) && errorsx.CodeOf(err) == errorsx.CodeUnknown {
		t.Fatalf("error neither ctx nor coded non-Unknown: %v", err)
	}
	if info.ID != "" {
		t.Fatalf("error carried non-zero JobInfo.ID %q", info.ID)
	}
}

// --- ported spike tests (constructors now return error) ---

// (c)+(s2): a stuck handler that ignores its cancelled ctx must not keep Run
// from returning; the un-acked task is actually redelivered by a fresh Worker.
func TestShutdown_StuckHandler_RunNil_NewWorkerRedelivers(t *testing.T) {
	_, addr := startRedisContainer(t)
	opts := []jobsasynq.Option{jobsasynq.WithQueue("c-queue"), jobsasynq.WithRetention(10 * time.Minute), jobsasynq.WithShutdownTimeout(shutTO)}

	var attempts atomic.Int32
	started := make(chan struct{}, 1)
	redelivered := make(chan struct{}, 1)
	release := make(chan struct{})
	handler := jobs.HandlerFunc(func(_ context.Context, _ jobs.Task) error {
		if attempts.Add(1) == 1 {
			started <- struct{}{}
			<-release // deliberately ignores ctx: the stuck straggler
			return nil
		}
		redelivered <- struct{}{}
		return nil
	})
	t.Cleanup(func() { close(release) })

	e := mustEnqueuer(t, addr, opts...)
	if _, err := e.Enqueue(context.Background(), jobs.Job{Type: "c:stuck", Payload: []byte("x")}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	w1 := mustWorker(t, asynq.RedisClientOpt{Addr: addr}, opts...)
	if err := w1.Register("c:stuck", handler); err != nil {
		t.Fatalf("Register w1: %v", err)
	}
	cancel1, done1 := runWorker(t, w1)
	select {
	case <-started:
	case <-time.After(30 * time.Second):
		t.Fatal("first delivery did not happen")
	}
	cancel1()
	assertRunNilWithin(t, done1, w1.ShutdownWithin(), "stuck-handler shutdown")

	w2 := mustWorker(t, asynq.RedisClientOpt{Addr: addr}, opts...)
	if err := w2.Register("c:stuck", handler); err != nil {
		t.Fatalf("Register w2: %v", err)
	}
	cancel2, done2 := runWorker(t, w2)
	select {
	case <-redelivered:
	case <-time.After(jobsasynq.RedeliverWithin):
		t.Fatalf("task not redelivered within RedeliverWithin %v", jobsasynq.RedeliverWithin)
	}
	cancel2()
	assertRunNilWithin(t, done2, w2.ShutdownWithin(), "second worker shutdown")
}

// (s1)+(s2)+(c teardown-failure variant): Redis taken down DURING shutdown —
// Run still returns nil, the requeue path provably hit the outage, and the job
// recovers (completed or actually redelivered) after restart.
func TestShutdown_RedisDownDuringShutdown_RunNil_JobRecovers(t *testing.T) {
	container, addr := startRedisContainer(t)
	hook := &recorderHook{}
	logger := &captureLogger{}
	workerOpts := []jobsasynq.Option{jobsasynq.WithQueue("s-queue"), jobsasynq.WithRetention(10 * time.Minute), jobsasynq.WithShutdownTimeout(shutTO), jobsasynq.WithLogger(logger)}

	var attempts atomic.Int32
	started := make(chan struct{}, 1)
	redelivered := make(chan struct{}, 1)
	release := make(chan struct{})
	handler := jobs.HandlerFunc(func(_ context.Context, _ jobs.Task) error {
		if attempts.Add(1) == 1 {
			started <- struct{}{}
			<-release
			return nil
		}
		redelivered <- struct{}{}
		return nil
	})
	t.Cleanup(func() { close(release) })

	e := mustEnqueuer(t, addr, jobsasynq.WithQueue("s-queue"), jobsasynq.WithRetention(10*time.Minute))
	info, err := e.Enqueue(context.Background(), jobs.Job{Type: "s:outage", Payload: []byte("x")})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	w1 := mustWorker(t, hookedConnOpt{addr: addr, hook: hook}, workerOpts...)
	if err := w1.Register("s:outage", handler); err != nil {
		t.Fatalf("Register w1: %v", err)
	}
	cancel1, done1 := runWorker(t, w1)
	select {
	case <-started:
	case <-time.After(30 * time.Second):
		t.Fatal("first delivery did not happen")
	}

	hook.armed.Store(true)
	logger.armed.Store(true)
	stopTimeout := 10 * time.Second
	if err := container.Stop(context.Background(), &stopTimeout); err != nil {
		t.Fatalf("stopping redis: %v", err)
	}
	cancel1()
	assertRunNilWithin(t, done1, w1.ShutdownWithin(), "shutdown during outage")
	hook.armed.Store(false)
	logger.armed.Store(false)

	if len(hook.failedTouchingActive()) == 0 {
		t.Fatalf("no failed redis command touching :active during outage; failures: %v", hook.failed)
	}
	if len(logger.matching("requeue", "back to queue", "push task")) == 0 {
		t.Fatalf("no asynq requeue-path error logged; entries: %v", logger.entries)
	}

	if err := container.Start(context.Background()); err != nil {
		t.Fatalf("restarting redis: %v", err)
	}
	addr2, err := container.Endpoint(context.Background(), "")
	if err != nil {
		t.Fatalf("re-resolving endpoint: %v", err)
	}
	insp := newInspector(t, addr2)
	w2 := mustWorker(t, asynq.RedisClientOpt{Addr: addr2}, jobsasynq.WithQueue("s-queue"), jobsasynq.WithRetention(10*time.Minute), jobsasynq.WithShutdownTimeout(shutTO))
	if err := w2.Register("s:outage", handler); err != nil {
		t.Fatalf("Register w2: %v", err)
	}
	cancel2, done2 := runWorker(t, w2)
	defer func() { cancel2(); assertRunNilWithin(t, done2, w2.ShutdownWithin(), "recovery shutdown") }()

	deadline := time.Now().Add(jobsasynq.RecoverWithin)
	for {
		select {
		case <-redelivered:
			return // retryable branch proven
		default:
		}
		switch classifyJob(t, insp, "s-queue", info.ID) {
		case stateCompleted:
			select {
			case <-redelivered:
				t.Fatal("job completed but was also redelivered")
			case <-time.After(5 * time.Second):
				return
			}
		case stateLostDiscarded:
			t.Fatalf("job %s lost (no completion evidence)", info.ID)
		}
		if time.Now().After(deadline) {
			t.Fatalf("job did not resolve within RecoverWithin %v", jobsasynq.RecoverWithin)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// (s1)+(s2) ack/shutdown race ×N: every job ends completed or retryable —
// never lost, never stuck active beyond the declared bound.
func TestShutdown_AckShutdownRace_CompletedOrRetryable(t *testing.T) {
	_, addr := startRedisContainer(t)
	opts := []jobsasynq.Option{jobsasynq.WithQueue("race-queue"), jobsasynq.WithRetention(10 * time.Minute), jobsasynq.WithShutdownTimeout(shutTO)}
	e := mustEnqueuer(t, addr, opts...)
	insp := newInspector(t, addr)

	const iterations = 20
	for i := 0; i < iterations; i++ {
		jobType := fmt.Sprintf("race:%d", i)
		inHandler := make(chan struct{}, 1)
		var deliveries atomic.Int32
		handler := jobs.HandlerFunc(func(context.Context, jobs.Task) error {
			deliveries.Add(1)
			select {
			case inHandler <- struct{}{}:
			default:
			}
			return nil // immediate ack, racing the shutdown
		})
		w1 := mustWorker(t, asynq.RedisClientOpt{Addr: addr}, opts...)
		if err := w1.Register(jobType, handler); err != nil {
			t.Fatalf("iter %d Register: %v", i, err)
		}
		cancel1, done1 := runWorker(t, w1)
		info, err := e.Enqueue(context.Background(), jobs.Job{Type: jobType, Payload: []byte("x")})
		if err != nil {
			t.Fatalf("iter %d Enqueue: %v", i, err)
		}
		select {
		case <-inHandler:
		case <-time.After(30 * time.Second):
			t.Fatalf("iter %d: delivery did not happen", i)
		}
		cancel1()
		assertRunNilWithin(t, done1, w1.ShutdownWithin(), fmt.Sprintf("iter %d shutdown", i))

		if classifyJob(t, insp, "race-queue", info.ID) == stateCompleted {
			continue
		}
		w2 := mustWorker(t, asynq.RedisClientOpt{Addr: addr}, opts...)
		if err := w2.Register(jobType, handler); err != nil {
			t.Fatalf("iter %d Register w2: %v", i, err)
		}
		cancel2, done2 := runWorker(t, w2)
		deadline := time.Now().Add(jobsasynq.RecoverWithin)
		resolved := false
		for !resolved {
			switch classifyJob(t, insp, "race-queue", info.ID) {
			case stateCompleted:
				resolved = true
			case stateLostDiscarded:
				t.Fatalf("iter %d: job %s lost", i, info.ID)
			}
			if !resolved && deliveries.Load() >= 2 {
				resolved = true // actual redelivery observed
			}
			if !resolved {
				if time.Now().After(deadline) {
					t.Fatalf("iter %d: job stuck beyond RecoverWithin %v", i, jobsasynq.RecoverWithin)
				}
				time.Sleep(200 * time.Millisecond)
			}
		}
		cancel2()
		assertRunNilWithin(t, done2, w2.ShutdownWithin(), fmt.Sprintf("iter %d recovery shutdown", i))
	}
}
```

- [ ] **Step 2: Run**

Run: `go test -tags=integration -race -run TestShutdown ./jobs/asynq/`
Expected: PASS (Docker required). The redis-down and ack-race tests are the slowest (container stop/start; 20 iterations) — allow several minutes.

- [ ] **Step 3: Commit**

```bash
git add jobs/asynq/shutdown_test.go
git commit -m "test(jobs/asynq): shutdown/recoverability criteria (c,h,j,l,s1,s2,t,f)"
```

---

## Phase 6 — CI & docs

### Task 11: Wire the integration job

**Files:** Modify `.github/workflows/ci.yml:77`

- [ ] **Step 1: Add the jobs/asynq path to the pgx-adapter integration step**

Change the last `run:` line so it also runs the new package:
```yaml
      - name: pgx adapter integration tests
        run: go test -tags=integration -race ./ports/database/pgx/... ./eventbus/outbox/pgx/... ./idempotency/redis/... ./jobs/asynq/...
```

- [ ] **Step 2: Commit**

```bash
git add .github/workflows/ci.yml
git commit -m "ci(jobs/asynq): run the integration suite in CI"
```

---

### Task 12: README adapter row + usage sketch

**Files:** Modify `README.md`

- [ ] **Step 1: Add a row to the adapter table**

Find the markdown adapter table (the one listing `idempotency/redis`, `logger/slogger`, etc.) and add, keeping column alignment:
```markdown
| `jobs/asynq` | `jobs.Enqueuer` / `jobs.Worker` | Redis-backed background jobs over hibiken/asynq v0.24.1; exact-type-match dispatch, 30d default scheduling horizon, at-least-once with retry→archive; `WithQueue` / `WithSchedulingHorizon` / `WithRetention` / `WithMaxRetry` / `WithRetryDelay` / `WithTaskTimeout` / `WithConcurrency` / `WithShutdownTimeout` / `WithLogger`. Redis 4.0+ |
```

- [ ] **Step 2: Add a short usage sketch under the adapter's section (after the table or in the existing per-adapter prose area)**

```markdown
### Background jobs (`jobs/asynq`)

```go
enq, err := jobsasynq.NewEnqueuer(asynq.RedisClientOpt{Addr: "localhost:6379"})
// ... handle err; defer enq.Close()
info, err := enq.Enqueue(ctx, jobs.Job{Type: "email:welcome", Payload: body})

w, err := jobsasynq.NewWorker(asynq.RedisClientOpt{Addr: "localhost:6379"})
// ... handle err
_ = w.Register("email:welcome", jobs.HandlerFunc(func(ctx context.Context, t jobs.Task) error {
    return send(ctx, t.Payload)
}))
err = w.Run(ctx) // blocks until ctx is cancelled, then returns nil
```
```
(Use the repo's existing fenced-block style; if it uses 4-backtick outer fences for nested code, match it.)

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "docs(jobs/asynq): README adapter row + usage sketch"
```

---

### Task 13: CHANGELOG entry

**Files:** Modify `CHANGELOG.md`

- [ ] **Step 1: Add under `[Unreleased]` → `Added`**

```markdown
### Added

- `jobs/asynq` (`jobsasynq`): the first `ports/jobs` adapter — a Redis-backed
  `Enqueuer` + `Worker` over `github.com/hibiken/asynq` v0.24.1. Exact-type-match
  dispatch (not `asynq.ServeMux`), two-class Enqueue error mapping, a 30-day
  default scheduling horizon (out-of-horizon `ProcessAt` rejected at Enqueue),
  1h default completed-task retention, and an at-least-once delivery guarantee
  with retry→archive dead-lettering. Passes core's `jobstest.RunContract` plus
  the full (0)+(a)–(v) tag-gate acceptance suite under `-race` + testcontainers
  Redis. Functional options: `WithQueue`, `WithSchedulingHorizon`,
  `WithRetention`, `WithMaxRetry`, `WithRetryDelay`, `WithTaskTimeout`,
  `WithConcurrency`, `WithShutdownTimeout`, `WithLogger`.
```
(If `[Unreleased]` has no `Added` subsection yet, create it. Do NOT add a version heading or compare link — that rides in the later dep-bump PR, per the v0.7.0/v0.8.0 convention.)

- [ ] **Step 2: Commit**

```bash
git add CHANGELOG.md
git commit -m "docs(jobs/asynq): CHANGELOG [Unreleased] entry"
```

---

## Phase 7 — Verify & PR

### Task 14: Full local verification + open the impl PR

**Files:** none (verification + PR)

- [ ] **Step 1: Full build/vet/test (root + examples/orders)**

Run:
```bash
go build ./... && go vet ./...
go test -race ./...
cd examples/orders && go build ./... && go vet ./... && go test ./... && cd ../..
```
Expected: all PASS. (`examples/orders` has no jobs usage; it only needs to compile against the bumped core pin.)

- [ ] **Step 2: Integration suite**

Run: `go test -tags=integration -race ./jobs/asynq/...`
Expected: PASS — every (0)+(a)–(v) test green. Docker required; a skip is a gate failure.

- [ ] **Step 3: Lint (default + integration tags)**

Run:
```bash
/usr/local/bin/golangci-lint run ./...
/usr/local/bin/golangci-lint run --build-tags=integration ./...
```
Expected: 0 issues. Fix any goimports grouping (`go-ddd-core/...` and `go-ddd-adapters/...` each in their own group, separate from third-party).

- [ ] **Step 4: Push the branch + open the PR**

```bash
git push -u origin feat/jobs-asynq-v0.9.0
gh pr create --title "feat(jobs/asynq): Redis-backed background-jobs adapter (v0.9.0 cycle, impl)" --body "$(cat <<'EOF'
## Summary
- First production `ports/jobs` consumer: `jobs/asynq` (`jobsasynq`) over hibiken/asynq v0.24.1.
- Exact-type-match dispatch, two-class Enqueue errors, 30d default scheduling horizon, 1h retention, at-least-once with retry→archive.
- Passes `jobstest.RunContract` + the full (0)+(a)–(v) tag-gate acceptance suite under `-race` + testcontainers Redis.
- Core pinned at the pseudo-version of `784ef3e` (ports/jobs); the core `v0.9.0` tag + adapter dep-bump/tag are deferred to a later cycle (§10 of the spec).

## Test plan
- [ ] `go build ./... && go vet ./...` (root + examples/orders)
- [ ] `go test -race ./...`
- [ ] `go test -tags=integration -race ./jobs/asynq/...` (real Redis)
- [ ] `golangci-lint run ./...` and `--build-tags=integration ./...`
- [ ] CI 5/5 green

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```
(The branch should have been created at the start of execution: `git checkout -b feat/jobs-asynq-v0.9.0`. If not yet on it, create it before the first commit.)

- [ ] **Step 5: Confirm CI**

Run: `gh pr checks --watch`
Expected: lint (×2), build-test (×2), integration — all green. Address any failures, push fixes as NEW commits (never `--amend` a pushed commit).

---

## Self-Review (run by the plan author after writing)

**Spec coverage — every (0)+(a)–(v) criterion maps to a task:**

- (0) RunContract → Task 8. (g)(m)(q)(p)(o)(k)(u) → Task 8.
- (a)(b)(d)(e)(n)(r) → Task 9.
- (c)(h)(j)(l)(s1)(s2)(t)(f) → Task 10.
- (v) single-source declared values: `DefaultSchedulingHorizon`, `RecoverWithin`,
  `RedeliverWithin` exported consts (Task 2) + `Worker.ShutdownWithin()` (Task 4),
  consumed by the fixtures in Tasks 8–10. ✓
- Spec §2 deps → Task 1. §4 Enqueue/dispatch → Tasks 3–4. §5 options/validation →
  Tasks 2–4 + unit Task 6. §5.2 no-Ping-at-New / Run reachability → Task 4 +
  asserted by (h) Task 10 and (f) Task 10. §6 introspection → Task 7
  (`classifyJob`). §7 Run lifecycle → Task 4. doc.go policy → Task 5. CI/docs →
  Tasks 11–13.

**Placeholder scan:** the deliberate plan-only placeholders (`timeDuration`/
`durationAlias`/`timeNow` in Task 3 Step 1, `fmt-ish` in Task 8 Step 1) each have
an explicit follow-up step (Task 3 Step 2, Task 8 Step 2) instructing the exact
replacement. No `TODO`/`TBD` remains in delivered source.

**Type consistency:** constructor signatures `NewEnqueuer(asynq.RedisConnOpt,
...Option) (*Enqueuer, error)` and `NewWorker(...) (*Worker, error)` are used
identically across Tasks 3, 4, 6, 7, 8, 9, 10. Helpers `mustEnqueuer`/`mustWorker`/
`runWorker`/`assertRunNilWithin`/`classifyJob`/`newInspector` defined in Task 7
are referenced consistently. `classifyBackendErr`/`snapshot`/`validateConnOptShape`
defined in Task 3 are reused in Task 4. Error sentinels (Task 2) match their
`errors.Is` assertions (Task 6). Option names match the spec §5.1 table.

**Known execution risks (flag during implementation, not blockers):**
1. `classifyBackendErr` string matching is heuristic; (f) drives the
   `net.Error`/"connection refused" path which is deterministic. The
   "unclassifiable → CodeInternal" branch is covered only by the unit test
   (Task 6), which is sufficient for criterion (f)'s "≠ CodeUnknown".
2. `(t)` uses a `time.Nanosecond` deadline to provoke the indeterminate class-2
   case; whichever branch wins (accepted vs ctx-error) is asserted. If neither
   triggers deterministically on fast CI, keep both branches (the test passes on
   either outcome — that IS the indeterminate contract).
3. asynq API names to confirm at first compile against v0.24.1:
   `asynq.Timeout`, `asynq.Retention`, `asynq.MaxRetry`, `asynq.ProcessAt`,
   `asynq.Queue`, `asynq.DefaultRetryDelayFunc`, `asynq.GetTaskID`,
   `Config.BaseContext`, `Inspector.GetTaskInfo/GetQueueInfo`,
   `TaskState*`/`ErrTaskNotFound`/`ErrQueueNotFound`. The spike already
   compiled against these except `BaseContext`/`GetQueueInfo` — verify both
   exist in v0.24.1 at Task 4/8 (they do: `Config.BaseContext` since asynq
   v0.22, `Inspector.GetQueueInfo` since v0.18).

