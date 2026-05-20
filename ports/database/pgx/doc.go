// Package pgxdb is the pgx/v5-backed implementation of
// go-ddd-core/ports/database.TxManager plus a small set of context
// helpers adapters can use to write tx-aware SQL without caring whether
// the caller is currently inside a transaction.
//
// # Usage
//
//	pool, err := pgxpool.New(ctx, "postgres://...")
//	if err != nil { /* ... */ }
//	defer pool.Close()
//
//	tm := pgxdb.NewTxManager(pool,
//	    pgxdb.WithTxOptions(pgx.TxOptions{IsoLevel: pgx.Serializable}),
//	)
//
//	err = tm.WithinTx(ctx, func(ctx context.Context) error {
//	    // Repositories and outbox.Stage read the *pgx.Tx out of ctx
//	    // via pgxdb.TxFromContext, or via pgxdb.Executor(ctx, pool)
//	    // when they don't care.
//	    return svc.Run(ctx)
//	})
//
// # Executor pattern
//
// Adapter code that doesn't need to know "am I in a tx?" calls
// [Executor]:
//
//	row := pgxdb.Executor(ctx, s.pool).QueryRow(ctx, sql, args...)
//
// Inside a WithinTx callback, Executor returns the pgx.Tx in ctx; outside
// one, it returns the pool. Pool and Tx both satisfy [PgxExecutor].
//
// # Tx options
//
// [WithTxOptions] sets the TxManager-level default pgx.TxOptions. Per-call
// isolation/access-mode override is intentionally not supported: core's
// database.TxManager.WithinTx contract has no slot for options.
package pgxdb
