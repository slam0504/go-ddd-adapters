//go:build integration

package redisidempotency_test

import (
	"context"
	"os"
	"testing"

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
