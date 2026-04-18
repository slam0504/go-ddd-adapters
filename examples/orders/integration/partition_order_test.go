//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/slam0504/go-ddd-adapters/eventbus/kafka"
	orderdom "github.com/slam0504/go-ddd-adapters/examples/orders/domain/order"
	"github.com/slam0504/go-ddd-adapters/examples/orders/infra/eventcodec"
)

// TestPartitionByAggregate_PreservesOrder publishes a stream of events for
// the same aggregate id and asserts the consumer sees them in the version
// order the publisher produced them. With PartitionByAggregate=true the
// publisher routes them all to one Kafka partition, which guarantees order.
func TestPartitionByAggregate_PreservesOrder(t *testing.T) {
	const (
		topic     = "orders.placed.partition"
		eventCount = 50
	)
	ctx := context.Background()

	codec := eventcodec.New()

	pub, err := kafka.NewPublisher(kafka.PublisherConfig{
		Brokers:              sharedBrokers,
		Codec:                codec,
		PartitionByAggregate: true,
	})
	if err != nil {
		t.Fatalf("new publisher: %v", err)
	}
	t.Cleanup(func() { _ = pub.Close() })

	sub, err := kafka.NewSubscriber(kafka.SubscriberConfig{
		Brokers:       sharedBrokers,
		ConsumerGroup: "po-" + uuid.NewString(),
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

	time.Sleep(2 * time.Second)

	aggregateID := uuid.NewString()
	for i := 1; i <= eventCount; i++ {
		ev := orderdom.NewOrderPlaced(uuid.NewString(), aggregateID, int64(i), "bob", int64(i)*100)
		if err := pub.Publish(ctx, topic, &ev); err != nil {
			t.Fatalf("publish #%d: %v", i, err)
		}
	}

	deadline := time.After(20 * time.Second)
	for want := int64(1); want <= eventCount; want++ {
		select {
		case env := <-envelopes:
			placed, ok := env.Event.(*orderdom.OrderPlaced)
			if !ok {
				t.Fatalf("event %d type: %T", want, env.Event)
			}
			if got := placed.Version(); got != want {
				t.Fatalf("out-of-order: want version %d, got %d (aggregate=%s)", want, got, placed.AggregateID())
			}
			env.Ack()
		case <-deadline:
			t.Fatalf("timeout at version %d / %d", want, eventCount)
		}
	}
}
