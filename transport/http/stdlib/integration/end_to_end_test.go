//go:build integration

// Package integration_test verifies the transport/http/stdlib adapter
// composed with the health sub-package through the real
// bootstrap.App lifecycle. It targets the integration boundary
// (multi-module wiring, two concurrent servers, end-to-end HTTP)
// rather than re-asserting unit-level body shapes already covered by
// the package tests. HTTP-only — no Docker / testcontainers.
package integration_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/slam0504/go-ddd-core/bootstrap"
	"github.com/slam0504/go-ddd-core/ports/auth"
	corehealth "github.com/slam0504/go-ddd-core/ports/health"

	authjwt "github.com/slam0504/go-ddd-adapters/auth/jwt"
	httpstdlib "github.com/slam0504/go-ddd-adapters/transport/http/stdlib"
	"github.com/slam0504/go-ddd-adapters/transport/http/stdlib/authmw"
	"github.com/slam0504/go-ddd-adapters/transport/http/stdlib/health"
)

func TestEndToEnd_MainAndAdminServers(t *testing.T) {
	// Main app mux: a trivial business endpoint.
	mainMux := http.NewServeMux()
	mainMux.HandleFunc("GET /ping", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "pong")
	})

	// Health registry with one always-passing and one toggleable Check.
	var flakyUnhealthy atomic.Bool
	reg := &health.Registry{}
	reg.MustRegister(corehealth.NewCheck("always", func(_ context.Context) error {
		return nil
	}))
	reg.MustRegister(corehealth.NewCheck("toggle", func(_ context.Context) error {
		if flakyUnhealthy.Load() {
			return errToggleUnhealthy
		}
		return nil
	}))

	mainSrv := httpstdlib.New("127.0.0.1:0", mainMux)
	adminSrv := httpstdlib.New("127.0.0.1:0", reg.Handler(),
		httpstdlib.WithModuleName("admin"))

	app := bootstrap.New(bootstrap.Options{})
	app.Use(mainSrv.Module(), adminSrv.Module())

	startCtx, startCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer startCancel()
	if err := app.Start(startCtx); err != nil {
		t.Fatalf("app.Start: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		if err := app.Stop(stopCtx); err != nil {
			t.Fatalf("app.Stop: %v", err)
		}
	})

	mainBase := "http://" + mainSrv.Addr()
	adminBase := "http://" + adminSrv.Addr()

	// Main server's business endpoint works.
	resp, err := http.Get(mainBase + "/ping")
	if err != nil {
		t.Fatalf("main GET /ping: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("main /ping status = %d, want 200", resp.StatusCode)
	}

	// Initial probes: liveness always 200; readiness 200 with both checks healthy.
	mustStatus(t, adminBase+"/healthz", http.StatusOK)
	mustStatus(t, adminBase+"/readyz", http.StatusOK)

	// Flip toggle → readiness transitions to 503; liveness unaffected.
	flakyUnhealthy.Store(true)
	mustStatus(t, adminBase+"/healthz", http.StatusOK)
	mustStatus(t, adminBase+"/readyz", http.StatusServiceUnavailable)

	// 503 body still lists BOTH checks (passing one + failing one).
	body := mustGetBody(t, adminBase+"/readyz")
	if !strings.Contains(body, `"name":"always"`) || !strings.Contains(body, `"name":"toggle"`) {
		t.Fatalf("503 body must list both checks; got %s", body)
	}
	if !strings.Contains(body, `"ok":true`) || !strings.Contains(body, `"ok":false`) {
		t.Fatalf("503 body must include both ok:true and ok:false; got %s", body)
	}

	// Flip back; readiness returns to 200.
	flakyUnhealthy.Store(false)
	mustStatus(t, adminBase+"/readyz", http.StatusOK)

	// app.Stop is exercised via t.Cleanup; failure there fails the test.
}

// TestEndToEnd_ProtectedRoute_AuthJWT wires Phase A (authjwt static-key
// verifier) and Phase B (authmw middleware) end-to-end through a real
// httpstdlib server: a valid HS256 bearer token reaches the protected handler
// and the verified Identity is readable from the request context; a request
// with no token is rejected with 401 before the handler runs.
func TestEndToEnd_ProtectedRoute_AuthJWT(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef") // 32 bytes, sufficient for HS256

	verifier, err := authjwt.New(
		authjwt.WithHMACSecret(secret),
		authjwt.WithAllowedAlgorithms("HS256"),
	)
	if err != nil {
		t.Fatalf("authjwt.New: %v", err)
	}
	protect, err := authmw.New(verifier)
	if err != nil {
		t.Fatalf("authmw.New: %v", err)
	}

	var handlerRan atomic.Bool
	me := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerRan.Store(true)
		id, ok := auth.IdentityFromContext(r.Context())
		if !ok {
			http.Error(w, "no identity", http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, id.Subject)
	})

	mux := http.NewServeMux()
	mux.Handle("GET /me", protect(me))

	srv := httpstdlib.New("127.0.0.1:0", mux)
	app := bootstrap.New(bootstrap.Options{})
	app.Use(srv.Module())

	startCtx, startCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer startCancel()
	if err := app.Start(startCtx); err != nil {
		t.Fatalf("app.Start: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		if err := app.Stop(stopCtx); err != nil {
			t.Fatalf("app.Stop: %v", err)
		}
	})

	base := "http://" + srv.Addr()

	// Valid token → 200 and the handler reads the verified subject.
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "alice",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	signed, err := token.SignedString(secret)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	status, body := getWithBearer(t, base+"/me", signed)
	if status != http.StatusOK {
		t.Fatalf("authenticated GET /me status = %d, want 200", status)
	}
	if body != "alice" {
		t.Fatalf("protected handler subject = %q, want alice", body)
	}

	// No token → 401 and the protected handler never runs.
	handlerRan.Store(false)
	status, _ = getWithBearer(t, base+"/me", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("anonymous GET /me status = %d, want 401", status)
	}
	if handlerRan.Load() {
		t.Fatal("protected handler ran for an unauthenticated request")
	}
}

func getWithBearer(t *testing.T, url, token string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request %s: %v", url, err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body %s: %v", url, err)
	}
	return resp.StatusCode, string(b)
}

var errToggleUnhealthy = &toggleErr{}

type toggleErr struct{}

func (*toggleErr) Error() string { return "toggle: simulated failure" }

func mustStatus(t *testing.T, url string, want int) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != want {
		t.Fatalf("GET %s status = %d, want %d", url, resp.StatusCode, want)
	}
}

func mustGetBody(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body %s: %v", url, err)
	}
	return string(b)
}
