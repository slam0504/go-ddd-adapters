package redisratelimit

import (
	"context"
	"errors"
	"net"
	"strings"

	"github.com/go-redis/redis_rate/v10"
	"github.com/redis/go-redis/v9"
	"github.com/slam0504/go-ddd-core/pkg/errorsx"
	"github.com/slam0504/go-ddd-core/ports/ratelimit"
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

// mapResult projects redis_rate's GCRA result onto core's advisory Result.
//   - Allowed: redis_rate.Allowed is a COUNT (0 = denied, >0 = allowed).
//   - RetryAfter: redis_rate returns -1 when allowed; the contract REQUIRES 0
//     on allow, so it is mapped explicitly (raw mapping would leak -1). On
//     denial res.RetryAfter is > 0 and <= Period.
//   - Limit: the GCRA instantaneous burst (limit.Burst), NOT a fixed-window
//     quota.
//   - Remaining: instantaneous capacity; GCRA guarantees <= Burst, so
//     Remaining <= Limit.
//   - ResetAt: absent (zero) — avoids client-clock skew (see doc.go scope-out).
func mapResult(res *redis_rate.Result, limit redis_rate.Limit) ratelimit.Result {
	out := ratelimit.Result{
		Allowed:   res.Allowed > 0,
		Limit:     limit.Burst,
		Remaining: res.Remaining,
	}
	if !out.Allowed {
		out.RetryAfter = res.RetryAfter
	}
	// Allowed: RetryAfter stays the zero value; res.RetryAfter (-1) is NEVER read.
	return out
}

// mapError maps a redis_rate.Allow error to the contract's CLASS-2 shape. A ctx
// error (cancellation/expiry observed DURING the call) is returned verbatim; any
// other backend error gets a coded errorsx surface whose CodeOf is never
// CodeUnknown.
func mapError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return errorsx.Wrap(classifyBackendErr(err), "redisratelimit: allow", err)
}

// classifyBackendErr maps a non-ctx backend error to CodeUnavailable (transport
// / reachability failure) or CodeInternal (everything else) — never
// CodeUnknown. Mirrors jobs/asynq.classifyBackendErr.
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

// Allow reports whether key may proceed right now, as Result data. Ordinary
// quota exhaustion is NOT an error — it returns Result{Allowed:false}, nil with
// a RetryAfter hint. Fixed precedence (contract): empty key → pre-cancelled or
// expired ctx → backend. The first two short-circuit before any Redis contact.
func (l *Limiter) Allow(ctx context.Context, key string) (ratelimit.Result, error) {
	if key == "" {
		return ratelimit.Result{}, errorsx.New(errorsx.CodeInvalidArgument,
			"redisratelimit: empty key (a missing partition key, not an anonymous caller)")
	}
	if err := ctx.Err(); err != nil {
		return ratelimit.Result{}, err
	}
	res, err := l.limiter.Allow(ctx, encode(l.keyPrefix, key), l.limit)
	if err != nil {
		return ratelimit.Result{}, mapError(err)
	}
	return mapResult(res, l.limit), nil
}

// Compile-time assertion that *Limiter implements core's ratelimit.Limiter.
var _ ratelimit.Limiter = (*Limiter)(nil)
