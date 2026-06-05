package authjwt_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"math/big"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/slam0504/go-ddd-core/ports/auth"

	authjwt "github.com/slam0504/go-ddd-adapters/auth/jwt"
)

// refNow is the fixed clock every Verifier in this package is pinned to via
// SetNow, so exp/nbf/leeway windows are exact and never depend on the wall
// clock. Tokens express their time claims relative to it.
var refNow = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// Shared key material, generated once in TestMain. RSA generation is the only
// slow step, so it is done a single time and reused read-only across tests
// (verifiers deep-copy their key, so sharing is safe).
var (
	hmacSecret64 []byte // 64 bytes: valid for the whole HMAC family (HS512 floor).
	rsaPriv      *rsa.PrivateKey
	ecP256       *ecdsa.PrivateKey
	ecP384       *ecdsa.PrivateKey
	ecP521       *ecdsa.PrivateKey
)

func TestMain(m *testing.M) {
	hmacSecret64 = bytes.Repeat([]byte("a"), 64)

	var err error
	if rsaPriv, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
		panic(err)
	}
	if ecP256, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader); err != nil {
		panic(err)
	}
	if ecP384, err = ecdsa.GenerateKey(elliptic.P384(), rand.Reader); err != nil {
		panic(err)
	}
	if ecP521, err = ecdsa.GenerateKey(elliptic.P521(), rand.Reader); err != nil {
		panic(err)
	}

	os.Exit(m.Run())
}

// newVerifier builds a Verifier and pins it to refNow. A construction error is
// fatal: callers use this only for configurations expected to succeed.
func newVerifier(t *testing.T, opts ...authjwt.Option) *authjwt.Verifier {
	t.Helper()
	v, err := authjwt.New(opts...)
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	v.SetNow(func() time.Time { return refNow })
	return v
}

// sign mints a signed JWT with the given method and key. Signing failures are
// a test-setup bug, so they are fatal.
func sign(t *testing.T, method jwt.SigningMethod, key any, claims jwt.MapClaims) string {
	t.Helper()
	s, err := jwt.NewWithClaims(method, claims).SignedString(key)
	if err != nil {
		t.Fatalf("SignedString() error: %v", err)
	}
	return s
}

// validClaims returns a minimal claim set that satisfies the secure-by-default
// gates (present sub, present future exp). Tests mutate the returned map.
func validClaims() jwt.MapClaims {
	return jwt.MapClaims{
		"sub": "user-1",
		"exp": refNow.Add(time.Hour).Unix(),
	}
}

func assertIs(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v; want errors.Is(err, %v)", err, want)
	}
}

// --- valid tokens & claim mapping -----------------------------------------

func TestVerify_ValidHMAC_MapsFullIdentity(t *testing.T) {
	v := newVerifier(t,
		authjwt.WithHMACSecret(hmacSecret64),
		authjwt.WithTenantClaim("tenant"),
	)
	claims := validClaims()
	claims["tenant"] = "acme"
	claims["roles"] = []any{"admin", "user"}
	claims["custom"] = "kept"

	token := sign(t, jwt.SigningMethodHS256, hmacSecret64, claims)

	id, err := v.Verify(context.Background(), token)
	if err != nil {
		t.Fatalf("Verify() error: %v", err)
	}
	if id.Subject != "user-1" {
		t.Errorf("Subject = %q; want user-1", id.Subject)
	}
	if id.TenantID != "acme" {
		t.Errorf("TenantID = %q; want acme", id.TenantID)
	}
	if !slices.Equal(id.Roles, []string{"admin", "user"}) {
		t.Errorf("Roles = %v; want [admin user]", id.Roles)
	}
	if id.Claims["custom"] != "kept" {
		t.Errorf("Claims[custom] = %v; want kept (raw claim must be preserved)", id.Claims["custom"])
	}
}

