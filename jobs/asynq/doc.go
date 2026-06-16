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
