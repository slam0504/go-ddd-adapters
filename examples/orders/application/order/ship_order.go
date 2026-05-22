package order

import (
	"context"
	"errors"

	"github.com/slam0504/go-ddd-core/application"
	"github.com/slam0504/go-ddd-core/domain"
	"github.com/slam0504/go-ddd-core/eventbus"

	orderdom "github.com/slam0504/go-ddd-adapters/examples/orders/domain/order"
)

// ShipOrderCommand transitions an existing placed order to shipped.
type ShipOrderCommand struct {
	OrderID string
	Carrier string
}

func (ShipOrderCommand) CommandName() string { return "order.ship" }

type ShipOrderResult struct {
	OrderID string
}

// ShipOrderHandler stages OrderShipped into the outbox in the same
// transaction as the aggregate save. Already-shipped is treated as
// successful (idempotent ack path); not-found propagates so callers
// can map to broker nack.
type ShipOrderHandler struct {
	uow    application.UnitOfWork
	repo   orderdom.Repository
	outbox eventbus.Outbox
	topic  string
	newID  func() string
}

func NewShipOrderHandler(
	uow application.UnitOfWork,
	repo orderdom.Repository,
	outbox eventbus.Outbox,
	topic string,
	newID func() string,
) *ShipOrderHandler {
	return &ShipOrderHandler{uow: uow, repo: repo, outbox: outbox, topic: topic, newID: newID}
}

func (h *ShipOrderHandler) Handle(ctx context.Context, cmd ShipOrderCommand) (ShipOrderResult, error) {
	o, err := h.repo.FindByID(ctx, orderdom.ID(cmd.OrderID))
	if err != nil {
		return ShipOrderResult{}, err
	}

	if err := o.Ship(h.newID(), cmd.Carrier); err != nil {
		var rv *domain.RuleViolation
		if errors.As(err, &rv) && rv.Code == "ORDER_ALREADY_SHIPPED" {
			return ShipOrderResult{OrderID: cmd.OrderID}, nil
		}
		return ShipOrderResult{}, err
	}

	err = h.uow.Do(ctx, func(ctx context.Context) error {
		if err := h.repo.Save(ctx, o); err != nil {
			return err
		}
		return h.outbox.Stage(ctx, h.topic, o.DomainEvents()...)
	})
	if err != nil {
		return ShipOrderResult{}, err
	}

	o.ClearEvents()
	return ShipOrderResult{OrderID: cmd.OrderID}, nil
}
