//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/google/uuid"

	"github.com/slam0504/go-ddd-core/application"
	"github.com/slam0504/go-ddd-core/application/command"
	"github.com/slam0504/go-ddd-core/domain"
	"github.com/slam0504/go-ddd-core/eventbus"

	"github.com/slam0504/go-ddd-adapters/eventbus/kafka"
	"github.com/slam0504/go-ddd-adapters/eventbus/outbox"
	pgxoutbox "github.com/slam0504/go-ddd-adapters/eventbus/outbox/pgx"
	apporder "github.com/slam0504/go-ddd-adapters/examples/orders/application/order"
	orderdom "github.com/slam0504/go-ddd-adapters/examples/orders/domain/order"
	"github.com/slam0504/go-ddd-adapters/examples/orders/infra/eventcodec"
	"github.com/slam0504/go-ddd-adapters/examples/orders/infra/pgxrepo"
	"github.com/slam0504/go-ddd-adapters/examples/orders/workerflow"
	pgxdb "github.com/slam0504/go-ddd-adapters/ports/database/pgx"
)

// seedPlacedOrder writes a placed order to the orders table directly,
// bypassing the outbox path. This keeps the ShipOrder outbox row count
// in tests at exactly one (OrderShipped) — seeding via PlaceOrderHandler
// would leave an OrderPlaced row that would pollute COUNT(*) assertions.
func seedPlacedOrder(t *testing.T, id, customer string, items []orderdom.Item, totalCents int64) {
	t.Helper()
	o := orderdom.Hydrate(orderdom.ID(id), customer, orderdom.StatusPlaced, 1, totalCents, items)
	repo := pgxrepo.New(sharedPool)
	if err := repo.Save(context.Background(), o); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// TestShipOrder_TransactionalOutbox seeds a placed order, dispatches
// ShipOrderCommand through ShipOrderHandler, then asserts: (1) orders
// row status moved to shipped + version incremented, (2) outbox_records
// has exactly one row with topic=orders.shipped + headers from ctx,
// (3) Relay publishes and Kafka envelope carries the headers.
func TestShipOrder_TransactionalOutbox(t *testing.T) {
	truncate(t)

	const (
		wantTrace       = "trace-ship"
		wantCausation   = "cause-ship"
		wantCorrelation = "correlation-ship"
		orderID         = "ord-ship-happy"
	)

	seedPlacedOrder(t, orderID, "carol",
		[]orderdom.Item{{SKU: "C", Quantity: 3, PriceCents: 100}}, 300)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	codec := eventcodec.New()
	uow := application.UnitOfWorkFromTxManager(pgxdb.NewTxManager(sharedPool))
	repo := pgxrepo.New(sharedPool)
	store, err := pgxoutbox.NewStore(pgxoutbox.Config{Pool: sharedPool, Codec: codec})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	topic := "orders.shipped.happy.test-" + uuid.NewString()
	handler := apporder.NewShipOrderHandler(uow, repo, store, topic, uuid.NewString)

	pubCtx := kafka.WithTraceID(ctx, wantTrace)
	pubCtx = kafka.WithCausationID(pubCtx, wantCausation)
	pubCtx = kafka.WithCorrelationID(pubCtx, wantCorrelation)

	if _, err := handler.Handle(pubCtx, apporder.ShipOrderCommand{
		OrderID: orderID,
		Carrier: "demo-carrier",
	}); err != nil {
		t.Fatalf("handle: %v", err)
	}

	// (i) orders row status = shipped, version incremented
	var status string
	var version int64
	if err := sharedPool.QueryRow(ctx,
		`SELECT status, version FROM orders WHERE id=$1`, orderID,
	).Scan(&status, &version); err != nil {
		t.Fatalf("orders row: %v", err)
	}
	if status != string(orderdom.StatusShipped) {
		t.Errorf("status: want %q, got %q", orderdom.StatusShipped, status)
	}
	if version != 2 {
		t.Errorf("version: want 2 (seed=1, Ship increments), got %d", version)
	}

	// (ii) exactly one outbox row for this aggregate, topic = orders.shipped, headers from ctx
	var rowCount int
	if err := sharedPool.QueryRow(ctx,
		`SELECT COUNT(*) FROM outbox_records WHERE aggregate_id=$1`, orderID,
	).Scan(&rowCount); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if rowCount != 1 {
		t.Errorf("outbox rows for %s: want 1, got %d", orderID, rowCount)
	}

	var (
		topicGot    string
		eventName   string
		headersJSON []byte
	)
	if err := sharedPool.QueryRow(ctx,
		`SELECT topic, event_name, headers FROM outbox_records WHERE aggregate_id=$1`,
		orderID,
	).Scan(&topicGot, &eventName, &headersJSON); err != nil {
		t.Fatalf("outbox row: %v", err)
	}
	if topicGot != topic {
		t.Errorf("outbox topic: want %q, got %q", topic, topicGot)
	}
	if eventName != orderdom.EventNameShipped {
		t.Errorf("outbox event_name: want %q, got %q", orderdom.EventNameShipped, eventName)
	}
	var hdrs map[string]string
	if err := json.Unmarshal(headersJSON, &hdrs); err != nil {
		t.Fatalf("headers json: %v", err)
	}
	if hdrs[eventbus.HeaderTraceID] != wantTrace {
		t.Errorf("outbox trace header: want %q, got %q", wantTrace, hdrs[eventbus.HeaderTraceID])
	}

	// (iii) Relay publishes; kafka envelope preserves headers
	sub, err := kafka.NewSubscriber(kafka.SubscriberConfig{
		Brokers:       sharedBrokers,
		ConsumerGroup: "test-ship-happy-" + uuid.NewString(),
		Codec:         codec,
	})
	if err != nil {
		t.Fatalf("new subscriber: %v", err)
	}
	t.Cleanup(func() { _ = sub.Close() })

	envelopes, err := sub.Subscribe(ctx, topic)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	pub, err := kafka.NewPublisher(kafka.PublisherConfig{
		Brokers:              sharedBrokers,
		Codec:                codec,
		PartitionByAggregate: true,
	})
	if err != nil {
		t.Fatalf("new publisher: %v", err)
	}
	t.Cleanup(func() { _ = pub.Close() })

	relay, err := outbox.NewRelay(
		outbox.RelayConfig{Store: store, Publisher: pub, Codec: codec, Logger: testLogger()},
		outbox.WithHeaderRestorer(kafka.RestoreCoreHeaders),
		outbox.WithPollInterval(50*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("new relay: %v", err)
	}

	relayCtx, relayCancel := context.WithCancel(ctx)
	relayDone := make(chan struct{})
	go func() {
		defer close(relayDone)
		if rerr := relay.Run(relayCtx); rerr != nil && !errors.Is(rerr, context.Canceled) {
			t.Errorf("relay.Run: %v", rerr)
		}
	}()

	select {
	case env := <-envelopes:
		meta := env.Raw.Metadata
		if got := meta.Get(eventbus.HeaderTraceID); got != wantTrace {
			t.Errorf("kafka trace header: want %q, got %q", wantTrace, got)
		}
		if got := meta.Get(eventbus.HeaderEventName); got != orderdom.EventNameShipped {
			t.Errorf("kafka event_name: want %q, got %q", orderdom.EventNameShipped, got)
		}
		env.Ack()
	case <-time.After(15 * time.Second):
		t.Fatal("timeout waiting for relay publish")
	}

	relayCancel()
	select {
	case <-relayDone:
	case <-time.After(5 * time.Second):
		t.Fatal("relay did not stop within 5s after cancel")
	}
}

// TestShipOrder_TxRollback seeds a placed order, then forces a rollback
// inside the handler's UoW. Asserts (a) handler returns the forced
// error, (b) orders.status stays "placed" (no aggregate mutation
// persisted), (c) zero new outbox rows.
func TestShipOrder_TxRollback(t *testing.T) {
	truncate(t)

	const orderID = "ord-ship-rollback"
	seedPlacedOrder(t, orderID, "dan",
		[]orderdom.Item{{SKU: "D", Quantity: 1, PriceCents: 50}}, 50)

	ctx := context.Background()

	codec := eventcodec.New()
	realUoW := application.UnitOfWorkFromTxManager(pgxdb.NewTxManager(sharedPool))
	repo := pgxrepo.New(sharedPool)
	store, err := pgxoutbox.NewStore(pgxoutbox.Config{Pool: sharedPool, Codec: codec})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	forceErr := errors.New("forced rollback")
	testUoW := application.UnitOfWorkFunc(func(ctx context.Context, fn func(context.Context) error) error {
		return realUoW.Do(ctx, func(ctx context.Context) error {
			if err := fn(ctx); err != nil {
				return err
			}
			return forceErr
		})
	})

	handler := apporder.NewShipOrderHandler(
		testUoW, repo, store, "orders.shipped.rollback.test", uuid.NewString)

	_, err = handler.Handle(ctx, apporder.ShipOrderCommand{
		OrderID: orderID,
		Carrier: "demo-carrier",
	})
	if !errors.Is(err, forceErr) {
		t.Fatalf("handle err: want forceErr, got %v", err)
	}

	// Aggregate row still in placed state (the seed row), no mutation persisted.
	var status string
	var version int64
	if err := sharedPool.QueryRow(ctx,
		`SELECT status, version FROM orders WHERE id=$1`, orderID,
	).Scan(&status, &version); err != nil {
		t.Fatalf("orders row: %v", err)
	}
	if status != string(orderdom.StatusPlaced) {
		t.Errorf("status: want %q (seed unchanged), got %q", orderdom.StatusPlaced, status)
	}
	if version != 1 {
		t.Errorf("version: want 1 (seed unchanged), got %d", version)
	}

	// Zero outbox rows for this aggregate.
	var rowCount int
	if err := sharedPool.QueryRow(ctx,
		`SELECT COUNT(*) FROM outbox_records WHERE aggregate_id=$1`, orderID,
	).Scan(&rowCount); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if rowCount != 0 {
		t.Errorf("outbox rows after rollback: want 0, got %d", rowCount)
	}
}

// TestWorker_HandleOrderPlaced_PropagatesHeaders covers the worker
// boundary that ShipOrder integration tests skip: subscriber decodes a
// Kafka envelope but does NOT restore propagation headers into ctx, so
// workerflow.HandleOrderPlaced must do that itself before dispatching
// ShipOrderCommand. Otherwise OrderShipped's outbox row would carry no
// trace_id / correlation_id and the distributed trace would break at
// the worker process boundary.
//
// Builds a synthetic envelope (raw watermill message with metadata),
// calls workerflow.HandleOrderPlaced directly, and asserts the staged
// OrderShipped outbox row carries the inbound trace/correlation and
// has causation_id set to the consumed OrderPlaced event id.
func TestWorker_HandleOrderPlaced_PropagatesHeaders(t *testing.T) {
	truncate(t)

	const (
		orderID         = "ord-worker-prop"
		consumedEventID = "evt-placed-incoming"
		wantTrace       = "trace-from-broker"
		wantCorrelation = "correlation-from-broker"
	)

	seedPlacedOrder(t, orderID, "elena",
		[]orderdom.Item{{SKU: "E", Quantity: 1, PriceCents: 100}}, 100)

	codec := eventcodec.New()
	uow := application.UnitOfWorkFromTxManager(pgxdb.NewTxManager(sharedPool))
	repo := pgxrepo.New(sharedPool)
	store, err := pgxoutbox.NewStore(pgxoutbox.Config{Pool: sharedPool, Codec: codec})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	bus := command.NewInMemoryBus()
	topic := "orders.shipped.worker-prop.test-" + uuid.NewString()
	command.Register[apporder.ShipOrderCommand, apporder.ShipOrderResult](
		bus,
		apporder.NewShipOrderHandler(uow, repo, store, topic, uuid.NewString),
	)

	placed := orderdom.NewOrderPlaced(consumedEventID, orderID, 1, "elena", 100)
	rawMsg := message.NewMessage(uuid.NewString(), []byte("payload-unused"))
	rawMsg.Metadata.Set(eventbus.HeaderTraceID, wantTrace)
	rawMsg.Metadata.Set(eventbus.HeaderCorrelationID, wantCorrelation)
	// Deliberately NO causation_id on inbound — workerflow must set it
	// to placed.EventID() per the propagation contract.

	env := eventbus.Envelope{
		Name:  orderdom.EventNamePlaced,
		Event: &placed,
		Raw:   rawMsg,
	}

	if err := workerflow.HandleOrderPlaced(context.Background(), testLogger(), bus, env); err != nil {
		t.Fatalf("HandleOrderPlaced: %v", err)
	}

	var headersJSON []byte
	if err := sharedPool.QueryRow(context.Background(),
		`SELECT headers FROM outbox_records WHERE aggregate_id=$1`, orderID,
	).Scan(&headersJSON); err != nil {
		t.Fatalf("outbox row: %v", err)
	}

	var hdrs map[string]string
	if err := json.Unmarshal(headersJSON, &hdrs); err != nil {
		t.Fatalf("headers json: %v", err)
	}
	if hdrs[eventbus.HeaderTraceID] != wantTrace {
		t.Errorf("trace_id: want %q (from inbound), got %q", wantTrace, hdrs[eventbus.HeaderTraceID])
	}
	if hdrs[eventbus.HeaderCorrelationID] != wantCorrelation {
		t.Errorf("correlation_id: want %q (from inbound), got %q", wantCorrelation, hdrs[eventbus.HeaderCorrelationID])
	}
	if hdrs[eventbus.HeaderCausationID] != consumedEventID {
		t.Errorf("causation_id: want %q (= consumed OrderPlaced event id), got %q",
			consumedEventID, hdrs[eventbus.HeaderCausationID])
	}
}

// TestShipOrder_NotFound asserts pgxrepo.FindByID maps pgx.ErrNoRows
// to domain.ErrNotFound and ShipOrderHandler surfaces it unchanged
// (so the worker can decide to nack vs ack-and-log). This locks the
// not-found contract that the spec calls out explicitly.
func TestShipOrder_NotFound(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	codec := eventcodec.New()
	uow := application.UnitOfWorkFromTxManager(pgxdb.NewTxManager(sharedPool))
	repo := pgxrepo.New(sharedPool)
	store, err := pgxoutbox.NewStore(pgxoutbox.Config{Pool: sharedPool, Codec: codec})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	handler := apporder.NewShipOrderHandler(
		uow, repo, store, "orders.shipped.notfound.test", uuid.NewString)

	_, err = handler.Handle(ctx, apporder.ShipOrderCommand{
		OrderID: "does-not-exist",
		Carrier: "demo-carrier",
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("handle err: want domain.ErrNotFound, got %v", err)
	}
}
