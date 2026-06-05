package authmw

import "net/http"

// Option configures the middleware built by New.
type Option func(*config)

type config struct {
	extractor func(*http.Request) (string, error)
	responder func(http.ResponseWriter, *http.Request, error)
}

func newConfig() *config {
	return &config{
		extractor: defaultBearerExtractor,
		responder: defaultErrorResponder,
	}
}

// WithTokenExtractor replaces the default bearer-token extractor. The extractor
// pulls the raw token out of the request; returning a non-nil error (or an empty
// token, which the middleware treats as auth.ErrTokenMissing) short-circuits to
// the responder without calling Verify. A nil fn is ignored so the default stays
// in place, mirroring the nil-ignore convention in transport/http/stdlib options.
func WithTokenExtractor(fn func(*http.Request) (string, error)) Option {
	return func(c *config) {
		if fn != nil {
			c.extractor = fn
		}
	}
}

// WithErrorResponder replaces the default error responder, which writes a
// sanitized JSON error (coded errors keep their status/message; uncoded errors
// collapse to a fixed 500 "internal error") and sets the RFC 6750
// WWW-Authenticate challenge on 401s. Override it to change the wire shape or to
// stop advertising the bearer challenge. A nil fn is ignored so the default
// stays in place.
func WithErrorResponder(fn func(http.ResponseWriter, *http.Request, error)) Option {
	return func(c *config) {
		if fn != nil {
			c.responder = fn
		}
	}
}
