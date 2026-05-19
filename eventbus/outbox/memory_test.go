package outbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"

	"github.com/slam0504/go-ddd-core/domain"
	"github.com/slam0504/go-ddd-core/eventbus"

	"github.com/slam0504/go-ddd-adapters/eventbus/outbox"
)

// --- test fixtures ---------------------------------------------------

type testEvent struct {
	domain.BaseEvent
	Body string `json:"body"`
}

func makeEvent(id, name, aggID, aggType, body string) *testEvent {
	base := domain.NewBaseEvent(id, name, aggID, aggType, 1)
	base.At = time.Unix(0, 0).UTC()
	return &testEvent{BaseEvent: base, Body: body}
}

// jsonCodec is a tiny eventbus.Codec used by Memory tests. It is
// intentionally simpler than the kafka JSONCodec — it does not set
// canonical headers from the event interface, which lets us verify
// that Memory.Stage reads those fields from the DomainEvent methods
// rather than from codec metadata (decision #23).
type jsonCodec struct {
	failOn     string // EventID that should fail Marshal; empty = never fail
	headerMode string // "none" | "all"; default "all" — set "none" to omit metadata
	registry   map[string]func() domain.DomainEvent
}

func newJSONCodec() *jsonCodec {
	return &jsonCodec{
		headerMode: "all",
		registry:   map[string]func() domain.DomainEvent{},
	}
}

func (c *jsonCodec) Register(name string, factory func() domain.DomainEvent) {
	c.registry[name] = factory
}

func (c *jsonCodec) Marshal(_ context.Context, ev domain.DomainEvent) (*message.Message, error) {
	if c.failOn != "" && ev.EventID() == c.failOn {
		return nil, fmt.Errorf("forced marshal error for %s", ev.EventID())
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		return nil, err
	}
	msg := message.NewMessage(ev.EventID(), payload)
	if c.headerMode == "all" {
		msg.Metadata.Set(eventbus.HeaderEventID, ev.EventID())
		msg.Metadata.Set(eventbus.HeaderEventName, ev.EventName())
		msg.Metadata.Set(eventbus.HeaderAggregateID, ev.AggregateID())
		msg.Metadata.Set(eventbus.HeaderAggregateType, ev.AggregateType())
	}
	return msg, nil
}

func (c *jsonCodec) Unmarshal(_ context.Context, msg *message.Message) (domain.DomainEvent, string, error) {
	name := msg.Metadata.Get(eventbus.HeaderEventName)
	factory, ok := c.registry[name]
	if !ok {
		return nil, name, fmt.Errorf("no factory for %q", name)
	}
	ev := factory()
	if err := json.Unmarshal(msg.Payload, ev); err != nil {
		return nil, name, err
	}
	return ev, name, nil
}

func newMemory(t *testing.T, opts ...outbox.MemoryOption) *outbox.Memory {
	t.Helper()
	m, err := outbox.NewMemory(outbox.MemoryConfig{Codec: newJSONCodec()}, opts...)
	if err != nil {
		t.Fatalf("NewMemory: %v", err)
	}
	return m
}

// --- Stage ----------------------------------------------------------

func TestStage_EmptyEventsIsNoOp(t *testing.T) {
	m := newMemory(t)
	if err := m.Stage(context.Background(), "topic"); err != nil {
		t.Fatalf("Stage(empty): %v", err)
	}
	if got := m.Size(); got != 0 {
		t.Fatalf("Size = %d, want 0", got)
	}
}

func TestStage_OneEventOneRecord(t *testing.T) {
	m := newMemory(t)
	ev := makeEvent("evt-1", "order.placed.v1", "agg-1", "Order", "body-1")
	if err := m.Stage(context.Background(), "orders", ev); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	recs, err := m.Fetch(context.Background(), 10)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("Fetch returned %d records, want 1", len(recs))
	}
	r := recs[0]
	if r.EventID != "evt-1" || r.Topic != "orders" || r.EventName != "order.placed.v1" {
		t.Fatalf("record fields off: %+v", r)
	}
}

// TestStage_CanonicalFieldsComeFromDomainEvent codifies decision #23 —
// even when the codec is configured to drop all metadata, the canonical
// EventName / AggregateID / AggregateType fields are still populated
// from the DomainEvent methods.
func TestStage_CanonicalFieldsComeFromDomainEvent(t *testing.T) {
	codec := newJSONCodec()
	codec.headerMode = "none"
	m, err := outbox.NewMemory(outbox.MemoryConfig{Codec: codec})
	if err != nil {
		t.Fatalf("NewMemory: %v", err)
	}
	ev := makeEvent("evt-2", "order.shipped.v1", "agg-9", "Order", "body")
	if err := m.Stage(context.Background(), "orders", ev); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	recs, _ := m.Fetch(context.Background(), 10)
	if recs[0].EventName != "order.shipped.v1" {
		t.Errorf("EventName = %q, want order.shipped.v1", recs[0].EventName)
	}
	if recs[0].AggregateID != "agg-9" {
		t.Errorf("AggregateID = %q, want agg-9", recs[0].AggregateID)
	}
	if recs[0].AggregateType != "Order" {
		t.Errorf("AggregateType = %q, want Order", recs[0].AggregateType)
	}
}

