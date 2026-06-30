package redisratelimit

import (
	"errors"
	"testing"
	"time"

	"github.com/go-redis/redis_rate/v10"
	"github.com/redis/go-redis/v9"
)

var validLimit = redis_rate.Limit{Rate: 1, Burst: 1, Period: time.Hour}

func TestNewRejectsNilClient(t *testing.T) {
	if _, err := New(nil, validLimit); !errors.Is(err, ErrNilClient) {
		t.Fatalf("New(nil): err = %v, want ErrNilClient", err)
	}
	if _, err := New((*redis.Client)(nil), validLimit); !errors.Is(err, ErrNilClient) {
		t.Fatalf("New((*redis.Client)(nil)): err = %v, want ErrNilClient", err)
	}
	if _, err := New((*redis.ClusterClient)(nil), validLimit); !errors.Is(err, ErrNilClient) {
		t.Fatalf("New((*redis.ClusterClient)(nil)): err = %v, want ErrNilClient", err)
	}
	if _, err := New((*redis.Ring)(nil), validLimit); !errors.Is(err, ErrNilClient) {
		t.Fatalf("New((*redis.Ring)(nil)): err = %v, want ErrNilClient", err)
	}
}

func TestNewRejectsInvalidLimit(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:0"})
	t.Cleanup(func() { _ = client.Close() })
	for _, tc := range []struct {
		name  string
		limit redis_rate.Limit
	}{
		{"zero value", redis_rate.Limit{}},
		{"zero rate", redis_rate.Limit{Rate: 0, Burst: 1, Period: time.Hour}},
		{"zero burst", redis_rate.Limit{Rate: 1, Burst: 0, Period: time.Hour}},
		{"zero period", redis_rate.Limit{Rate: 1, Burst: 1, Period: 0}},
		{"negative rate", redis_rate.Limit{Rate: -1, Burst: 1, Period: time.Hour}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(client, tc.limit); !errors.Is(err, ErrInvalidLimit) {
				t.Fatalf("New(%+v): err = %v, want ErrInvalidLimit", tc.limit, err)
			}
		})
	}
}

func TestNewValid(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:0"})
	t.Cleanup(func() { _ = client.Close() })
	l, err := New(client, validLimit, WithKeyPrefix("custom"))
	if err != nil {
		t.Fatalf("New valid: %v", err)
	}
	if l == nil {
		t.Fatal("New valid returned nil Limiter")
	}
	if l.keyPrefix != "custom" {
		t.Fatalf("keyPrefix = %q, want %q", l.keyPrefix, "custom")
	}
}
