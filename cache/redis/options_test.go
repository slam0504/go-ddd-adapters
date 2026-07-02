package rediscache

import "testing"

func TestDefaultKeyPrefix(t *testing.T) {
	cfg := config{keyPrefix: defaultKeyPrefix}
	if cfg.keyPrefix != "cache" {
		t.Fatalf("default keyPrefix = %q, want %q", cfg.keyPrefix, "cache")
	}
}

func TestWithKeyPrefixOverrides(t *testing.T) {
	cfg := config{keyPrefix: defaultKeyPrefix}
	WithKeyPrefix("sessions")(&cfg)
	if cfg.keyPrefix != "sessions" {
		t.Fatalf("keyPrefix after WithKeyPrefix = %q, want %q", cfg.keyPrefix, "sessions")
	}
}
