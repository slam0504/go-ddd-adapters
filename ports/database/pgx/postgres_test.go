//go:build integration

package pgxdb_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/slam0504/go-ddd-adapters/internal/pgxtest"
)

// sharedPool is the pgxpool.Pool against the testcontainer Postgres
// started by TestMain. Other *_integration_test.go files in this package
// read it directly. It is intentionally a self-contained schema
// (pgxdb_test_kv) so TxManager tests do not depend on the outbox
// adapter's migrations (decision #20).
var sharedPool *pgxpool.Pool

const createKVTable = `CREATE TABLE IF NOT EXISTS pgxdb_test_kv (
    k TEXT PRIMARY KEY,
    v TEXT NOT NULL
)`

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

// run wraps the suite so deferred cleanup actually executes — TestMain
// itself cannot use defer because os.Exit aborts the goroutine before
// any defers fire.
func run(m *testing.M) int {
	ctx := context.Background()

	pool, cleanup, err := pgxtest.StartContainer(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pgxdb integration: start container: %v\n", err)
		return 1
	}
	defer cleanup()

	if _, err := pool.Exec(ctx, createKVTable); err != nil {
		fmt.Fprintf(os.Stderr, "pgxdb integration: create pgxdb_test_kv: %v\n", err)
		return 1
	}

	sharedPool = pool
	fmt.Fprintln(os.Stderr, "pgxdb integration: postgres ready")
	return m.Run()
}
