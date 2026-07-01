//go:build integration

package redisratelimit_test

import (
	"context"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-redis/redis_rate/v10"
	"github.com/redis/go-redis/v9"
	"github.com/slam0504/go-ddd-core/pkg/errorsx"
	"github.com/slam0504/go-ddd-core/ports/ratelimit"
	"github.com/slam0504/go-ddd-core/ports/ratelimit/ratelimittest"

	"github.com/slam0504/go-ddd-adapters/internal/redistest"
	redisratelimit "github.com/slam0504/go-ddd-adapters/ratelimit/redisrate"
)

var sharedClient *redis.Client

func TestMain(m *testing.M) {
	ctx := context.Background()
	client, cleanup, err := redistest.StartContainer(ctx)
	if err != nil {
		panic(err)
	}
	sharedClient = client
	code := m.Run()
	cleanup()
	os.Exit(code)
}

// factoryCounter guarantees a UNIQUE namespace per factory call. RunContract
// reuses fixed keys (ratelimittest:a / :b) across subtests and TestMain boots
// the container ONCE; under -count>1 / -race reruns t.Name() repeats, so an
// atomic counter (not t.Name() alone, not the long Period) is what isolates
// state across calls.
var factoryCounter atomic.Uint64

// TestRateLimitContract runs core's deterministic conformance suite. Profile:
// Burst 1 → first same-key Allow allowed, second denied; the long Period keeps
// GCRA refill from racing the suite's no-sleep invariants.
func TestRateLimitContract(t *testing.T) {
	ratelimittest.RunContract(t, func(t *testing.T) ratelimit.Limiter {
		n := factoryCounter.Add(1)
		l, err := redisratelimit.New(
			sharedClient,
			redis_rate.Limit{Rate: 1, Burst: 1, Period: time.Hour},
			redisratelimit.WithKeyPrefix("ratelimittest:"+t.Name()+":"+strconv.FormatUint(n, 10)),
		)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return l
	})
}

// TestAllowRedisUnavailable: a valid key + live ctx against an unreachable Redis
// must surface CodeUnavailable (never CodeUnknown). Port 1 refuses immediately.
func TestAllowRedisUnavailable(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = client.Close() })
	l, err := redisratelimit.New(client, redis_rate.Limit{Rate: 1, Burst: 1, Period: time.Hour})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = l.Allow(context.Background(), "k")
	if code := errorsx.CodeOf(err); code != errorsx.CodeUnavailable {
		t.Fatalf("unreachable Redis: code = %v, want CodeUnavailable (err=%v)", code, err)
	}
}

// TestAllowRecoversAfterRefill exercises the timing-dependent behaviour
// RunContract deliberately omits: deplete the bucket, wait past RetryAfter, and
// the key is allowed again (GCRA refill).
func TestAllowRecoversAfterRefill(t *testing.T) {
	n := factoryCounter.Add(1)
	l, err := redisratelimit.New(sharedClient,
		redis_rate.Limit{Rate: 1, Burst: 1, Period: 500 * time.Millisecond},
		redisratelimit.WithKeyPrefix("recover:"+strconv.FormatUint(n, 10)),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	first, err := l.Allow(ctx, "k")
	if err != nil || !first.Allowed {
		t.Fatalf("first Allow: allowed=%v err=%v, want allowed", first.Allowed, err)
	}
	if first.RetryAfter != 0 {
		t.Fatalf("allowed RetryAfter = %v, want 0", first.RetryAfter)
	}
	denied, err := l.Allow(ctx, "k")
	if err != nil {
		t.Fatalf("second Allow: %v", err)
	}
	if denied.Allowed {
		t.Fatal("second Allow: want denied")
	}
	if denied.RetryAfter <= 0 {
		t.Fatalf("denied RetryAfter = %v, want > 0", denied.RetryAfter)
	}
	time.Sleep(denied.RetryAfter + 100*time.Millisecond)
	recovered, err := l.Allow(ctx, "k")
	if err != nil {
		t.Fatalf("recovery Allow: %v", err)
	}
	if !recovered.Allowed {
		t.Fatal("after waiting past RetryAfter the key must be allowed again (GCRA refill)")
	}
}

// TestKeyPrefixIsolationEndToEnd: two Limiters with different prefixes do not
// share a bucket for the same key (end-to-end check on top of the encode unit
// test).
func TestKeyPrefixIsolationEndToEnd(t *testing.T) {
	n := factoryCounter.Add(1)
	mk := func(prefix string) ratelimit.Limiter {
		l, err := redisratelimit.New(sharedClient,
			redis_rate.Limit{Rate: 1, Burst: 1, Period: time.Hour},
			redisratelimit.WithKeyPrefix(prefix+":"+strconv.FormatUint(n, 10)),
		)
		if err != nil {
			t.Fatalf("New(%s): %v", prefix, err)
		}
		return l
	}
	a := mk("tenant-a")
	b := mk("tenant-b")
	ctx := context.Background()
	if _, err := a.Allow(ctx, "same-key"); err != nil {
		t.Fatalf("a deplete #1: %v", err)
	}
	depleted, err := a.Allow(ctx, "same-key")
	if err != nil {
		t.Fatalf("a deplete #2: %v", err)
	}
	if depleted.Allowed {
		t.Fatal("a second Allow should be denied (Burst 1)")
	}
	res, err := b.Allow(ctx, "same-key")
	if err != nil {
		t.Fatalf("b Allow: %v", err)
	}
	if !res.Allowed {
		t.Fatal("tenant-b first Allow on the same key was denied; prefixes must isolate buckets")
	}
}
