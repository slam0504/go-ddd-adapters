//go:build integration

package redistest

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

// RedisImage is the container image used by StartContainer. Pinned to a
// specific major so behavior is reproducible; the adapter only needs EVAL +
// PEXPIRE (Redis 2.6+), so 7 is comfortably above the floor.
const RedisImage = "redis:7-alpine"

// StartContainer boots a throwaway Redis testcontainer, opens a connected
// *redis.Client against it, and returns the client plus a cleanup func. The
// cleanup closes the client and terminates the container; callers should defer
// it from TestMain.
func StartContainer(ctx context.Context) (*redis.Client, func(), error) {
	container, err := tcredis.Run(ctx, RedisImage)
	if err != nil {
		return nil, nil, fmt.Errorf("redistest: run container: %w", err)
	}
	uri, err := container.ConnectionString(ctx)
	if err != nil {
		_ = testcontainers.TerminateContainer(container)
		return nil, nil, fmt.Errorf("redistest: connection string: %w", err)
	}
	opts, err := redis.ParseURL(uri)
	if err != nil {
		_ = testcontainers.TerminateContainer(container)
		return nil, nil, fmt.Errorf("redistest: parse url: %w", err)
	}
	client := redis.NewClient(opts)
	// Sanity-check the client actually reaches Redis before handing it back.
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		_ = testcontainers.TerminateContainer(container)
		return nil, nil, fmt.Errorf("redistest: ping: %w", err)
	}
	cleanup := func() {
		_ = client.Close()
		_ = testcontainers.TerminateContainer(container)
	}
	return client, cleanup, nil
}
