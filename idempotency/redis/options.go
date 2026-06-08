package redisidempotency

import "time"

// Option configures the Store built by New. Options are pure setters into the
// private config; all validation runs in New after every option is applied, so
// option order never changes the result (mirrors the casbinauth pattern).
type Option func(*config)

type config struct {
	keyPrefix string
	leaseTTL  time.Duration
	retention time.Duration
}

const (
	defaultKeyPrefix = "idem"
	defaultLeaseTTL  = 30 * time.Second // in-progress reclaim bound
	defaultRetention = 24 * time.Hour   // completed-record retention
)

// WithKeyPrefix overrides the Redis key namespace (default "idem"). An empty
// prefix makes New fail loud (ErrEmptyKeyPrefix).
func WithKeyPrefix(p string) Option { return func(c *config) { c.keyPrefix = p } }

// WithLeaseTTL bounds how long an in-progress reservation is held before it is
// reclaimable so the (scope, key) can be reserved again. Must be >= 1ms: the
// value is passed to Redis PEXPIRE (millisecond granularity), so a sub-ms
// duration would truncate to 0 and New rejects it (ErrLeaseTTLTooSmall) rather
// than silently rounding it up.
func WithLeaseTTL(d time.Duration) Option { return func(c *config) { c.leaseTTL = d } }

// WithRetention sets how long a completed record (and its replayable response)
// is kept after Finish. Must be >= 1ms (same PEXPIRE millisecond-granularity
// reason as WithLeaseTTL); New rejects a sub-ms duration (ErrRetentionTooSmall).
func WithRetention(d time.Duration) Option { return func(c *config) { c.retention = d } }
