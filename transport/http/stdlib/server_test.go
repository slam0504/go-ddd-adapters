package httpstdlib_test

import (
	"context"
	"io"
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
