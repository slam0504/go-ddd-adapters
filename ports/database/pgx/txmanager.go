package pgxdb

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/slam0504/go-ddd-core/ports/database"
)

// TxManager satisfies go-ddd-core/ports/database.TxManager backed by a
// pgxpool.Pool. WithinTx opens a tx via pool.BeginTx with the configured
// default pgx.TxOptions, stores it in ctx via WithTx, invokes fn, and
// commits / rolls back based on fn's return.
type TxManager struct {
	pool      *pgxpool.Pool
	defaultTx pgx.TxOptions
}

// TxManagerOption configures a TxManager at construction.
type TxManagerOption func(*TxManager)

// WithTxOptions sets the TxManager-level default pgx.TxOptions used by
// every WithinTx call. The zero value (default) is pgx's
// ReadCommitted / ReadWrite / non-deferred. Use this to opt into
// Serializable, ReadOnly, or deferred at the TxManager scope.
//
// Per-call options are not supported because core's
// database.TxManager.WithinTx signature carries no options slot.
func WithTxOptions(o pgx.TxOptions) TxManagerOption {
	return func(m *TxManager) {
		m.defaultTx = o
	}
}

// NewTxManager constructs a TxManager. pool must be non-nil.
func NewTxManager(pool *pgxpool.Pool, opts ...TxManagerOption) *TxManager {
	if pool == nil {
		panic("ports/database/pgx: NewTxManager pool must not be nil")
	}
	m := &TxManager{pool: pool}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// WithinTx opens a tx, runs fn with the tx attached to ctx, and commits
// or rolls back based on fn's return:
//
//   - fn returns nil → Commit. If Commit fails, the error is returned.
//   - fn returns non-nil error → Rollback (Rollback errors are joined into
//     the returned error so callers can observe them).
//   - fn panics → Rollback and re-panic. The original panic value is
//     preserved verbatim so panic handlers above WithinTx see it as if
//     WithinTx weren't in the stack.
func (m *TxManager) WithinTx(ctx context.Context, fn database.TxFunc) error {
	tx, err := m.pool.BeginTx(ctx, m.defaultTx)
	if err != nil {
		return fmt.Errorf("ports/database/pgx: begin tx: %w", err)
	}

	committed := false
	defer func() {
		if r := recover(); r != nil {
			// Use background ctx for rollback because the caller ctx
			// may already be cancelled and we still want the rollback
			// to attempt to complete.
			_ = tx.Rollback(context.Background())
			panic(r)
		}
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()

	ctx = WithTx(ctx, tx)
	if err := fn(ctx); err != nil {
		if rbErr := tx.Rollback(context.Background()); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			return errors.Join(err, fmt.Errorf("ports/database/pgx: rollback: %w", rbErr))
		}
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("ports/database/pgx: commit: %w", err)
	}
	committed = true
	return nil
}

// Compile-time assertion.
var _ database.TxManager = (*TxManager)(nil)
