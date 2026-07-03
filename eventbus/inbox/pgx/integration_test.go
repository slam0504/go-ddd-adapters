//go:build integration

package pgxinbox_test

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

	"github.com/slam0504/go-ddd-core/eventbus"
	"github.com/slam0504/go-ddd-core/eventbus/inboxtest"

	pgxinbox "github.com/slam0504/go-ddd-adapters/eventbus/inbox/pgx"
	pgxinboxmigrations "github.com/slam0504/go-ddd-adapters/eventbus/inbox/pgx/migrations"
	"github.com/slam0504/go-ddd-adapters/internal/pgxtest"
)

// sharedPool is the pgxpool.Pool against the testcontainer Postgres started
// by TestMain after pgxinboxmigrations.FS has been applied via golang-migrate's
// iofs source + pgx/v5 database driver. All integration tests in this package
// read it directly.
var sharedPool *pgxpool.Pool

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

// run wraps the suite so deferred cleanup actually executes — TestMain itself
// cannot use defer because os.Exit aborts the goroutine before any defers
// fire. Mirrors the pattern in eventbus/outbox/pgx tests.
func run(m *testing.M) int {
	ctx := context.Background()

	pool, cleanup, err := pgxtest.StartContainer(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pgxinbox integration: start container: %v\n", err)
		return 1
	}
	defer cleanup()

	if err := applyMigrations(pool); err != nil {
		fmt.Fprintf(os.Stderr, "pgxinbox integration: migrations: %v\n", err)
		return 1
	}

	sharedPool = pool
	fmt.Fprintln(os.Stderr, "pgxinbox integration: postgres ready, migrations applied")
	return m.Run()
}

// applyMigrations runs the embedded pgxinboxmigrations.FS via golang-migrate's
// iofs source + pgx/v5 database driver. The driver expects a `pgx5://` scheme
// — the testcontainer hands us `postgres://`, so a single string replacement
// bridges the two. Mirrors eventbus/outbox/pgx's applyMigrations.
func applyMigrations(pool *pgxpool.Pool) error {
	src, err := iofs.New(pgxinboxmigrations.FS, ".")
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
		_, _ = mig.Close()
	}()

	if err := mig.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate.Up: %w", err)
	}
	return nil
}

// TestRunContract runs core's inbox conformance suite against real Postgres.
// Factory isolation: each subtest gets a fresh keyspace by truncating
// inbox_records (the suite uses fixed key values).
func TestRunContract(t *testing.T) {
	inboxtest.RunContract(t, func(t *testing.T) eventbus.Inbox {
		if _, err := sharedPool.Exec(context.Background(), `TRUNCATE inbox_records`); err != nil {
			t.Fatalf("truncate: %v", err)
		}
		ibx, err := pgxinbox.New(sharedPool)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return ibx
	})
}
