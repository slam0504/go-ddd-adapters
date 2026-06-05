// Package casbinauth adapts a Casbin enforcer to the go-ddd-core
// ports/auth.Authorizer contract. It wraps an enforcer the caller has already
// built and loaded; it does not read model/policy files or manage policy
// storage.
//
// # Request mapping
//
// By default Allow maps the core (caller, action, resource) into the classic
// Casbin (sub, obj, act) triple:
//
//	sub = caller.Subject
//	obj = resource.Type            // when resource.ID == ""
//	obj = resource.Type + ":" + resource.ID  // when resource.ID != ""
//	act = action
//
// WithRequestBuilder replaces this mapping wholesale, e.g. to emit a
// (sub, dom, obj, act) tuple for a domain/tenant-scoped policy.
//
// # Error mapping
//
//	nil                              allowed (Enforce returned true).
//	auth.ErrForbidden                policy denied (Enforce returned false) → 403.
//	auth.ErrInvalidAuthorizationRequest  malformed input (zero Identity, empty
//	                                 action, empty Resource.Type, or a builder
//	                                 that returned an empty tuple) → 400.
//	ctx.Err()                        the context was cancelled before evaluation.
//	any other error                  the decision could not be made; the engine
//	                                 or builder error is returned verbatim and
//	                                 never disguised as a denial.
//
// # Concurrency
//
// Authorizer.Allow mutates no internal state; the Authorizer is immutable after
// New and safe to share across goroutines. New requires that the supplied
// Enforcer.Enforce is safe to call concurrently. If the policy/model is
// reloaded or mutated at runtime, pass a *casbin.SyncedEnforcer (its Enforce
// takes an RLock) or otherwise synchronise access. A plain *casbin.Enforcer
// (no internal lock) is only appropriate when the model/policy is not modified
// after construction. Allow honours ctx cancellation by checking ctx.Err()
// before invoking the enforcer; Casbin's Enforce is synchronous and not
// interruptible mid-evaluation.
package casbinauth
