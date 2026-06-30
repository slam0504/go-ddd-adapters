package redisratelimit

import (
	"github.com/go-redis/redis_rate/v10"
	"github.com/redis/go-redis/v9"
)

// Limiter is a distributed inbound rate limiter backed by Redis (redis_rate's
// GCRA). It implements core's ratelimit.Limiter and is safe for concurrent use.
type Limiter struct {
	limiter   *redis_rate.Limiter
	limit     redis_rate.Limit
	keyPrefix string
}

// New builds a Limiter wrapping client under the given redis_rate limit. client
// may be *redis.Client, *redis.ClusterClient, or *redis.Ring (all satisfy
// redis.UniversalClient, which in turn satisfies redis_rate's internal rediser
// — that needs Del, which redis.Scripter lacks). New rejects a nil interface and
// a typed-nil of those three concrete types; a typed-nil custom client is NOT
// guarded and surfaces on first Allow. limit is positional and required: Rate,
// Burst, and Period MUST all be > 0 — there is no universal safe default, and a
// silent one would turn "forgot to configure" into a silent bug.
func New(client redis.UniversalClient, limit redis_rate.Limit, opts ...Option) (*Limiter, error) {
	if client == nil {
		return nil, ErrNilClient
	}
	// An interface holding a nil pointer is itself non-nil, so the check above
	// does not catch New((*redis.Client)(nil), ...). Type-switch the documented
	// concrete types and reject a nil pointer of each (mirrors redisidempotency).
	switch c := client.(type) {
	case *redis.Client:
		if c == nil {
			return nil, ErrNilClient
		}
	case *redis.ClusterClient:
		if c == nil {
			return nil, ErrNilClient
		}
	case *redis.Ring:
		if c == nil {
			return nil, ErrNilClient
		}
	}
	if limit.Rate <= 0 || limit.Burst <= 0 || limit.Period <= 0 {
		return nil, ErrInvalidLimit
	}
	cfg := config{keyPrefix: defaultKeyPrefix}
	for _, opt := range opts {
		opt(&cfg)
	}
	return &Limiter{
		limiter:   redis_rate.NewLimiter(client),
		limit:     limit,
		keyPrefix: cfg.keyPrefix,
	}, nil
}
