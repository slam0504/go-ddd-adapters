//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/slam0504/go-ddd-core/eventbus"

	"github.com/slam0504/go-ddd-adapters/eventbus/kafka"
	orderdom "github.com/slam0504/go-ddd-adapters/examples/orders/domain/order"
	"github.com/slam0504/go-ddd-adapters/examples/orders/infra/eventcodec"
)

// TestRoundTrip_AllHeaders publishes one OrderPlaced and asserts every
// header populated by the codec — six identity headers always set, plus
// three optional headers driven by ctx — survives the round-trip.
func TestRoundTrip_AllHeaders(t *testing.T) {
	const topic = "orders.placed.roundtrip"
	ctx := context.Background()

	codec := eventcodec.New()

	pub, err := kafka.NewPublisher(kafka.PublisherConfig{
		Brokers: sharedBrokers,
		Codec:   codec,
	})
	if err != nil {
		t.Fatalf("new publisher: %v", err)
	}
	t.Cleanup(func() { _ = pub.Close() })

	sub, err := kafka.NewSubscriber(kafka.SubscriberConfig{
		Brokers:       sharedBrokers,
		ConsumerGroup: "rt-" + uuid.NewString(),
		Codec:         codec,
	})
	if err != nil {
		t.Fatalf("new subscriber: %v", err)
	}
	t.Cleanup(func() { _ = sub.Close() })

	subCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)

	envelopes, err := sub.Subscribe(subCtx, topic)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// Sleep briefly so the consumer-group join completes before we publish.
	// Without this the message can land before the assignment is ready and
	// the test races on the offset reset.
	time.Sleep(2 * time.Second)

	eventID := uuid.NewString()
	aggregateID := uuid.NewString()
	const wantTrace, wantCausation, wantCorrelation = "trace-abc", "cause-xyz", "correlation-123"

	placed := orderdom.NewOrderPlaced(eventID, aggregateID, 1, "alice", 12345)

	pubCtx := kafka.WithTraceID(ctx, wantTrace)
	pubCtx = kafka.WithCausationID(pubCtx, wantCausation)
	pubCtx = kafka.WithCorrelationID(pubCtx, wantCorrelation)

	if err := pub.Publish(pubCtx, topic, &placed); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case env := <-envelopes:
		assertHeaders(t, env, eventID, aggregateID, wantTrace, wantCausation, wantCorrelation)
		assertPayload(t, env)
		env.Ack()
	case <-time.After(15 * time.Second):
		t.Fatal("timeout waiting for envelope")
	}
}

func assertHeaders(t *testing.T, env eventbus.Envelope, eventID, aggregateID, trace, causation, correlation string) {
	t.Helper()
	if env.Name != orderdom.EventNamePlaced {
		t.Errorf("envelope name: want %q, got %q", orderdom.EventNamePlaced, env.Name)
	}
	meta := env.Raw.Metadata
	checks := []struct {
		header, want string
	}{
		{eventbus.HeaderEventID, eventID},
		{eventbus.HeaderEventName, orderdom.EventNamePlaced},
		{eventbus.HeaderAggregateID, aggregateID},
		{eventbus.HeaderAggregateType, "Order"},
		{eventbus.HeaderEventVersion, "1"},
		{eventbus.HeaderTraceID, trace},
		{eventbus.HeaderCausationID, causation},
		{eventbus.HeaderCorrelationID, correlation},
	}
	for _, c := range checks {
		if got := meta.Get(c.header); got != c.want {
			t.Errorf("header %s: want %q, got %q", c.header, c.want, got)
		}
	}
	if got := meta.Get(eventbus.HeaderOccurredAt); got == "" {
		t.Errorf("header %s: empty", eventbus.HeaderOccurredAt)
	} else if _, err := time.Parse(time.RFC3339Nano, got); err != nil {
		t.Errorf("header %s: not RFC3339Nano (%q): %v", eventbus.HeaderOccurredAt, got, err)
	}
}

func assertPayload(t *testing.T, env eventbus.Envelope) {
	t.Helper()
	got, ok := env.Event.(*orderdom.OrderPlaced)
	if !ok {
		t.Fatalf("event type: want *orderdom.OrderPlaced, got %T", env.Event)
	}
	if got.CustomerID != "alice" {
		t.Errorf("payload.CustomerID: want %q, got %q", "alice", got.CustomerID)
	}
	if got.TotalCents != 12345 {
		t.Errorf("payload.TotalCents: want %d, got %d", 12345, got.TotalCents)
	}
}
