package rediscache_test

import (
	"testing"

	"github.com/redis/go-redis/v9"

	rediscache "github.com/slam0504/go-ddd-adapters/cache/redis"
)

// newUnitCache builds a Cache whose client is never dialed — Name() must not
// touch the network.
func newUnitCache(t *testing.T) *rediscache.Cache {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	c, err := rediscache.New(client)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// An empty name must fall back to the documented default so the probe never
// reaches a registry (which rejects empty names) unnamed.
func TestHealthCheck_EmptyNameDefaultsToCacheRedis(t *testing.T) {
	if got := newUnitCache(t).HealthCheck("").Name(); got != "cache-redis" {
		t.Fatalf("Name() = %q, want %q", got, "cache-redis")
	}
}

// A custom name must pass through verbatim — multiple Cache instances in one
// process disambiguate their probes by name.
func TestHealthCheck_CustomNamePassesThrough(t *testing.T) {
	if got := newUnitCache(t).HealthCheck("redis-session").Name(); got != "redis-session" {
		t.Fatalf("Name() = %q, want %q", got, "redis-session")
	}
}
