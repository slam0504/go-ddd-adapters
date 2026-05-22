//go:build integration

// Package integration runs the Kafka + Postgres adapters against real
// services started via testcontainers. Tests are gated behind the
// `integration` build tag so `go test ./...` stays fast for everyday
// work.
package integration

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/modules/redpanda"

	pgxoutboxmigs "github.com/slam0504/go-ddd-adapters/eventbus/outbox/pgx/migrations"

	ordersmigs "github.com/slam0504/go-ddd-adapters/examples/orders/migrations"
)

const (
	redpandaImage = "docker.redpanda.com/redpandadata/redpanda:v24.2.7"
	postgresImage = "postgres:16-alpine"
)

// sharedBrokers is populated by run() with the broker address of a
// single redpanda container shared across all tests in this package.
var sharedBrokers []string

// sharedPool is the pgx pool against the shared Postgres container,
// populated after migrations apply. Tests TRUNCATE between cases to
// keep one container fast.
var sharedPool *pgxpool.Pool

// TestMain wraps run so deferred cleanups (container Terminate, pool
// Close) fire before the process exits. os.Exit(m.Run()) would skip
// every defer above it.
func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	ctx := context.Background()

	// Postgres
	pgC, err := postgres.Run(ctx, postgresImage,
		postgres.WithDatabase("orders"),
		postgres.WithUsername("orders"),
		postgres.WithPassword("orders"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		log.Printf("integration: start postgres: %v", err)
		return 1
	}
	defer func() {
		if terr := pgC.Terminate(ctx); terr != nil {
			log.Printf("integration: terminate postgres: %v", terr)
		}
	}()

	pgDSN, err := pgC.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		log.Printf("integration: postgres dsn: %v", err)
		return 1
	}

	pool, err := pgxpool.New(ctx, pgDSN)
	if err != nil {
		log.Printf("integration: pgxpool: %v", err)
		return 1
	}
	defer pool.Close()
	sharedPool = pool

	if err := applyMigrations(pgDSN, "outbox_schema_migrations", pgxoutboxmigs.FS); err != nil {
		log.Printf("integration: outbox migrations: %v", err)
		return 1
	}
	if err := applyMigrations(pgDSN, "orders_schema_migrations", ordersmigs.FS); err != nil {
		log.Printf("integration: orders migrations: %v", err)
		return 1
	}

	// Redpanda
	rpC, err := redpanda.Run(ctx, redpandaImage, redpanda.WithAutoCreateTopics())
	if err != nil {
		log.Printf("integration: start redpanda: %v", err)
		return 1
	}
	defer func() {
		if terr := rpC.Terminate(ctx); terr != nil {
			log.Printf("integration: terminate redpanda: %v", terr)
		}
	}()

	broker, err := rpC.KafkaSeedBroker(ctx)
	if err != nil {
		log.Printf("integration: kafka broker: %v", err)
		return 1
	}
	sharedBrokers = []string{broker}

	fmt.Fprintf(os.Stderr, "integration: postgres ready at %s\n", pgDSN)
	fmt.Fprintf(os.Stderr, "integration: redpanda ready at %s\n", broker)

	return m.Run()
}

// applyMigrations runs `up` for srcFS against pgDSN with the given
// migrations-state table. The pgx5 driver requires the URL scheme
// pgx5://; the testcontainer DSN starts with postgres://, so we rewrite.
func applyMigrations(pgDSN, table string, srcFS embed.FS) error {
	src, err := iofs.New(srcFS, ".")
	if err != nil {
		return fmt.Errorf("iofs: %w", err)
	}

	pgx5DSN := strings.Replace(pgDSN, "postgres://", "pgx5://", 1)
	if strings.Contains(pgx5DSN, "?") {
		pgx5DSN += "&x-migrations-table=" + table
	} else {
		pgx5DSN += "?x-migrations-table=" + table
	}

	m, err := migrate.NewWithSourceInstance("iofs", src, pgx5DSN)
	if err != nil {
		return fmt.Errorf("new migrate (%s): %w", table, err)
	}
	defer func() {
		srcErr, dbErr := m.Close()
		if srcErr != nil {
			log.Printf("integration: migrate close src (%s): %v", table, srcErr)
		}
		if dbErr != nil {
			log.Printf("integration: migrate close db (%s): %v", table, dbErr)
		}
	}()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("up (%s): %w", table, err)
	}
	return nil
}

// truncate clears all three tables. Called by each test that needs a
// clean slate. The two outbox tables and orders are TRUNCATEd together
// in one statement so foreign keys (none today) wouldn't trip ordering.
func truncate(t *testing.T) {
	t.Helper()
	_, err := sharedPool.Exec(context.Background(),
		`TRUNCATE orders, outbox_records, outbox_dead_letters RESTART IDENTITY`)
	if err != nil {
		t.Fatalf("truncate: %v", err)
	}
}
