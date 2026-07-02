package stdhttp

import (
	"context"
	"net/http"

	"github.com/slam0504/go-ddd-core/ports/httpclient"
)

// Contextual returns a context-first view of c implementing core's
// httpclient.ContextualClient. Do(ctx, req) is exactly
// c.Do(req.WithContext(ctx)) — no added semantics. A separate wrapper type
// is required because Client.Do(req) and ContextualClient.Do(ctx, req)
// share a method name with different signatures, so one concrete type
// cannot implement both interfaces.
func (c *Client) Contextual() httpclient.ContextualClient {
	return contextualClient{c: c}
}

type contextualClient struct{ c *Client }

func (w contextualClient) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	return w.c.Do(req.WithContext(ctx))
}

// Compile-time assertion that the wrapper implements ContextualClient.
var _ httpclient.ContextualClient = contextualClient{}
