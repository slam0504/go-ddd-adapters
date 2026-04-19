// Package eventcodec centralises the JSONCodec setup so all three cmd
// binaries register the same event types. Without this helper each cmd
// would copy the same Register calls and drift over time.
package eventcodec

import (
	"github.com/slam0504/go-ddd-core/domain"

	"github.com/slam0504/go-ddd-adapters/eventbus/kafka"
	orderdom "github.com/slam0504/go-ddd-adapters/examples/orders/domain/order"
)

// New returns a JSONCodec with all order events registered. Factories
// return pointer values so json.Unmarshal can populate them.
func New() *kafka.JSONCodec {
	c := kafka.NewJSONCodec()
	c.Register(orderdom.EventNamePlaced, func() domain.DomainEvent { return &orderdom.OrderPlaced{} })
	c.Register(orderdom.EventNameShipped, func() domain.DomainEvent { return &orderdom.OrderShipped{} })
	return c
}