func TestStage_VariadicProducesOneRecordPerEvent_SharedTimestamps(t *testing.T) {
	fixed := time.Unix(1_700_000_000, 0).UTC()
	m := newMemory(t, outbox.WithClock(func() time.Time { return fixed }))

	evs := []domain.DomainEvent{
		makeEvent("evt-a", "n", "agg", "T", "a"),
		makeEvent("evt-b", "n", "agg", "T", "b"),
		makeEvent("evt-c", "n", "agg", "T", "c"),
	}
	if err := m.Stage(context.Background(), "orders", evs...); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	recs, _ := m.Fetch(context.Background(), 10)
	if len(recs) != 3 {
		t.Fatalf("got %d records, want 3", len(recs))
	}
	seenIDs := map[string]bool{}
	for _, r := range recs {
		if !r.CreatedAt.Equal(fixed) || !r.AvailableAt.Equal(fixed) {
			t.Errorf("record %s timestamps off: created=%v available=%v", r.EventID, r.CreatedAt, r.AvailableAt)
		}
		if r.Topic != "orders" {
			t.Errorf("topic = %q", r.Topic)
		}
		if seenIDs[r.ID] {
			t.Errorf("duplicate record ID %q", r.ID)
		}
		seenIDs[r.ID] = true
	}
}

func TestStage_AllOrNothingOnMarshalFailure(t *testing.T) {
	codec := newJSONCodec()
	codec.failOn = "evt-bad"
	m, err := outbox.NewMemory(outbox.MemoryConfig{Codec: codec})
	if err != nil {
		t.Fatalf("NewMemory: %v", err)
	}
	evs := []domain.DomainEvent{
		makeEvent("evt-1", "n", "agg", "T", ""),
		makeEvent("evt-bad", "n", "agg", "T", ""),
		makeEvent("evt-2", "n", "agg", "T", ""),
	}
	if err := m.Stage(context.Background(), "topic", evs...); err == nil {
		t.Fatal("Stage with bad marshal: want error, got nil")
	}
	if got := m.Size(); got != 0 {
		t.Fatalf("Size after partial failure = %d, want 0 (all-or-nothing)", got)
	}
}

func TestStage_ReturnsErrOutboxFullWhenCapExceeded(t *testing.T) {
	m := newMemory(t, outbox.WithMaxSize(2))
	evs := []domain.DomainEvent{
		makeEvent("e1", "n", "a", "T", ""),
		makeEvent("e2", "n", "a", "T", ""),
		makeEvent("e3", "n", "a", "T", ""),
	}
	if err := m.Stage(context.Background(), "topic", evs...); !errors.Is(err, outbox.ErrOutboxFull) {
		t.Fatalf("Stage err = %v, want ErrOutboxFull", err)
	}
	if got := m.Size(); got != 0 {
		t.Fatalf("Size after ErrOutboxFull = %d, want 0 (atomic reject)", got)
	}
}

// --- Fetch / MarkSent / MarkFailed ---------------------------------

func TestFetch_ReturnsDueRecordsInOrder(t *testing.T) {
	t0 := time.Unix(0, 0).UTC()
	now := t0
	m := newMemory(t, outbox.WithClock(func() time.Time { return now }))

	// All staged at t0.
	for _, id := range []string{"evt-c", "evt-a", "evt-b"} {
		if err := m.Stage(context.Background(), "topic", makeEvent(id, "n", "a", "T", "")); err != nil {
			t.Fatalf("Stage %s: %v", id, err)
		}
	}
	recs, _ := m.Fetch(context.Background(), 10)
	if len(recs) != 3 {
		t.Fatalf("got %d records", len(recs))
	}
	// Equal AvailableAt → ordered by ID. Default generator gives
	// mem-outbox-1, mem-outbox-2, mem-outbox-3 in Stage order, so
	// stable sort preserves stage order.
	for i := 1; i < len(recs); i++ {
		if recs[i-1].ID >= recs[i].ID {
			t.Fatalf("Fetch not sorted by ID: %s before %s", recs[i-1].ID, recs[i].ID)
		}
	}
}

