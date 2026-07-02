// Package rediscache is a Redis-backed implementation of core's
// ports/cache.Cache, over github.com/redis/go-redis/v9.
//
// # Construction
//
//	c, err := rediscache.New(client, rediscache.WithKeyPrefix("sessions"))
//
// client may be *redis.Client, *redis.ClusterClient, or *redis.Ring (any
// redis.Cmdable). WithKeyPrefix overrides the key namespace (default "cache");
// keys are encoded prefix-free so distinct (prefix, key) tuples never collide.
//
// # Semantics
//
// Get returns cache.ErrMiss for an absent key. Set treats ttl == 0 as no expiry
// and rejects ttl < 0 as CodeInvalidArgument before backend contact (guards the
// go-redis KeepTTL == -1 footgun). Delete is idempotent. An empty key is
// CodeInvalidArgument on every method. Byte ownership: Get returns a fresh copy
// (wire decode) and Set does not retain the caller's slice (go-redis copies to
// the wire) — the cachetest input/output aliasing invariants hold.
//
// # Errors
//
// A non-nil error other than cache.ErrMiss means the call could not reach a
// decision: empty key / negative ttl -> CodeInvalidArgument (no backend); a ctx
// cancelled/expired -> that ctx error verbatim; a backend failure -> coded
// errorsx (unreachable -> CodeUnavailable, else CodeInternal), never
// CodeUnknown.
//
// # Redis Cluster / Ring
//
// Every operation is single-key, so it stays within one hash slot by
// construction — *redis.ClusterClient / *redis.Ring SHOULD work with no hash
// tag. API-derived claim; CI exercises single-node redis:7-alpine.
package rediscache