func TestVerify_ValidPerFamily(t *testing.T) {
	tests := []struct {
		name   string
		newOpt authjwt.Option
		method jwt.SigningMethod
		key    any
	}{
		{"HMAC/HS256", authjwt.WithHMACSecret(hmacSecret64), jwt.SigningMethodHS256, hmacSecret64},
		{"RSA/RS256", authjwt.WithRSAPublicKey(&rsaPriv.PublicKey), jwt.SigningMethodRS256, rsaPriv},
		{"ECDSA/ES256", authjwt.WithECDSAPublicKey(&ecP256.PublicKey), jwt.SigningMethodES256, ecP256},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := newVerifier(t, tc.newOpt)
			token := sign(t, tc.method, tc.key, validClaims())
			id, err := v.Verify(context.Background(), token)
			if err != nil {
				t.Fatalf("Verify() error: %v", err)
			}
			if id.Subject != "user-1" {
				t.Errorf("Subject = %q; want user-1", id.Subject)
			}
		})
	}
}

func TestVerify_HMACFamily_AllAlgsAccepted(t *testing.T) {
	v := newVerifier(t, authjwt.WithHMACSecret(hmacSecret64))
	for _, m := range []jwt.SigningMethod{jwt.SigningMethodHS256, jwt.SigningMethodHS384, jwt.SigningMethodHS512} {
		t.Run(m.Alg(), func(t *testing.T) {
			token := sign(t, m, hmacSecret64, validClaims())
			if _, err := v.Verify(context.Background(), token); err != nil {
				t.Fatalf("Verify(%s) error: %v", m.Alg(), err)
			}
		})
	}
}

func TestVerify_RSAFamily_AllAlgsAccepted(t *testing.T) {
	v := newVerifier(t, authjwt.WithRSAPublicKey(&rsaPriv.PublicKey))
	for _, m := range []jwt.SigningMethod{jwt.SigningMethodRS256, jwt.SigningMethodRS384, jwt.SigningMethodRS512} {
		t.Run(m.Alg(), func(t *testing.T) {
			token := sign(t, m, rsaPriv, validClaims())
			if _, err := v.Verify(context.Background(), token); err != nil {
				t.Fatalf("Verify(%s) error: %v", m.Alg(), err)
			}
		})
	}
}

// --- algorithm locking: cross-family / none / narrowing -------------------

func TestVerify_CrossFamilyAndNoneRejected(t *testing.T) {
	hmacV := newVerifier(t, authjwt.WithHMACSecret(hmacSecret64))
	rsaV := newVerifier(t, authjwt.WithRSAPublicKey(&rsaPriv.PublicKey))

	// HMAC verifier receives an RS256 token: alg not in [HS*] → rejected before keyfunc.
	rsToken := sign(t, jwt.SigningMethodRS256, rsaPriv, validClaims())
	if _, err := hmacV.Verify(context.Background(), rsToken); !errors.Is(err, auth.ErrTokenInvalid) {
		t.Errorf("HMAC verifier on RS256: err = %v; want ErrTokenInvalid", err)
	}

	// RSA verifier receives an HS256 token (classic RS↔HS confusion): alg not in
	// [RS*] → rejected before the public key can be misused as an HMAC secret.
	hsToken := sign(t, jwt.SigningMethodHS256, hmacSecret64, validClaims())
	if _, err := rsaV.Verify(context.Background(), hsToken); !errors.Is(err, auth.ErrTokenInvalid) {
		t.Errorf("RSA verifier on HS256: err = %v; want ErrTokenInvalid", err)
	}

	// alg:none is rejected by every verifier.
	noneToken := sign(t, jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, validClaims())
	for name, v := range map[string]*authjwt.Verifier{"HMAC": hmacV, "RSA": rsaV} {
		if _, err := v.Verify(context.Background(), noneToken); !errors.Is(err, auth.ErrTokenInvalid) {
			t.Errorf("%s verifier on alg:none: err = %v; want ErrTokenInvalid", name, err)
		}
	}
}

