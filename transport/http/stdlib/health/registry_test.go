package health_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	corehealth "github.com/slam0504/go-ddd-core/ports/health"

	"github.com/slam0504/go-ddd-adapters/transport/http/stdlib/health"
)

// shared helpers (referenced by handler-level tests in later tasks)
func okFn(_ context.Context) error { return nil }
func failFn(msg string) func(context.Context) error {
	return func(_ context.Context) error { return errors.New(msg) }
}

func TestRegistry_ZeroValueRegisters(t *testing.T) {
	var r health.Registry
	if err := r.Register(corehealth.NewCheck("postgres", okFn)); err != nil {
		t.Fatalf("zero-value Register: %v", err)
	}
}

func TestRegistry_DuplicateNameReturnsError(t *testing.T) {
	r := &health.Registry{}
	if err := r.Register(corehealth.NewCheck("postgres", okFn)); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	err := r.Register(corehealth.NewCheck("postgres", okFn))
	if err == nil {
		t.Fatalf("duplicate Register: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "postgres") {
		t.Fatalf("error %q does not mention duplicate name", err.Error())
	}
}

// TestRegistry_StateUnchangedAfterDuplicate verifies that a failed
// duplicate-registration does NOT mutate the registry: the original
// name is still considered registered (a third attempt with the same
// name still errors), and an unrelated fresh name still registers
// cleanly.
func TestRegistry_StateUnchangedAfterDuplicate(t *testing.T) {
	r := &health.Registry{}
	if err := r.Register(corehealth.NewCheck("postgres", okFn)); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := r.Register(corehealth.NewCheck("postgres", okFn)); err == nil {
		t.Fatalf("second Register: expected duplicate error, got nil")
	}
	if err := r.Register(corehealth.NewCheck("postgres", okFn)); err == nil {
		t.Fatalf("third Register: original 'postgres' should still be considered registered")
	}
	if err := r.Register(corehealth.NewCheck("kafka", okFn)); err != nil {
		t.Fatalf("Register fresh name after dup attempt: %v", err)
	}
}

func TestRegistry_MustRegisterPanicsOnDuplicate(t *testing.T) {
	r := &health.Registry{}
	r.MustRegister(corehealth.NewCheck("postgres", okFn))

	defer func() {
		if rec := recover(); rec == nil {
			t.Fatalf("MustRegister: expected panic on duplicate")
		}
	}()
	r.MustRegister(corehealth.NewCheck("postgres", okFn))
}

func TestRegistry_EmptyNameRejected(t *testing.T) {
	r := &health.Registry{}
	err := r.Register(corehealth.NewCheck("", okFn))
	if err == nil {
		t.Fatalf("Register(empty name): expected error, got nil")
	}
	// After rejection, an empty-name attempt must still error (state unchanged).
	if err := r.Register(corehealth.NewCheck("", okFn)); err == nil {
		t.Fatalf("Register(empty name) second time: expected error, got nil")
	}
	// A real name must still register.
	if err := r.Register(corehealth.NewCheck("postgres", okFn)); err != nil {
		t.Fatalf("Register(valid name) after empty-name reject: %v", err)
	}
}
