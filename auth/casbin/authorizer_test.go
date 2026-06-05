package casbinauth_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/casbin/casbin/v3"
	casbinauth "github.com/slam0504/go-ddd-adapters/auth/casbin"
	"github.com/slam0504/go-ddd-core/ports/auth"
)

// fakeEnforcer implements casbinauth.Enforcer for black-box unit tests. It
// records the rvals it was called with and returns a scripted decision.
type fakeEnforcer struct {
	ok       bool
	err      error
	gotRvals []any
	calls    int
}

func (f *fakeEnforcer) Enforce(rvals ...any) (bool, error) {
	f.calls++
	f.gotRvals = rvals
	return f.ok, f.err
}

func validIdentity() auth.Identity { return auth.Identity{Subject: "alice"} }

func TestNew_NilEnforcer(t *testing.T) {
	cases := map[string]casbinauth.Enforcer{
		"plain nil interface":      nil,
		"typed-nil Enforcer":       (*casbin.Enforcer)(nil),
		"typed-nil SyncedEnforcer": (*casbin.SyncedEnforcer)(nil),
	}
	for name, enf := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := casbinauth.New(enf)
			if !errors.Is(err, casbinauth.ErrNilEnforcer) {
				t.Fatalf("New(%s): got err %v, want ErrNilEnforcer", name, err)
			}
		})
	}
}

func TestNew_NilRequestBuilder(t *testing.T) {
	_, err := casbinauth.New(&fakeEnforcer{}, casbinauth.WithRequestBuilder(nil))
	if !errors.Is(err, casbinauth.ErrNilRequestBuilder) {
		t.Fatalf("got err %v, want ErrNilRequestBuilder", err)
	}
}

func TestAllow_Allowed(t *testing.T) {
	f := &fakeEnforcer{ok: true}
	az, err := casbinauth.New(f)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := az.Allow(context.Background(), validIdentity(), "read", auth.Resource{Type: "order"}); err != nil {
		t.Fatalf("Allow: got %v, want nil", err)
	}
}

func TestAllow_Denied(t *testing.T) {
	f := &fakeEnforcer{ok: false}
	az, _ := casbinauth.New(f)
	err := az.Allow(context.Background(), validIdentity(), "read", auth.Resource{Type: "order"})
	if !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("Allow: got %v, want ErrForbidden", err)
	}
}

func TestAllow_EngineError(t *testing.T) {
	sentinel := errors.New("policy store unavailable")
	f := &fakeEnforcer{ok: false, err: sentinel}
	az, _ := casbinauth.New(f)
	err := az.Allow(context.Background(), validIdentity(), "read", auth.Resource{Type: "order"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Allow: got %v, want the engine error verbatim", err)
	}
	if errors.Is(err, auth.ErrForbidden) {
		t.Fatal("Allow: engine error must not be disguised as ErrForbidden")
	}
}

func TestAllow_MalformedInput(t *testing.T) {
	cases := map[string]struct {
		id       auth.Identity
		action   string
		resource auth.Resource
	}{
		"zero identity":      {auth.Identity{}, "read", auth.Resource{Type: "order"}},
		"empty action":       {validIdentity(), "", auth.Resource{Type: "order"}},
		"empty resource type": {validIdentity(), "read", auth.Resource{Type: ""}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakeEnforcer{ok: true}
			az, _ := casbinauth.New(f)
			err := az.Allow(context.Background(), c.id, c.action, c.resource)
			if !errors.Is(err, auth.ErrInvalidAuthorizationRequest) {
				t.Fatalf("Allow: got %v, want ErrInvalidAuthorizationRequest", err)
			}
			if f.calls != 0 {
				t.Fatalf("enforcer was called %d times; malformed input must short-circuit", f.calls)
			}
		})
	}
}