func TestVerify_WithAllowedAlgorithms_Narrows(t *testing.T) {
	v := newVerifier(t,
		authjwt.WithRSAPublicKey(&rsaPriv.PublicKey),
		authjwt.WithAllowedAlgorithms("RS256"),
	)
	// RS256 stays valid.
	if _, err := v.Verify(context.Background(), sign(t, jwt.SigningMethodRS256, rsaPriv, validClaims())); err != nil {
		t.Errorf("RS256 within narrowed set: err = %v; want nil", err)
	}
	// RS384/RS512 are now outside the accepted set even though the key could verify them.
	for _, m := range []jwt.SigningMethod{jwt.SigningMethodRS384, jwt.SigningMethodRS512} {
		token := sign(t, m, rsaPriv, validClaims())
		if _, err := v.Verify(context.Background(), token); !errors.Is(err, auth.ErrTokenInvalid) {
			t.Errorf("%s outside narrowed set: err = %v; want ErrTokenInvalid", m.Alg(), err)
		}
	}
}

func TestNew_WithAllowedAlgorithms_Boundary(t *testing.T) {
	tests := []struct {
		name string
		opts []authjwt.Option
	}{
		{"cross-family alg", []authjwt.Option{authjwt.WithRSAPublicKey(&rsaPriv.PublicKey), authjwt.WithAllowedAlgorithms("HS256")}},
		{"unknown alg", []authjwt.Option{authjwt.WithRSAPublicKey(&rsaPriv.PublicKey), authjwt.WithAllowedAlgorithms("FOO")}},
		{"zero args", []authjwt.Option{authjwt.WithRSAPublicKey(&rsaPriv.PublicKey), authjwt.WithAllowedAlgorithms()}},
		{"empty string", []authjwt.Option{authjwt.WithRSAPublicKey(&rsaPriv.PublicKey), authjwt.WithAllowedAlgorithms("")}},
		{"blank string", []authjwt.Option{authjwt.WithRSAPublicKey(&rsaPriv.PublicKey), authjwt.WithAllowedAlgorithms("  ")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := authjwt.New(tc.opts...); err == nil {
				t.Fatalf("New() = nil error; want error for %s", tc.name)
			}
		})
	}
}

func TestNew_WithAllowedAlgorithms_NotCalled_KeepsFamilyDefault(t *testing.T) {
	// Not calling the option is distinct from calling it empty: the family
	// default stays in effect, so a non-RS256 family alg still verifies.
	v := newVerifier(t, authjwt.WithRSAPublicKey(&rsaPriv.PublicKey))
	token := sign(t, jwt.SigningMethodRS384, rsaPriv, validClaims())
	if _, err := v.Verify(context.Background(), token); err != nil {
		t.Fatalf("RS384 under family default: err = %v; want nil", err)
	}
}

func TestNew_OptionOrderIndependent(t *testing.T) {
	mk := func(opts ...authjwt.Option) *authjwt.Verifier { return newVerifier(t, opts...) }
	v1 := mk(authjwt.WithAllowedAlgorithms("RS256"), authjwt.WithRSAPublicKey(&rsaPriv.PublicKey))
	v2 := mk(authjwt.WithRSAPublicKey(&rsaPriv.PublicKey), authjwt.WithAllowedAlgorithms("RS256"))

	rs256 := sign(t, jwt.SigningMethodRS256, rsaPriv, validClaims())
	rs384 := sign(t, jwt.SigningMethodRS384, rsaPriv, validClaims())
	for _, v := range []*authjwt.Verifier{v1, v2} {
		if _, err := v.Verify(context.Background(), rs256); err != nil {
			t.Errorf("RS256: err = %v; want nil", err)
		}
		if _, err := v.Verify(context.Background(), rs384); !errors.Is(err, auth.ErrTokenInvalid) {
			t.Errorf("RS384: err = %v; want ErrTokenInvalid", err)
		}
	}
}

func TestNew_DuplicateKeyRejected(t *testing.T) {
	tests := []struct {
		name string
		opts []authjwt.Option
	}{
		{"same family twice", []authjwt.Option{authjwt.WithHMACSecret(hmacSecret64), authjwt.WithHMACSecret(hmacSecret64)}},
		{"two RSA keys", []authjwt.Option{authjwt.WithRSAPublicKey(&rsaPriv.PublicKey), authjwt.WithRSAPublicKey(&rsaPriv.PublicKey)}},
		{"mixed families", []authjwt.Option{authjwt.WithHMACSecret(hmacSecret64), authjwt.WithRSAPublicKey(&rsaPriv.PublicKey)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := authjwt.New(tc.opts...); err == nil {
				t.Fatalf("New() = nil error; want error for %s", tc.name)
			}
		})
	}
}

