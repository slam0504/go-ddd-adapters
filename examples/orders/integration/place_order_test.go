//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/slam0504/go-ddd-core/application"
	"github.com/slam0504/go-ddd-core/eventbus"
	"github.com/slam0504/go-ddd-core/ports/logger"

	"github.com/slam0504/go-ddd-adapters/eventbus/kafka"
	"github.com/slam0504/go-ddd-adapters/eventbus/outbox"
	pgxoutbox "github.com/slam0504/go-ddd-adapters/eventbus/outbox/pgx"
	apporder "github.com/slam0504/go-ddd-adapters/examples/orders/application/order"
	orderdom "github.com/slam0504/go-ddd-adapters/examples/orders/domain/order"
	"github.com/slam0504/go-ddd-adapters/examples/orders/infra/eventcodec"
	"github.com/slam0504/go-ddd-adapters/examples/orders/infra/pgxrepo"
	slogger "github.com/slam0504/go-ddd-adapters/logger/slogger"
	pgxdb "github.com/slam0504/go-ddd-adapters/ports/database/pgx"
)

func testLogger() logger.Logger {
	return slogger.New(slogger.Config{Level: slog.LevelWarn})
}

// TestPlaceOrder_TransactionalOutbox asserts: (1) orders row exists,
// (2) outbox_records row exists with headers from ctx, (3) the Relay
// publishes the row and preserves trace/causation/correlation headers
// through to the Kafka envelope.
func TestPlaceOrder_TransactionalOutbox(t *testing.T) {
	truncate(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	codec := eventcodec.New()
	uow := application.UnitOfWorkFromTxManager(pgxdb.NewTxManager(sharedPool))
	repo := pgxrepo.New(sharedPool)
	store, err := pgxoutbox.NewStore(pgxoutbox.Config{Pool: sharedPool, Codec: codec})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	topic := "orders.placed.happy.test-" + uuid.NewString()
	handler := apporder.NewPlaceOrderHandler(uow, repo, store, topic, uuid.NewString)

	const (
		wantTrace       = "trace-pop"
		wantCausation   = "cause-pop"
		wantCorrelation = "correlation-pop"
		orderID         = "ord-happy"
	)

	pubCtx := kafka.WithTraceID(ctx, wantTrace)
	pubCtx = kafka.WithCausationID(pubCtx, wantCausation)
	pubCtx = kafka.WithCorrelationID(pubCtx, wantCorrelation)

	res, err := handler.Handle(pubCtx, apporder.PlaceOrderCommand{
		OrderID:    orderID,
		CustomerID: "alice",
		Items:      []orderdom.Item{{SKU: "A", Quantity: 2, PriceCents: 499}},
	})
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if res.TotalCents != 998 {
		t.Errorf("result total_cents: want 998, got %d", res.TotalCents)
	}

	// (i) orders row exists with the right shape
	var (
		customer   string
		status     string
		version    int64
		totalCents int64
	)
	if err := sharedPool.QueryRow(ctx,
		`SELECT customer_id, status, version, total_cents FROM orders WHERE id=$1`,
		orderID,
	).Scan(&customer, &status, &version, &totalCents); err != nil {
		t.Fatalf("orders row: %v", err)
	}
	if customer != "alice" {
		t.Errorf("customer: want alice, got %q", customer)
	}
	if status != string(orderdom.StatusPlaced) {
		t.Errorf("status: want %q, got %q", orderdom.StatusPlaced, status)
	}
	if totalCents != 998 {
		t.Errorf("total_cents: want 998, got %d", totalCents)
	}
	if version <= 0 {
		t.Errorf("version: want > 0, got %d", version)
	}

	// (ii) outbox_records row exists with the headers we set on ctx
	var (
		topicGot    string
		eventName   string
		aggregateID string
		headersJSON []byte
	)
	if err := sharedPool.QueryRow(ctx,
		`SELECT topic, event_name, aggregate_id, headers FROM outbox_records WHERE aggregate_id=$1`,
		orderID,
	).Scan(&topicGot, &eventName, &aggregateID, &headersJSON); err != nil {
		t.Fatalf("outbox row: %v", err)
	}
	if topicGot != topic {
		t.Errorf("outbox topic: want %q, got %q", topic, topicGot)
	}
	if eventName != orderdom.EventNamePlaced {
		t.Errorf("outbox event_name: want %q, got %q", orderdom.EventNamePlaced, eventName)
	}
	var hdrs map[string]string
	if err := json.Unmarshal(headersJSON, &hdrs); err != nil {
		t.Fatalf("headers json: %v", err)
	}
	if hdrs[eventbus.HeaderTraceID] != wantTrace {
		t.Errorf("outbox trace header: want %q, got %q", wantTrace, hdrs[eventbus.HeaderTraceID])
	}
	if hdrs[eventbus.HeaderCausationID] != wantCausation {
		t.Errorf("outbox causation header: want %q, got %q", wantCausation, hdrs[eventbus.HeaderCausationID])
	}
	if hdrs[eventbus.HeaderCorrelationID] != wantCorrelation {
		t.Errorf("outbox correlation header: want %q, got %q", wantCorrelation, hdrs[eventbus.HeaderCorrelationID])
	}

	// (iii) Subscribe + Relay; assert the envelope carries the headers
	// across the outbox -> relay -> kafka boundary.
	sub, err := kafka.NewSubscriber(kafka.SubscriberConfig{
		Brokers:       sharedBrokers,
		ConsumerGroup: "test-place-happy-" + uuid.NewString(),
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
		if got := meta.Get(eventbus.HeaderCausationID); got != wantCausation {
			t.Errorf("kafka causation header: want %q, got %q", wantCausation, got)
		}
		if got := meta.Get(eventbus.HeaderCorrelationID); got != wantCorrelation {
			t.Errorf("kafka correlation header: want %q, got %q", wantCorrelation, got)
		}
		if meta.Get(eventbus.HeaderEventName) != orderdom.EventNamePlaced {
			t.Errorf("kafka event_name header: want %q, got %q", orderdom.EventNamePlaced, meta.Get(eventbus.HeaderEventName))
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

// TestPlaceOrder_TxRollback wraps the real UoW to force an error after
// the handler's repo.Save + outbox.Stage succeed. The pgx tx must
// rollback, leaving zero rows in orders and outbox_records.
func TestPlaceOrder_TxRollback(t *testing.T) {
	truncate(t)
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

	handler := apporder.NewPlaceOrderHandler(
		testUoW, repo, store, "orders.placed.rollback.test", uuid.NewString)

	_, err = handler.Handle(ctx, apporder.PlaceOrderCommand{
		OrderID:    "ord-rollback",
		CustomerID: "bob",
		Items:      []orderdom.Item{{SKU: "B", Quantity: 1, PriceCents: 99}},
	})
	if !errors.Is(err, forceErr) {
		t.Fatalf("handle err: want forceErr, got %v", err)
	}

	var orderCount, outboxCount int
	if err := sharedPool.QueryRow(ctx, `SELECT COUNT(*) FROM orders`).Scan(&orderCount); err != nil {
		t.Fatalf("count orders: %v", err)
	}
	if err := sharedPool.QueryRow(ctx, `SELECT COUNT(*) FROM outbox_records`).Scan(&outboxCount); err != nil {
		t.Fatalf("count outbox_records: %v", err)
	}
	if orderCount != 0 {
		t.Errorf("orders rows after rollback: want 0, got %d", orderCount)
	}
	if outboxCount != 0 {
		t.Errorf("outbox rows after rollback: want 0, got %d", outboxCount)
	}
}
