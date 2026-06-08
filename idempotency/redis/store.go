package redisidempotency

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/slam0504/go-ddd-core/pkg/errorsx"
	"github.com/slam0504/go-ddd-core/ports/idempotency"
)

// Constructor errors surface wiring bugs at New time; they are never returned
// from Begin/Finish/Cancel.
var (
	ErrNilClient         = errors.New("redisidempotency: client must not be nil")
	ErrEmptyKeyPrefix    = errors.New("redisidempotency: key prefix must not be empty")
	ErrLeaseTTLTooSmall  = errors.New("redisidempotency: lease TTL must be >= 1ms")
	ErrRetentionTooSmall = errors.New("redisidempotency: retention must be >= 1ms")
)

// Store implements idempotency.Store over Redis. Immutable after New and safe
// for concurrent use (each method is one atomic single-key Lua script, so it
// also works against *redis.ClusterClient / *redis.Ring — see doc.go).
type Store struct {
	client    redis.Scripter
	keyPrefix string
	leaseTTL  time.Duration
	retention time.Duration
}

var _ idempotency.Store = (*Store)(nil)

// New wraps client (any redis.Scripter — *redis.Client, *redis.ClusterClient,
// *redis.Ring, or a test fake) as an idempotency.Store. It rejects a nil
// interface and a typed-nil of the three documented go-redis concrete types; a
// typed-nil custom Scripter is NOT guarded (documented in doc.go) and surfaces
// on first command.
func New(client redis.Scripter, opts ...Option) (*Store, error) {
	if client == nil {
		return nil, ErrNilClient
	}
	// An interface holding a nil pointer is itself non-nil, so the check above
	// does not catch New((*redis.Client)(nil)). Type-switch the documented
	// concrete types and reject a nil pointer of each (mirrors casbinauth). No
	// generic reflection — a typed-nil custom Scripter stays the caller's bug.
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

	c := &config{keyPrefix: defaultKeyPrefix, leaseTTL: defaultLeaseTTL, retention: defaultRetention}
	for _, opt := range opts {
		opt(c)
	}
	// Floor at 1ms, NOT just > 0: Redis PEXPIRE takes milliseconds, so a sub-ms
	// positive duration truncates to 0 via (time.Duration).Milliseconds() and the
	// runtime Redis call would fail after New already returned a Store. Reject it
	// at construction (fail-loud) instead of silently rounding up the caller's
	// stated TTL.
	switch {
	case c.keyPrefix == "":
		return nil, ErrEmptyKeyPrefix
	case c.leaseTTL < time.Millisecond:
		return nil, ErrLeaseTTLTooSmall
	case c.retention < time.Millisecond:
		return nil, ErrRetentionTooSmall
	}
	return &Store{client: client, keyPrefix: c.keyPrefix, leaseTTL: c.leaseTTL, retention: c.retention}, nil
}

// compositeKey length-prefixes scope so ("a:b","c") and ("a","b:c") never
// collide (TupleSeparationSafety).
func (s *Store) compositeKey(scope, key string) string {
	return s.keyPrefix + ":" + strconv.Itoa(len(scope)) + ":" + scope + key
}

// Begin — Redis path implemented in Task 4. Validation only for now.
func (s *Store) Begin(ctx context.Context, scope, key, fingerprint string) (idempotency.Reservation, error) {
	if scope == "" || key == "" || fingerprint == "" {
		return idempotency.Reservation{}, errorsx.New(errorsx.CodeInvalidArgument,
			"redisidempotency: scope, key, and fingerprint must be non-empty")
	}
	return idempotency.Reservation{}, errors.New("redisidempotency: Begin not implemented")
}

// Finish — Redis path implemented in Task 4. Validation only for now.
func (s *Store) Finish(ctx context.Context, r idempotency.Reservation, response []byte) error {
	if r.Scope == "" || r.Key == "" || r.LeaseToken == "" {
		return errorsx.New(errorsx.CodeInvalidArgument,
			"redisidempotency: reservation Scope, Key, and LeaseToken must be non-empty")
	}
	return errors.New("redisidempotency: Finish not implemented")
}

// Cancel — Redis path implemented in Task 4. Validation only for now.
func (s *Store) Cancel(ctx context.Context, r idempotency.Reservation) error {
	if r.Scope == "" || r.Key == "" || r.LeaseToken == "" {
		return errorsx.New(errorsx.CodeInvalidArgument,
			"redisidempotency: reservation Scope, Key, and LeaseToken must be non-empty")
	}
	return errors.New("redisidempotency: Cancel not implemented")
}
