package stdhttp_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	stdhttp "github.com/slam0504/go-ddd-adapters/httpclient/std"
)

func TestDo_RoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c, err := stdhttp.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("got %d %q, want 200 \"ok\"", resp.StatusCode, body)
	}
}

// WithTimeout(d>0) must abort a slow request with a stdlib timeout error —
// passthrough, not an errorsx-coded error.
func TestDo_TimeoutAbortsSlowRequest(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	c, err := stdhttp.New(stdhttp.WithTimeout(50 * time.Millisecond))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	_, err = c.Do(req) //nolint:bodyclose // error path: no body on non-nil err
	var uerr *url.Error
	if !errors.As(err, &uerr) || !uerr.Timeout() {
		t.Fatalf("err = %v, want *url.Error with Timeout()==true", err)
	}
}

// The default client has NO client-level timeout (stdlib parity): a request
// slower than any would-be "safe default" must still succeed.
func TestDo_DefaultHasNoClientTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c, err := stdhttp.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
}

// Cancelling req.Context() mid-flight must abort the request with
// context.Canceled surfaced through the stdlib error chain.
func TestDo_HonoursRequestContextCancellation(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()

	c, err := stdhttp.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	go func() {
		<-started
		cancel()
	}()
	_, err = c.Do(req) //nolint:bodyclose // error path: no body on non-nil err
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled in chain", err)
	}
}
