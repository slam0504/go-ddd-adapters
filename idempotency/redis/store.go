package redisidempotency

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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

// compositeKey length-prefixes BOTH keyPrefix and scope so the
// (keyPrefix, scope, key) triple encodes without ambiguity. The length header
// makes ("a:b","c") and ("a","b:c") distinct (TupleSeparationSafety) AND stops a
// client-supplied key from flattening into another Store's keyPrefix namespace:
// keyPrefix="x"+key=":1:bc" and keyPrefix="x:1:a"+key="c" no longer collide,
// because the leading len(keyPrefix) digits differ (1 vs 5).
func (s *Store) compositeKey(scope, key string) string {
	return strconv.Itoa(len(s.keyPrefix)) + ":" + s.keyPrefix + ":" +
		strconv.Itoa(len(scope)) + ":" + scope + key
}

// newLeaseToken mints a 128-bit random ownership token in Go (NOT in Lua —
// scripts must stay deterministic for replication). crypto/rand makes a fresh
// token after reclaim distinct from the expired one, satisfying the reclaim
// contract's "fresh distinct token" guarantee.
func newLeaseToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// Begin atomically reserves (scope,key) for fingerprint, or reports the
// existing record's status. On StatusNew it returns a fresh lease token + the
// advisory lease TTL; a transport failure is INDETERMINATE (a coded error that
// is NOT CodeConflict), so the caller recovers by re-issuing Begin.
func (s *Store) Begin(ctx context.Context, scope, key, fingerprint string) (idempotency.Reservation, error) {
	if scope == "" || key == "" || fingerprint == "" {
		return idempotency.Reservation{}, errorsx.New(errorsx.CodeInvalidArgument,
			"redisidempotency: scope, key, and fingerprint must be non-empty")
	}
	token, err := newLeaseToken()
	if err != nil {
		return idempotency.Reservation{}, errorsx.Wrap(errorsx.CodeInternal, "redisidempotency: generate lease token", err)
	}
	raw, err := beginScript.Run(ctx, s.client,
		[]string{s.compositeKey(scope, key)},
		fingerprint, token, s.leaseTTL.Milliseconds(),
	).Result()
	if err != nil {
		// INDETERMINATE: a re-issued Begin is the recovery path, so any
		// non-conflict coded error is acceptable here.
		return idempotency.Reservation{}, errorsx.Wrap(errorsx.CodeUnavailable, "redisidempotency: begin", err)
	}
	arr, ok := raw.([]any)
	if !ok || len(arr) != 3 {
		return idempotency.Reservation{}, errorsx.New(errorsx.CodeInternal, "redisidempotency: unexpected begin reply")
	}
	statusStr, _ := arr[0].(string)
	tokStr, _ := arr[1].(string)
	respStr, _ := arr[2].(string)

	r := idempotency.Reservation{Scope: scope, Key: key}
	switch statusStr {
	case "new":
		r.Status = idempotency.StatusNew
		r.LeaseToken = tokStr
		r.LeaseTTL = s.leaseTTL
	case "inprogress":
		r.Status = idempotency.StatusInProgress
	case "completed":
		r.Status = idempotency.StatusCompleted
		r.Response = []byte(respStr)
	case "mismatch":
		r.Status = idempotency.StatusMismatch
	default:
		return idempotency.Reservation{}, errorsx.New(errorsx.CodeInternal, "redisidempotency: unknown begin status "+statusStr)
	}
	return r, nil
}

// Finish marks the reservation completed and stores response for replay. A
// stale/forged token, an already-completed record, or a vanished record yields
// CodeConflict (NOT applied for certain); a transport error is INDETERMINATE.
func (s *Store) Finish(ctx context.Context, r idempotency.Reservation, response []byte) error {
	if r.Scope == "" || r.Key == "" || r.LeaseToken == "" {
		return errorsx.New(errorsx.CodeInvalidArgument,
			"redisidempotency: reservation Scope, Key, and LeaseToken must be non-empty")
	}
	n, err := finishScript.Run(ctx, s.client,
		[]string{s.compositeKey(r.Scope, r.Key)},
		r.LeaseToken, response, s.retention.Milliseconds(),
	).Int()
	if err != nil {
		return errorsx.Wrap(errorsx.CodeUnavailable, "redisidempotency: finish", err) // INDETERMINATE
	}
	if n != 0 {
		return errorsx.New(errorsx.CodeConflict, "redisidempotency: finish rejected (stale/expired/terminal reservation)")
	}
	return nil
}

// Cancel releases an in-progress reservation. It never deletes a completed
// record (that conflicts instead, preserving the stored response). Conflict
// and INDETERMINATE semantics mirror Finish.
func (s *Store) Cancel(ctx context.Context, r idempotency.Reservation) error {
	if r.Scope == "" || r.Key == "" || r.LeaseToken == "" {
		return errorsx.New(errorsx.CodeInvalidArgument,
			"redisidempotency: reservation Scope, Key, and LeaseToken must be non-empty")
	}
	n, err := cancelScript.Run(ctx, s.client,
		[]string{s.compositeKey(r.Scope, r.Key)},
		r.LeaseToken,
	).Int()
	if err != nil {
		return errorsx.Wrap(errorsx.CodeUnavailable, "redisidempotency: cancel", err) // INDETERMINATE
	}
	if n != 0 {
		return errorsx.New(errorsx.CodeConflict, "redisidempotency: cancel rejected (stale/expired/terminal reservation)")
	}
	return nil
}
