//go:build integration

package casbinauth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/casbin/casbin/v3"
	"github.com/casbin/casbin/v3/model"
	"github.com/slam0504/go-ddd-core/ports/auth"

	casbinauth "github.com/slam0504/go-ddd-adapters/auth/casbin"
)

func TestIntegration_AllowDeny(t *testing.T) {
	m, err := model.NewModelFromString(aclModel)
	if err != nil {
		t.Fatalf("NewModelFromString: %v", err)
	}
	e, err := casbin.NewEnforcer(m)
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}
	// In-memory policy: alice may read orders, nothing else.
	if _, err := e.AddPolicy("alice", "order", "read"); err != nil {
		t.Fatalf("AddPolicy: %v", err)
	}

	az, err := casbinauth.New(e)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()
	alice := auth.Identity{Subject: "alice"}

	// Permitted: the default builder's (alice, order, read) tuple matches the
	// policy end-to-end through a real Casbin enforcer.
	if err := az.Allow(ctx, alice, "read", auth.Resource{Type: "order"}); err != nil {
		t.Fatalf("Allow read: got %v, want nil", err)
	}

	// Denied: no policy grants write.
	if err := az.Allow(ctx, alice, "write", auth.Resource{Type: "order"}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("Allow write: got %v, want ErrForbidden", err)
	}
}