// --- HMAC secret minimum length (RFC 7518 §3.2), enforced at New ----------

func TestNew_HMACSecretMinLength(t *testing.T) {
	tests := []struct {
		name      string
		secretLen int
		allowed   []string // nil → family default (floor 64)
		wantErr   bool
	}{
		{"family default 63 too short", 63, nil, true},
		{"family default 64 ok", 64, nil, false},
		{"HS256 only 31 too short", 31, []string{"HS256"}, true},
		{"HS256 only 32 ok", 32, []string{"HS256"}, false},
		{"HS256+HS384 47 too short", 47, []string{"HS256", "HS384"}, true},
		{"HS256+HS384 48 ok", 48, []string{"HS256", "HS384"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := []authjwt.Option{authjwt.WithHMACSecret(bytes.Repeat([]byte("k"), tc.secretLen))}
			if tc.allowed != nil {
				opts = append(opts, authjwt.WithAllowedAlgorithms(tc.allowed...))
			}
			_, err := authjwt.New(opts...)
			if tc.wantErr && err == nil {
				t.Fatalf("New() = nil error; want error (secret too short, must fail at New not Verify)")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("New() error = %v; want nil", err)
			}
		})
	}
}

// --- time-based claims: exp / missing-exp / nbf / leeway ------------------

func TestVerify_Expired(t *testing.T) {
	v := newVerifier(t, authjwt.WithHMACSecret(hmacSecret64))
	claims := validClaims()
	claims["exp"] = refNow.Add(-time.Hour).Unix()
	token := sign(t, jwt.SigningMethodHS256, hmacSecret64, claims)

	_, err := v.Verify(context.Background(), token)
	assertIs(t, err, auth.ErrTokenExpired)
}

func TestVerify_MissingExp_IsInvalid(t *testing.T) {
	// Secure-by-default: a token with no exp is rejected (not treated as a
	// perpetual credential). It is Invalid, not Expired.
	v := newVerifier(t, authjwt.WithHMACSecret(hmacSecret64))
	claims := jwt.MapClaims{"sub": "user-1"} // no exp
	token := sign(t, jwt.SigningMethodHS256, hmacSecret64, claims)

	_, err := v.Verify(context.Background(), token)
	assertIs(t, err, auth.ErrTokenInvalid)
	if errors.Is(err, auth.ErrTokenExpired) {
		t.Errorf("missing exp must not map to ErrTokenExpired: %v", err)
	}
}

func TestVerify_NotYetValid_IsInvalidNotExpired(t *testing.T) {
	v := newVerifier(t, authjwt.WithHMACSecret(hmacSecret64))
	claims := validClaims()
	claims["nbf"] = refNow.Add(time.Hour).Unix()
	claims["exp"] = refNow.Add(2 * time.Hour).Unix()
	token := sign(t, jwt.SigningMethodHS256, hmacSecret64, claims)

	_, err := v.Verify(context.Background(), token)
	assertIs(t, err, auth.ErrTokenInvalid)
	if errors.Is(err, auth.ErrTokenExpired) {
		t.Errorf("not-yet-valid must not map to ErrTokenExpired: %v", err)
	}
}

func TestVerify_Leeway(t *testing.T) {
	v := newVerifier(t,
		authjwt.WithHMACSecret(hmacSecret64),
		authjwt.WithLeeway(5*time.Minute),
	)
	within := validClaims()
	within["exp"] = refNow.Add(-2 * time.Minute).Unix() // expired but inside leeway
	if _, err := v.Verify(context.Background(), sign(t, jwt.SigningMethodHS256, hmacSecret64, within)); err != nil {
		t.Errorf("exp within leeway: err = %v; want nil", err)
	}

	beyond := validClaims()
	beyond["exp"] = refNow.Add(-10 * time.Minute).Unix() // expired past leeway
	_, err := v.Verify(context.Background(), sign(t, jwt.SigningMethodHS256, hmacSecret64, beyond))
	assertIs(t, err, auth.ErrTokenExpired)
}

