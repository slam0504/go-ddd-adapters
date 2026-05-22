// Package workerflow holds the worker-side adapter that translates a
// consumed Kafka OrderPlaced envelope into a ShipOrderCommand dispatch.
// It lives outside cmd/internal so integration tests can exercise the
// propagation contract directly without spinning a real consumer loop.
package workerflow

import (
	"context"
	"fmt"

	"github.com/slam0504/go-ddd-core/application/command"
	"github.com/slam0504/go-ddd-core/eventbus"
	"github.com/slam0504/go-ddd-core/ports/logger"

	"github.com/slam0504/go-ddd-adapters/eventbus/kafka"
	apporder "github.com/slam0504/go-ddd-adapters/examples/orders/application/order"
	orderdom "github.com/slam0504/go-ddd-adapters/examples/orders/domain/order"
)

// DefaultCarrier is the demo-only carrier value stamped on
// ShipOrderCommand. Production services would derive this from the
// incoming event or a routing table.
const DefaultCarrier = "demo-carrier"

// HandleOrderPlaced is the worker's Kafka -> command bus boundary.
// It restores the three core propagation headers
// (trace_id / causation_id / correlation_id) from the consumed
// envelope's metadata into ctx, then OVERWRITES the causation_id with
// the consumed event's id — the OrderShipped this dispatch will stage
// is *caused by* the OrderPlaced we just consumed. The codec picks the
// headers up at outbox.Stage time, so the staged OrderShipped row
// inherits the inbound trace + correlation and reports the
// causation chain correctly.
//
// env.Raw is allowed to be nil (synthetic envelopes in tests); the
// restorer is skipped but causation_id is still set.
func HandleOrderPlaced(
	ctx context.Context,
	log logger.Logger,
	cmdBus *command.InMemoryBus,
	env eventbus.Envelope,
) error {
	placed, ok := env.Event.(*orderdom.OrderPlaced)
	if !ok {
		return fmt.Errorf("unexpected event type: %s", env.Name)
	}

	dispatchCtx := ctx
	if env.Raw != nil {
		dispatchCtx = kafka.RestoreCoreHeaders(dispatchCtx, map[string]string(env.Raw.Metadata))
	}
	dispatchCtx = kafka.WithCausationID(dispatchCtx, placed.EventID())

	if _, err := cmdBus.Dispatch(dispatchCtx, apporder.ShipOrderCommand{
		OrderID: placed.AggregateID(),
		Carrier: DefaultCarrier,
	}); err != nil {
		return fmt.Errorf("ship dispatch order=%s: %w", placed.AggregateID(), err)
	}

	log.Log(dispatchCtx, logger.LevelInfo, "shipped",
		logger.F("order_id", placed.AggregateID()),
		logger.F("carrier", DefaultCarrier))
	return nil
}
