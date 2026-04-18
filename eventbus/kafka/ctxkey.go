package kafka

import "context"

// ctxKey is unexported to prevent collisions with caller-defined keys.
type ctxKey int

const (
	keyTraceID ctxKey = iota
	keyCausationID
	keyCorrelationID
)

// WithTraceID returns a context carrying the supplied trace id. The codec
// promotes it to the eventbus.HeaderTraceID header on Marshal.
func WithTraceID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, keyTraceID, id)
}

// WithCausationID returns a context carrying the id of the message that
// caused the event being published.
func WithCausationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, keyCausationID, id)
}

// WithCorrelationID returns a context carrying a workflow-level correlation
// id that links many events under the same logical operation.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, keyCorrelationID, id)
}

// TraceIDFrom returns the trace id stored on ctx and a presence flag.
func TraceIDFrom(ctx context.Context) (string, bool) { return stringValue(ctx, keyTraceID) }

// CausationIDFrom returns the causation id stored on ctx.
func CausationIDFrom(ctx context.Context) (string, bool) {
	return stringValue(ctx, keyCausationID)
}

// CorrelationIDFrom returns the correlation id stored on ctx.
func CorrelationIDFrom(ctx context.Context) (string, bool) {
	return stringValue(ctx, keyCorrelationID)
}

func stringValue(ctx context.Context, k ctxKey) (string, bool) {
	v, ok := ctx.Value(k).(string)
	if !ok || v == "" {
		return "", false
	}
	return v, true
}
