package rediscache

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/slam0504/go-ddd-core/pkg/errorsx"
	"github.com/slam0504/go-ddd-core/ports/cache"
)

// Cache is a Redis-backed implementation of core's ports/cache.Cache. It is
// safe for concurrent use (go-redis clients are). Keys are namespaced with a
// prefix-free encoding (see key.go).
type Cache struct {
	client    redis.Cmdable
	keyPrefix string
}

// New builds a Cache over client. client may be *redis.Client,
// *redis.ClusterClient, or *redis.Ring (all satisfy redis.Cmdable). redis.Cmdable
// is not a minimal interface but is the existing public go-redis entry that
// exposes Get/Set/Del/Exists without the lifecycle/subscribe semantics of
// UniversalClient. New rejects a nil interface and a typed-nil of the three
// documented concrete types; a typed-nil CUSTOM Cmdable is not guarded and
// surfaces on first call (the caller's bug).
func New(client redis.Cmdable, opts ...Option) (*Cache, error) {
	if client == nil {
		return nil, ErrNilClient
	}
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
	cfg := config{keyPrefix: defaultKeyPrefix}
	for _, opt := range opts {
		opt(&cfg)
	}
	return &Cache{client: client, keyPrefix: cfg.keyPrefix}, nil
}

// Get returns the value for key or cache.ErrMiss if absent. Precedence:
// empty key -> pre-cancelled ctx -> backend.
func (c *Cache) Get(ctx context.Context, key string) ([]byte, error) {
	if key == "" {
		return nil, errInvalidKey()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b, err := c.client.Get(ctx, encode(c.keyPrefix, key)).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, cache.ErrMiss
		}
		return nil, mapBackendErr("get", err)
	}
	return b, nil
}

// Set stores value under key with the given ttl. ttl == 0 means no expiry; ttl
// < 0 is invalid input. Precedence: empty key -> negative ttl -> pre-cancelled
// ctx -> backend.
func (c *Cache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if key == "" {
		return errInvalidKey()
	}
	if ttl < 0 {
		return errorsx.New(errorsx.CodeInvalidArgument, "rediscache: negative ttl")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.client.Set(ctx, encode(c.keyPrefix, key), value, ttl).Err(); err != nil {
		return mapBackendErr("set", err)
	}
	return nil
}

// Delete removes key. Deleting an absent key is not an error (idempotent).
// Precedence: empty key -> pre-cancelled ctx -> backend.
func (c *Cache) Delete(ctx context.Context, key string) error {
	if key == "" {
		return errInvalidKey()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.client.Del(ctx, encode(c.keyPrefix, key)).Err(); err != nil {
		return mapBackendErr("delete", err)
	}
	return nil
}

// Exists reports whether key is present. Precedence: empty key ->
// pre-cancelled ctx -> backend.
func (c *Cache) Exists(ctx context.Context, key string) (bool, error) {
	if key == "" {
		return false, errInvalidKey()
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	n, err := c.client.Exists(ctx, encode(c.keyPrefix, key)).Result()
	if err != nil {
		return false, mapBackendErr("exists", err)
	}
	return n > 0, nil
}

func errInvalidKey() error {
	return errorsx.New(errorsx.CodeInvalidArgument, "rediscache: empty key")
}

// mapBackendErr wraps a non-ctx backend error as a coded errorsx whose CodeOf
// is never CodeUnknown. A ctx error observed during the call is returned
// verbatim so callers can errors.Is it.
func mapBackendErr(op string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return errorsx.Wrap(classifyBackendErr(err), "rediscache: "+op, err)
}

// classifyBackendErr maps a non-ctx backend error to CodeUnavailable (transport
// / reachability) or CodeInternal (everything else) — never CodeUnknown.
// Mirrors ratelimit/redisrate and jobs/asynq.
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

// Compile-time assertion that *Cache implements core's cache.Cache.
var _ cache.Cache = (*Cache)(nil)