// --- malformed / bad signature / empty token ------------------------------

func TestVerify_BadSignature(t *testing.T) {
	v := newVerifier(t, authjwt.WithHMACSecret(hmacSecret64))
	other := bytes.Repeat([]byte("b"), 64)
	token := sign(t, jwt.SigningMethodHS256, other, validClaims()) // signed with the wrong secret
	_, err := v.Verify(context.Background(), token)
	assertIs(t, err, auth.ErrTokenInvalid)
}

func TestVerify_Malformed(t *testing.T) {
	v := newVerifier(t, authjwt.WithHMACSecret(hmacSecret64))
	_, err := v.Verify(context.Background(), "not-a-jwt")
	assertIs(t, err, auth.ErrTokenInvalid)
}

func TestVerify_EmptyToken(t *testing.T) {
	v := newVerifier(t, authjwt.WithHMACSecret(hmacSecret64))
	_, err := v.Verify(context.Background(), "")
	assertIs(t, err, auth.ErrTokenMissing)
}

// --- claim extraction: roles / subject / tenant ---------------------------

func TestVerify_Roles(t *testing.T) {
	tests := []struct {
		name string
		raw  any
		want []string
	}{
		{"array of strings", []any{"admin", "user"}, []string{"admin", "user"}},
		{"mixed array skips non-strings", []any{"admin", 42, "user"}, []string{"admin", "user"}},
		{"single string", "admin", []string{"admin"}},
		{"missing claim", nil, nil},
	}
	v := newVerifier(t, authjwt.WithHMACSecret(hmacSecret64))
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			claims := validClaims()
			if tc.raw != nil {
				claims["roles"] = tc.raw
			}
			id, err := v.Verify(context.Background(), sign(t, jwt.SigningMethodHS256, hmacSecret64, claims))
			if err != nil {
				t.Fatalf("Verify() error: %v", err)
			}
			if !slices.Equal(id.Roles, tc.want) {
				t.Errorf("Roles = %v; want %v", id.Roles, tc.want)
			}
		})
	}
}

func TestVerify_Subject_StrictInvalid(t *testing.T) {
	tests := []struct {
		name string
		sub  any // nil → omit claim
	}{
		{"missing", nil},
		{"empty string", ""},
		{"non-string number", 123},
	}
	v := newVerifier(t, authjwt.WithHMACSecret(hmacSecret64))
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			claims := jwt.MapClaims{"exp": refNow.Add(time.Hour).Unix()}
			if tc.sub != nil {
				claims["sub"] = tc.sub
			}
			_, err := v.Verify(context.Background(), sign(t, jwt.SigningMethodHS256, hmacSecret64, claims))
			assertIs(t, err, auth.ErrTokenInvalid)
		})
	}
}

func TestVerify_TenantClaim(t *testing.T) {
	tests := []struct {
		name   string
		raw    any // nil → omit claim
		wantID string
	}{
		{"string value", "acme", "acme"},
		{"missing", nil, ""},
		{"empty string", "", ""},
		{"non-string number", 42, ""},
	}
	v := newVerifier(t,
		authjwt.WithHMACSecret(hmacSecret64),
		authjwt.WithTenantClaim("tenant"),
	)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			claims := validClaims()
			if tc.raw != nil {
				claims["tenant"] = tc.raw
			}
			id, err := v.Verify(context.Background(), sign(t, jwt.SigningMethodHS256, hmacSecret64, claims))
			if err != nil {
				t.Fatalf("Verify() error: %v", err)
			}
			if id.TenantID != tc.wantID {
				t.Errorf("TenantID = %q; want %q", id.TenantID, tc.wantID)
			}
		})
	}
}

// --- issuer / audience gates ----------------------------------------------

