package pgxdb

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// PgxExecutor is the read/write surface shared by *pgxpool.Pool and pgx.Tx.
// Adapter code targets this interface so it can run either inside a
// caller's tx (via WithinTx → ctx) or against the pool directly without
// branching on which.
type PgxExecutor interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Executor returns the pgx.Tx stored in ctx if any, otherwise pool.
// Stage-style code that REQUIRES a tx should not use this — it should
// call TxFromContext and return ErrNoTx when absent. Fetch / MarkSent /
// MarkFailed / Terminate, which run independently of caller txs, should
// pass the pool directly and skip Executor.
func Executor(ctx context.Context, pool PgxExecutor) PgxExecutor {
	if tx, ok := TxFromContext(ctx); ok {
		return tx
	}
	return pool
}
