//go:build integration

package pgxdb_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"

	pgxdb "github.com/slam0504/go-ddd-adapters/ports/database/pgx"
)

// insertKV writes (key, val) into pgxdb_test_kv using the tx in ctx.
// Tests share the table but use t.Name() as the unique key so they are
// independent and can run in any order.
func insertKV(ctx context.Context, key, val string) error {
	tx, ok := pgxdb.TxFromContext(ctx)
	if !ok {
		return errors.New("expected pgx.Tx in ctx")
	}
	_, err := tx.Exec(ctx, "INSERT INTO pgxdb_test_kv (k, v) VALUES ($1, $2)", key, val)
	return err
}

// readKV reads v for k directly from the pool (outside any tx).
func readKV(ctx context.Context, key string) (string, error) {
	var v string
	err := sharedPool.QueryRow(ctx, "SELECT v FROM pgxdb_test_kv WHERE k = $1", key).Scan(&v)
	return v, err
}

func TestWithinTx_Commit_RowVisibleAfterReturn(t *testing.T) {
	ctx := context.Background()
	tm := pgxdb.NewTxManager(sharedPool)

	key := t.Name()
	want := "committed"

	if err := tm.WithinTx(ctx, func(ctx context.Context) error {
		return insertKV(ctx, key, want)
	}); err != nil {
		t.Fatalf("WithinTx returned error: %v", err)
	}

	got, err := readKV(ctx, key)
	if err != nil {
		t.Fatalf("readKV after commit: %v", err)
	}
	if got != want {
		t.Fatalf("readKV got %q, want %q", got, want)
	}
}

func TestWithinTx_FnError_RollsBackAndPropagatesError(t *testing.T) {
	ctx := context.Background()
	tm := pgxdb.NewTxManager(sharedPool)

	key := t.Name()
	sentinel := errors.New("force rollback")

	err := tm.WithinTx(ctx, func(ctx context.Context) error {
		if err := insertKV(ctx, key, "should-not-survive"); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("WithinTx error = %v, want it to wrap sentinel", err)
	}

	_, readErr := readKV(ctx, key)
	if !errors.Is(readErr, pgx.ErrNoRows) {
		t.Fatalf("expected pgx.ErrNoRows after rollback, got %v", readErr)
	}
}

func TestWithinTx_FnPanics_RollsBackAndRepanics(t *testing.T) {
	ctx := context.Background()
	tm := pgxdb.NewTxManager(sharedPool)

	key := t.Name()
	panicValue := "boom-on-purpose"

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_ = tm.WithinTx(ctx, func(ctx context.Context) error {
			if err := insertKV(ctx, key, "should-not-survive"); err != nil {
				return err
			}
			panic(panicValue)
		})
	}()

	if recovered != panicValue {
		t.Fatalf("expected re-panic with %q, got %v", panicValue, recovered)
	}

	_, readErr := readKV(ctx, key)
	if !errors.Is(readErr, pgx.ErrNoRows) {
		t.Fatalf("expected pgx.ErrNoRows after panic rollback, got %v", readErr)
	}
}

func TestWithTxOptions_Serializable_AppliedToBeginTx(t *testing.T) {
	ctx := context.Background()
	tm := pgxdb.NewTxManager(
		sharedPool,
		pgxdb.WithTxOptions(pgx.TxOptions{IsoLevel: pgx.Serializable}),
	)

	if err := tm.WithinTx(ctx, func(ctx context.Context) error {
		tx, ok := pgxdb.TxFromContext(ctx)
		if !ok {
			return errors.New("expected tx in ctx")
		}
		var iso string
		if err := tx.QueryRow(ctx, "SHOW transaction_isolation").Scan(&iso); err != nil {
			return fmt.Errorf("SHOW transaction_isolation: %w", err)
		}
		if iso != "serializable" {
			return fmt.Errorf("transaction_isolation = %q, want %q", iso, "serializable")
		}
		return nil
	}); err != nil {
		t.Fatalf("WithinTx: %v", err)
	}
}
