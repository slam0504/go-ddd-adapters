package inbox_test

import (
	"context"
	"testing"
	"time"

	"github.com/slam0504/go-ddd-adapters/eventbus/inbox"
	"github.com/slam0504/go-ddd-core/eventbus"
)

func key(consumer, eventID string) eventbus.InboxKey {
	return eventbus.InboxKey{Consumer: consumer, EventID: eventID}
}

func TestSeen_FalseBeforeRecord(t *testing.T) {
	in := inbox.NewMemory()
	seen, err := in.Seen(context.Background(), key("c1", "evt-1"))
	if err != nil {
		t.Fatalf("Seen: %v", err)
	}
	if seen {
		t.Fatalf("Seen should be false before Record")
	}
}

func TestRecord_ThenSeenIsTrue(t *testing.T) {
	in := inbox.NewMemory()
	if err := in.Record(context.Background(), key("c1", "evt-1")); err != nil {
		t.Fatalf("Record: %v", err)
	}
	seen, _ := in.Seen(context.Background(), key("c1", "evt-1"))
	if !seen {
		t.Fatalf("Seen should be true after Record")
	}
}

func TestRecord_IsIdempotent(t *testing.T) {
	in := inbox.NewMemory()
	for i := 0; i < 5; i++ {
		if err := in.Record(context.Background(), key("c1", "evt-1")); err != nil {
			t.Fatalf("Record #%d: %v", i, err)
		}
	}
	if got := in.Size(); got != 1 {
		t.Fatalf("Size = %d, want 1", got)
	}
}

// TestConsumerScopeIsolation guards the core invariant of the scoped Inbox
// design: two consumers seeing the same event id must not share state. If
// they did, one consumer's Record would let another consumer skip the
// handler — silent message loss.
func TestConsumerScopeIsolation(t *testing.T) {
	in := inbox.NewMemory()
	ctx := context.Background()

	if err := in.Record(ctx, key("projector", "evt-1")); err != nil {
		t.Fatalf("Record projector: %v", err)
	}

	seen, _ := in.Seen(ctx, key("reactor", "evt-1"))
	if seen {
		t.Fatalf("reactor must not see evt-1 already recorded by projector")
	}

	if err := in.Record(ctx, key("reactor", "evt-1")); err != nil {
		t.Fatalf("Record reactor: %v", err)
	}
	if got := in.Size(); got != 2 {
		t.Fatalf("Size = %d, want 2 (one per consumer)", got)
	}
}

func TestEviction_TriggersOnceMaxSizeExceeded(t *testing.T) {
	tick := time.Unix(0, 0)
	clock := func() time.Time {
		tick = tick.Add(time.Second)
		return tick
	}
	in := inbox.NewMemory(inbox.WithMaxSize(4), inbox.WithClock(clock))

	for i, id := range []string{"a", "b", "c", "d", "e"} {
		if err := in.Record(context.Background(), key("c1", id)); err != nil {
			t.Fatalf("Record #%d: %v", i, err)
		}
	}
	// After insert of "e" the map exceeded 4 → oldest half (2 entries) evicted.
	if got := in.Size(); got != 3 {
		t.Fatalf("Size after eviction = %d, want 3", got)
	}
	// "a" and "b" are oldest and should be gone; "c", "d", "e" remain.
	for _, id := range []string{"a", "b"} {
		if seen, _ := in.Seen(context.Background(), key("c1", id)); seen {
			t.Fatalf("expected %q evicted", id)
		}
	}
	for _, id := range []string{"c", "d", "e"} {
		if seen, _ := in.Seen(context.Background(), key("c1", id)); !seen {
			t.Fatalf("expected %q retained", id)
		}
	}
}

func TestUnboundedByDefault(t *testing.T) {
	in := inbox.NewMemory()
	for i := 0; i < 1000; i++ {
		_ = in.Record(context.Background(), key("c1", idString(i)))
	}
	if got := in.Size(); got != 1000 {
		t.Fatalf("default should not evict, got Size=%d", got)
	}
}

func TestTTL_SeenReportsFalseAfterExpiry(t *testing.T) {
	now := time.Unix(0, 0)
	clock := func() time.Time { return now }
	in := inbox.NewMemory(inbox.WithTTL(10*time.Second), inbox.WithClock(clock))
	ctx := context.Background()
	k := key("c1", "evt-1")

	if err := in.Record(ctx, k); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if seen, _ := in.Seen(ctx, k); !seen {
		t.Fatalf("expected Seen=true immediately after Record")
	}

	// Advance past TTL boundary.
	now = now.Add(11 * time.Second)
	if seen, _ := in.Seen(ctx, k); seen {
		t.Fatalf("expected Seen=false after TTL expiry")
	}

	// After expiry, Record overwrites with a fresh timestamp.
	if err := in.Record(ctx, k); err != nil {
		t.Fatalf("Record after expiry: %v", err)
	}
	if seen, _ := in.Seen(ctx, k); !seen {
		t.Fatalf("expected Seen=true after re-Record post-expiry")
	}
}

func idString(i int) string {
	return "evt-" + intToStr(i)
}

func intToStr(n int) string {
	if n == 0 {
		return "0"
	}
	var s []byte
	for n > 0 {
		s = append([]byte{byte('0' + n%10)}, s...)
		n /= 10
	}
	return string(s)
}
