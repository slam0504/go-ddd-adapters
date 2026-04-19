// Package order is the example aggregate exercising go-ddd-core domain
// primitives + go-ddd-adapters Kafka/slog/OTel adapters end-to-end.
//
// The aggregate models a tiny order lifecycle: Draft → Placed → Shipped.
// Placing emits OrderPlaced; shipping emits OrderShipped. A worker service
// consumes OrderPlaced and dispatches the ship command, demonstrating an
// event-driven handler chain.
package order

import "github.com/slam0504/go-ddd-core/domain"

type ID string

type Status string

const (
	StatusDraft   Status = "draft"
	StatusPlaced  Status = "placed"
	StatusShipped Status = "shipped"
)

type Item struct {
	SKU        string
	Quantity   int
	PriceCents int64
}

type Order struct {
	domain.BaseAggregate[ID]
	customerID string
	items      []Item
	status     Status
	totalCents int64
}

func New(id ID, customerID string) *Order {
	return &Order{
		BaseAggregate: domain.NewBaseAggregate(id),
		customerID:    customerID,
		status:        StatusDraft,
	}
}

// Hydrate reconstructs an Order at the supplied state without recording
// any events. The example worker uses this to seed its per-process repo
// from a received OrderPlaced (no shared DB across cmds). Real services
// load from a transactional store and never need this.
func Hydrate(id ID, customerID string, status Status, version int64, totalCents int64) *Order {
	o := New(id, customerID)
	o.status = status
	o.totalCents = totalCents
	o.SetVersion(version)
	return o
}

func (o *Order) CustomerID() string { return o.customerID }

func (o *Order) Status() Status { return o.status }

func (o *Order) Items() []Item {
	out := make([]Item, len(o.items))
	copy(out, o.items)
	return out
}

func (o *Order) TotalCents() int64 { return o.totalCents }

// Place transitions a draft order to placed and records OrderPlaced.
func (o *Order) Place(items []Item, eventID string) error {
	if o.status != StatusDraft {
		return domain.NewRuleViolation("ORDER_NOT_DRAFT", "order is not in draft state")
	}
	if len(items) == 0 {
		return domain.NewRuleViolation("ORDER_EMPTY", "order must have at least one item")
	}
	var total int64
	for _, it := range items {
		if it.Quantity <= 0 {
			return domain.NewRuleViolation("ITEM_INVALID_QTY", "item quantity must be positive")
		}
		total += it.PriceCents * int64(it.Quantity)
	}
	o.items = items
	o.totalCents = total
	o.status = StatusPlaced
	o.IncrementVersion()
	o.Record(NewOrderPlaced(eventID, string(o.ID()), o.Version(), o.customerID, total))
	return nil
}

// Ship transitions a placed order to shipped and records OrderShipped.
// Idempotent at the rule layer: shipping an already-shipped order is a
// no-op rule violation, so the worker can ack duplicates without crashing.
func (o *Order) Ship(eventID, carrier string) error {
	if o.status == StatusShipped {
		return domain.NewRuleViolation("ORDER_ALREADY_SHIPPED", "order is already shipped")
	}
	if o.status != StatusPlaced {
		return domain.NewRuleViolation("ORDER_NOT_PLACED", "order must be placed before shipping")
	}
	o.status = StatusShipped
	o.IncrementVersion()
	o.Record(NewOrderShipped(eventID, string(o.ID()), o.Version(), carrier))
	return nil
}
