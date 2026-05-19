package outbox

import (
	"context"
	"errors"
	"fmt"

	"github.com/slam0504/go-ddd-core/bootstrap"
	"github.com/slam0504/go-ddd-core/ports/logger"
)

// RelayModule wraps a [Relay] into a [bootstrap.ModuleFunc]. Start
// spawns one goroutine running Relay.Run with a runtime ctx derived
// from context.WithCancel(context.WithoutCancel(startCtx)) — the same
// detach-then-cancel pattern as kafka.ConsumerModule, so trace values
// flow through but the relay is not killed by a short-lived startCtx.
// Stop cancels the runtime ctx and waits for Run to return, bounded
// by the bootstrap shutdown ctx.
//
// context.Canceled from Run is treated as clean shutdown (silent).
// Any other Run return error is logged at Error level but not
// surfaced back to bootstrap — the loop is the intended terminal
// state. Per-record panic recovery lives inside Relay.process; the
// module goroutine is panic-free by construction.
func RelayModule(r *Relay, log logger.Logger) bootstrap.ModuleFunc {
	var (
		cancel  context.CancelFunc
		runDone chan struct{}
	)
	return bootstrap.ModuleFunc{
		ModuleName: "outbox-relay",
		StartFn: func(startCtx context.Context, _ *bootstrap.App) error {
			runCtx, c := context.WithCancel(context.WithoutCancel(startCtx))
			cancel = c
			runDone = make(chan struct{})
			go func() {
				defer close(runDone)
				if err := r.Run(runCtx); err != nil && !errors.Is(err, context.Canceled) {
					logger.Error(runCtx, log, "outbox relay exited with error",
						logger.F("err", err.Error()))
				}
			}()
			return nil
		},
		StopFn: func(stopCtx context.Context) error {
			if cancel == nil {
				return nil
			}
			cancel()
			select {
			case <-runDone:
				return nil
			case <-stopCtx.Done():
				return fmt.Errorf("outbox relay drain: %w", stopCtx.Err())
			}
		},
	}
}