func TestVerify_Issuer(t *testing.T) {
	v := newVerifier(t,
		authjwt.WithHMACSecret(hmacSecret64),
		authjwt.WithIssuer("iss-1"),
	)
	tests := []struct {
		name    string
		iss     any // nil → omit
		wantErr bool
	}{
		{"match", "iss-1", false},
		{"mismatch", "other", true},
		{"missing", nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			claims := validClaims()
			if tc.iss != nil {
				claims["iss"] = tc.iss
			}
			_, err := v.Verify(context.Background(), sign(t, jwt.SigningMethodHS256, hmacSecret64, claims))
			if tc.wantErr {
				assertIs(t, err, auth.ErrTokenInvalid)
			} else if err != nil {
				t.Errorf("Verify() error = %v; want nil", err)
			}
		})
	}
}

func TestVerify_Audience(t *testing.T) {
	v := newVerifier(t,
		authjwt.WithHMACSecret(hmacSecret64),
		authjwt.WithAudience("aud-1"),
	)
	tests := []struct {
		name    string
		aud     any // nil → omit
		wantErr bool
	}{
		{"match string", "aud-1", false},
		{"match in array", []any{"other", "aud-1"}, false},
		{"mismatch", "other", true},
		{"missing", nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			claims := validClaims()
			if tc.aud != nil {
				claims["aud"] = tc.aud
			}
			_, err := v.Verify(context.Background(), sign(t, jwt.SigningMethodHS256, hmacSecret64, claims))
			if tc.wantErr {
				assertIs(t, err, auth.ErrTokenInvalid)
			} else if err != nil {
				t.Errorf("Verify() error = %v; want nil", err)
			}
		})
	}
}

// --- context handling ------------------------------------------------------

func TestVerify_CanceledContext_ReturnsRawCtxErr(t *testing.T) {
	v := newVerifier(t, authjwt.WithHMACSecret(hmacSecret64))
	token := sign(t, jwt.SigningMethodHS256, hmacSecret64, validClaims())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := v.Verify(ctx, token)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v; want context.Canceled", err)
	}
	if errors.Is(err, auth.ErrTokenInvalid) || errors.Is(err, auth.ErrTokenMissing) {
		t.Errorf("cancellation must not map to an auth sentinel: %v", err)
	}
}

func TestVerify_EmptyTokenBeatsCanceledContext(t *testing.T) {
	v := newVerifier(t, authjwt.WithHMACSecret(hmacSecret64))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := v.Verify(ctx, "")
	assertIs(t, err, auth.ErrTokenMissing)
}

// --- concurrency (core contract: TokenVerifier must be safe for concurrent use)

