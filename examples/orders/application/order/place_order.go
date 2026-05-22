// Package order contains the application-layer command and query handlers
// for the Order aggregate. Handlers are kept transport-agnostic; the cmd
// wiring layer plugs them into HTTP, Kafka subscribers, etc.
package order

import (
	"context"

	"github.com/slam0504/go-ddd-core/application"
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

// PlaceOrderHandler stages OrderPlaced into the outbox in the same
// transaction as the aggregate save. The UnitOfWork bridges to the
// underlying pgx TxManager; the handler itself stays driver-agnostic.
type PlaceOrderHandler struct {
	uow    application.UnitOfWork
	repo   orderdom.Repository
	outbox eventbus.Outbox
	topic  string
	newID  func() string
}

func NewPlaceOrderHandler(
	uow application.UnitOfWork,
	repo orderdom.Repository,
	outbox eventbus.Outbox,
	topic string,
	newID func() string,
) *PlaceOrderHandler {
	return &PlaceOrderHandler{uow: uow, repo: repo, outbox: outbox, topic: topic, newID: newID}
}

func (h *PlaceOrderHandler) Handle(ctx context.Context, cmd PlaceOrderCommand) (PlaceOrderResult, error) {
	o := orderdom.New(orderdom.ID(cmd.OrderID), cmd.CustomerID)
	if err := o.Place(cmd.Items, h.newID()); err != nil {
		return PlaceOrderResult{}, err
	}

	err := h.uow.Do(ctx, func(ctx context.Context) error {
		if err := h.repo.Save(ctx, o); err != nil {
			return err
		}
		return h.outbox.Stage(ctx, h.topic, o.DomainEvents()...)
	})
	if err != nil {
		return PlaceOrderResult{}, err
	}

	o.ClearEvents()
	return PlaceOrderResult{OrderID: cmd.OrderID, TotalCents: o.TotalCents()}, nil
}
