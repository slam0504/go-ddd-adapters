// Package order contains the application-layer command and query handlers
// for the Order aggregate. Handlers are kept transport-agnostic; the cmd
// wiring layer plugs them into HTTP, Kafka subscribers, etc.
package order

import (
	"context"

	"github.com/slam0504/go-ddd-core/eventbus"

	orderdom "github.com/slam0504/go-ddd-adapters/examples/orders/domain/order"
)

// PlaceOrderCommand creates a new order and transitions it to placed.
type PlaceOrderCommand struct {
	OrderID    string
	CustomerID string
	Items      []orderdom.Item
}

func (PlaceOrderCommand) CommandName() string { return "order.place" }

type PlaceOrderResult struct {
	OrderID    string
	TotalCents int64
}

// PlaceOrderHandler executes PlaceOrderCommand. Publisher is required —
// without it the worker chain cannot fire — so we do not nil-guard.
type PlaceOrderHandler struct {
	repo      orderdom.Repository
	publisher eventbus.Publisher
	topic     string
	newID     func() string
}

func NewPlaceOrderHandler(
	repo orderdom.Repository,
	publisher eventbus.Publisher,
	topic string,
	newID func() string,
) *PlaceOrderHandler {
	return &PlaceOrderHandler{repo: repo, publisher: publisher, topic: topic, newID: newID}
}

func (h *PlaceOrderHandler) Handle(ctx context.Context, cmd PlaceOrderCommand) (PlaceOrderResult, error) {
	o := orderdom.New(orderdom.ID(cmd.OrderID), cmd.CustomerID)
	if err := o.Place(cmd.Items, h.newID()); err != nil {
		return PlaceOrderResult{}, err
	}
	if err := h.repo.Save(ctx, o); err != nil {
		return PlaceOrderResult{}, err
	}
	if err := h.publisher.Publish(ctx, h.topic, o.DomainEvents()...); err != nil {
		return PlaceOrderResult{}, err
	}
	o.ClearEvents()
	return PlaceOrderResult{OrderID: cmd.OrderID, TotalCents: o.TotalCents()}, nil
}
