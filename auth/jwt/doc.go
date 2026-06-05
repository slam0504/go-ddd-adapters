// Package authjwt implements go-ddd-core ports/auth.TokenVerifier for signed
// JSON Web Tokens, verified against a static key. It is the JWT adapter a
// downstream service plugs into the auth contract; it carries no transport
// concerns (HTTP bearer extraction lives in transport/http/stdlib/authmw) and
// no authorization logic (role/permission checks are out of scope for the
// contract).
//
// # Scope
//
// This adapter verifies against a single static key family per Verifier:
//
//	WithHMACSecret      → HS256 / HS384 / HS512
//	WithRSAPublicKey    → RS256 / RS384 / RS512
//	WithECDSAPublicKey  → one of ES256 / ES384 / ES512, fixed by the curve
//
// JWKS endpoints and background key rotation are intentionally deferred to a
// later cycle. A Verifier needs exactly one key family; configuring zero, more
// than one, or the same family twice makes New return an error (set more keys
// by composing multiple Verifiers).
//
// # Algorithm locking
//
// Without WithAllowedAlgorithms a Verifier accepts every algorithm its key type
// can technically verify (the "family default"). That default is a convenience,
// not the most secure setting: it reflects what the key can verify, not what the
// issuer promises to sign. Production callers SHOULD pin the exact algorithm
// their issuer uses via WithAllowedAlgorithms (e.g. "RS256"); the accepted set
// is always intersected with the key family and can never cross it, so an
// RS256↔HS256 algorithm-confusion attack and "alg":"none" are rejected at
// construction or parse time.
//
// # Secure-by-default
//
//   - exp is required (jwt.WithExpirationRequired): a token without an
//     expiry is rejected as invalid rather than treated as a perpetual
//     credential. This cycle offers no option to disable the check.
//   - The HMAC secret must meet the RFC 7518 §3.2 minimum length (≥ the hash
//     size of the largest accepted HMAC algorithm); a too-short secret fails at
//     New (deploy time), not at Verify.
//   - RSA keys must have a modulus of at least 2048 bits (NIST SP 800-57) and an
//     odd public exponent ≥ 3.
//   - ECDSA keys must sit on a supported curve and pass an on-curve check.
//   - Security-gate options provided with an empty value (WithIssuer(""),
//     WithAudience(""), WithAllowedAlgorithms() / blank entries) make New return
//     an error rather than silently disabling a check. Extraction-name options
//     (WithTenantClaim(""), WithRolesClaim("")) instead keep their default.
//
// # Claim mapping
//
//	Identity.Subject  ← sub (missing, empty, or non-string → ErrTokenInvalid)
//	Identity.TenantID ← WithTenantClaim(name), default off → ""
//	Identity.Roles    ← WithRolesClaim(name), default "roles"
//	Identity.Claims   ← the full raw claim map, exposed as-is
//
// Identity.Claims is the raw, un-normalized claim map. core's Identity.clone
// shallow-copies only the top-level map; nested values must be treated as
// read-only by all parties. Because claims are parsed fresh on every Verify,
// no map is shared across requests.
//
// # Error mapping
//
//	empty token                         → auth.ErrTokenMissing
//	expired (exp in the past)           → auth.ErrTokenExpired
//	missing exp / nbf-not-yet-valid /
//	  bad signature / disallowed alg /
//	  malformed / failed iss or aud /
//	  invalid sub                       → auth.ErrTokenInvalid
//
// not-before (nbf) in the future maps to ErrTokenInvalid, not ErrTokenExpired:
// a not-yet-valid token has the opposite meaning of an expired one (retrying
// later succeeds). Underlying causes are wrapped with %w, but the coded 401
// surface stays generic and leaks no token content. A cancelled context returns
// the raw ctx.Err() (not an auth sentinel); an empty token returns
// ErrTokenMissing even when the context is already cancelled.
package authjwt
