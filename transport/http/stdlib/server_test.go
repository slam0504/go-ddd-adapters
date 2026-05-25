package httpstdlib_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/slam0504/go-ddd-adapters/transport/http/stdlib"
)

// TestServer_StartBindsListenerSynchronously pins the primary public
// surface: New(addr, handler, opts...) → *Server, srv.Module() →
// bootstrap.ModuleFunc, srv.Addr() observable immediately after Start.
//
// 127.0.0.1:0 is used (not :0) so the OS picks an ephemeral port on
// loopback explicitly, avoiding IPv6 / unspecified-address ambiguity in
// the client URL.
func TestServer_StartBindsListenerSynchronously(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ping", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "pong")
	})

	srv := httpstdlib.New("127.0.0.1:0", mux)
	mod := srv.Module()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := mod.Start(ctx, nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		_ = mod.Stop(stopCtx)
	})

	addr := srv.Addr()
	if addr == "" {
		t.Fatalf("Addr returned empty after Start")
	}
	if !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Fatalf("Addr = %q, want 127.0.0.1:<port>", addr)
	}

	resp, err := http.Get("http://" + addr + "/ping")
	if err != nil {
		t.Fatalf("GET /ping: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "pong" {
		t.Fatalf("body = %q, want pong", string(body))
	}
}

// TestServer_AddrEmptyBeforeStart pins that Addr returns "" until a
// successful Start has bound a listener.
func TestServer_AddrEmptyBeforeStart(t *testing.T) {
	srv := httpstdlib.New("127.0.0.1:0", http.NewServeMux())
	if got := srv.Addr(); got != "" {
		t.Fatalf("Addr before Start = %q, want empty", got)
	}
}

// TestServer_StartBindErrorReturned pins that a port-conflict surfaces
// as a Start error synchronously and leaves Addr empty.
//
// The conflicting listener is created via net.Listen directly (not via
// a second httpstdlib.Server) so the test exercises only the
// adapter-under-test's bind code path.
func TestServer_StartBindErrorReturned(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy listener: %v", err)
	}
	t.Cleanup(func() { _ = occupied.Close() })

	srv := httpstdlib.New(occupied.Addr().String(), http.NewServeMux())
	mod := srv.Module()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	startErr := mod.Start(ctx, nil)
	if startErr == nil {
		t.Fatalf("Start: expected bind error, got nil")
	}
	if srv.Addr() != "" {
		t.Fatalf("Addr after failed Start = %q, want empty", srv.Addr())
	}
}
