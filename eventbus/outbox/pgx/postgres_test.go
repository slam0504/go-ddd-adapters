//go:build integration

package pgxoutbox_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"

	pgxmigrations "github.com/slam0504/go-ddd-adapters/eventbus/outbox/pgx/migrations"
	"github.com/slam0504/go-ddd-adapters/internal/pgxtest"
)

// sharedPool is the pgxpool.Pool against the testcontainer Postgres
// started by TestMain after migrations.FS has been applied via
// golang-migrate's iofs source + pgx/v5 database driver. All
// integration tests in this package read it directly.
var sharedPool *pgxpool.Pool

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

// run wraps the suite so deferred cleanup actually executes — TestMain
// itself cannot use defer because os.Exit aborts the goroutine before
// any defers fire. Mirrors the pattern in ports/database/pgx tests.
func run(m *testing.M) int {
	ctx := context.Background()

	pool, cleanup, err := pgxtest.StartContainer(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pgxoutbox integration: start container: %v\n", err)
		return 1
	}
	defer cleanup()

	if err := applyMigrations(pool); err != nil {
		fmt.Fprintf(os.Stderr, "pgxoutbox integration: migrations: %v\n", err)
		return 1
	}

	sharedPool = pool
	fmt.Fprintln(os.Stderr, "pgxoutbox integration: postgres ready, migrations applied")
	return m.Run()
}

// applyMigrations runs the embedded migrations.FS via golang-migrate's
// iofs source + pgx/v5 database driver. The driver expects a
// `pgx5://` scheme — the testcontainer hands us `postgres://`, so a
// single string replacement bridges the two.
func applyMigrations(pool *pgxpool.Pool) error {
	src, err := iofs.New(pgxmigrations.FS, ".")
	if err != nil {
		return fmt.Errorf("iofs.New: %w", err)
	}
	defer func() { _ = src.Close() }()

	dsn := strings.Replace(pool.Config().ConnString(), "postgres://", "pgx5://", 1)

	mig, err := migrate.NewWithSourceInstance("iofs", src, dsn)
	if err != nil {
		return fmt.Errorf("migrate.NewWithSourceInstance: %w", err)
	}
	defer func() {
		// migrate.Migrate.Close returns (source-err, db-err) — neither
		// is worth bubbling up at TestMain shutdown; testcontainers will
		// reap the database regardless.
		_, _ = mig.Close()
	}()

	if err := mig.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate.Up: %w", err)
	}
	return nil
}

// truncateTables empties both outbox tables and resets the identity
// sequence. Tests call it at the top so they can rely on a clean slate
// without being sensitive to other tests' state. RESTART IDENTITY is
// purely cosmetic for these tests (we never assert exact dbid values),
// but it keeps the rows readable when debugging via psql.
func truncateTables(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	const sql = "TRUNCATE outbox_records, outbox_dead_letters RESTART IDENTITY"
	if _, err := sharedPool.Exec(ctx, sql); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}
