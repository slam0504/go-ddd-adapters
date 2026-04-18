package kafka

import (
	"context"
	"errors"
	"fmt"

	"github.com/IBM/sarama"
	"github.com/ThreeDotsLabs/watermill"
	wmkafka "github.com/ThreeDotsLabs/watermill-kafka/v3/pkg/kafka"

	"github.com/slam0504/go-ddd-core/eventbus"
)

// SubscriberConfig configures a Kafka [Subscriber].
type SubscriberConfig struct {
	// Brokers is the list of Kafka bootstrap brokers.
	Brokers []string

	// ConsumerGroup is the Kafka consumer group id. When empty the
	// subscriber consumes all partitions itself (no group coordination).
	ConsumerGroup string

	// Codec decodes incoming watermill messages into DomainEvents. Required.
	Codec eventbus.Codec

	// SaramaConfig overrides the Sarama configuration. When nil the
	// subscriber uses watermill-kafka's default subscriber config
	// (Version=2.1.0, Offsets.Initial=OldestOffset).
	SaramaConfig *sarama.Config

	// Logger sinks watermill diagnostic logs. Defaults to watermill.NopLogger.
	Logger watermill.LoggerAdapter
}

// Subscriber implements eventbus.Subscriber over watermill-kafka v3.
type Subscriber struct {
	sub   *wmkafka.Subscriber
	codec eventbus.Codec
}

// NewSubscriber constructs a Subscriber.
func NewSubscriber(cfg SubscriberConfig) (*Subscriber, error) {
	if len(cfg.Brokers) == 0 {
		return nil, errors.New("kafka subscriber: Brokers is required")
	}
	if cfg.Codec == nil {
		return nil, errors.New("kafka subscriber: Codec is required")
	}

	logger := cfg.Logger
	if logger == nil {
		logger = watermill.NopLogger{}
	}

	saramaCfg := cfg.SaramaConfig
	if saramaCfg == nil {
		saramaCfg = wmkafka.DefaultSaramaSubscriberConfig()
		// Read from the start of the topic when there is no committed
		// offset. watermill-kafka's default leaves this at OffsetNewest,
		// which causes a slow-joining consumer group to miss messages
		// that were published before the assignment landed — surprising
		// in tests and in cold-start production scenarios alike.
		saramaCfg.Consumer.Offsets.Initial = sarama.OffsetOldest
	}

	sub, err := wmkafka.NewSubscriber(wmkafka.SubscriberConfig{
		Brokers:               cfg.Brokers,
		Unmarshaler:           wmkafka.DefaultMarshaler{},
		OverwriteSaramaConfig: saramaCfg,
		ConsumerGroup:         cfg.ConsumerGroup,
	}, logger)
	if err != nil {
		return nil, fmt.Errorf("kafka subscriber: %w", err)
	}

	return &Subscriber{sub: sub, codec: cfg.Codec}, nil
}

// Subscribe begins consuming the given topic. The returned channel emits
// decoded envelopes; when decode fails the underlying message is Nack'd and
// skipped (callers wanting poison-pill handling should wrap the codec).
//
// The output channel closes when ctx is cancelled or the subscriber is
// closed; callers should always read until close to avoid goroutine leaks.
// Ack/Nack of successfully decoded events is the caller's responsibility,
// matching the core Router contract.
func (s *Subscriber) Subscribe(ctx context.Context, topic string) (<-chan eventbus.Envelope, error) {
	raw, err := s.sub.Subscribe(ctx, topic)
	if err != nil {
		return nil, fmt.Errorf("kafka subscriber: subscribe to %s: %w", topic, err)
	}

	out := make(chan eventbus.Envelope)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-raw:
				if !ok {
					return
				}
				ev, name, err := s.codec.Unmarshal(ctx, msg)
				if err != nil {
					msg.Nack()
					continue
				}
				select {
				case <-ctx.Done():
					msg.Nack()
					return
				case out <- eventbus.Envelope{Event: ev, Name: name, Raw: msg}:
				}
			}
		}
	}()

	return out, nil
}

// Close shuts down the underlying watermill subscriber.
func (s *Subscriber) Close() error { return s.sub.Close() }

var _ eventbus.Subscriber = (*Subscriber)(nil)