func TestFetch_RespectsLimit(t *testing.T) {
	m := newMemory(t)
	for i := 0; i < 5; i++ {
		_ = m.Stage(context.Background(), "topic", makeEvent(fmt.Sprintf("e-%d", i), "n", "a", "T", ""))
	}
	recs, _ := m.Fetch(context.Background(), 2)
	if len(recs) != 2 {
		t.Fatalf("Fetch(2) returned %d", len(recs))
	}
}

func TestFetch_SkipsRecordsNotYetDue(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	m := newMemory(t, outbox.WithClock(func() time.Time { return now }))
	_ = m.Stage(context.Background(), "topic", makeEvent("e1", "n", "a", "T", ""))

	recs, _ := m.Fetch(context.Background(), 10)
	if len(recs) != 1 {
		t.Fatalf("initial Fetch: %d records", len(recs))
	}
	// Defer it past now.
	future := now.Add(10 * time.Second)
	if err := m.MarkFailed(context.Background(), recs[0].ID, "boom", future); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	got, _ := m.Fetch(context.Background(), 10)
	if len(got) != 0 {
		t.Fatalf("Fetch returned %d records, want 0 (not yet due)", len(got))
	}
	// Advance the clock past the deferred time and confirm visibility.
	now = future.Add(time.Nanosecond)
	got, _ = m.Fetch(context.Background(), 10)
	if len(got) != 1 {
		t.Fatalf("after due, Fetch returned %d, want 1", len(got))
	}
}

func TestFetch_DoesNotReturnDLQRecords(t *testing.T) {
	m := newMemory(t)
	_ = m.Stage(context.Background(), "topic", makeEvent("e1", "n", "a", "T", ""))
	recs, _ := m.Fetch(context.Background(), 10)
	if err := m.Terminate(context.Background(), recs[0].ID, "poison"); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	if got := m.Size(); got != 0 {
		t.Fatalf("active Size = %d, want 0", got)
	}
	again, _ := m.Fetch(context.Background(), 10)
	if len(again) != 0 {
		t.Fatalf("Fetch returned %d records after Terminate, want 0", len(again))
	}
}

func TestMarkSent_RemovesRecord(t *testing.T) {
	m := newMemory(t)
	_ = m.Stage(context.Background(), "topic", makeEvent("e1", "n", "a", "T", ""))
	recs, _ := m.Fetch(context.Background(), 10)
	if err := m.MarkSent(context.Background(), recs[0].ID); err != nil {
		t.Fatalf("MarkSent: %v", err)
	}
	if got := m.Size(); got != 0 {
		t.Fatalf("Size after MarkSent = %d, want 0", got)
	}
}

func TestMarkFailed_BumpsAttemptsAndDefersAvailableAt(t *testing.T) {
	m := newMemory(t)
	_ = m.Stage(context.Background(), "topic", makeEvent("e1", "n", "a", "T", ""))
	recs, _ := m.Fetch(context.Background(), 10)
	next := time.Unix(0, 0).Add(time.Hour)
	if err := m.MarkFailed(context.Background(), recs[0].ID, "boom", next); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	// Re-read via Fetch after advancing clock past next.
	clock := next.Add(time.Nanosecond)
	m2 := m // same store; switch to a clock-controlled Memory would
	_ = clock
	_ = m2
	// We can't switch clocks mid-flight on the existing Memory, so
	// inspect via a fresh Fetch that ignores due-by-now (which is
	// past next here since we used Unix epoch + 1h, well in the past).
	got, _ := m.Fetch(context.Background(), 10)
	if len(got) != 1 {
		t.Fatalf("expected 1 due record, got %d", len(got))
	}
	if got[0].Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", got[0].Attempts)
	}
	if !got[0].AvailableAt.Equal(next) {
		t.Errorf("AvailableAt = %v, want %v", got[0].AvailableAt, next)
	}
}

// --- Terminate / DLQ -----------------------------------------------

func TestTerminate_MovesActiveRecordToDLQ(t *testing.T) {
	fixed := time.Unix(2000, 0).UTC()
	m := newMemory(t, outbox.WithClock(func() time.Time { return fixed }))
	_ = m.Stage(context.Background(), "topic", makeEvent("e1", "n", "a", "T", ""))
	recs, _ := m.Fetch(context.Background(), 10)

	if err := m.Terminate(context.Background(), recs[0].ID, "poison"); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	dlq := m.DeadLettered()
	if len(dlq) != 1 {
		t.Fatalf("DLQ size = %d, want 1", len(dlq))
	}
	entry := dlq[recs[0].ID]
	if entry.Reason != "poison" {
		t.Errorf("Reason = %q, want poison", entry.Reason)
	}
	if !entry.FailedAt.Equal(fixed) {
		t.Errorf("FailedAt = %v, want %v", entry.FailedAt, fixed)
	}
	if entry.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1 (rec.Attempts(0) + 1)", entry.Attempts)
	}
}

