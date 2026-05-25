// Package pgxrepo is a pgx/v5-backed implementation of the Order
// Repository. Save participates in the caller's transaction via
// pgxdb.Executor: when ctx carries a tx (UoW path), it uses the tx;
// otherwise it falls through to the pool. FindByID maps pgx.ErrNoRows
// to domain.ErrNotFound to mirror the memrepo contract — swapping repo
// implementations must not drift error semantics.
package pgxrepo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/slam0504/go-ddd-core/domain"

	pgxdb "github.com/slam0504/go-ddd-adapters/ports/database/pgx"

	orderdom "github.com/slam0504/go-ddd-adapters/examples/orders/domain/order"
)

type OrderRepository struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *OrderRepository {
	return &OrderRepository{pool: pool}
}

func (r *OrderRepository) FindByID(ctx context.Context, id orderdom.ID) (*orderdom.Order, error) {
	exec := pgxdb.Executor(ctx, r.pool)
	var (
		customer   string
		status     string
		version    int64
		totalCents int64
		itemsJSON  []byte
	)
	err := exec.QueryRow(ctx, `
		SELECT customer_id, status, version, total_cents, items
		FROM orders
		WHERE id = $1
	`, string(id)).Scan(&customer, &status, &version, &totalCents, &itemsJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("pgxrepo: find order %s: %w", id, err)
	}

	var items []orderdom.Item
	if err := json.Unmarshal(itemsJSON, &items); err != nil {
		return nil, fmt.Errorf("pgxrepo: decode items for %s: %w", id, err)
	}

	return orderdom.Hydrate(id, customer, orderdom.Status(status), version, totalCents, items), nil
}

// Save upserts the order, enforcing optimistic locking via the
// EXCLUDED.version - 1 guard on the UPDATE branch. The invariant
// aggregate.Version() == priorLoadedVersion + 1 holds in this example
// because Order.Place / Order.Ship each call IncrementVersion exactly
// once and are each followed by exactly one Save. A future use case
// that stages multiple events per Save (Version() jumps by > 1) must
// switch to an explicit loadedVersion field on the aggregate.
func (r *OrderRepository) Save(ctx context.Context, o *orderdom.Order) error {
	exec := pgxdb.Executor(ctx, r.pool)
	itemsJSON, err := json.Marshal(o.Items())
	if err != nil {
		return fmt.Errorf("pgxrepo: encode items for %s: %w", o.ID(), err)
	}
	cmd, err := exec.Exec(ctx, `
		INSERT INTO orders (id, customer_id, status, version, total_cents, items)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (id) DO UPDATE SET
			customer_id = EXCLUDED.customer_id,
			status      = EXCLUDED.status,
			version     = EXCLUDED.version,
			total_cents = EXCLUDED.total_cents,
			items       = EXCLUDED.items,
			updated_at  = now()
		WHERE orders.version = EXCLUDED.version - 1
	`,
		string(o.ID()),
		o.CustomerID(),
		string(o.Status()),
		o.Version(),
		o.TotalCents(),
		itemsJSON,
	)
	if err != nil {
		return fmt.Errorf("pgxrepo: upsert order %s: %w", o.ID(), err)
	}
	if cmd.RowsAffected() == 0 {
		return fmt.Errorf("pgxrepo: optimistic lock conflict on order %s: %w",
			o.ID(), domain.ErrConcurrencyConflict)
	}
	return nil
}

func (r *OrderRepository) Delete(ctx context.Context, id orderdom.ID) error {
	exec := pgxdb.Executor(ctx, r.pool)
	_, err := exec.Exec(ctx, `DELETE FROM orders WHERE id = $1`, string(id))
	if err != nil {
		return fmt.Errorf("pgxrepo: delete order %s: %w", id, err)
	}
	return nil
}

var _ orderdom.Repository = (*OrderRepository)(nil)
