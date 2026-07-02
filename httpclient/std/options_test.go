package stdhttp_test

import (
	"errors"
	"testing"
	"time"

	"github.com/slam0504/go-ddd-core/pkg/errorsx"
	"go.opentelemetry.io/otel/trace/noop"

	stdhttp "github.com/slam0504/go-ddd-adapters/httpclient/std"
)

// requireInvalidArgument pins the coded invariant from the plan's Global
// Constraints: constructor sentinels are errorsx-coded CodeInvalidArgument,
// so errorsx.CodeOf never reports CodeUnknown for them.
func requireInvalidArgument(t *testing.T, err error) {
	t.Helper()
	if got := errorsx.CodeOf(err); got != errorsx.CodeInvalidArgument {
		t.Fatalf("errorsx.CodeOf(%v) = %v, want CodeInvalidArgument", err, got)
	}
}

// The zero-option client must construct — its behavior (stdlib parity) is
// asserted behaviorally in client_test.go.
func TestNew_Defaults(t *testing.T) {
	c, err := stdhttp.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c == nil {
		t.Fatal("New returned nil client")
	}
}

// d < 0 is a caller bug and must fail loud at construction, not surface as
// weird stdlib behavior later.
func TestNew_NegativeTimeoutRejected(t *testing.T) {
	_, err := stdhttp.New(stdhttp.WithTimeout(-time.Second))
	if !errors.Is(err, stdhttp.ErrNegativeTimeout) {
		t.Fatalf("err = %v, want ErrNegativeTimeout", err)
	}
	requireInvalidArgument(t, err)
}

// d == 0 explicitly means "no client-level timeout" (stdlib parity) and must
// be accepted.
func TestNew_ZeroTimeoutAccepted(t *testing.T) {
	if _, err := stdhttp.New(stdhttp.WithTimeout(0)); err != nil {
		t.Fatalf("New(WithTimeout(0)): %v", err)
	}
}

// A nil transport passed explicitly is a caller bug — fail loud instead of
// silently falling back to http.DefaultTransport.
func TestNew_NilTransportRejected(t *testing.T) {
	_, err := stdhttp.New(stdhttp.WithTransport(nil))
	if !errors.Is(err, stdhttp.ErrNilTransport) {
		t.Fatalf("err = %v, want ErrNilTransport", err)
	}
	requireInvalidArgument(t, err)
}

// Tracing is explicit-injection only; a nil provider would silently no-op via
// the otel global, which this repo's wiring convention forbids.
func TestNew_NilTracerProviderRejected(t *testing.T) {
	_, err := stdhttp.New(stdhttp.WithTracing(nil))
	if !errors.Is(err, stdhttp.ErrNilTracerProvider) {
		t.Fatalf("err = %v, want ErrNilTracerProvider", err)
	}
	requireInvalidArgument(t, err)
}

// A non-nil provider must be accepted (span emission is asserted in
// tracing_test.go).
func TestNew_TracingAccepted(t *testing.T) {
	if _, err := stdhttp.New(stdhttp.WithTracing(noop.NewTracerProvider())); err != nil {
		t.Fatalf("New(WithTracing): %v", err)
	}
}
