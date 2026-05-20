//go:build integration

package pgxtest

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// PostgresImage is the container image used by StartContainer. Pinned to
// a specific minor so test behavior is reproducible. Postgres 16 gives
// modern semantics while staying above the adapter's declared 12+
// baseline.
const PostgresImage = "postgres:16-alpine"

// StartContainer boots a Postgres testcontainer, opens a pgxpool.Pool
// against it, and returns the pool plus a cleanup func. The cleanup
// closes the pool and terminates the container; callers should defer
// it from TestMain.
//
// Callers are responsible for any schema setup (CREATE TABLE / running
// migrations) after StartContainer returns. The helper deliberately
// stays schema-agnostic.
func StartContainer(ctx context.Context) (*pgxpool.Pool, func(), error) {
	container, err := tcpostgres.Run(
		ctx,
		PostgresImage,
		tcpostgres.WithDatabase("pgxtest"),
		tcpostgres.WithUsername("pgxtest"),
		tcpostgres.WithPassword("pgxtest"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("pgxtest: start container: %w", err)
	}

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = container.Terminate(context.Background())
		return nil, nil, fmt.Errorf("pgxtest: connection string: %w", err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		_ = container.Terminate(context.Background())
		return nil, nil, fmt.Errorf("pgxtest: pgxpool.New: %w", err)
	}

	// Sanity-check the pool actually works before handing it back.
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		_ = container.Terminate(context.Background())
		return nil, nil, fmt.Errorf("pgxtest: ping: %w", err)
	}

	cleanup := func() {
		pool.Close()
		if terr := container.Terminate(context.Background()); terr != nil {
			// Mirrors the redpanda helper: log via fmt but don't fail
			// the suite because terminate is best-effort.
			fmt.Printf("pgxtest: terminate container: %v\n", terr)
		}
	}

	return pool, cleanup, nil
}
