//go:build integration

package pgxinbox_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/slam0504/go-ddd-core/eventbus"
	"github.com/slam0504/go-ddd-core/eventbus/inboxtest"

	pgxinbox "github.com/slam0504/go-ddd-adapters/eventbus/inbox/pgx"
	pgxinboxmigrations "github.com/slam0504/go-ddd-adapters/eventbus/inbox/pgx/migrations"
	"github.com/slam0504/go-ddd-adapters/internal/pgxtest"
	pgxdb "github.com/slam0504/go-ddd-adapters/ports/database/pgx"
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

// TestRecord_ConcurrentDuplicate_ExactlyOneWins proves the transactional
// guard: two transactions Record the same key concurrently; the second
// INSERT blocks on the first tx's lock and raises 23505 only after the
// winner commits — the loser observes ErrAlreadyRecorded and rolls back.
func TestRecord_ConcurrentDuplicate_ExactlyOneWins(t *testing.T) {
	ctx := context.Background()
	if _, err := sharedPool.Exec(ctx, `TRUNCATE inbox_records`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	ibx, err := pgxinbox.New(sharedPool)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	key := eventbus.InboxKey{Consumer: "race-consumer", EventID: "race-evt"}

	tx1, err := sharedPool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx1: %v", err)
	}
	if err := ibx.Record(pgxdb.WithTx(ctx, tx1), key); err != nil {
		t.Fatalf("tx1 Record: %v", err)
	}

	// tx2 Record must BLOCK until tx1 commits, then observe 23505.
	loserErr := make(chan error, 1)
	go func() {
		tx2, err := sharedPool.Begin(ctx)
		if err != nil {
			loserErr <- fmt.Errorf("begin tx2: %w", err)
			return
		}
		defer func() { _ = tx2.Rollback(ctx) }()
		loserErr <- ibx.Record(pgxdb.WithTx(ctx, tx2), key)
	}()

	select {
	case err := <-loserErr:
		t.Fatalf("tx2 Record returned %v before tx1 committed — INSERT did not block", err)
	case <-time.After(300 * time.Millisecond):
		// still blocked: expected
	}

	if err := tx1.Commit(ctx); err != nil {
		t.Fatalf("tx1 commit: %v", err)
	}
	select {
	case err := <-loserErr:
		if !errors.Is(err, eventbus.ErrAlreadyRecorded) {
			t.Fatalf("loser err = %v, want ErrAlreadyRecorded", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("tx2 Record still blocked 5s after tx1 commit")
	}

	var n int
	if err := sharedPool.QueryRow(ctx, `SELECT count(*) FROM inbox_records`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("inbox_records count = %d, want exactly 1", n)
	}
}
