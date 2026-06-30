package redisratelimit

import "testing"

// TestDefaultKeyPrefixNonEmpty: a non-empty default namespace is a design
// requirement (spec §6) — the adapter's keys must not sit at the bare top level
// of a shared Redis.
func TestDefaultKeyPrefixNonEmpty(t *testing.T) {
	if defaultKeyPrefix == "" {
		t.Fatal("defaultKeyPrefix is empty; the adapter must namespace its keys by default")
	}
}

// TestWithKeyPrefixOverrides: WithKeyPrefix replaces the default in the config.
func TestWithKeyPrefixOverrides(t *testing.T) {
	cfg := config{keyPrefix: defaultKeyPrefix}
	WithKeyPrefix("tenant-x")(&cfg)
	if cfg.keyPrefix != "tenant-x" {
		t.Fatalf("WithKeyPrefix: keyPrefix = %q, want %q", cfg.keyPrefix, "tenant-x")
	}
}
