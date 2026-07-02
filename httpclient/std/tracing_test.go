package stdhttp_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	stdhttp "github.com/slam0504/go-ddd-adapters/httpclient/std"
)

func doOK(t *testing.T, c *stdhttp.Client, url string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	// otelhttp ends the client span when the body is fully consumed + closed.
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

// WithTracing(tp) must emit a client span per request through the injected
// provider — proving otelhttp is wired to the EXPLICIT provider, not the
// otel global.
func TestTracing_EmitsSpanThroughInjectedProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	c, err := stdhttp.New(stdhttp.WithTracing(tp))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	doOK(t, c, srv.URL)
	if got := len(rec.Ended()); got != 1 {
		t.Fatalf("ended spans = %d, want 1", got)
	}
}

// Without WithTracing the request path is pure stdlib — zero spans anywhere.
func TestTracing_OffByDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	rec := tracetest.NewSpanRecorder()
	_ = sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)) // provider exists but is NOT injected
	c, err := stdhttp.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	doOK(t, c, srv.URL)
	if got := len(rec.Ended()); got != 0 {
		t.Fatalf("ended spans = %d, want 0", got)
	}
}
