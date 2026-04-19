package order

import (
	"context"
	"errors"

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

type ShipOrderHandler struct {
	repo      orderdom.Repository
	publisher eventbus.Publisher
	topic     string
	newID     func() string
}

func NewShipOrderHandler(
	repo orderdom.Repository,
	publisher eventbus.Publisher,
	topic string,
	newID func() string,
) *ShipOrderHandler {
	return &ShipOrderHandler{repo: repo, publisher: publisher, topic: topic, newID: newID}
}

// Handle is duplicate-safe: if the worker re-delivers an OrderPlaced after
// the order is already shipped (no transactional outbox in this example),
// the rule violation is treated as success so the consumer can ack.
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
	if err := h.repo.Save(ctx, o); err != nil {
		return ShipOrderResult{}, err
	}
	if err := h.publisher.Publish(ctx, h.topic, o.DomainEvents()...); err != nil {
		return ShipOrderResult{}, err
	}
	o.ClearEvents()
	return ShipOrderResult{OrderID: cmd.OrderID}, nil
}
