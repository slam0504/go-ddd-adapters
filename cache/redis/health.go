package rediscache

import (
	"context"

	"github.com/slam0504/go-ddd-core/ports/health"
)

// defaultHealthCheckName names the probe when the caller passes "".
const defaultHealthCheckName = "cache-redis"

// HealthCheck returns a core ports/health.Check that probes the underlying
// Redis client with PING. An empty name defaults to "cache-redis"; processes
// wiring several Caches into one registry should pass distinct names
// (registries reject duplicates). Ping errors pass through as-is — the health
// contract only distinguishes nil from non-nil, so no errorsx coding is
// applied. The client was already nil-guarded by New; HealthCheck adds no
// further validation.
func (c *Cache) HealthCheck(name string) health.Check {
	if name == "" {
		name = defaultHealthCheckName
	}
	return health.NewCheck(name, func(ctx context.Context) error {
		return c.client.Ping(ctx).Err()
	})
}
