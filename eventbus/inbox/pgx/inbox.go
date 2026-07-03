// Package pgxinbox is the Postgres-backed durable implementation of core's
// eventbus.Inbox over pgx/v5. Record is a plain INSERT guarded by the
// (consumer, event_id) primary key — deliberately NOT ON CONFLICT DO
// NOTHING: a duplicate surfaces as eventbus.ErrAlreadyRecorded so the
// caller's transaction rolls back. Under concurrent duplicate delivery the
// second INSERT blocks on the first transaction's lock and raises the
// unique violation only after the winner commits — that blocking INSERT is
// the transactional exactly-once guard.
//
// Both methods join an ambient pgx transaction when ctx carries one
// (pgxdb.WithTx / TxFromContext), else they run on the pool — the same
// shape as outbox Stage.
//
// Limitation (v1): inbox_records grows without bound; recorded_at exists to
// keep a future retention/cleanup migration possible. See the design spec
// 2026-07-02 §7.
package pgxinbox

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/slam0504/go-ddd-core/eventbus"
	"github.com/slam0504/go-ddd-core/pkg/errorsx"

	pgxdb "github.com/slam0504/go-ddd-adapters/ports/database/pgx"
)

// Inbox is a durable Postgres eventbus.Inbox. Safe for concurrent use.
type Inbox struct {
	pool *pgxpool.Pool
}

// New builds an Inbox over pool. The inbox_records table must exist (apply
// Migrations).
func New(pool *pgxpool.Pool) (*Inbox, error) {
	if pool == nil {
		return nil, errorsx.New(errorsx.CodeInvalidArgument, "pgxinbox: nil pool")
	}
	return &Inbox{pool: pool}, nil
}

func errInvalidKey() error {
	return errorsx.New(errorsx.CodeInvalidArgument, "pgxinbox: empty consumer or event id")
}

// Seen reports whether key has been recorded. Advisory under concurrency —
// see the core eventbus.Inbox contract. Precedence: empty key -> ctx ->
// backend.
func (i *Inbox) Seen(ctx context.Context, key eventbus.InboxKey) (bool, error) {
	if key.Consumer == "" || key.EventID == "" {
		return false, errInvalidKey()
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	q := pgxdb.Executor(ctx, i.pool)
	var seen bool
	err := q.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM inbox_records WHERE consumer = $1 AND event_id = $2)`,
		key.Consumer, key.EventID,
	).Scan(&seen)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return false, err
		}
		return false, fmt.Errorf("pgxinbox: seen: %w", err)
	}
	return seen, nil
}

// Record marks key as processed via a plain INSERT. A duplicate key raises
// Postgres unique_violation (23505), returned as a wrapped
// eventbus.ErrAlreadyRecorded — run Record inside the handler's transaction
// and roll back on it. Precedence: empty key -> ctx -> backend.
func (i *Inbox) Record(ctx context.Context, key eventbus.InboxKey) error {
	if key.Consumer == "" || key.EventID == "" {
		return errInvalidKey()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	q := pgxdb.Executor(ctx, i.pool)
	_, err := q.Exec(ctx,
		`INSERT INTO inbox_records (consumer, event_id) VALUES ($1, $2)`,
		key.Consumer, key.EventID,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return fmt.Errorf("pgxinbox: %w", eventbus.ErrAlreadyRecorded)
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return fmt.Errorf("pgxinbox: record: %w", err)
	}
	return nil
}

// Compile-time assertion that *Inbox implements core's eventbus.Inbox.
var _ eventbus.Inbox = (*Inbox)(nil)
