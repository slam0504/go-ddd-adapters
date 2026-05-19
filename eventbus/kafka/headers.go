package kafka

import (
	"context"

	"github.com/slam0504/go-ddd-core/eventbus"
)

// RestoreCoreHeaders is a header-restorer callback intended for use
// with the outbox Relay's WithHeaderRestorer option. It promotes the
// three core well-known propagation headers
// ([eventbus.HeaderTraceID], [eventbus.HeaderCausationID],
// [eventbus.HeaderCorrelationID]) from a stored OutboxRecord.Headers
// map back into ctx via this package's [WithTraceID] /
// [WithCausationID] / [WithCorrelationID] helpers. The JSONCodec then
// re-promotes them to message metadata on its next Marshal.
//
// Wire it on the Relay side when the Publisher uses the kafka codec:
//
//	relay, _ := outbox.NewRelay(cfg,
//	    outbox.WithHeaderRestorer(kafka.RestoreCoreHeaders),
//	)
//
// Headers that aren't one of the three well-known names are NOT
// propagated — arbitrary header pass-through through
// Publisher.Publish is not part of the outbox adapter's contract.
func RestoreCoreHeaders(ctx context.Context, h map[string]string) context.Context {
	if v := h[eventbus.HeaderTraceID]; v != "" {
		ctx = WithTraceID(ctx, v)
	}
	if v := h[eventbus.HeaderCausationID]; v != "" {
		ctx = WithCausationID(ctx, v)
	}
	if v := h[eventbus.HeaderCorrelationID]; v != "" {
		ctx = WithCorrelationID(ctx, v)
	}
	return ctx
}