func TestTerminate_PreservesTerminalAttemptCount(t *testing.T) {
	m := newMemory(t)
	_ = m.Stage(context.Background(), "topic", makeEvent("e1", "n", "a", "T", ""))
	recs, _ := m.Fetch(context.Background(), 10)
	for i := 0; i < 3; i++ {
		_ = m.MarkFailed(context.Background(), recs[0].ID, "x", time.Unix(0, 0))
	}
	// Refresh after 3 failed attempts; rec.Attempts == 3.
	recs, _ = m.Fetch(context.Background(), 10)
	if recs[0].Attempts != 3 {
		t.Fatalf("setup wrong: Attempts = %d, want 3", recs[0].Attempts)
	}
	_ = m.Terminate(context.Background(), recs[0].ID, "exhausted")
	dlq := m.DeadLettered()
	if dlq[recs[0].ID].Attempts != 4 {
		t.Errorf("DLQ Attempts = %d, want 4 (3 survivable + the terminating attempt)", dlq[recs[0].ID].Attempts)
	}
}

func TestDeadLettered_IsReadOnlySnapshot(t *testing.T) {
	m := newMemory(t)
	_ = m.Stage(context.Background(), "topic", makeEvent("e1", "n", "a", "T", ""))
	recs, _ := m.Fetch(context.Background(), 10)
	_ = m.Terminate(context.Background(), recs[0].ID, "p")

	dlq := m.DeadLettered()
	// Mutate the returned map and its Record.Headers / Payload.
	delete(dlq, recs[0].ID)
	for _, v := range dlq {
		v.Record.Headers["x"] = "y"
		if len(v.Record.Payload) > 0 {
			v.Record.Payload[0] = 0xff
		}
	}
	again := m.DeadLettered()
	if len(again) != 1 {
		t.Fatalf("snapshot mutation leaked: got %d, want 1", len(again))
	}
	for _, v := range again {
		if v.Record.Headers["x"] == "y" {
			t.Errorf("Headers mutation leaked into store")
		}
		if len(v.Record.Payload) > 0 && v.Record.Payload[0] == 0xff {
			t.Errorf("Payload mutation leaked into store")
		}
	}
}

// --- Defensive copies on Fetch -------------------------------------

func TestFetch_ReturnsDefensiveCopies(t *testing.T) {
	m := newMemory(t)
	_ = m.Stage(context.Background(), "topic", makeEvent("e1", "n", "a", "T", "body"))

	first, _ := m.Fetch(context.Background(), 10)
	if len(first) != 1 {
		t.Fatalf("setup: got %d records", len(first))
	}
	first[0].Headers["evil"] = "yes"
	if len(first[0].Payload) > 0 {
		first[0].Payload[0] = 0xff
	}

	second, _ := m.Fetch(context.Background(), 10)
	if _, ok := second[0].Headers["evil"]; ok {
		t.Errorf("Headers mutation leaked across Fetch")
	}
	if len(second[0].Payload) > 0 && second[0].Payload[0] == 0xff {
		t.Errorf("Payload mutation leaked across Fetch")
	}
}

// --- ID generation -------------------------------------------------

func TestStage_ConcurrentCallsProduceUniqueIDs(t *testing.T) {
	m := newMemory(t)
	const goroutines = 32
	const perG = 16

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				ev := makeEvent(fmt.Sprintf("g%d-i%d", g, i), "n", "a", "T", "")
				if err := m.Stage(context.Background(), "topic", ev); err != nil {
					t.Errorf("Stage: %v", err)
					return
				}
			}
		}(g)
	}
	wg.Wait()

	recs, _ := m.Fetch(context.Background(), goroutines*perG+1)
	if len(recs) != goroutines*perG {
		t.Fatalf("Fetch len = %d, want %d", len(recs), goroutines*perG)
	}
	seen := make(map[string]bool, len(recs))
	for _, r := range recs {
		if seen[r.ID] {
			t.Fatalf("duplicate OutboxRecord.ID under concurrent Stage: %s", r.ID)
		}
		seen[r.ID] = true
	}
}

func TestStage_DefaultIDGeneratorIsMonotonic(t *testing.T) {
	m := newMemory(t)
	for i := 0; i < 3; i++ {
		_ = m.Stage(context.Background(), "topic", makeEvent(fmt.Sprintf("e-%d", i), "n", "a", "T", ""))
	}
	recs, _ := m.Fetch(context.Background(), 10)
	for i := 1; i < len(recs); i++ {
		if recs[i-1].ID >= recs[i].ID {
			t.Fatalf("default generator not monotonic: %s before %s", recs[i-1].ID, recs[i].ID)
		}
	}
}