func TestVerify_ConcurrentSafe(t *testing.T) {
	v := newVerifier(t, authjwt.WithHMACSecret(hmacSecret64))

	valid := sign(t, jwt.SigningMethodHS256, hmacSecret64, validClaims())

	expiredClaims := validClaims()
	expiredClaims["exp"] = refNow.Add(-time.Hour).Unix()
	expired := sign(t, jwt.SigningMethodHS256, hmacSecret64, expiredClaims)

	badSig := sign(t, jwt.SigningMethodHS256, bytes.Repeat([]byte("b"), 64), validClaims())

	cases := []struct {
		token   string
		wantErr error // nil → success
	}{
		{valid, nil},
		{expired, auth.ErrTokenExpired},
		{badSig, auth.ErrTokenInvalid},
	}

	var wg sync.WaitGroup
	for i := range 60 {
		tc := cases[i%len(cases)]
		wg.Go(func() {
			_, err := v.Verify(context.Background(), tc.token)
			if tc.wantErr == nil {
				if err != nil {
					t.Errorf("valid token concurrently: err = %v; want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("err = %v; want errors.Is(_, %v)", err, tc.wantErr)
			}
		})
	}
	wg.Wait()
}

// --- raw claims exposure ---------------------------------------------------

func TestVerify_RawClaimsExposed(t *testing.T) {
	v := newVerifier(t, authjwt.WithHMACSecret(hmacSecret64))
	claims := validClaims()
	claims["meta"] = map[string]any{"k": "v"}
	claims["list"] = []any{"a", "b"}
	token := sign(t, jwt.SigningMethodHS256, hmacSecret64, claims)

	id, err := v.Verify(context.Background(), token)
	if err != nil {
		t.Fatalf("Verify() error: %v", err)
	}
	meta, ok := id.Claims["meta"].(map[string]any)
	if !ok || meta["k"] != "v" {
		t.Errorf("Claims[meta] = %v; want nested map with k=v", id.Claims["meta"])
	}
	list, ok := id.Claims["list"].([]any)
	if !ok || len(list) != 2 || list[0] != "a" {
		t.Errorf("Claims[list] = %v; want [a b]", id.Claims["list"])
	}
}

// --- ECDSA curve → alg mapping --------------------------------------------

func TestVerify_ECDSACurves(t *testing.T) {
	tests := []struct {
		name   string
		newOpt authjwt.Option
		method jwt.SigningMethod
		key    *ecdsa.PrivateKey
	}{
		{"P-256/ES256", authjwt.WithECDSAPublicKey(&ecP256.PublicKey), jwt.SigningMethodES256, ecP256},
		{"P-384/ES384", authjwt.WithECDSAPublicKey(&ecP384.PublicKey), jwt.SigningMethodES384, ecP384},
		{"P-521/ES512", authjwt.WithECDSAPublicKey(&ecP521.PublicKey), jwt.SigningMethodES512, ecP521},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := newVerifier(t, tc.newOpt)
			token := sign(t, tc.method, tc.key, validClaims())
			if _, err := v.Verify(context.Background(), token); err != nil {
				t.Fatalf("Verify() error: %v", err)
			}
		})
	}
}

func TestVerify_ECDSAWrongCurveRejected(t *testing.T) {
	// Verifier is pinned to P-256 (→ ES256 only); an ES384 token is outside the
	// single accepted alg and is rejected.
	v := newVerifier(t, authjwt.WithECDSAPublicKey(&ecP256.PublicKey))
	token := sign(t, jwt.SigningMethodES384, ecP384, validClaims())
	_, err := v.Verify(context.Background(), token)
	assertIs(t, err, auth.ErrTokenInvalid)
}

// --- key immutability (mutating the caller's key after New is inert) -------

func TestKeyImmutability(t *testing.T) {
	t.Run("HMAC secret", func(t *testing.T) {
		secret := bytes.Repeat([]byte("s"), 64)
		token := sign(t, jwt.SigningMethodHS256, slices.Clone(secret), validClaims())

		v := newVerifier(t, authjwt.WithHMACSecret(secret))
		secret[0] ^= 0xFF // mutate the caller's slice after construction

		if _, err := v.Verify(context.Background(), token); err != nil {
			t.Fatalf("Verify() after mutating caller secret: err = %v; want nil", err)
		}
	})

	t.Run("RSA modulus", func(t *testing.T) {
		pub := &rsa.PublicKey{N: new(big.Int).Set(rsaPriv.N), E: rsaPriv.E}
		token := sign(t, jwt.SigningMethodRS256, rsaPriv, validClaims())

		v := newVerifier(t, authjwt.WithRSAPublicKey(pub))
		pub.N.SetInt64(1) // corrupt the caller's modulus after construction

		if _, err := v.Verify(context.Background(), token); err != nil {
			t.Fatalf("Verify() after mutating caller RSA N: err = %v; want nil", err)
		}
	})

	t.Run("ECDSA coordinate", func(t *testing.T) {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatalf("GenerateKey: %v", err)
		}
		token := sign(t, jwt.SigningMethodES256, k, validClaims())

		v := newVerifier(t, authjwt.WithECDSAPublicKey(&k.PublicKey))
		k.PublicKey.X.SetInt64(1) //nolint:staticcheck // deliberately corrupt the caller's deprecated coordinate to prove the Verifier deep-copied

		if _, err := v.Verify(context.Background(), token); err != nil {
			t.Fatalf("Verify() after mutating caller ECDSA X: err = %v; want nil", err)
		}
	})
}

// --- New() key-structure boundaries ---------------------------------------

