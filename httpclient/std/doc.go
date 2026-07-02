// Package stdhttp is the stdlib-backed adapter for core's
// ports/httpclient contract. It wraps *net/http.Client behind
// httpclient.Client, with a Contextual() view for
// httpclient.ContextualClient.
//
// Defaults deliberately match a zero-value net/http.Client: NO
// client-level timeout, http.DefaultTransport, no tracing. This adapter
// is named "std" — its defaults must not silently diverge from the
// stdlib. Production callers are strongly encouraged to set
// WithTimeout; the adapter does not choose one for them.
//
// Tracing is opt-in via WithTracing(tp) with an EXPLICITLY injected
// trace.TracerProvider (no otel-global fallback — this repo wires
// observability explicitly). Errors from Do are stdlib passthrough
// (*url.Error etc.), never errorsx-coded: the port contract is
// net/http-shaped. Retries and circuit breaking are deliberately out of
// scope (see the 2026-07-02 design spec §8).
package stdhttp
