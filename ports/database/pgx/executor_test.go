package pgxdb_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	pgxdb "github.com/slam0504/go-ddd-adapters/ports/database/pgx"
)

// stubTx satisfies pgx.Tx via the embedded nil interface so we can use a
// known pointer identity in the context. Methods will panic if called —
// these tests only exercise ctx-store / retrieve / pick semantics, not
// any pgx.Tx behaviour.
type stubTx struct {
	pgx.Tx
}

// stubExecutor satisfies pgxdb.PgxExecutor for the "pool" argument in
// Executor tests. Same nil-interface embedding trick.
type stubExecutor struct {
	pgxdb.PgxExecutor
}

func TestTxFromContext_EmptyContext_ReturnsOkFalse(t *testing.T) {
	t.Parallel()

	got, ok := pgxdb.TxFromContext(context.Background())
	if ok {
		t.Fatalf("expected ok=false on empty ctx, got tx=%v ok=true", got)
	}
	if got != nil {
		t.Fatalf("expected nil tx, got %v", got)
	}
}

func TestTxFromContext_AfterWithTx_ReturnsSameTx(t *testing.T) {
	t.Parallel()

	tx := &stubTx{}
	ctx := pgxdb.WithTx(context.Background(), tx)

	got, ok := pgxdb.TxFromContext(ctx)
	if !ok {
		t.Fatalf("expected ok=true after WithTx, got ok=false")
	}
	if got != tx {
		t.Fatalf("TxFromContext returned different tx: got %p, want %p", got, tx)
	}
}

func TestExecutor_NoTx_ReturnsPool(t *testing.T) {
	t.Parallel()

	pool := &stubExecutor{}
	got := pgxdb.Executor(context.Background(), pool)
	if got != pool {
		t.Fatalf("Executor with empty ctx should return pool unchanged: got %p, want %p", got, pool)
	}
}

func TestExecutor_WithTx_ReturnsTxNotPool(t *testing.T) {
	t.Parallel()

	tx := &stubTx{}
	pool := &stubExecutor{}
	ctx := pgxdb.WithTx(context.Background(), tx)

	got := pgxdb.Executor(ctx, pool)
	if got != pgxdb.PgxExecutor(tx) {
		t.Fatalf("Executor returned %p, want the tx %p stored in ctx", got, tx)
	}
}
