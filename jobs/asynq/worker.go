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

// Compile-time interface assertions: both *Enqueuer and *Worker must satisfy
// their respective core contracts. A missing method yields a build error here.
var (
	_ jobs.Enqueuer = (*Enqueuer)(nil)
	_ jobs.Worker   = (*Worker)(nil)
)
