package authmw

import (
	"errors"
	"net/http"
	"strings"

	"github.com/slam0504/go-ddd-core/pkg/errorsx"
	"github.com/slam0504/go-ddd-core/pkg/errorsx/httpx"
	"github.com/slam0504/go-ddd-core/ports/auth"
)

// Middleware is a standard net/http decorator. Apply the value returned by New
// to the handler (or mux) passed to httpstdlib.New.
type Middleware func(http.Handler) http.Handler

// ErrNilVerifier is returned by New when the verifier is a nil interface value
// or a nil auth.TokenVerifierFunc. See New for the bounds of this guard.
var ErrNilVerifier = errors.New("authmw: verifier must be a non-nil auth.TokenVerifier")

// New builds bearer-authentication middleware around verifier. On each request
// it extracts the token, calls verifier.Verify with the request context, and on
// success stores the resulting auth.Identity in the context (readable downstream
// via auth.IdentityFromContext) before invoking the next handler. Any extraction
// or verification failure is written by the error responder and next is not
// called.
//
// The verifier must be a non-nil auth.TokenVerifier interface value. New rejects
// two nil shapes at construction time so the failure surfaces at startup rather
// than as a nil-dereference on the first request:
//
//   - a literal-nil interface (New(nil, ...)),
//   - a nil auth.TokenVerifierFunc (the core helper most easily left typed-nil).
//
// Other typed-nil concrete verifiers (e.g. a nil *myVerifier) are NOT detected —
// catching them would require reflection, which a constructor should not carry;
// such a value fails loudly on its first Verify call instead.
func New(verifier auth.TokenVerifier, opts ...Option) (Middleware, error) {
	if verifier == nil {
		return nil, ErrNilVerifier
	}
	if fn, ok := verifier.(auth.TokenVerifierFunc); ok && fn == nil {
		return nil, ErrNilVerifier
	}

	cfg := newConfig()
	for _, opt := range opts {
		opt(cfg)
	}

	mw := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, err := cfg.extractor(r)
			if err != nil {
				cfg.responder(w, r, err)
				return
			}
			if token == "" {
				// An empty token with no error (common from a custom extractor)
				// is treated as missing — never handed to Verify.
				cfg.responder(w, r, auth.ErrTokenMissing)
				return
			}

			id, err := verifier.Verify(r.Context(), token)
			if err != nil {
				cfg.responder(w, r, err)
				return
			}

			r = r.WithContext(auth.WithIdentity(r.Context(), id))
			next.ServeHTTP(w, r)
		})
	}
	return mw, nil
}

// defaultBearerExtractor reads the token from a single Authorization: Bearer
// header. It is deliberately strict and performs no trimming, because a JWT is
// base64url and never contains whitespace:
//
//   - zero or more-than-one Authorization header → ambiguous → ErrTokenMissing
//     (Header.Values, not Header.Get, so multiple headers are not silently
//     reduced to the first),
//   - the value is split on the first space; reject when there is no space, the
//     scheme is not "Bearer" (case-insensitive), the token is empty, or the
//     token contains any whitespace.
//
// All rejections map to auth.ErrTokenMissing → 401.
func defaultBearerExtractor(r *http.Request) (string, error) {
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return "", auth.ErrTokenMissing
	}
	scheme, token, ok := strings.Cut(values[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" || strings.ContainsAny(token, " \t\r\n") {
		return "", auth.ErrTokenMissing
	}
	return token, nil
}

// defaultErrorResponder writes a sanitized JSON error and, on 401, the RFC 6750
// WWW-Authenticate challenge. It never reflects an uncoded error's text:
//
//   - errors carrying an *errorsx.Error (core auth sentinels and their %w
//     wrappers) keep their coded status and message via httpx.WriteJSON,
//   - everything else collapses to a fixed 500 "internal error" so a custom
//     verifier cannot leak token/claim content into the response body.
//
// On a 401 the challenge is set before the body: bare "Bearer" for a missing
// token (RFC 6750 §3, no error code when no credentials were presented) and
// Bearer error="invalid_token" for invalid/expired tokens (§3.1). No realm or
// error_description is emitted — the former is service-specific, the latter can
// leak verification detail. Non-401 responses carry no challenge.
func defaultErrorResponder(w http.ResponseWriter, _ *http.Request, err error) {
	var coded *errorsx.Error
	if !errors.As(err, &coded) {
		httpx.WriteJSON(w, errorsx.New(errorsx.CodeUnknown, "internal error"))
		return
	}

	if coded.Code == errorsx.CodeUnauthorized {
		if errors.Is(err, auth.ErrTokenMissing) {
			w.Header().Set("WWW-Authenticate", "Bearer")
		} else {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		}
	}
	httpx.WriteJSON(w, err)
}