func TestAllow_MalformedBeforeCtxCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &fakeEnforcer{ok: true}
	az, _ := casbinauth.New(f)
	// Empty action AND cancelled ctx: malformed input must win (400, not ctx err).
	err := az.Allow(ctx, validIdentity(), "", auth.Resource{Type: "order"})
	if !errors.Is(err, auth.ErrInvalidAuthorizationRequest) {
		t.Fatalf("Allow: got %v, want ErrInvalidAuthorizationRequest (malformed before ctx)", err)
	}
}

func TestAllow_CtxCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &fakeEnforcer{ok: true}
	az, _ := casbinauth.New(f)
	err := az.Allow(ctx, validIdentity(), "read", auth.Resource{Type: "order"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Allow: got %v, want context.Canceled verbatim", err)
	}
	if f.calls != 0 {
		t.Fatalf("enforcer was called %d times; cancelled ctx must short-circuit", f.calls)
	}
}

func TestAllow_DefaultBuilderTuple(t *testing.T) {
	cases := map[string]struct {
		resource auth.Resource
		wantObj  string
	}{
		"collection-level (empty ID)": {auth.Resource{Type: "order"}, "order"},
		"instance-level (Type:ID)":    {auth.Resource{Type: "order", ID: "42"}, "order:42"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakeEnforcer{ok: true}
			az, _ := casbinauth.New(f)
			if err := az.Allow(context.Background(), auth.Identity{Subject: "alice"}, "read", c.resource); err != nil {
				t.Fatalf("Allow: %v", err)
			}
			want := []any{"alice", c.wantObj, "read"}
			if !reflect.DeepEqual(f.gotRvals, want) {
				t.Fatalf("rvals = %#v, want %#v", f.gotRvals, want)
			}
		})
	}
}

func TestAllow_BuilderOverride(t *testing.T) {
	f := &fakeEnforcer{ok: true}
	// 4-tuple (sub, dom, obj, act) using TenantID as the domain.
	builder := func(_ context.Context, caller auth.Identity, action string, resource auth.Resource) ([]any, error) {
		return []any{caller.Subject, caller.TenantID, resource.Type, action}, nil
	}
	az, err := casbinauth.New(f, casbinauth.WithRequestBuilder(builder))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	id := auth.Identity{Subject: "alice", TenantID: "acme"}
	if err := az.Allow(context.Background(), id, "read", auth.Resource{Type: "order"}); err != nil {
		t.Fatalf("Allow: %v", err)
	}
	want := []any{"alice", "acme", "order", "read"}
	if !reflect.DeepEqual(f.gotRvals, want) {
		t.Fatalf("rvals = %#v, want %#v", f.gotRvals, want)
	}
}

func TestAllow_BuilderError(t *testing.T) {
	sentinel := errors.New("builder failed")
	builder := func(_ context.Context, _ auth.Identity, _ string, _ auth.Resource) ([]any, error) {
		return nil, sentinel
	}
	f := &fakeEnforcer{ok: true}
	az, _ := casbinauth.New(f, casbinauth.WithRequestBuilder(builder))
	err := az.Allow(context.Background(), validIdentity(), "read", auth.Resource{Type: "order"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Allow: got %v, want builder error verbatim", err)
	}
	if f.calls != 0 {
		t.Fatalf("enforcer was called %d times; a builder error must short-circuit", f.calls)
	}
}

func TestAllow_BuilderEmptyTuple(t *testing.T) {
	builder := func(_ context.Context, _ auth.Identity, _ string, _ auth.Resource) ([]any, error) {
		return []any{}, nil
	}
	f := &fakeEnforcer{ok: true}
	az, _ := casbinauth.New(f, casbinauth.WithRequestBuilder(builder))
	err := az.Allow(context.Background(), validIdentity(), "read", auth.Resource{Type: "order"})
	if !errors.Is(err, auth.ErrInvalidAuthorizationRequest) {
		t.Fatalf("Allow: got %v, want ErrInvalidAuthorizationRequest", err)
	}
	if f.calls != 0 {
		t.Fatalf("enforcer was called %d times; an empty tuple must short-circuit", f.calls)
	}
}
