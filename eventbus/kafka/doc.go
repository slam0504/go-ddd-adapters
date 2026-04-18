// Package kafka provides eventbus.Publisher / eventbus.Subscriber / eventbus.Codec
// implementations backed by ThreeDotsLabs/watermill-kafka v3 (Sarama under
// the hood).
//
// The adapter ships a JSON codec with a type registry. Concrete events are
// reconstructed on the consumer side by registering a factory for each
// event name:
//
//	codec := kafka.NewJSONCodec()
//	codec.Register("game.submitted.v1", func() domain.DomainEvent { return &GameSubmitted{} })
//
// Trace, causation and correlation IDs are propagated as message headers
// when present in the publish-side context. Use [WithTraceID],
// [WithCausationID], [WithCorrelationID] on the way in and the corresponding
// "FromEnvelope" helpers on the way out.
package kafka
