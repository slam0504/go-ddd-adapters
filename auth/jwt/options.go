package authjwt

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rsa"
	"time"
)

const defaultRolesClaim = "roles"

// Option configures a Verifier built via New. Options are pure setters: they
// only record intent into the config. All cross-field validation (key family
// count, algorithm intersection, key structure, security-gate emptiness) runs
// in New after every option has been applied, so option order never changes
// the result.
type Option func(*config)

// keyFamily identifies which signing-key family a Verifier was configured with.
// Exactly one family is allowed per Verifier; New rejects zero or duplicate.
type keyFamily int

const (
	familyNone keyFamily = iota
	familyHMAC
	familyRSA
	familyECDSA
)

type config struct {
	// Key material. family + keyCalls let New distinguish "no key", "one key",
	// and "key set more than once / mixed families" without relying on a field
	// being nil (an empty HMAC secret is still an HMAC family selection).
	family   keyFamily
	keyCalls int

	hmacSecret []byte
	rsaKey     *rsa.PublicKey
	ecdsaKey   *ecdsa.PublicKey

	// allowedAlgs narrows the family's algorithm set to what the issuer actually
	// signs. allowedAlgsSet distinguishes "not called" (keep family default)
	// from "called with an empty list" (a security-gate misconfiguration → New
	// returns an error).
	allowedAlgs    []string
	allowedAlgsSet bool

	// issuer/audience are security gates: set-but-empty is a misconfiguration
	// (env not injected) and must fail loud, so a Set flag is tracked separately
	// from the value.
	issuer      string
	issuerSet   bool
	audience    string
	audienceSet bool

	leeway time.Duration

	// tenantClaim is disabled by default (empty → TenantID stays ""). rolesClaim
	// defaults to "roles". Both are extraction-name options: an empty argument is
	// ignored and the default is kept (not a security gate).
	tenantClaim string
	rolesClaim  string
}

// WithHMACSecret configures the Verifier to verify HS256/HS384/HS512 tokens
// against secret. The secret is defensively copied, so later mutation of the
// caller's slice does not affect the Verifier. New enforces the RFC 7518 §3.2
// minimum length (≥ the hash size of the largest accepted HMAC algorithm).
func WithHMACSecret(secret []byte) Option {
	return func(c *config) {
		c.family = familyHMAC
		c.keyCalls++
		c.hmacSecret = bytes.Clone(secret)
	}
}

// WithRSAPublicKey configures the Verifier to verify RS256/RS384/RS512 tokens
// against key. New validates the key structure (non-nil, modulus ≥ 2048 bits,
// odd public exponent ≥ 3) and deep-copies it.
func WithRSAPublicKey(key *rsa.PublicKey) Option {
	return func(c *config) {
		c.family = familyRSA
		c.keyCalls++
		c.rsaKey = key
	}
}

// WithECDSAPublicKey configures the Verifier to verify ECDSA tokens against
// key. The curve fixes the single accepted algorithm (P-256→ES256,
// P-384→ES384, P-521→ES512). New validates the key (non-nil, supported curve,
// point on curve) and deep-copies it.
func WithECDSAPublicKey(key *ecdsa.PublicKey) Option {
	return func(c *config) {
		c.family = familyECDSA
		c.keyCalls++
		c.ecdsaKey = key
	}
}

// WithAllowedAlgorithms narrows the accepted algorithms to those the issuer
// actually signs. This is a security gate, not the nil-ignore convention used
// by extraction-name options: calling it with no arguments, or with any empty
// or blank entry, makes New return an error rather than silently widening the
// accepted set. Every algorithm must belong to the configured key family;
// cross-family entries (e.g. "HS256" with an RSA key) make New return an error.
// Not calling this option keeps the family default.
func WithAllowedAlgorithms(algs ...string) Option {
	return func(c *config) {
		c.allowedAlgsSet = true
		c.allowedAlgs = algs
	}
}

// WithIssuer requires the iss claim to equal issuer (and to be present). It is
// a security gate: an empty issuer makes New return an error. Not calling it
// disables issuer checking.
func WithIssuer(issuer string) Option {
	return func(c *config) {
		c.issuerSet = true
		c.issuer = issuer
	}
}

// WithAudience requires the aud claim to contain audience (and to be present).
// It is a security gate: an empty audience makes New return an error. Not
// calling it disables audience checking.
func WithAudience(audience string) Option {
	return func(c *config) {
		c.audienceSet = true
		c.audience = audience
	}
}

// WithLeeway sets the clock-skew tolerance applied to time-based claims
// (exp/nbf). Default is no leeway.
func WithLeeway(d time.Duration) Option {
	return func(c *config) { c.leeway = d }
}

// WithTenantClaim maps the named claim into Identity.TenantID. Disabled by
// default (TenantID stays ""). An empty name is ignored (extraction-name
// convention). When set, a missing, empty, or non-string claim leaves TenantID
// empty (lenient).
func WithTenantClaim(name string) Option {
	return func(c *config) {
		if name != "" {
			c.tenantClaim = name
		}
	}
}

// WithRolesClaim overrides the claim mapped into Identity.Roles (default
// "roles"). An empty name is ignored, keeping the default.
func WithRolesClaim(name string) Option {
	return func(c *config) {
		if name != "" {
			c.rolesClaim = name
		}
	}
}
