package health_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// TestRegistry_LivenessAlways200_DoesNotInvokeChecks pins the liveness
// contract: /healthz is dep-free, always 200, and must NOT execute any
// registered Check (a failing dep must not cause an orchestrator to
// restart the pod).
func TestRegistry_LivenessAlways200_DoesNotInvokeChecks(t *testing.T) {
	var invocations atomic.Int32

	r := &health.Registry{}
	r.MustRegister(corehealth.NewCheck("alwaysFails", func(_ context.Context) error {
		invocations.Add(1)
		return errors.New("would-be-failure")
	}))

	rec := httptest.NewRecorder()
	r.LivenessHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v (raw=%q)", err, rec.Body.String())
	}
	if body.Status != "ok" {
		t.Fatalf("status = %q, want ok", body.Status)
	}
	if n := invocations.Load(); n != 0 {
		t.Fatalf("registered Check was invoked %d times; liveness must run no Checks", n)
	}
}

// TestRegistry_ReadinessEmptyRegistry: empty registry → 200 with
// status:"ok" and checks:[] (not null).
func TestRegistry_ReadinessEmptyRegistry(t *testing.T) {
	r := &health.Registry{}

	rec := httptest.NewRecorder()
	r.ReadinessHandler().ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	if !strings.Contains(rec.Body.String(), `"checks":[]`) {
		t.Fatalf("body must contain literal 'checks:[]' (not null); got %q", rec.Body.String())
	}
	var b struct {
		Status string          `json:"status"`
		Checks json.RawMessage `json:"checks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("decode: %v (raw=%q)", err, rec.Body.String())
	}
	if b.Status != "ok" {
		t.Fatalf("status = %q, want ok", b.Status)
	}
	if string(b.Checks) != "[]" {
		t.Fatalf("checks raw = %q, want []", string(b.Checks))
	}
}

// TestRegistry_ReadinessAllHealthy: every Check listed in registration
// order; all ok:true; overall status:"ok"; 200.
func TestRegistry_ReadinessAllHealthy(t *testing.T) {
	r := &health.Registry{}
	r.MustRegister(corehealth.NewCheck("postgres", okFn))
	r.MustRegister(corehealth.NewCheck("kafka", okFn))
	r.MustRegister(corehealth.NewCheck("redis", okFn))

	rec := httptest.NewRecorder()
	r.ReadinessHandler().ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var b struct {
		Status string `json:"status"`
		Checks []struct {
			Name  string `json:"name"`
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if b.Status != "ok" {
		t.Fatalf("status = %q, want ok", b.Status)
	}
	wantOrder := []string{"postgres", "kafka", "redis"}
	if len(b.Checks) != len(wantOrder) {
		t.Fatalf("len(checks) = %d, want %d", len(b.Checks), len(wantOrder))
	}
	for i, want := range wantOrder {
		if b.Checks[i].Name != want {
			t.Fatalf("checks[%d].name = %q, want %q (registration order)", i, b.Checks[i].Name, want)
		}
		if !b.Checks[i].OK {
			t.Fatalf("checks[%d] (%q) ok=false, want true", i, b.Checks[i].Name)
		}
		if b.Checks[i].Error != "" {
			t.Fatalf("checks[%d] (%q) carries error %q, want empty", i, b.Checks[i].Name, b.Checks[i].Error)
		}
	}
}

// TestRegistry_ReadinessOneUnhealthy: 503 overall; passing + failing
// checks both listed in registration order; failing check has the
// error text from its Check fn.
func TestRegistry_ReadinessOneUnhealthy(t *testing.T) {
	r := &health.Registry{}
	r.MustRegister(corehealth.NewCheck("postgres", okFn))
	r.MustRegister(corehealth.NewCheck("kafka", failFn("connection refused")))
	r.MustRegister(corehealth.NewCheck("redis", okFn))

	rec := httptest.NewRecorder()
	r.ReadinessHandler().ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	var b struct {
		Status string `json:"status"`
		Checks []struct {
			Name  string `json:"name"`
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if b.Status != "unhealthy" {
		t.Fatalf("status = %q, want unhealthy", b.Status)
	}
	if len(b.Checks) != 3 {
		t.Fatalf("len(checks) = %d, want 3 (failing body must list every check)", len(b.Checks))
	}
	wantOrder := []string{"postgres", "kafka", "redis"}
	wantOK := []bool{true, false, true}
	for i, c := range b.Checks {
		if c.Name != wantOrder[i] {
			t.Fatalf("checks[%d].name = %q, want %q", i, c.Name, wantOrder[i])
		}
		if c.OK != wantOK[i] {
			t.Fatalf("checks[%d] (%q) ok=%v, want %v", i, c.Name, c.OK, wantOK[i])
		}
	}
	if !strings.Contains(b.Checks[1].Error, "connection refused") {
		t.Fatalf("kafka error = %q, want 'connection refused' substring", b.Checks[1].Error)
	}
	if b.Checks[0].Error != "" || b.Checks[2].Error != "" {
		t.Fatalf("passing checks must omit error; got %q / %q", b.Checks[0].Error, b.Checks[2].Error)
	}
}

// TestRegistry_ReadinessChecksRunSequentially pins that Checks execute
// one after another in registration order — not concurrently.
//
// Each Check captures the value of a shared atomic counter at its
// entry, then increments it. If execution is sequential, the captured
// values must be 0, 1, 2 in registration order. Concurrent execution
// would race and produce non-monotonic captures.
func TestRegistry_ReadinessChecksRunSequentially(t *testing.T) {
	var counter atomic.Int32
	type captured struct {
		name   string
		seenAt int32
	}
	var captures []captured
	var mu sync.Mutex
	mkCheck := func(name string) corehealth.Check {
		return corehealth.NewCheck(name, func(_ context.Context) error {
			seen := counter.Load()
			counter.Add(1)
			mu.Lock()
			captures = append(captures, captured{name: name, seenAt: seen})
			mu.Unlock()
			return nil
		})
	}

	r := &health.Registry{}
	r.MustRegister(mkCheck("a"))
	r.MustRegister(mkCheck("b"))
	r.MustRegister(mkCheck("c"))

	rec := httptest.NewRecorder()
	r.ReadinessHandler().ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if len(captures) != 3 {
		t.Fatalf("captures = %v; want 3 entries", captures)
	}
	wantOrder := []string{"a", "b", "c"}
	for i, c := range captures {
		if c.name != wantOrder[i] {
			t.Fatalf("capture %d name=%q, want %q (registration order)", i, c.name, wantOrder[i])
		}
		if c.seenAt != int32(i) {
			t.Fatalf("capture %d (%q) seenAt=%d, want %d (sequential)", i, c.name, c.seenAt, i)
		}
	}
}

// TestRegistry_ReadinessProbeTimeoutCancelsAndContinues pins three
// properties of the per-Check ProbeTimeout:
//
//  1. A Check that respects ctx (waits on <-ctx.Done() and returns
//     ctx.Err()) is canceled when the per-Check deadline elapses.
//  2. The slow Check is reported as ok:false with an error wrapping
//     context.DeadlineExceeded; overall status is 503.
//  3. A later Check registered AFTER the slow one still runs and
//     appears in the body. The handler must visit every Check
//     regardless of earlier failures or timeouts.
func TestRegistry_ReadinessProbeTimeoutCancelsAndContinues(t *testing.T) {
	r := &health.Registry{}
	r.SetProbeTimeout(25 * time.Millisecond)

	var afterRan atomic.Bool

	r.MustRegister(corehealth.NewCheck("slow", func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}))
	r.MustRegister(corehealth.NewCheck("after", func(_ context.Context) error {
		afterRan.Store(true)
		return nil
	}))

	rec := httptest.NewRecorder()
	r.ReadinessHandler().ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}

	var b struct {
		Status string `json:"status"`
		Checks []struct {
			Name  string `json:"name"`
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("decode: %v (raw=%q)", err, rec.Body.String())
	}
	if b.Status != "unhealthy" {
		t.Fatalf("status = %q, want unhealthy", b.Status)
	}

	if len(b.Checks) != 2 {
		t.Fatalf("len(checks) = %d, want 2 (slow + after)", len(b.Checks))
	}

	// Slow check: failed, error mentions deadline.
	if b.Checks[0].Name != "slow" {
		t.Fatalf("checks[0].name = %q, want slow", b.Checks[0].Name)
	}
	if b.Checks[0].OK {
		t.Fatalf("checks[0] (slow) ok=true, want false")
	}
	// The check returns ctx.Err() which is context.DeadlineExceeded;
	// its Error() text is exactly "context deadline exceeded".
	if !strings.Contains(b.Checks[0].Error, context.DeadlineExceeded.Error()) {
		t.Fatalf("checks[0] error = %q, want substring %q",
			b.Checks[0].Error, context.DeadlineExceeded.Error())
	}

	// After check: still ran and reported as healthy.
	if b.Checks[1].Name != "after" {
		t.Fatalf("checks[1].name = %q, want after", b.Checks[1].Name)
	}
	if !b.Checks[1].OK {
		t.Fatalf("checks[1] (after) ok=false, want true")
	}
	if !afterRan.Load() {
		t.Fatalf("'after' check did not run — handler stopped at first failure")
	}
}

// TestRegistry_HandlerExposesBothPaths verifies that r.Handler()
// multiplexes liveness and readiness on the canonical exact paths.
func TestRegistry_HandlerExposesBothPaths(t *testing.T) {
	r := &health.Registry{}
	r.MustRegister(corehealth.NewCheck("ok", okFn))
	h := r.Handler()

	t.Run("GET /healthz", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var b struct {
			Status string          `json:"status"`
			Checks json.RawMessage `json:"checks"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &b)
		if b.Status != "ok" {
			t.Fatalf("status = %q, want ok", b.Status)
		}
		if b.Checks != nil {
			t.Fatalf("liveness body must not include checks; got %s", string(b.Checks))
		}
	})

	t.Run("GET /readyz", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var b struct {
			Status string          `json:"status"`
			Checks json.RawMessage `json:"checks"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &b)
		if b.Status != "ok" {
			t.Fatalf("status = %q, want ok", b.Status)
		}
		if b.Checks == nil {
			t.Fatalf("readiness body must include checks field")
		}
	})
}

// TestRegistry_HandlerMethodNotAllowed verifies non-GET on the
// registered paths returns 405 (provided by Go 1.22's method-aware mux).
func TestRegistry_HandlerMethodNotAllowed(t *testing.T) {
	r := &health.Registry{}
	h := r.Handler()

	for _, path := range []string{"/healthz", "/readyz"} {
		t.Run("POST "+path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("POST %s status = %d, want 405", path, rec.Code)
			}
		})
	}
}

// TestRegistry_HandlerUnknownPath verifies any path other than
// /healthz or /readyz returns 404 (provided by the mux).
func TestRegistry_HandlerUnknownPath(t *testing.T) {
	r := &health.Registry{}
	h := r.Handler()

	for _, path := range []string{"/", "/nope", "/healthzz", "/admin/healthz"} {
		t.Run("GET "+path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("GET %s status = %d, want 404", path, rec.Code)
			}
		})
	}
}

// TestRegistry_HandlerMountsViaStripPrefix verifies the documented
// prefix-mounting pattern: wrap r.Handler() with http.StripPrefix to
// serve /admin/healthz and /admin/readyz on an admin server's mux.
func TestRegistry_HandlerMountsViaStripPrefix(t *testing.T) {
	r := &health.Registry{}
	r.MustRegister(corehealth.NewCheck("ok", okFn))
	mounted := http.StripPrefix("/admin", r.Handler())

	for _, tc := range []struct {
		path string
		want int
	}{
		{"/admin/healthz", http.StatusOK},
		{"/admin/readyz", http.StatusOK},
	} {
		t.Run("GET "+tc.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mounted.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Code != tc.want {
				t.Fatalf("GET %s status = %d, want %d", tc.path, rec.Code, tc.want)
			}
		})
	}
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
