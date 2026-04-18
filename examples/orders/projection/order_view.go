// Package projection holds the read-model projections used by the reader
// cmd. Projections subscribe to domain events and update an in-memory
// view; the GetOrder query handler reads from the view rather than from
// the write-side aggregate, demonstrating CQRS read/write separation.
package projection

import (
	"context"
	"sync"

	"github.com/slam0504/go-ddd-core/domain"

	apporder "github.com/slam0504/go-ddd-adapters/examples/orders/application/order"
	orderdom "github.com/slam0504/go-ddd-adapters/examples/orders/domain/order"
)

// OrderViewStore is an in-memory projection of OrderPlaced + OrderShipped
// events into apporder.View. Safe for concurrent use.
type OrderViewStore struct {
	mu    sync.RWMutex
	views map[string]apporder.View
}

func NewOrderViewStore() *OrderViewStore {
	return &OrderViewStore{views: map[string]apporder.View{}}
}

// Apply dispatches to the type-specific projection update. Unknown event
// types are ignored — the reader subscribes to specific topics and the
// codec rejects unknown event names earlier, so this branch should not
// be hit in normal operation.
func (s *OrderViewStore) Apply(ev domain.DomainEvent) {
	switch e := ev.(type) {
	case *orderdom.OrderPlaced:
		s.applyPlaced(*e)
	case orderdom.OrderPlaced:
		s.applyPlaced(e)
	case *orderdom.OrderShipped:
		s.applyShipped(*e)
	case orderdom.OrderShipped:
		s.applyShipped(e)
	}
}

func (s *OrderViewStore) applyPlaced(e orderdom.OrderPlaced) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.views[e.AggregateID()] = apporder.View{
		OrderID:    e.AggregateID(),
		CustomerID: e.CustomerID,
		Status:     string(orderdom.StatusPlaced),
		TotalCents: e.TotalCents,
		Version:    e.Version(),
	}
}

func (s *OrderViewStore) applyShipped(e orderdom.OrderShipped) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.views[e.AggregateID()]
	if !ok {
		// out-of-order delivery: shipped arrived before placed. Build a
		// minimal stub; placed will overwrite the missing fields if it
		// arrives later. In production this is where you would buffer or
		// fetch the aggregate snapshot.
		v = apporder.View{OrderID: e.AggregateID()}
	}
	v.Status = string(orderdom.StatusShipped)
	v.Carrier = e.Carrier
	v.Version = e.Version()
	s.views[e.AggregateID()] = v
}

func (s *OrderViewStore) Find(_ context.Context, id string) (apporder.View, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.views[id]
	if !ok {
		return apporder.View{}, domain.ErrNotFound
	}
	return v, nil
}

var _ apporder.Reader = (*OrderViewStore)(nil)
