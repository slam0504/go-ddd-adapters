//go:build integration

package rediscache_test

import (
	"context"
	"errors"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/slam0504/go-ddd-core/pkg/errorsx"
	"github.com/slam0504/go-ddd-core/ports/cache"
	"github.com/slam0504/go-ddd-core/ports/cache/cachetest"

	rediscache "github.com/slam0504/go-ddd-adapters/cache/redis"
	"github.com/slam0504/go-ddd-adapters/internal/redistest"
)

var testClient *redis.Client

// cacheCounter salts each Factory / test keyspace so subtests stay isolated on
// the shared container. A package-level atomic counter (not a per-Test var, not
// the wall clock) stays unique across -count>1 / -race reruns within one go
// test process; mirrors ratelimit/redisrate's factoryCounter.
var cacheCounter atomic.Uint64

func nextPrefix(tag string) string {
	return tag + "-" + strconv.FormatUint(cacheCounter.Add(1), 10)
}

func TestMain(m *testing.M) {
	ctx := context.Background()
	client, cleanup, err := redistest.StartContainer(ctx)
	if err != nil {
		panic(err)
	}
	testClient = client
	code := m.Run()
	cleanup()
	os.Exit(code)
}

// TestRunContract runs core's cache conformance suite against the Redis adapter.
// Each Factory call gets a globally-unique key prefix (subtest name + counter)
// so subtests are state-isolated against the shared container.
func TestRunContract(t *testing.T) {
	cachetest.RunContract(t, func(t *testing.T) cache.Cache {
		c, err := rediscache.New(testClient, rediscache.WithKeyPrefix(nextPrefix("cachetest:"+t.Name())))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return c
	})
}

// TestTTLExpiry is the adapter intent test the deterministic suite excludes: a
// key set with a short TTL is gone after it elapses.
func TestTTLExpiry(t *testing.T) {
	c, err := rediscache.New(testClient, rediscache.WithKeyPrefix(nextPrefix("ttl")))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if err := c.Set(ctx, "k", []byte("v"), 50*time.Millisecond); err != nil {
		t.Fatalf("Set: %v", err)
	}
	time.Sleep(120 * time.Millisecond)
	if _, err := c.Get(ctx, "k"); !errors.Is(err, cache.ErrMiss) {
		t.Fatalf("Get after TTL err = %v, want cache.ErrMiss", err)
	}
}

// TestKeyPrefixIsolation proves two prefixes never share an entry for one key.
func TestKeyPrefixIsolation(t *testing.T) {
	ctx := context.Background()
	a, _ := rediscache.New(testClient, rediscache.WithKeyPrefix(nextPrefix("A")))
	b, _ := rediscache.New(testClient, rediscache.WithKeyPrefix(nextPrefix("B")))
	if err := a.Set(ctx, "shared", []byte("va"), 0); err != nil {
		t.Fatalf("a.Set: %v", err)
	}
	if _, err := b.Get(ctx, "shared"); !errors.Is(err, cache.ErrMiss) {
		t.Fatalf("prefix leak: b.Get(shared) err = %v, want cache.ErrMiss", err)
	}
}

// TestNegativeTTLRejected confirms the validation-first precedence end to end
// (also covered deterministically by the suite).
func TestNegativeTTLRejected(t *testing.T) {
	c, _ := rediscache.New(testClient, rediscache.WithKeyPrefix(nextPrefix("neg")))
	err := c.Set(context.Background(), "k", []byte("v"), -1)
	if errorsx.CodeOf(err) != errorsx.CodeInvalidArgument {
		t.Fatalf("Set(ttl<0) CodeOf = %v, want CodeInvalidArgument", errorsx.CodeOf(err))
	}
}

// TestHealthCheck_Ping verifies the exported probe against a live container:
// nil on healthy PING, non-nil under a cancelled ctx (deterministic error
// path — no container stop needed).
func TestHealthCheck_Ping(t *testing.T) {
	c, err := rediscache.New(testClient, rediscache.WithKeyPrefix(nextPrefix("health")))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.HealthCheck("").Check(context.Background()); err != nil {
		t.Fatalf("Check on live container: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.HealthCheck("").Check(ctx); err == nil {
		t.Fatal("Check with cancelled ctx: want non-nil error, got nil")
	}
}
