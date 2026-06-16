package jobsasynq

import (
	"strings"
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
	ErrNilRedisConnOpt         = errorsx.New(errorsx.CodeInvalidArgument, "jobs/asynq: nil RedisConnOpt")
	ErrInvalidRedisConnOpt     = errorsx.New(errorsx.CodeInvalidArgument, "jobs/asynq: RedisConnOpt.MakeRedisClient did not return a redis.UniversalClient")
	ErrEmptyQueue              = errorsx.New(errorsx.CodeInvalidArgument, "jobs/asynq: queue name is blank")
	ErrSchedulingHorizonNonPos = errorsx.New(errorsx.CodeInvalidArgument, "jobs/asynq: scheduling horizon must be > 0")
	ErrRetentionTooSmall       = errorsx.New(errorsx.CodeInvalidArgument, "jobs/asynq: retention must be >= 1s (Asynq stores integer seconds)")
	ErrTaskTimeoutNonPos       = errorsx.New(errorsx.CodeInvalidArgument, "jobs/asynq: task timeout must be > 0")
	ErrMaxRetryNegative        = errorsx.New(errorsx.CodeInvalidArgument, "jobs/asynq: max retry must be >= 0")
	ErrConcurrencyNonPos       = errorsx.New(errorsx.CodeInvalidArgument, "jobs/asynq: concurrency must be > 0")
	ErrShutdownTimeoutNonPos   = errorsx.New(errorsx.CodeInvalidArgument, "jobs/asynq: shutdown timeout must be > 0")
	ErrNilRetryDelay           = errorsx.New(errorsx.CodeInvalidArgument, "jobs/asynq: retry delay func must not be nil")
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

func strimEmpty(s string) bool { return strings.TrimSpace(s) == "" }
