package kafka

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"

	"github.com/slam0504/go-ddd-core/domain"
	"github.com/slam0504/go-ddd-core/eventbus"
)

// ErrUnknownEvent is returned by [JSONCodec.Unmarshal] when the incoming
// message carries an event name that has not been registered.
var ErrUnknownEvent = errors.New("kafka: unknown event name")

// JSONCodec encodes DomainEvents as JSON and reconstructs them via a
// caller-populated type registry. Safe for concurrent use.
type JSONCodec struct {
	mu       sync.RWMutex
	registry map[string]func() domain.DomainEvent
}

// NewJSONCodec returns an empty codec. Register event factories before
// passing the codec to a Subscriber.
func NewJSONCodec() *JSONCodec {
	return &JSONCodec{registry: make(map[string]func() domain.DomainEvent)}
}

// Register binds an event name to a factory that returns a fresh, addressable
// instance suitable for json.Unmarshal. The factory must return a pointer.
//
// Re-registering an existing name overwrites the previous factory.
func (c *JSONCodec) Register(name string, factory func() domain.DomainEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.registry[name] = factory
}

// Marshal encodes ev as JSON and produces a watermill message. The watermill
// message UUID is set to ev.EventID() so retries and dedupe see a stable id.
//
// Six identity headers are always populated:
// event_id, event_name, aggregate_id, aggregate_type, occurred_at
// (RFC3339Nano), event_version. trace_id, causation_id, correlation_id are
// pulled from ctx via [TraceIDFrom] / [CausationIDFrom] / [CorrelationIDFrom]
// and only set when present.
func (c *JSONCodec) Marshal(ctx context.Context, ev domain.DomainEvent) (*message.Message, error) {
	payload, err := json.Marshal(ev)
	if err != nil {
		return nil, fmt.Errorf("kafka codec: marshal payload: %w", err)
	}

	msg := message.NewMessage(ev.EventID(), payload)
	msg.SetContext(ctx)

	msg.Metadata.Set(eventbus.HeaderEventID, ev.EventID())
	msg.Metadata.Set(eventbus.HeaderEventName, ev.EventName())
	msg.Metadata.Set(eventbus.HeaderAggregateID, ev.AggregateID())
	msg.Metadata.Set(eventbus.HeaderAggregateType, ev.AggregateType())
	msg.Metadata.Set(eventbus.HeaderOccurredAt, ev.OccurredAt().Format(time.RFC3339Nano))
	msg.Metadata.Set(eventbus.HeaderEventVersion, strconv.FormatInt(ev.Version(), 10))

	if v, ok := TraceIDFrom(ctx); ok {
		msg.Metadata.Set(eventbus.HeaderTraceID, v)
	}
	if v, ok := CausationIDFrom(ctx); ok {
		msg.Metadata.Set(eventbus.HeaderCausationID, v)
	}
	if v, ok := CorrelationIDFrom(ctx); ok {
		msg.Metadata.Set(eventbus.HeaderCorrelationID, v)
	}

	return msg, nil
}

// Unmarshal looks up the registered factory for the message's event_name
// header, instantiates a fresh event, and decodes the JSON payload into it.
func (c *JSONCodec) Unmarshal(_ context.Context, msg *message.Message) (domain.DomainEvent, string, error) {
	name := msg.Metadata.Get(eventbus.HeaderEventName)
	if name == "" {
		return nil, "", fmt.Errorf("kafka codec: %w: missing %s header", ErrUnknownEvent, eventbus.HeaderEventName)
	}

	c.mu.RLock()
	factory, ok := c.registry[name]
	c.mu.RUnlock()
	if !ok {
		return nil, name, fmt.Errorf("kafka codec: %w: %s", ErrUnknownEvent, name)
	}

	ev := factory()
	if err := json.Unmarshal(msg.Payload, ev); err != nil {
		return nil, name, fmt.Errorf("kafka codec: unmarshal %s: %w", name, err)
	}
	return ev, name, nil
}

var _ eventbus.Codec = (*JSONCodec)(nil)
