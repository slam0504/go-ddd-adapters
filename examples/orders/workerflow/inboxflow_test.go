package workerflow_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/slam0504/go-ddd-core/ports/database"

	"github.com/slam0504/go-ddd-adapters/examples/orders/workerflow"
	pgxdb "github.com/slam0504/go-ddd-adapters/ports/database/pgx"
)

// stubTx is a minimal pgx.Tx that satisfies the interface. It is only used
// as a non-nil marker stored in ctx via pgxdb.WithTx — no methods are
// called on it, so all implementations panic to catch accidental use.
type stubTx struct{}

func (stubTx) Begin(context.Context) (pgx.Tx, error) { panic("not called") }
func (stubTx) Commit(context.Context) error          { panic("not called") }
func (stubTx) Rollback(context.Context) error        { panic("not called") }
func (stubTx) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	panic("not called")
}
func (stubTx) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults { panic("not called") }
func (stubTx) LargeObjects() pgx.LargeObjects                         { panic("not called") }
func (stubTx) Prepare(context.Context, string, string) (*pgconn.StatementDescription, error) {
	panic("not called")
}
func (stubTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) { panic("not called") }
func (stubTx) Query(context.Context, string, ...any) (pgx.Rows, error)         { panic("not called") }
func (stubTx) QueryRow(context.Context, string, ...any) pgx.Row                { panic("not called") }
func (stubTx) Conn() *pgx.Conn                                                 { panic("not called") }

// fakeTxManager records whether WithinTx was invoked — the joining UoW must
// NOT open a new transaction when ctx already carries one.
type fakeTxManager struct{ opened bool }

func (f *fakeTxManager) WithinTx(ctx context.Context, fn database.TxFunc) error {
	f.opened = true
	return fn(ctx)
}

func TestJoiningUnitOfWork_JoinsAmbientTx(t *testing.T) {
	f := &fakeTxManager{}
	uow := workerflow.JoiningUnitOfWork(f)
	// pgxdb.WithTx requires a non-nil pgx.Tx for TxFromContext to return ok=true.
	// A nil pgx.Tx interface value stored via context.WithValue is retrieved as
	// a nil any, causing the .(pgx.Tx) assertion to return ok=false. stubTx is
	// the minimal non-nil marker satisfying the interface.
	ctx := pgxdb.WithTx(context.Background(), stubTx{})
	ran := false
	if err := uow.Do(ctx, func(context.Context) error { ran = true; return nil }); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if !ran {
		t.Fatal("fn did not run")
	}
	if f.opened {
		t.Fatal("WithinTx was called despite ambient tx — joining broken (would nest a second transaction)")
	}
}

func TestJoiningUnitOfWork_OpensTxWhenNoAmbient(t *testing.T) {
	f := &fakeTxManager{}
	uow := workerflow.JoiningUnitOfWork(f)
	if err := uow.Do(context.Background(), func(context.Context) error { return nil }); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if !f.opened {
		t.Fatal("WithinTx not called with no ambient tx")
	}
}
