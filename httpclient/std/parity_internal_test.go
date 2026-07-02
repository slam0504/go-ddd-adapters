package stdhttp

import "testing"

// The headline spec invariant, pinned white-box: New() with no options must
// equal a zero-value net/http.Client — Timeout 0, Transport nil (stdlib
// resolves nil to http.DefaultTransport at request time), and NO wrapping.
// A traced-by-default or safe-default-timeout regression fails here even if
// every behavioral test still passes.
func TestNew_ZeroValueStdlibParity(t *testing.T) {
	c, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.hc.Timeout != 0 {
		t.Fatalf("default Timeout = %v, want 0 (stdlib parity)", c.hc.Timeout)
	}
	if c.hc.Transport != nil {
		t.Fatalf("default Transport = %T, want nil (stdlib parity, no wrapping)", c.hc.Transport)
	}
}

// WithTimeout(0) must remain an explicit no-timeout, not be rejected or
// remapped — pinned at the field level.
func TestWithTimeout_ZeroSetsNoTimeout(t *testing.T) {
	c, err := New(WithTimeout(0))
	if err != nil {
		t.Fatalf("New(WithTimeout(0)): %v", err)
	}
	if c.hc.Timeout != 0 {
		t.Fatalf("Timeout = %v, want 0", c.hc.Timeout)
	}
}
