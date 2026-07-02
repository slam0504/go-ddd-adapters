package stdhttp_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	stdhttp "github.com/slam0504/go-ddd-adapters/httpclient/std"
)

// Contextual().Do(ctx, req) must attach ctx to the request — cancelling ctx
// aborts a request that was built WITHOUT a context.
func TestContextual_AttachesContext(t *testing.T) {
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
	req, err := http.NewRequest(http.MethodGet, srv.URL, nil) // deliberately no ctx
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	go func() {
		<-started
		cancel()
	}()
	_, err = c.Contextual().Do(ctx, req) //nolint:bodyclose // error path
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled in chain", err)
	}
}
