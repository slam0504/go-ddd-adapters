package otel

import (
	"context"

	"github.com/slam0504/go-ddd-core/bootstrap"
)

// Module wraps Provider.Shutdown into a bootstrap.ModuleFunc so the app's
// lifecycle flushes both tracer and meter providers on stop without the
// caller having to write the ModuleFunc{StopFn: ...} boilerplate.
//
// The Provider is constructed outside the module (callers need it at
// wiring time to build tracers), so Module only manages the shutdown side.
func Module(p *Provider) bootstrap.ModuleFunc {
	return bootstrap.ModuleFunc{
		ModuleName: "otel",
		StopFn: func(ctx context.Context) error {
			return p.Shutdown(ctx)
		},
	}
}
