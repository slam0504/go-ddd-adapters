package authjwt

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/x509"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/slam0504/go-ddd-core/ports/auth"
)

// Algorithm families. The family a key selects is the set of algorithms its key
// type can technically verify; WithAllowedAlgorithms may narrow it further, but
// never across families (that is how RS256↔HS256 alg-confusion is rejected at
// construction time).
var (
	hmacFamilyAlgs = []string{"HS256", "HS384", "HS512"}
	rsaFamilyAlgs  = []string{"RS256", "RS384", "RS512"}
)

// hmacKeyBytes is the RFC 7518 §3.2 minimum secret length per HMAC algorithm:
// the secret MUST be at least as long as the hash output.
var hmacKeyBytes = map[string]int{
	"HS256": 32,
	"HS384": 48,
	"HS512": 64,
}

// Verifier turns a signed JWT into a verified auth.Identity. It holds an
// immutable, deep-copied key and a fixed parser option set, so it is safe for
// concurrent use: Verify allocates fresh claims per call and never mutates the
// Verifier.
type Verifier struct {
	key         any // []byte | *rsa.PublicKey | *ecdsa.PublicKey
	parserOpts  []jwt.ParserOption
	tenantClaim string
	rolesClaim  string

	// now is the clock used for exp/nbf/leeway. It defaults to time.Now and is
	// swapped only by tests (see export_test.go) for deterministic time windows.
	now func() time.Time
}

var _ auth.TokenVerifier = (*Verifier)(nil)

// New builds a Verifier from opts. Exactly one key family must be configured
// (WithHMACSecret / WithRSAPublicKey / WithECDSAPublicKey); zero, duplicate, or
// mixed key options return an error. All validation happens here so that
// failures surface at startup rather than on the first request.
func New(opts ...Option) (*Verifier, error) {
	cfg := &config{rolesClaim: defaultRolesClaim}
	for _, opt := range opts {
		opt(cfg)
	}

	if cfg.keyCalls == 0 {
		return nil, errors.New("authjwt: no key configured; set exactly one of WithHMACSecret, WithRSAPublicKey, or WithECDSAPublicKey")
	}
	if cfg.keyCalls > 1 {
		return nil, errors.New("authjwt: a key was configured more than once; a Verifier accepts exactly one key family, set exactly once")
	}

	familyAlgs, key, err := resolveKey(cfg)
	if err != nil {
		return nil, err
	}

	effective, err := effectiveAlgs(cfg, familyAlgs)
	if err != nil {
		return nil, err
	}

	if cfg.family == familyHMAC {
		floor := maxHMACKeyBytes(effective)
		if len(cfg.hmacSecret) < floor {
			return nil, fmt.Errorf("authjwt: HMAC secret is %d bytes; %v require at least %d (RFC 7518 §3.2)", len(cfg.hmacSecret), effective, floor)
		}
		key = cfg.hmacSecret // already a defensive copy made in WithHMACSecret
	}

	if cfg.issuerSet && cfg.issuer == "" {
		return nil, errors.New("authjwt: WithIssuer requires a non-empty issuer")
	}
	if cfg.audienceSet && cfg.audience == "" {
		return nil, errors.New("authjwt: WithAudience requires a non-empty audience")
	}

	v := &Verifier{
		key:         key,
		tenantClaim: cfg.tenantClaim,
		rolesClaim:  cfg.rolesClaim,
		now:         time.Now,
	}

	parserOpts := []jwt.ParserOption{
		jwt.WithValidMethods(effective),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(func() time.Time { return v.now() }),
	}
	if cfg.issuerSet {
		parserOpts = append(parserOpts, jwt.WithIssuer(cfg.issuer))
	}
	if cfg.audienceSet {
		parserOpts = append(parserOpts, jwt.WithAudience(cfg.audience))
	}
	if cfg.leeway > 0 {
		parserOpts = append(parserOpts, jwt.WithLeeway(cfg.leeway))
	}
	v.parserOpts = parserOpts

	return v, nil
}

