package redisratelimit

import "testing"

// TestEncodeInjective proves the length prefix makes (keyPrefix, key) injective.
// A naive prefix+":"+key collapses ("a","b:c") and ("a:b","c") both to "a:b:c".
// Asserting "two DIFFERENT-prefix limiters get different keys" would NOT catch
// this — distinct prefixes are already distinct strings and never exercise the
// flatten bug. We must assert the COLLIDING tuples map to DISTINCT keys.
func TestEncodeInjective(t *testing.T) {
	got1 := encode("a", "b:c")
	got2 := encode("a:b", "c")
	if got1 == got2 {
		t.Fatalf("encode collision: encode(%q,%q)=%q == encode(%q,%q)=%q", "a", "b:c", got1, "a:b", "c", got2)
	}
	if want := "1:a:b:c"; got1 != want {
		t.Fatalf("encode(\"a\",\"b:c\") = %q, want %q", got1, want)
	}
	if want := "3:a:b:c"; got2 != want {
		t.Fatalf("encode(\"a:b\",\"c\") = %q, want %q", got2, want)
	}
}

// TestEncodeEmptyPrefixInjective: an empty keyPrefix still encodes injectively.
func TestEncodeEmptyPrefixInjective(t *testing.T) {
	if got, want := encode("", "k"), "0::k"; got != want {
		t.Fatalf("encode(\"\",\"k\") = %q, want %q", got, want)
	}
}