func TestNew_KeyBoundaries(t *testing.T) {
	tests := []struct {
		name string
		opts []authjwt.Option
	}{
		{"no key", nil},
		{"nil RSA key", []authjwt.Option{authjwt.WithRSAPublicKey(nil)}},
		{"nil ECDSA key", []authjwt.Option{authjwt.WithECDSAPublicKey(nil)}},
		{"RSA N==nil", []authjwt.Option{authjwt.WithRSAPublicKey(&rsa.PublicKey{N: nil, E: 65537})}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := authjwt.New(tc.opts...); err == nil {
				t.Fatalf("New() = nil error; want error for %s", tc.name)
			}
		})
	}
}

func TestNew_RSAKeyBoundary(t *testing.T) {
	weak, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("GenerateKey(1024): %v", err)
	}
	tests := []struct {
		name    string
		key     *rsa.PublicKey
		wantErr bool
	}{
		{"1024-bit modulus too small", &weak.PublicKey, true},
		{"even exponent", &rsa.PublicKey{N: new(big.Int).Set(rsaPriv.N), E: 4}, true},
		{"exponent below 3", &rsa.PublicKey{N: new(big.Int).Set(rsaPriv.N), E: 1}, true},
		{"2048-bit E=65537 ok", &rsa.PublicKey{N: new(big.Int).Set(rsaPriv.N), E: 65537}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := authjwt.New(authjwt.WithRSAPublicKey(tc.key))
			if tc.wantErr && err == nil {
				t.Fatalf("New() = nil error; want error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("New() error = %v; want nil", err)
			}
		})
	}
}

func TestNew_ECDSAKeyBoundary(t *testing.T) {
	tests := []struct {
		name string
		key  *ecdsa.PublicKey
	}{
		{"nil curve", &ecdsa.PublicKey{}},
		{"unsupported curve", &ecdsa.PublicKey{Curve: elliptic.P224()}},
		{"off-curve point", &ecdsa.PublicKey{Curve: elliptic.P256(), X: big.NewInt(1), Y: big.NewInt(1)}}, //nolint:staticcheck // deliberately set deprecated coords to build an off-curve key
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := authjwt.New(authjwt.WithECDSAPublicKey(tc.key)); err == nil {
				t.Fatalf("New() = nil error; want error for %s", tc.name)
			}
		})
	}
}

// --- empty-value option semantics: security gates vs extraction names ------

func TestNew_SecurityGateEmptyOptions_FailLoud(t *testing.T) {
	tests := []struct {
		name string
		opts []authjwt.Option
	}{
		{"WithIssuer empty", []authjwt.Option{authjwt.WithHMACSecret(hmacSecret64), authjwt.WithIssuer("")}},
		{"WithAudience empty", []authjwt.Option{authjwt.WithHMACSecret(hmacSecret64), authjwt.WithAudience("")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := authjwt.New(tc.opts...); err == nil {
				t.Fatalf("New() = nil error; want error for %s", tc.name)
			}
		})
	}
}

func TestNew_ExtractionNameEmptyOptions_KeepDefault(t *testing.T) {
	// Empty extraction-name options are ignored (not a security gate): New
	// succeeds, tenant stays off, roles default to "roles".
	v := newVerifier(t,
		authjwt.WithHMACSecret(hmacSecret64),
		authjwt.WithTenantClaim(""),
		authjwt.WithRolesClaim(""),
	)
	claims := validClaims()
	claims["tenant"] = "acme" // tenant extraction is off → must be ignored
	claims["roles"] = []any{"admin"}
	id, err := v.Verify(context.Background(), sign(t, jwt.SigningMethodHS256, hmacSecret64, claims))
	if err != nil {
		t.Fatalf("Verify() error: %v", err)
	}
	if id.TenantID != "" {
		t.Errorf("TenantID = %q; want empty (WithTenantClaim(\"\") keeps tenant off)", id.TenantID)
	}
	if !slices.Equal(id.Roles, []string{"admin"}) {
		t.Errorf("Roles = %v; want [admin] (WithRolesClaim(\"\") keeps default \"roles\")", id.Roles)
	}
}