// resolveKey validates the configured key's structure and returns the family's
// algorithm set together with a deep-copied key. HMAC length validation is
// deferred to New because the floor depends on the effective algorithm set.
func resolveKey(cfg *config) (familyAlgs []string, key any, err error) {
	switch cfg.family {
	case familyHMAC:
		return hmacFamilyAlgs, nil, nil // key copied in New after length check
	case familyRSA:
		k := cfg.rsaKey
		if k == nil {
			return nil, nil, errors.New("authjwt: WithRSAPublicKey requires a non-nil key")
		}
		if k.N == nil || k.N.Sign() <= 0 {
			return nil, nil, errors.New("authjwt: RSA public key has an invalid modulus")
		}
		if k.N.BitLen() < 2048 {
			return nil, nil, fmt.Errorf("authjwt: RSA modulus is %d bits; a minimum of 2048 is required (NIST SP 800-57)", k.N.BitLen())
		}
		if k.E < 3 || k.E%2 == 0 {
			return nil, nil, fmt.Errorf("authjwt: RSA public exponent %d is invalid; it must be odd and at least 3", k.E)
		}
		return rsaFamilyAlgs, &rsa.PublicKey{N: new(big.Int).Set(k.N), E: k.E}, nil
	case familyECDSA:
		k := cfg.ecdsaKey
		if k == nil {
			return nil, nil, errors.New("authjwt: WithECDSAPublicKey requires a non-nil key")
		}
		if k.Curve == nil {
			return nil, nil, errors.New("authjwt: ECDSA public key is missing its curve")
		}
		var alg string
		switch k.Curve {
		case elliptic.P256():
			alg = "ES256"
		case elliptic.P384():
			alg = "ES384"
		case elliptic.P521():
			alg = "ES512"
		default:
			return nil, nil, fmt.Errorf("authjwt: unsupported ECDSA curve %q; only P-256, P-384, and P-521 are allowed", k.Curve.Params().Name)
		}
		// ECDH() is the non-deprecated validity check: it rejects nil
		// coordinates and off-curve / point-at-infinity keys without reading the
		// deprecated ecdsa.PublicKey.X/Y fields directly.
		if _, e := k.ECDH(); e != nil {
			return nil, nil, fmt.Errorf("authjwt: ECDSA public key is not a valid point on its curve: %w", e)
		}
		copied, e := clonePublicKey(k)
		if e != nil {
			return nil, nil, e
		}
		return []string{alg}, copied, nil
	default:
		return nil, nil, errors.New("authjwt: no key configured")
	}
}

// clonePublicKey returns an independent copy of an ECDSA public key using a
// PKIX encode/decode round-trip — the non-deprecated way to clone a public key
// in Go 1.25 — so later mutation of the caller's key cannot reach the Verifier.
// Callers must validate the key (curve + on-curve) before calling this.
func clonePublicKey(k *ecdsa.PublicKey) (*ecdsa.PublicKey, error) {
	der, err := x509.MarshalPKIXPublicKey(k)
	if err != nil {
		return nil, fmt.Errorf("authjwt: cannot encode ECDSA public key: %w", err)
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("authjwt: cannot decode ECDSA public key: %w", err)
	}
	ec, ok := parsed.(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.New("authjwt: decoded key is not an ECDSA public key")
	}
	return ec, nil
}

// effectiveAlgs returns the algorithm whitelist fed to jwt.WithValidMethods.
// Without WithAllowedAlgorithms it is the family default; with it, the option's
// entries are validated and intersected with the family set (never widened,
// never cross-family).
func effectiveAlgs(cfg *config, familyAlgs []string) ([]string, error) {
	if !cfg.allowedAlgsSet {
		return familyAlgs, nil
	}
	if len(cfg.allowedAlgs) == 0 {
		return nil, errors.New("authjwt: WithAllowedAlgorithms requires at least one algorithm")
	}
	seen := make(map[string]bool, len(cfg.allowedAlgs))
	narrowed := make([]string, 0, len(cfg.allowedAlgs))
	for _, a := range cfg.allowedAlgs {
		if strings.TrimSpace(a) == "" {
			return nil, errors.New("authjwt: WithAllowedAlgorithms contains an empty algorithm")
		}
		if !slices.Contains(familyAlgs, a) {
			return nil, fmt.Errorf("authjwt: algorithm %q is not valid for the configured key family %v", a, familyAlgs)
		}
		if !seen[a] {
			seen[a] = true
			narrowed = append(narrowed, a)
		}
	}
	return narrowed, nil
}

