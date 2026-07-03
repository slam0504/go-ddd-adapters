package pgxinbox_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/slam0504/go-ddd-core/eventbus"
	"github.com/slam0504/go-ddd-core/pkg/errorsx"

	pgxinbox "github.com/slam0504/go-ddd-adapters/eventbus/inbox/pgx"
)

// lazyPool returns a pool that never dials — pgxpool.New is lazy, and every
// path under test here must fail BEFORE any backend call.
func lazyPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), "postgres://127.0.0.1:1/never?sslmode=disable")
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestNew_NilPoolRejected(t *testing.T) {
	_, err := pgxinbox.New(nil)
	if errorsx.CodeOf(err) != errorsx.CodeInvalidArgument {
		t.Fatalf("New(nil): CodeOf = %v, want CodeInvalidArgument", errorsx.CodeOf(err))
	}
}

// Empty-field and ctx precedence run pre-backend: with a never-dialed pool,
// any attempt to reach Postgres would hang/error differently — a passing
// test here proves the check ordering.
func TestValidationPrecedence_PreBackend(t *testing.T) {
	ibx, err := pgxinbox.New(lazyPool(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	bad := eventbus.InboxKey{Consumer: "", EventID: "e"}
	if _, err := ibx.Seen(ctx, bad); errorsx.CodeOf(err) != errorsx.CodeInvalidArgument {
		t.Fatalf("Seen empty consumer: CodeOf = %v, want CodeInvalidArgument", errorsx.CodeOf(err))
	}
	if err := ibx.Record(ctx, bad); errorsx.CodeOf(err) != errorsx.CodeInvalidArgument {
		t.Fatalf("Record empty consumer: CodeOf = %v, want CodeInvalidArgument", errorsx.CodeOf(err))
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	good := eventbus.InboxKey{Consumer: "c", EventID: "e"}
	if _, err := ibx.Seen(cancelled, good); !errors.Is(err, context.Canceled) {
		t.Fatalf("Seen cancelled ctx: err = %v, want context.Canceled", err)
	}
	if err := ibx.Record(cancelled, good); !errors.Is(err, context.Canceled) {
		t.Fatalf("Record cancelled ctx: err = %v, want context.Canceled", err)
	}
}
