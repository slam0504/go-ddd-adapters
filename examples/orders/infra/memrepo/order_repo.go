// Package memrepo is an in-memory implementation of the Order Repository.
// Real services would back this with Postgres / DynamoDB / etc; the example
// keeps it in memory to focus on the adapter wiring instead of the storage.
package memrepo

import (
	"context"
	"sync"

	"github.com/slam0504/go-ddd-core/domain"

	orderdom "github.com/slam0504/go-ddd-adapters/examples/orders/domain/order"
)

type OrderRepository struct {
	mu    sync.RWMutex
	store map[orderdom.ID]*orderdom.Order
}

func NewOrderRepository() *OrderRepository {
	return &OrderRepository{store: map[orderdom.ID]*orderdom.Order{}}
}

func (r *OrderRepository) FindByID(_ context.Context, id orderdom.ID) (*orderdom.Order, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	o, ok := r.store[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return o, nil
}

func (r *OrderRepository) Save(_ context.Context, o *orderdom.Order) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.store[o.ID()] = o
	return nil
}

func (r *OrderRepository) Delete(_ context.Context, id orderdom.ID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.store, id)
	return nil
}

var _ orderdom.Repository = (*OrderRepository)(nil)
