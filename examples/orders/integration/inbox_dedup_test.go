//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/slam0504/go-ddd-core/application/command"
	"github.com/slam0504/go-ddd-core/eventbus"

	pgxinbox "github.com/slam0504/go-ddd-adapters/eventbus/inbox/pgx"
	pgxoutbox "github.com/slam0504/go-ddd-adapters/eventbus/outbox/pgx"
	apporder "github.com/slam0504/go-ddd-adapters/examples/orders/application/order"
	orderdom "github.com/slam0504/go-ddd-adapters/examples/orders/domain/order"
	"github.com/slam0504/go-ddd-adapters/examples/orders/infra/eventcodec"
	"github.com/slam0504/go-ddd-adapters/examples/orders/infra/pgxrepo"
	"github.com/slam0504/go-ddd-adapters/examples/orders/workerflow"
	pgxdb "github.com/slam0504/go-ddd-adapters/ports/database/pgx"
)

// TestWorker_DuplicateDelivery_ExactlyOnce proves the wired guarantee:
// delivering the same OrderPlaced envelope twice ships the order once,
// stages exactly one OrderShipped outbox row, and records exactly one
// inbox key — the duplicate is acked without side effects.
func TestWorker_DuplicateDelivery_ExactlyOnce(t *testing.T) {
	truncate(t)
	const (
		orderID         = "ord-inbox-dedup"
		consumedEventID = "evt-placed-dup"
	)
	seedPlacedOrder(t, orderID, "dana",
		[]orderdom.Item{{SKU: "D", Quantity: 1, PriceCents: 100}}, 100)

	codec := eventcodec.New()
	txMgr := pgxdb.NewTxManager(sharedPool)
	uow := workerflow.JoiningUnitOfWork(txMgr)
	repo := pgxrepo.New(sharedPool)
	store, err := pgxoutbox.NewStore(pgxoutbox.Config{Pool: sharedPool, Codec: codec})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	ibx, err := pgxinbox.New(sharedPool)
	if err != nil {
		t.Fatalf("pgxinbox: %v", err)
	}

	bus := command.NewInMemoryBus()
	topic := "orders.shipped.inbox-dedup.test-" + uuid.NewString()
	command.Register[apporder.ShipOrderCommand, apporder.ShipOrderResult](
		bus,
		apporder.NewShipOrderHandler(uow, repo, store, topic, uuid.NewString),
	)

	placed := orderdom.NewOrderPlaced(consumedEventID, orderID, 1, "dana", 100)
	env := eventbus.Envelope{Name: orderdom.EventNamePlaced, Event: &placed}

	ctx := context.Background()
	if err := workerflow.HandleOrderPlacedOnce(ctx, testLogger(), txMgr, ibx, bus, env); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	if err := workerflow.HandleOrderPlacedOnce(ctx, testLogger(), txMgr, ibx, bus, env); err != nil {
		t.Fatalf("duplicate delivery: want nil (ack), got %v", err)
	}

	var outboxRows int
	if err := sharedPool.QueryRow(ctx,
		`SELECT count(*) FROM outbox_records WHERE aggregate_id = $1`, orderID,
	).Scan(&outboxRows); err != nil {
		t.Fatalf("outbox count: %v", err)
	}
	if outboxRows != 1 {
		t.Fatalf("outbox rows = %d, want exactly 1 (duplicate must not re-stage)", outboxRows)
	}

	var inboxRows int
	if err := sharedPool.QueryRow(ctx,
		`SELECT count(*) FROM inbox_records WHERE consumer = $1 AND event_id = $2`,
		workerflow.ConsumerName, consumedEventID,
	).Scan(&inboxRows); err != nil {
		t.Fatalf("inbox count: %v", err)
	}
	if inboxRows != 1 {
		t.Fatalf("inbox rows = %d, want exactly 1", inboxRows)
	}
}
