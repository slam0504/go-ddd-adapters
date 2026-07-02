package rediscache

import "github.com/slam0504/go-ddd-core/pkg/errorsx"

// defaultKeyPrefix namespaces keys when the caller supplies no WithKeyPrefix.
const defaultKeyPrefix = "cache"

// ErrNilClient is returned by New when client is a nil interface or a typed-nil
// of one of the documented concrete go-redis client types. It is an
// errorsx-coded CodeInvalidArgument (mirrors ratelimit/redisrate) so
// errorsx.CodeOf(ErrNilClient) is never CodeUnknown, consistent with the
// empty-key / negative-ttl coding used elsewhere in this adapter.
var ErrNilClient = errorsx.New(errorsx.CodeInvalidArgument, "rediscache: nil redis client")

type config struct {
	keyPrefix string
}

// Option configures a Cache at construction time.
type Option func(*config)

// WithKeyPrefix overrides the key namespace (default "cache"). Two caches with
// different prefixes never share an entry for the same key (see key.go).
func WithKeyPrefix(prefix string) Option {
	return func(c *config) { c.keyPrefix = prefix }
}
