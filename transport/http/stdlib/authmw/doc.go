// Package authmw provides net/http bearer-authentication middleware that wires
// a go-ddd-core auth.TokenVerifier into the request pipeline. It extracts a
// bearer token, verifies it, and on success stores the resulting auth.Identity
// in the request context for downstream handlers to read via
// auth.IdentityFromContext.
//
// # Usage
//
// New returns a standard decorator; apply it to the handler given to
// transport/http/stdlib's New (the Server itself needs no change):
//
//	verifier, err := authjwt.New(authjwt.WithRSAPublicKey(pub),
//		authjwt.WithAllowedAlgorithms("RS256"))
//	// handle err
//	protect, err := authmw.New(verifier)
//	// handle err
//	srv := httpstdlib.New(addr, protect(mux))
//
// New returns (Middleware, error) — matching authjwt.New's error-return shape —
// rather than panicking, so a misconfigured verifier fails at startup.
//
// # Token extraction (default, overridable via WithTokenExtractor)
//
// The default extractor accepts exactly one "Authorization: Bearer <token>"
// header. It is strict and does not trim, because a JWT is base64url and never
// contains whitespace. A request is rejected (→ auth.ErrTokenMissing → 401)
// when any of the following holds:
//
//   - zero, or more than one, Authorization header (multiple headers are
//     ambiguous and are not silently reduced to the first),
//   - the value has no space separating scheme and token,
//   - the scheme is not "Bearer" (matched case-insensitively),
//   - the token is empty, or contains any whitespace (space, tab, CR, LF).
//
// A custom extractor returning ("", nil) is treated as a missing token: Verify
// is not called.
//
// # Error responses (default, overridable via WithErrorResponder)
//
// The default responder sanitizes failures. Errors carrying an *errorsx.Error
// (the core auth sentinels ErrTokenMissing / ErrTokenInvalid / ErrTokenExpired
// and their %w wrappers) keep their coded HTTP status and message — auth
// sentinels render as 401 with a deliberate generic message that leaks no token
// content. Any other (uncoded) error collapses to a fixed 500 "internal error",
// so a custom verifier returning a raw error cannot leak token or claim content
// into the response body. All branching is by errors.Is / errors.As, never by
// string matching.
//
// On a 401, the responder sets an RFC 6750 WWW-Authenticate challenge before
// writing the body: bare "Bearer" for a missing token (§3 advertises no error
// code when no credentials were presented) and Bearer error="invalid_token" for
// an invalid or expired token (§3.1). No realm or error_description is emitted.
// Non-401 responses (including the sanitized 500) carry no challenge.
//
// # Scope
//
// This cycle ships only WithTokenExtractor and WithErrorResponder. There is
// deliberately no logging option: the middleware reports every failure through
// the responder, and a logger would add a surface that could accidentally record
// token or claim content. Authorization (role/permission checks) is a separate
// concern and is out of scope here.
package authmw
