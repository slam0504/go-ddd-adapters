package redisidempotency

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/slam0504/go-ddd-core/pkg/errorsx"
	"github.com/slam0504/go-ddd-core/ports/idempotency"
)

func newValidatingStore(t *testing.T) *Store {
	t.Helper()
	// Client is never dialed: every assertion below returns before a command.
	c := redis.NewClient(&redis.Options{Addr: "127.0.0.1:0"})
	s, err := New(c)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestNewRejectsNilClient(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("New(nil interface): want error, got nil")
	}
	// Typed-nil concrete clients: an interface holding a nil *redis.Client is
	// itself non-nil, so the plain == nil check above does NOT catch these.
	if _, err := New((*redis.Client)(nil)); err == nil {
		t.Fatal("New((*redis.Client)(nil)): want error, got nil")
	}
	if _, err := New((*redis.ClusterClient)(nil)); err == nil {
		t.Fatal("New((*redis.ClusterClient)(nil)): want error, got nil")
	}
	if _, err := New((*redis.Ring)(nil)); err == nil {
		t.Fatal("New((*redis.Ring)(nil)): want error, got nil")
	}
}

func TestNewRejectsSubMillisecondDurations(t *testing.T) {
	c := redis.NewClient(&redis.Options{Addr: "127.0.0.1:0"})
	// Non-positive is rejected.
	if _, err := New(c, WithLeaseTTL(0)); err == nil {
		t.Fatal("WithLeaseTTL(0): want error")
	}
	if _, err := New(c, WithRetention(-1)); err == nil {
		t.Fatal("WithRetention(-1): want error")
	}
	// Positive but sub-millisecond is ALSO rejected: it would truncate to 0ms at
	// PEXPIRE, so New must fail loud rather than build a Store that errors at runtime.
	if _, err := New(c, WithLeaseTTL(500*time.Microsecond)); err == nil {
		t.Fatal("WithLeaseTTL(500µs): want error (truncates to 0ms)")
	}
	if _, err := New(c, WithRetention(999*time.Nanosecond)); err == nil {
		t.Fatal("WithRetention(999ns): want error (truncates to 0ms)")
	}
	// Exactly 1ms is the accepted floor.
	if _, err := New(c, WithLeaseTTL(time.Millisecond), WithRetention(time.Millisecond)); err != nil {
		t.Fatalf("WithLeaseTTL/Retention(1ms): want ok, got %v", err)
	}
}

func TestNewRejectsEmptyKeyPrefix(t *testing.T) {
	c := redis.NewClient(&redis.Options{Addr: "127.0.0.1:0"})
	if _, err := New(c, WithKeyPrefix("")); err == nil {
		t.Fatal("WithKeyPrefix(\"\"): want error")
	}
}

func TestBeginRejectsMalformedInput(t *testing.T) {
	s := newValidatingStore(t)
	ctx := context.Background()
	for _, tc := range []struct{ scope, key, fp string }{
		{"", "k", "f"}, {"sc", "", "f"}, {"sc", "k", ""},
	} {
		_, err := s.Begin(ctx, tc.scope, tc.key, tc.fp)
		if got := errorsx.CodeOf(err); got != errorsx.CodeInvalidArgument {
			t.Fatalf("Begin(%q,%q,%q): code=%v want CodeInvalidArgument", tc.scope, tc.key, tc.fp, got)
		}
	}
}

func TestFinishCancelRejectMalformedReservation(t *testing.T) {
	s := newValidatingStore(t)
	ctx := context.Background()
	for _, r := range []idempotency.Reservation{
		{}, {Scope: "sc", Key: "k"}, {Key: "k", LeaseToken: "t"}, {Scope: "sc", LeaseToken: "t"},
	} {
		if got := errorsx.CodeOf(s.Finish(ctx, r, []byte("x"))); got != errorsx.CodeInvalidArgument {
			t.Fatalf("Finish(%+v): code=%v want CodeInvalidArgument", r, got)
		}
		if got := errorsx.CodeOf(s.Cancel(ctx, r)); got != errorsx.CodeInvalidArgument {
			t.Fatalf("Cancel(%+v): code=%v want CodeInvalidArgument", r, got)
		}
	}
}

func TestCompositeKeyTupleSeparation(t *testing.T) {
	s := newValidatingStore(t)
	a := s.compositeKey("tenant:op", "k")
	b := s.compositeKey("tenant", "op:k")
	if a == b {
		t.Fatalf("composite keys must differ: %q == %q", a, b)
	}
}
