package order

import "context"

// GetOrderQuery reads the projected view for a single order id.
type GetOrderQuery struct {
	OrderID string
}

func (GetOrderQuery) QueryName() string { return "order.get" }

// View is the read-side DTO surfaced over the reader cmd HTTP API.
type View struct {
	OrderID    string `json:"order_id"`
	CustomerID string `json:"customer_id"`
	Status     string `json:"status"`
	TotalCents int64  `json:"total_cents"`
	Carrier    string `json:"carrier,omitempty"`
	Version    int64  `json:"version"`
}

// Reader is the read-model contract the handler needs. The projection
// package implements it; tests can mock it without spinning Kafka.
type Reader interface {
	Find(ctx context.Context, id string) (View, error)
}

type GetOrderHandler struct {
	reader Reader
}

func NewGetOrderHandler(reader Reader) *GetOrderHandler {
	return &GetOrderHandler{reader: reader}
}

func (h *GetOrderHandler) Handle(ctx context.Context, q GetOrderQuery) (View, error) {
	return h.reader.Find(ctx, q.OrderID)
}
