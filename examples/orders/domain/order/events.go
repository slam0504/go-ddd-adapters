package order

import "github.com/slam0504/go-ddd-core/domain"

// EventNamePlaced is the registered name for OrderPlaced. The codec
// registry uses this as the lookup key so the consumer can reconstruct
// the typed event.
const (
	EventNamePlaced  = "order.placed"
	EventNameShipped = "order.shipped"
)

type OrderPlaced struct {
	domain.BaseEvent
	CustomerID string
	TotalCents int64
}

func NewOrderPlaced(eventID, aggregateID string, version int64, customerID string, totalCents int64) OrderPlaced {
	return OrderPlaced{
		BaseEvent:  domain.NewBaseEvent(eventID, EventNamePlaced, aggregateID, "Order", version),
		CustomerID: customerID,
		TotalCents: totalCents,
	}
}

type OrderShipped struct {
	domain.BaseEvent
	Carrier string
}

func NewOrderShipped(eventID, aggregateID string, version int64, carrier string) OrderShipped {
	return OrderShipped{
		BaseEvent: domain.NewBaseEvent(eventID, EventNameShipped, aggregateID, "Order", version),
		Carrier:   carrier,
	}
}
