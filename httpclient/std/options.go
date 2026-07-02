package stdhttp

import (
	"net/http"
	"time"

	"github.com/slam0504/go-ddd-core/pkg/errorsx"
	"go.opentelemetry.io/otel/trace"
)

// Constructor sentinels. errorsx-coded CodeInvalidArgument (mirrors
// rediscache.ErrNilClient) so errorsx.CodeOf never reports CodeUnknown.
var (
	// ErrNegativeTimeout is returned by New when WithTimeout got d < 0.
	ErrNegativeTimeout = errorsx.New(errorsx.CodeInvalidArgument, "stdhttp: negative timeout")
	// ErrNilTransport is returned by New when WithTransport got nil.
	ErrNilTransport = errorsx.New(errorsx.CodeInvalidArgument, "stdhttp: nil transport")
	// ErrNilTracerProvider is returned by New when WithTracing got nil.
	ErrNilTracerProvider = errorsx.New(errorsx.CodeInvalidArgument, "stdhttp: nil tracer provider")
)

type config struct {
	timeout        time.Duration
	transport      http.RoundTripper
	transportSet   bool
	tracerProvider trace.TracerProvider
	tracingSet     bool
}

// Option configures a Client at construction time.
type Option func(*config)

// WithTimeout sets the client-level timeout. d == 0 (the default) means no
// client-level timeout, identical to net/http.Client; d < 0 is rejected by
// New. Production callers are strongly encouraged to set a timeout — the
// adapter deliberately does not choose one for them.
func WithTimeout(d time.Duration) Option {
	return func(c *config) { c.timeout = d }
}

// WithTransport replaces the base http.RoundTripper (default
// http.DefaultTransport). nil is rejected by New.
func WithTransport(rt http.RoundTripper) Option {
	return func(c *config) { c.transport = rt; c.transportSet = true }
}

// WithTracing wraps the base transport with otelhttp using the EXPLICITLY
// injected provider. There is deliberately no fallback to the otel global
// provider — this repo wires observability explicitly (observability/otel
// registers no globals). nil is rejected by New.
func WithTracing(tp trace.TracerProvider) Option {
	return func(c *config) { c.tracerProvider = tp; c.tracingSet = true }
}
