package outbox

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	rt "runtime/debug"
	"sync/atomic"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"

	"github.com/slam0504/go-ddd-core/eventbus"
	"github.com/slam0504/go-ddd-core/ports/logger"
)

// ErrRelayAlreadyRunning is returned by [Relay.Run] when invoked while
// a previous Run on the SAME *Relay is still in flight. Two different
// *Relay instances against the same store are NOT defended; see
// package doc for the multi-Relay caveat.
var ErrRelayAlreadyRunning = errors.New("outbox: relay already running on this instance")

// HeaderRestorer is an optional callback that re-injects selected
// headers from a stored [eventbus.OutboxRecord.Headers] map back into
// the publish ctx before [eventbus.Publisher.Publish] is called.
//
// Default is nil — no headers are re-injected. Pass
// [github.com/slam0504/go-ddd-adapters/eventbus/kafka].RestoreCoreHeaders
// to propagate the three core well-known headers (trace_id /
// causation_id / correlation_id) through the kafka codec.
type HeaderRestorer func(ctx context.Context, headers map[string]string) context.Context

// RelayConfig configures a [Relay].
type RelayConfig struct {
	// Store is the read/ack side of the outbox. When MaxAttempts > 0,
	// the supplied Store MUST also implement DeadLetterRecorder;
	// NewRelay returns an error otherwise.
	Store eventbus.OutboxStore
	// Publisher delivers the reconstructed events to the broker.
	Publisher eventbus.Publisher
	// Codec MUST be the same codec instance (or an equivalent registry)
	// used at Stage time. Mismatch routes records through fail().
	Codec eventbus.Codec
	// Logger sinks structured log records (drain failures, panic
	// recoveries, mark-failed/terminate diagnostics).
	Logger logger.Logger
}

// RelayOption configures a [Relay].
type RelayOption func(*Relay)

// WithPollInterval overrides the default poll interval (1s).
func WithPollInterval(d time.Duration) RelayOption {
	return func(r *Relay) { r.pollInterval = d }
}

// WithBatchSize overrides the default Fetch batch size (100).
func WithBatchSize(n int) RelayOption {
	return func(r *Relay) { r.batchSize = n }
}

// WithBackoff overrides the default exponential-with-jitter backoff
// (cap 60s). The fn receives the upcoming attempt number (1-based)
// and returns the delay until next attempt.
func WithBackoff(fn func(attempts int) time.Duration) RelayOption {
	return func(r *Relay) { r.backoff = fn }
}

// WithMaxAttempts caps the per-record retry budget. After Attempts
// reaches the cap, the next failure routes the record through
// Terminate (DLQ). 0 (the default) means unlimited; the constructor
// then accepts a store that does NOT implement DeadLetterRecorder.
//
// The default is 10 — change deliberately based on your operational
// retry expectations.
func WithMaxAttempts(n int) RelayOption {
	return func(r *Relay) { r.maxAttempts = n }
}

// WithRelayClock overrides time.Now for the Relay. Useful for
// deterministic tests; should match the Memory store's clock.
func WithRelayClock(now func() time.Time) RelayOption {
	return func(r *Relay) { r.now = now }
}

// WithHeaderRestorer installs a callback that re-injects selected
// stored headers back into the publish ctx. Default is nil (no
// restoration). See the [HeaderRestorer] doc for the kafka helper.
func WithHeaderRestorer(fn HeaderRestorer) RelayOption {
	return func(r *Relay) { r.headerRestorer = fn }
}

// Relay drains an [eventbus.OutboxStore] and publishes records via an
// [eventbus.Publisher]. Driver-agnostic: depends only on the
// OutboxStore interface plus the local DeadLetterRecorder extension.
type Relay struct {
	store     eventbus.OutboxStore
	dlq       DeadLetterRecorder // nil iff maxAttempts == 0
	publisher eventbus.Publisher
	codec     eventbus.Codec
	log       logger.Logger

	pollInterval   time.Duration
	batchSize      int
	maxAttempts    int
	backoff        func(attempts int) time.Duration
	now            func() time.Time
	headerRestorer HeaderRestorer

	running atomic.Bool
}

