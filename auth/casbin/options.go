package casbinauth

import (
	"context"

	"github.com/slam0504/go-ddd-core/ports/auth"
)

// RequestBuilder translates core's (caller, action, resource) into the rvals
// passed to Enforcer.Enforce. ctx is supplied so a builder may consult
// request-scoped values (e.g. a tenant pulled from context); the default
// builder ignores it. A builder that returns a non-nil error aborts the
// decision and the error is returned verbatim by Allow; a builder that returns
// an empty slice signals malformed input (Allow maps it to
// ErrInvalidAuthorizationRequest and never calls the enforcer).
type RequestBuilder func(ctx context.Context, caller auth.Identity, action string, resource auth.Resource) ([]any, error)

// Option configures the Authorizer built by New. Options are pure setters into
// the private config; all validation runs in New after every option is
// applied, so option order never changes the result. (Mirrors the authjwt
// private-config pattern.)
type Option func(*config)

type config struct {
	requestBuilder RequestBuilder
}

// WithRequestBuilder replaces the default (sub, obj, act) builder. Passing a
// nil fn makes New return ErrNilRequestBuilder (fail loud — this is a wiring
// gate, not a nil-ignore convention).
func WithRequestBuilder(fn RequestBuilder) Option {
	return func(c *config) {
		c.requestBuilder = fn
	}
}
