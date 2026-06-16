package jobsasynq

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"

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
	schedulingHorizon time.Duration
	retention         time.Duration
	maxRetry          int
	taskTimeout       time.Duration
}

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
	if !job.ProcessAt.IsZero() && job.ProcessAt.After(time.Now().Add(e.schedulingHorizon)) {
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
