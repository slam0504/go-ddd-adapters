package kafka_test

import (
	"context"
	"testing"
	"time"

	"github.com/slam0504/go-ddd-core/domain"
	"github.com/slam0504/go-ddd-core/eventbus"

	"github.com/slam0504/go-ddd-adapters/eventbus/kafka"
)

type testEvent struct {
	domain.BaseEvent
	Payload string `json:"payload"`
}

func newTestEvent() *testEvent {
	base := domain.NewBaseEvent("evt-1", "test.event.v1", "agg-1", "Test", 1)
	base.At = time.Unix(0, 0).UTC()
	return &testEvent{BaseEvent: base}
}

// TestRestoreCoreHeaders_RoundtripPromotesValuesIntoMetadata pins the
// full outbox→relay→codec roundtrip: a stored Headers map carrying the
// three well-known propagation headers is restored into ctx by
// RestoreCoreHeaders, and the kafka JSON codec then re-promotes them
// to msg.Metadata on Marshal. The combined contract is what makes the
// outbox Relay's WithHeaderRestorer wiring meaningful.
func TestRestoreCoreHeaders_RoundtripPromotesValuesIntoMetadata(t *testing.T) {
	stored := map[string]string{
		eventbus.HeaderTraceID:       "trace-xyz",
		eventbus.HeaderCausationID:   "cause-xyz",
		eventbus.HeaderCorrelationID: "corr-xyz",
	}

	ctx := kafka.RestoreCoreHeaders(context.Background(), stored)
	codec := kafka.NewJSONCodec()
	msg, err := codec.Marshal(ctx, newTestEvent())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	cases := map[string]string{
		eventbus.HeaderTraceID:       "trace-xyz",
		eventbus.HeaderCausationID:   "cause-xyz",
		eventbus.HeaderCorrelationID: "corr-xyz",
	}
	for header, want := range cases {
		if got := msg.Metadata.Get(header); got != want {
			t.Errorf("Metadata[%s] = %q, want %q", header, got, want)
		}
	}
}

// TestRestoreCoreHeaders_OmitsEmptyValues guarantees the helper does
// not overwrite ctx with empty strings — the codec's ctx getters only
// set metadata when the value is non-empty, so an empty restore is a
// no-op that should not pollute any subsequent Marshal.
func TestRestoreCoreHeaders_OmitsEmptyValues(t *testing.T) {
	stored := map[string]string{
		eventbus.HeaderTraceID: "", // intentionally empty
		// HeaderCausationID and HeaderCorrelationID absent entirely.
	}

	ctx := kafka.RestoreCoreHeaders(context.Background(), stored)
	codec := kafka.NewJSONCodec()
	msg, err := codec.Marshal(ctx, newTestEvent())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	for _, header := range []string{
		eventbus.HeaderTraceID,
		eventbus.HeaderCausationID,
		eventbus.HeaderCorrelationID,
	} {
		if got := msg.Metadata.Get(header); got != "" {
			t.Errorf("Metadata[%s] = %q, want empty", header, got)
		}
	}
}

// TestRestoreCoreHeaders_IgnoresNonWellKnownHeaders documents that the
// helper is intentionally selective: arbitrary headers in the stored
// map do NOT flow through. This is the limitation the outbox Relay's
// WithHeaderRestorer pattern accepts.
func TestRestoreCoreHeaders_IgnoresNonWellKnownHeaders(t *testing.T) {
	stored := map[string]string{
		"my-custom-header": "should-not-propagate",
		eventbus.HeaderTraceID: "trace-x",
	}

	ctx := kafka.RestoreCoreHeaders(context.Background(), stored)
	codec := kafka.NewJSONCodec()
	msg, err := codec.Marshal(ctx, newTestEvent())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if got := msg.Metadata.Get("my-custom-header"); got != "" {
		t.Errorf("expected my-custom-header NOT propagated, got %q", got)
	}
	if got := msg.Metadata.Get(eventbus.HeaderTraceID); got != "trace-x" {
		t.Errorf("trace_id = %q, want trace-x", got)
	}
}
