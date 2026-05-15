package kafka

import (
	"context"
	"fmt"
	rt "runtime/debug"
	"sync"

	"github.com/slam0504/go-ddd-core/bootstrap"
	"github.com/slam0504/go-ddd-core/eventbus"
	"github.com/slam0504/go-ddd-core/ports/logger"
)

// PublisherModule wraps Publisher.Close into a bootstrap.ModuleFunc.
// Publishers are constructed eagerly (handlers need them at registration
// time), so this module only manages the close-on-stop side.
func PublisherModule(p *Publisher) bootstrap.ModuleFunc {
	return bootstrap.ModuleFunc{
		ModuleName: "kafka-publisher",
		StopFn:     func(_ context.Context) error { return p.Close() },
	}
}

// SubscriberModule wraps Subscriber.Close. Typically paired with one or
// more ConsumerModule instances sharing the same *Subscriber.
func SubscriberModule(s *Subscriber) bootstrap.ModuleFunc {
	return bootstrap.ModuleFunc{
		ModuleName: "kafka-subscriber",
		StopFn:     func(_ context.Context) error { return s.Close() },
	}
}

// ConsumerModule subscribes to topic on Start and runs handle on each
// envelope. The module owns its goroutine lifecycle end-to-end:
//
//   - Start derives a consumer ctx from context.WithoutCancel(startCtx) so
//     log/trace values flow through but the consumer is not killed if a
//     short-lived startCtx is cancelled. Cancellation is driven by Stop.
//   - Each envelope runs under a per-envelope recover; a panic is logged
//     with stack and Nack'd. handle returning a non-nil error is also
//     Nack'd; nil is Ack'd. Callers no longer need to call Ack/Nack on
//     the envelope themselves.
//   - Stop cancels the consumer ctx and waits for the in-flight goroutine
//     to drain, bounded by the bootstrap shutdown ctx. SubscriberModule
//     (registered earlier) closes the underlying watermill subscriber
//     after this module has drained, matching bootstrap's reverse-order
//     stop guarantee.
//
// Register ConsumerModule AFTER SubscriberModule so the stop order is
// drain-then-close. For multiple topics sharing a Subscriber, prefer
// [ConsumerGroup] which constructs one drainable module per topic.
func ConsumerModule(
	sub *Subscriber,
	topic string,
	log logger.Logger,
	handle func(ctx context.Context, env eventbus.Envelope) error,
) bootstrap.ModuleFunc {
	name := "kafka-consumer:" + topic
	// cancel and wg are written in StartFn and read in StopFn. Safe
	// without explicit synchronization: bootstrap.App invokes Start
	// serially and only invokes Stop after Start has returned, so the
	// orchestrator establishes the necessary happens-before edge.
	var (
		cancel context.CancelFunc
		wg     sync.WaitGroup
	)
	return bootstrap.ModuleFunc{
		ModuleName: name,
		StartFn: func(startCtx context.Context, _ *bootstrap.App) error {
			consumerCtx, c := context.WithCancel(context.WithoutCancel(startCtx))
			cancel = c
			ch, err := sub.Subscribe(consumerCtx, topic)
			if err != nil {
				cancel()
				return fmt.Errorf("subscribe %s: %w", topic, err)
			}
			log.Log(consumerCtx, logger.LevelInfo, "consumer started", logger.F("topic", topic))
			wg.Add(1)
			go func() {
				defer wg.Done()
				for env := range ch {
					processEnvelope(consumerCtx, log, name, handle, env)
				}
			}()
			return nil
		},
		StopFn: func(stopCtx context.Context) error {
			if cancel == nil {
				return nil
			}
			cancel()
			done := make(chan struct{})
			go func() {
				wg.Wait()
				close(done)
			}()
			select {
			case <-done:
				return nil
			case <-stopCtx.Done():
				return fmt.Errorf("consumer %s drain: %w", topic, stopCtx.Err())
			}
		},
	}
}

// ConsumerGroup constructs one [ConsumerModule] per topic, all sharing
// the same Subscriber and handle. Each module drains independently on
// Stop, so a slow topic does not block the others' cancel() phase (only
// their joint Wait phase, capped by the bootstrap shutdown timeout).
//
// Typical use:
//
//	app.Use(kafka.SubscriberModule(sub))
//	app.Use(kafka.ConsumerGroup(sub, []string{"a", "b"}, log, apply)...)
func ConsumerGroup(
	sub *Subscriber,
	topics []string,
	log logger.Logger,
	handle func(ctx context.Context, env eventbus.Envelope) error,
) []bootstrap.Module {
	out := make([]bootstrap.Module, 0, len(topics))
	for _, t := range topics {
		out = append(out, ConsumerModule(sub, t, log, handle))
	}
	return out
}

// processEnvelope runs one handle invocation with panic recovery and
// translates the outcome to Ack/Nack. Extracted so the consumer
// goroutine stays small and so tests can exercise the recover path.
func processEnvelope(
	ctx context.Context,
	log logger.Logger,
	where string,
	handle func(ctx context.Context, env eventbus.Envelope) error,
	env eventbus.Envelope,
) {
	defer func() {
		if r := recover(); r != nil {
			log.Log(ctx, logger.LevelError, "consumer panic recovered",
				logger.F("where", where),
				logger.F("event_name", env.Name),
				logger.F("recover", fmt.Sprint(r)),
				logger.F("stack", string(rt.Stack())))
			env.Nack()
		}
	}()
	if err := handle(ctx, env); err != nil {
		log.Log(ctx, logger.LevelError, "consumer handle failed",
			logger.F("where", where),
			logger.F("event_name", env.Name),
			logger.F("err", err.Error()))
		env.Nack()
		return
	}
	env.Ack()
}
