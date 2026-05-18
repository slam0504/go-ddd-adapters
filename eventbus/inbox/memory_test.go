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

// TestTTL_BehaviorTable nails down the Seen() boundary semantics.
// The implementation uses a strict greater-than (now()-seenAt > ttl), so an
// entry at exactly ttl is still fresh and one nanosecond past is expired.
// Pinning this in a table protects against accidental >= or rounding drift.
func TestTTL_BehaviorTable(t *testing.T) {
	cases := []struct {
		name      string
		ttl       time.Duration
		ageAtRead time.Duration
		wantSeen  bool
	}{
		{"ttl_disabled_when_zero", 0, 100 * time.Hour, true},
		{"fresh_within_ttl", 10 * time.Second, 5 * time.Second, true},
		{"exact_boundary_still_fresh", 10 * time.Second, 10 * time.Second, true},
		{"one_nanosecond_past_ttl_expired", 10 * time.Second, 10*time.Second + time.Nanosecond, false},
		{"long_past_ttl_expired", 10 * time.Second, 30 * time.Second, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Unix(0, 0)
			clock := func() time.Time { return now }
			in := inbox.NewMemory(inbox.WithTTL(tc.ttl), inbox.WithClock(clock))
			ctx := context.Background()
			k := key("c1", "evt-1")
			if err := in.Record(ctx, k); err != nil {
				t.Fatalf("Record: %v", err)
			}
			now = now.Add(tc.ageAtRead)
			got, err := in.Seen(ctx, k)
			if err != nil {
				t.Fatalf("Seen: %v", err)
			}
			if got != tc.wantSeen {
				t.Fatalf("ttl=%v age=%v: Seen=%v, want %v",
					tc.ttl, tc.ageAtRead, got, tc.wantSeen)
			}
		})
	}
}

// TestTTL_RecordOnExpiredOverwritesInPlace ensures the expired-overwrite
// path in Record reuses the existing map slot rather than allocating a new
// one — Size must stay at 1 even though the entry was effectively replaced.
func TestTTL_RecordOnExpiredOverwritesInPlace(t *testing.T) {
	now := time.Unix(0, 0)
	clock := func() time.Time { return now }
	in := inbox.NewMemory(inbox.WithTTL(5*time.Second), inbox.WithClock(clock))
	ctx := context.Background()
	k := key("c1", "evt-1")

	if err := in.Record(ctx, k); err != nil {
		t.Fatalf("first Record: %v", err)
	}
	if got := in.Size(); got != 1 {
		t.Fatalf("after first Record Size=%d, want 1", got)
	}

	now = now.Add(10 * time.Second) // past TTL
	if err := in.Record(ctx, k); err != nil {
		t.Fatalf("Record after expiry: %v", err)
	}
	if got := in.Size(); got != 1 {
		t.Fatalf("Record on expired key should overwrite in place, Size=%d, want 1", got)
	}
}

// TestTTL_AndMaxSizeCompose verifies the two eviction rules co-exist:
// MaxSize bounds the map at write time, TTL filters at read time. Neither
// disables the other.
func TestTTL_AndMaxSizeCompose(t *testing.T) {
	now := time.Unix(0, 0)
	clock := func() time.Time { return now }
	in := inbox.NewMemory(
		inbox.WithTTL(5*time.Second),
		inbox.WithMaxSize(4),
		inbox.WithClock(clock),
	)
	ctx := context.Background()

	// Record a..d at t=0s (all tied on timestamp).
	for _, id := range []string{"a", "b", "c", "d"} {
		if err := in.Record(ctx, key("c1", id)); err != nil {
			t.Fatalf("Record %q: %v", id, err)
		}
	}

	// Jump past TTL, then insert "e" at t=10s — map hits 5, evictOldestHalf
	// drops two entries. Surviving a-d are still at t=0s, so they're past
	// TTL by the time we read.
	now = now.Add(10 * time.Second)
	if err := in.Record(ctx, key("c1", "e")); err != nil {
		t.Fatalf("Record e: %v", err)
	}
	if got := in.Size(); got != 3 {
		t.Fatalf("after MaxSize eviction Size=%d, want 3", got)
	}

	// "e" is fresh (age=0s), should be Seen.
	if seen, _ := in.Seen(ctx, key("c1", "e")); !seen {
		t.Fatalf("e at age 0s should be Seen")
	}

	// Any of a..d that survived MaxSize eviction is still past TTL, so
	// Seen must report false for all of them.
	for _, id := range []string{"a", "b", "c", "d"} {
		if seen, _ := in.Seen(ctx, key("c1", id)); seen {
			t.Fatalf("%q at age=10s with TTL=5s should be filtered out by TTL", id)
		}
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
