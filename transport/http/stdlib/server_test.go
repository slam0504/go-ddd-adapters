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

// TestServer_StopWaitsForInFlightRequest pins the graceful-shutdown
// contract: Stop must NOT return while a handler is mid-request.
//
// The test drives synchronization with two channels (`started` for
// "handler reached", `release` for "let handler finish") so the
// assertions are causal rather than wall-clock-based. The bounded
// select timeouts in this test are deadlock guards only — the only
// timing-shaped wait is the brief 50 ms grace that lets the Stop
// goroutine actually enter srv.Shutdown before we probe it (Stop
// itself does not signal "I am inside Shutdown"), and that probe is
// followed by a non-blocking select.
func TestServer_StopWaitsForInFlightRequest(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-release:
			_, _ = io.WriteString(w, "done")
		case <-r.Context().Done():
		}
	})

	srv := httpstdlib.New("127.0.0.1:0", mux)
	mod := srv.Module()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := mod.Start(ctx, nil); err != nil {
		t.Fatalf("Start: %v", err)
	}

	type result struct {
		status int
		body   string
		err    error
	}
	resCh := make(chan result, 1)
	go func() {
		resp, err := http.Get("http://" + srv.Addr() + "/slow")
		if err != nil {
			resCh <- result{err: err}
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		resCh <- result{status: resp.StatusCode, body: string(b)}
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatalf("handler did not start within 5s (deadlock guard)")
	}

	stopErrCh := make(chan error, 1)
	go func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		stopErrCh <- mod.Stop(stopCtx)
	}()

	// 50ms grace so the Stop goroutine reaches srv.Shutdown.
	// Once it is in Shutdown with an in-flight request, it must
	// stay there until release — i.e. the assertion below is causal,
	// not timing-based.
	time.Sleep(50 * time.Millisecond)
	select {
	case err := <-stopErrCh:
		t.Fatalf("Stop returned before release: err=%v", err)
	default:
	}

	close(release)

	var res result
	select {
	case res = <-resCh:
	case <-time.After(5 * time.Second):
		t.Fatalf("in-flight request did not finish (deadlock guard)")
	}
	if res.err != nil {
		t.Fatalf("in-flight GET: %v", res.err)
	}
	if res.status != http.StatusOK || res.body != "done" {
		t.Fatalf("in-flight result: status=%d body=%q", res.status, res.body)
	}

	select {
	case err := <-stopErrCh:
		if err != nil {
			t.Fatalf("Stop returned error after release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Stop did not return after release (deadlock guard)")
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
