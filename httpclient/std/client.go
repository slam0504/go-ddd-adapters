package stdhttp

import (
	"net/http"

	"github.com/slam0504/go-ddd-core/ports/httpclient"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Client is a stdlib-backed implementation of core's ports/httpclient.Client.
// Defaults match net/http.Client exactly: no client-level timeout,
// http.DefaultTransport, no tracing.
type Client struct {
	hc *http.Client
}

// New builds a Client. With no options the client behaves exactly like a
// zero-value net/http.Client.
func New(opts ...Option) (*Client, error) {
	var cfg config
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.timeout < 0 {
		return nil, ErrNegativeTimeout
	}
	if cfg.transportSet && cfg.transport == nil {
		return nil, ErrNilTransport
	}
	if cfg.tracingSet && cfg.tracerProvider == nil {
		return nil, ErrNilTracerProvider
	}
	transport := cfg.transport
	if cfg.tracingSet {
		base := transport
		if base == nil {
			base = http.DefaultTransport
		}
		transport = otelhttp.NewTransport(base, otelhttp.WithTracerProvider(cfg.tracerProvider))
	}
	return &Client{hc: &http.Client{Timeout: cfg.timeout, Transport: transport}}, nil
}

// Do sends req with net/http.Client.Do semantics: req.Context() cancellation
// is honoured; a nil error means the caller owns closing resp.Body; errors
// are stdlib passthrough (*url.Error etc.), never re-coded — the port
// contract is explicitly net/http-shaped.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	return c.hc.Do(req)
}

// Compile-time assertion that *Client implements core's httpclient.Client.
var _ httpclient.Client = (*Client)(nil)
