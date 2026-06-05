package casbinauth

import (
	"context"
	"errors"

	"github.com/casbin/casbin/v3"
	"github.com/slam0504/go-ddd-core/ports/auth"
)

// Enforcer is the only Casbin behaviour this adapter depends on at the Allow
// runtime call path. Both *casbin.Enforcer and *casbin.SyncedEnforcer satisfy
// it. Depending on the interface — not the concrete *casbin.Enforcer — keeps
// the concurrency choice with the caller (see doc.go) and lets unit tests
// substitute a fake without building a real Casbin model.
type Enforcer interface {
	Enforce(rvals ...any) (bool, error)
}

// Constructor errors. These surface wiring bugs at New time and are never
// returned from Allow (Allow returns only the core auth sentinels, ctx errors,
// or passed-through engine/builder errors).
var (
	ErrNilEnforcer       = errors.New("casbinauth: enforcer must not be nil")
	ErrNilRequestBuilder = errors.New("casbinauth: request builder must not be nil")
)

// Authorizer wraps an Enforcer as a core auth.Authorizer. It is immutable after
// New: Allow reads enforcer + requestBuilder and mutates no internal state, so
// a single Authorizer is safe to share across goroutines (subject to the
// enforcer requirement documented in doc.go).
type Authorizer struct {
	enforcer       Enforcer
	requestBuilder RequestBuilder
}

var _ auth.Authorizer = (*Authorizer)(nil)

// New wraps enforcer as an auth.Authorizer. It returns ErrNilEnforcer for a nil
// interface or a typed-nil *casbin.Enforcer / *casbin.SyncedEnforcer, and
// ErrNilRequestBuilder if WithRequestBuilder(nil) was supplied. With no
// WithRequestBuilder, the default (sub, obj, act) builder is used.
func New(enforcer Enforcer, opts ...Option) (*Authorizer, error) {
	if enforcer == nil {
		return nil, ErrNilEnforcer
	}
	// An interface holding a typed-nil pointer is itself non-nil, so the check
	// above does not catch New((*casbin.Enforcer)(nil)). Type-switch on the two
	// public Casbin types this adapter documents and reject a nil pointer of
	// either. *casbin.SyncedEnforcer embeds *casbin.Enforcer but is a distinct
	// dynamic type, so both cases are required.
	switch e := enforcer.(type) {
	case *casbin.Enforcer:
		if e == nil {
			return nil, ErrNilEnforcer
		}
	case *casbin.SyncedEnforcer:
		if e == nil {
			return nil, ErrNilEnforcer
		}
	}

	cfg := &config{requestBuilder: defaultRequestBuilder}
	for _, opt := range opts {
		opt(cfg)
	}
	if cfg.requestBuilder == nil {
		return nil, ErrNilRequestBuilder
	}

	return &Authorizer{
		enforcer:       enforcer,
		requestBuilder: cfg.requestBuilder,
	}, nil
}

// Allow reports whether caller may perform action on resource, per the core
// ports/auth.Authorizer contract. Validation order: malformed input (zero
// Identity / empty action / empty Resource.Type) is rejected before the ctx
// check, so a caller bug is never masked by a cancelled context.
func (a *Authorizer) Allow(ctx context.Context, caller auth.Identity, action string, resource auth.Resource) error {
	if isZeroIdentity(caller) {
		return auth.ErrInvalidAuthorizationRequest
	}
	if action == "" {
		return auth.ErrInvalidAuthorizationRequest
	}
	if resource.Type == "" {
		return auth.ErrInvalidAuthorizationRequest
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	rvals, err := a.requestBuilder(ctx, caller, action, resource)
	if err != nil {
		return err
	}
	if len(rvals) == 0 {
		return auth.ErrInvalidAuthorizationRequest
	}

	ok, err := a.enforcer.Enforce(rvals...)
	if err != nil {
		return err
	}
	if !ok {
		return auth.ErrForbidden
	}
	return nil
}

// isZeroIdentity reports whether id is the zero Identity. Per the core contract
// a zero Identity is malformed input, not an anonymous principal.
func isZeroIdentity(id auth.Identity) bool {
	return id.Subject == "" && id.TenantID == "" && len(id.Roles) == 0 && len(id.Claims) == 0
}

// defaultRequestBuilder produces the classic Casbin (sub, obj, act) triple. A
// resource with a non-empty ID is encoded as "Type:ID"; a collection-level
// resource (empty ID) uses just "Type".
func defaultRequestBuilder(_ context.Context, caller auth.Identity, action string, resource auth.Resource) ([]any, error) {
	obj := resource.Type
	if resource.ID != "" {
		obj = resource.Type + ":" + resource.ID
	}
	return []any{caller.Subject, obj, action}, nil
}
