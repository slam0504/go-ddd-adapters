package redisratelimit

import "github.com/slam0504/go-ddd-core/pkg/errorsx"

// defaultKeyPrefix namespaces this limiter's Redis keys. Non-empty by design so
// the adapter's keys never sit at the bare top level of a shared Redis. It still
// encodes injectively via encode (see key.go).
const defaultKeyPrefix = "ratelimit"

var (
	// ErrNilClient is returned by New for a nil client interface or a typed-nil
	// of the three documented go-redis concrete types.
	ErrNilClient = errorsx.New(errorsx.CodeInvalidArgument, "redisratelimit: nil Redis client")
	// ErrInvalidLimit is returned by New when Rate, Burst, or Period is not > 0.
	ErrInvalidLimit = errorsx.New(errorsx.CodeInvalidArgument, "redisratelimit: limit must have positive Rate, Burst, and Period")
)

// Option configures a Limiter. Options are pure setters into the private config;
// validation runs in New AFTER every option is applied, so option order never
// changes the result (mirrors redisidempotency / jobsasynq).
type Option func(*config)

type config struct {
	keyPrefix string
}

// WithKeyPrefix overrides the Redis key namespace (default "ratelimit"). Two
// Limiters with different prefixes never share a bucket for the same key.
func WithKeyPrefix(prefix string) Option {
	return func(c *config) { c.keyPrefix = prefix }
}
