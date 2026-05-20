package pgxdb

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// txKey is the package-private context key under which TxManager.WithinTx
// stores the active pgx.Tx. Keeping it unexported prevents callers from
// crafting fake tx contexts that bypass WithinTx semantics.
type txKey struct{}

// WithTx stores tx in ctx under the package-private key. Exposed for
// manual injection in tests and for adapters that already hold a Tx and
// want to call code that expects to read it from ctx.
func WithTx(ctx context.Context, tx pgx.Tx) context.Context {
	return context.WithValue(ctx, txKey{}, tx)
}

// TxFromContext returns the pgx.Tx stored in ctx, or ok=false if none.
func TxFromContext(ctx context.Context) (pgx.Tx, bool) {
	tx, ok := ctx.Value(txKey{}).(pgx.Tx)
	return tx, ok
}
