package rediscache

import (
	"errors"
	"testing"

	"github.com/redis/go-redis/v9"
)

func TestNewRejectsNilInterface(t *testing.T) {
	if _, err := New(nil); !errors.Is(err, ErrNilClient) {
		t.Fatalf("New(nil) err = %v, want ErrNilClient", err)
	}
}

func TestNewRejectsTypedNilClient(t *testing.T) {
	var c *redis.Client // typed nil
	if _, err := New(c); !errors.Is(err, ErrNilClient) {
		t.Fatalf("New(typed-nil *redis.Client) err = %v, want ErrNilClient", err)
	}
}

func TestNewAcceptsRealClient(t *testing.T) {
	// A non-nil client need not reach a server for construction to succeed.
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:0"})
	t.Cleanup(func() { _ = client.Close() })
	c, err := New(client, WithKeyPrefix("t"))
	if err != nil {
		t.Fatalf("New(real client) err = %v, want nil", err)
	}
	if c == nil {
		t.Fatal("New returned nil Cache")
	}
}