func maxHMACKeyBytes(algs []string) int {
	floor := 0
	for _, a := range algs {
		if n := hmacKeyBytes[a]; n > floor {
			floor = n
		}
	}
	return floor
}

// Verify implements auth.TokenVerifier. An empty token returns
// auth.ErrTokenMissing (checked before ctx so the core contract holds even on a
// cancelled context). A cancelled ctx returns the raw ctx.Err(), not an auth
// sentinel, because cancellation is not a token problem. Otherwise the token is
// parsed and verified, and golang-jwt errors are mapped to the auth sentinels.
func (v *Verifier) Verify(ctx context.Context, token string) (auth.Identity, error) {
	if token == "" {
		return auth.Identity{}, auth.ErrTokenMissing
	}
	if err := ctx.Err(); err != nil {
		return auth.Identity{}, err
	}

	claims := jwt.MapClaims{}
	if _, err := jwt.ParseWithClaims(token, claims, v.keyfunc, v.parserOpts...); err != nil {
		return auth.Identity{}, mapVerifyError(err)
	}

	return v.identity(claims)
}

// keyfunc returns the verification key. Algorithm validity is already enforced
// by jwt.WithValidMethods before keyfunc runs, so no per-token method check is
// needed here.
func (v *Verifier) keyfunc(*jwt.Token) (any, error) {
	return v.key, nil
}

// identity maps verified claims to an auth.Identity. The full raw claim map is
// exposed as Identity.Claims; the caller must treat nested values as read-only
// (core's Identity.clone shallow-copies the top-level map only). Because claims
// are parsed fresh per Verify, no map is shared across requests.
func (v *Verifier) identity(claims jwt.MapClaims) (auth.Identity, error) {
	sub, err := claims.GetSubject()
	if err != nil || sub == "" {
		return auth.Identity{}, fmt.Errorf("%w: subject claim is missing, empty, or not a string", auth.ErrTokenInvalid)
	}

	id := auth.Identity{
		Subject: sub,
		Roles:   extractRoles(claims[v.rolesClaim]),
		Claims:  map[string]any(claims),
	}
	if v.tenantClaim != "" {
		if t, ok := claims[v.tenantClaim].(string); ok {
			id.TenantID = t
		}
	}
	return id, nil
}

// extractRoles reads coarse role labels leniently: a JSON array keeps its
// string elements in order (non-string elements are skipped), a single string
// becomes a one-element slice (not split on whitespace), and any other shape
// (including a missing claim) yields nil.
func extractRoles(v any) []string {
	switch r := v.(type) {
	case string:
		return []string{r}
	case []any:
		out := make([]string, 0, len(r))
		for _, e := range r {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	default:
		return nil
	}
}

// mapVerifyError translates a golang-jwt error into a core auth sentinel. Only
// an expired exp maps to ErrTokenExpired; everything else (malformed, bad
// signature, disallowed algorithm, nbf-not-yet-valid, missing required claim,
// failed iss/aud) maps to ErrTokenInvalid. The cause is wrapped with %w so
// downstream errors.Is/As still works, while the coded 401 message stays
// generic and leaks no token content.
func mapVerifyError(err error) error {
	if errors.Is(err, jwt.ErrTokenExpired) {
		return fmt.Errorf("%w: %w", auth.ErrTokenExpired, err)
	}
	return fmt.Errorf("%w: %w", auth.ErrTokenInvalid, err)
}