// NewRelay constructs a Relay. Returns an error when any required
// dependency is nil or when MaxAttempts > 0 and the supplied Store
// does not implement DeadLetterRecorder.
func NewRelay(cfg RelayConfig, opts ...RelayOption) (*Relay, error) {
	switch {
	case cfg.Store == nil:
		return nil, errors.New("outbox relay: Store is required")
	case cfg.Publisher == nil:
		return nil, errors.New("outbox relay: Publisher is required")
	case cfg.Codec == nil:
		return nil, errors.New("outbox relay: Codec is required")
	case cfg.Logger == nil:
		return nil, errors.New("outbox relay: Logger is required")
	}
	r := &Relay{
		store:        cfg.Store,
		publisher:    cfg.Publisher,
		codec:        cfg.Codec,
		log:          cfg.Logger,
		pollInterval: time.Second,
		batchSize:    100,
		maxAttempts:  10,
		backoff:      defaultBackoff,
		now:          time.Now,
	}
	for _, opt := range opts {
		opt(r)
	}
	if r.maxAttempts > 0 {
		dlq, ok := cfg.Store.(DeadLetterRecorder)
		if !ok {
			return nil, errors.New("outbox relay: MaxAttempts > 0 requires a Store that implements DeadLetterRecorder")
		}
		r.dlq = dlq
	}
	return r, nil
}

// Run drains the store on each tick of the poll interval until ctx is
// cancelled. Returns ctx.Err() on cancellation, or
// [ErrRelayAlreadyRunning] when a previous Run on the SAME *Relay is
// still in flight.
func (r *Relay) Run(ctx context.Context) error {
	if !r.running.CompareAndSwap(false, true) {
		return ErrRelayAlreadyRunning
	}
	defer r.running.Store(false)

	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := r.drainOnce(ctx); err != nil {
				logger.Error(ctx, r.log, "outbox relay drain failed",
					logger.F("err", err.Error()))
			}
		}
	}
}

func (r *Relay) drainOnce(ctx context.Context) error {
	records, err := r.store.Fetch(ctx, r.batchSize)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	for _, rec := range records {
		r.process(ctx, rec)
	}
	return nil
}

// process runs the full decode → restore → publish → mark-sent cycle
// for one record under a panic guard. A panic anywhere in the sequence
// is recovered, logged with stack, and routed through fail() exactly
// like a returned error — so a poison record cannot kill the relay
// goroutine and panics still honour MaxAttempts / DLQ.
func (r *Relay) process(ctx context.Context, rec eventbus.OutboxRecord) {
	defer func() {
		if rv := recover(); rv != nil {
			logger.Error(ctx, r.log, "outbox relay: panic recovered",
				logger.F("record_id", rec.ID),
				logger.F("event_id", rec.EventID),
				logger.F("recover", fmt.Sprint(rv)),
				logger.F("stack", string(rt.Stack())))
			r.fail(ctx, rec, fmt.Errorf("panic: %v", rv))
		}
	}()

	msg := message.NewMessage(rec.EventID, rec.Payload)
	for k, v := range rec.Headers {
		msg.Metadata.Set(k, v)
	}
	ev, _, err := r.codec.Unmarshal(ctx, msg)
	if err != nil {
		r.fail(ctx, rec, fmt.Errorf("decode: %w", err))
		return
	}

	pubCtx := ctx
	if r.headerRestorer != nil {
		pubCtx = r.headerRestorer(ctx, rec.Headers)
	}

	if err := r.publisher.Publish(pubCtx, rec.Topic, ev); err != nil {
		r.fail(ctx, rec, fmt.Errorf("publish: %w", err))
		return
	}

	if err := r.store.MarkSent(ctx, rec.ID); err != nil {
		logger.Error(ctx, r.log, "outbox relay: mark sent failed",
			logger.F("record_id", rec.ID),
			logger.F("err", err.Error()))
	}
}

func (r *Relay) fail(ctx context.Context, rec eventbus.OutboxRecord, reason error) {
	nextAttempt := rec.Attempts + 1
	if r.maxAttempts > 0 && nextAttempt >= r.maxAttempts {
		if err := r.dlq.Terminate(ctx, rec.ID, reason.Error()); err != nil {
			logger.Error(ctx, r.log, "outbox relay: terminate failed",
				logger.F("record_id", rec.ID),
				logger.F("err", err.Error()))
		}
		return
	}
	nextAttemptAt := r.now().Add(r.backoff(nextAttempt))
	if err := r.store.MarkFailed(ctx, rec.ID, reason.Error(), nextAttemptAt); err != nil {
		logger.Error(ctx, r.log, "outbox relay: mark failed",
			logger.F("record_id", rec.ID),
			logger.F("err", err.Error()))
	}
}

// defaultBackoff is exponential with full jitter, capped at 60s. The
// curve is intentionally generic — services with specific SLO needs
// should supply their own via [WithBackoff].
func defaultBackoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	base := time.Second << (attempts - 1)
	const maxBackoff = 60 * time.Second
	if base <= 0 || base > maxBackoff {
		base = maxBackoff
	}
	// Jitter doesn't need cryptographic randomness; math/rand/v2 is the
	// correct stdlib choice here. gosec G404 doesn't distinguish use cases.
	//nolint:gosec // not a security-sensitive RNG
	return time.Duration(rand.Int64N(int64(base)))
}

var _ eventbus.Relay = (*Relay)(nil)
