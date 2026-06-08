//go:build integration

package redisidempotency_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/slam0504/go-ddd-core/ports/idempotency"
	"github.com/slam0504/go-ddd-core/ports/idempotency/idempotencytest"

	redisidempotency "github.com/slam0504/go-ddd-adapters/idempotency/redis"
	"github.com/slam0504/go-ddd-adapters/internal/redistest"
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

func TestStoreContract(t *testing.T) {
	idempotencytest.RunStoreContract(t, func(t *testing.T) idempotency.Store {
		// Unique prefix per factory call → isolated keyspace per sub-test.
		store, err := redisidempotency.New(
			sharedClient,
			redisidempotency.WithKeyPrefix("idem-"+t.Name()),
		)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return store
	})
}

// TestReclaimContract proves the liveness/reclaim guarantee: a short
// WithLeaseTTL makes the in-progress PEXPIRE fire well within ReclaimWithin, so
// the same (scope,key) reserves again as StatusNew with a fresh distinct token.
func TestReclaimContract(t *testing.T) {
	idempotencytest.RunReclaimContract(t, func(t *testing.T) idempotency.Store {
		store, err := redisidempotency.New(
			sharedClient,
			redisidempotency.WithKeyPrefix("reclaim-"+t.Name()),
			redisidempotency.WithLeaseTTL(200*time.Millisecond), // reclaim bound << ReclaimWithin
		)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return store
	}, idempotencytest.ReclaimOptions{ReclaimWithin: 2 * time.Second})
}

// TestCompletedRecordRetentionExpires verifies adapter retention policy (NOT
// covered by core's conformance suite): a completed record replays until its
// WithRetention window elapses, after which the (scope,key) reserves as new.
func TestCompletedRecordRetentionExpires(t *testing.T) {
	ctx := context.Background()
	store, err := redisidempotency.New(sharedClient,
		redisidempotency.WithKeyPrefix("retention-"+t.Name()),
		redisidempotency.WithRetention(300*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	res, err := store.Begin(ctx, "tenant:op", "k", "fp")
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := store.Finish(ctx, res, []byte("body")); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	got, err := store.Begin(ctx, "tenant:op", "k", "fp")
	if err != nil {
		t.Fatalf("Begin replay: %v", err)
	}
	if got.Status != idempotency.StatusCompleted {
		t.Fatalf("immediate replay: want Completed, got %v", got.Status)
	}
	time.Sleep(500 * time.Millisecond) // past retention
	got, err = store.Begin(ctx, "tenant:op", "k", "fp")
	if err != nil {
		t.Fatalf("Begin after retention: %v", err)
	}
	if got.Status != idempotency.StatusNew {
		t.Fatalf("after retention expiry: want New, got %v", got.Status)
	}
}

// TestCompletedRetentionDoesNotSlide verifies retention is measured from Finish
// and a replay does NOT extend it (non-sliding policy): repeated Begins within
// the window keep returning Completed, then the record still expires on time.
func TestCompletedRetentionDoesNotSlide(t *testing.T) {
	ctx := context.Background()
	store, err := redisidempotency.New(sharedClient,
		redisidempotency.WithKeyPrefix("noslide-"+t.Name()),
		redisidempotency.WithRetention(400*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	res, err := store.Begin(ctx, "tenant:op", "k", "fp")
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	finishedAt := time.Now()
	if err := store.Finish(ctx, res, []byte("body")); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	// Replay repeatedly within the window. If retention slid on each replay,
	// the record would never expire while we keep replaying.
	for time.Now().Before(finishedAt.Add(300 * time.Millisecond)) {
		got, err := store.Begin(ctx, "tenant:op", "k", "fp")
		if err != nil {
			t.Fatalf("replay Begin: %v", err)
		}
		if got.Status != idempotency.StatusCompleted {
			t.Fatalf("replay: want Completed, got %v", got.Status)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Expiry is measured from Finish, not from the last replay.
	time.Sleep(time.Until(finishedAt.Add(600 * time.Millisecond)))
	got, err := store.Begin(ctx, "tenant:op", "k", "fp")
	if err != nil {
		t.Fatalf("Begin after non-sliding window: %v", err)
	}
	if got.Status != idempotency.StatusNew {
		t.Fatalf("retention must not slide on replay: want New, got %v", got.Status)
	}
}
