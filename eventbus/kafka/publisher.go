package kafka

import (
	"context"
	"errors"
	"fmt"

	"github.com/IBM/sarama"
	"github.com/ThreeDotsLabs/watermill"
	wmkafka "github.com/ThreeDotsLabs/watermill-kafka/v3/pkg/kafka"
	"github.com/ThreeDotsLabs/watermill/message"

	"github.com/slam0504/go-ddd-core/domain"
	"github.com/slam0504/go-ddd-core/eventbus"
)

// PublisherConfig configures a Kafka [Publisher].
type PublisherConfig struct {
	// Brokers is the list of Kafka bootstrap brokers.
	Brokers []string

	// Codec converts DomainEvents into watermill messages. Required.
	Codec eventbus.Codec

	// SaramaConfig overrides the Sarama configuration. When nil, the
	// publisher uses watermill-kafka's default sync publisher config
	// (Return.Successes=true, Version=2.1.0).
	SaramaConfig *sarama.Config

	// Logger sinks watermill diagnostic logs. Defaults to watermill.NopLogger.
	Logger watermill.LoggerAdapter

	// PartitionByAggregate, when true, sets the Kafka partition key to the
	// HeaderAggregateID of each outgoing message so events for the same
	// aggregate land on the same partition and preserve causal order.
	PartitionByAggregate bool
}

// Publisher implements eventbus.Publisher over watermill-kafka v3.
type Publisher struct {
	pub   message.Publisher
	codec eventbus.Codec
}

// NewPublisher constructs a Publisher.
func NewPublisher(cfg PublisherConfig) (*Publisher, error) {
	if len(cfg.Brokers) == 0 {
		return nil, errors.New("kafka publisher: Brokers is required")
	}
	if cfg.Codec == nil {
		return nil, errors.New("kafka publisher: Codec is required")
	}

	logger := cfg.Logger
	if logger == nil {
		logger = watermill.NopLogger{}
	}

	saramaCfg := cfg.SaramaConfig
	if saramaCfg == nil {
		saramaCfg = wmkafka.DefaultSaramaSyncPublisherConfig()
	}

	var marshaler wmkafka.Marshaler = wmkafka.DefaultMarshaler{}
	if cfg.PartitionByAggregate {
		marshaler = wmkafka.NewWithPartitioningMarshaler(
			func(_ string, msg *message.Message) (string, error) {
				return msg.Metadata.Get(eventbus.HeaderAggregateID), nil
			},
		)
	}

	pub, err := wmkafka.NewPublisher(wmkafka.PublisherConfig{
		Brokers:               cfg.Brokers,
		Marshaler:             marshaler,
		OverwriteSaramaConfig: saramaCfg,
	}, logger)
	if err != nil {
		return nil, fmt.Errorf("kafka publisher: %w", err)
	}

	return &Publisher{pub: pub, codec: cfg.Codec}, nil
}

// Publish encodes each event with the codec and sends them as a single
// watermill batch. Encoding errors abort the call before any send.
func (p *Publisher) Publish(ctx context.Context, topic string, events ...domain.DomainEvent) error {
	if len(events) == 0 {
		return nil
	}
	msgs := make([]*message.Message, 0, len(events))
	for _, ev := range events {
		msg, err := p.codec.Marshal(ctx, ev)
		if err != nil {
			return fmt.Errorf("kafka publisher: encode %s: %w", ev.EventName(), err)
		}
		msgs = append(msgs, msg)
	}
	if err := p.pub.Publish(topic, msgs...); err != nil {
		return fmt.Errorf("kafka publisher: publish to %s: %w", topic, err)
	}
	return nil
}

// Close shuts down the underlying watermill publisher.
func (p *Publisher) Close() error { return p.pub.Close() }

var _ eventbus.Publisher = (*Publisher)(nil)
