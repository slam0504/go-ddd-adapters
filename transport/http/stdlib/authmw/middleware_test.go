package authmw_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/slam0504/go-ddd-core/pkg/errorsx"
	"github.com/slam0504/go-ddd-core/pkg/errorsx/httpx"
	"github.com/slam0504/go-ddd-core/ports/auth"

	"github.com/slam0504/go-ddd-adapters/transport/http/stdlib/authmw"
)

// fakeVerifier is a recording auth.TokenVerifier double: it counts calls and
// captures the token and context it received, and returns a preset result.
type fakeVerifier struct {
	calls   int
	lastTok string
	lastCtx context.Context
	id      auth.Identity
	err     error
}

func (f *fakeVerifier) Verify(ctx context.Context, token string) (auth.Identity, error) {
	f.calls++
	f.lastTok = token
	f.lastCtx = ctx
	return f.id, f.err
}

// recordingHandler is the "next" handler: it records whether it ran and the
// Identity visible in the request context.
type recordingHandler struct {
	called bool
	gotID  auth.Identity
	hadID  bool
}

func (h *recordingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.called = true
	h.gotID, h.hadID = auth.IdentityFromContext(r.Context())
	w.WriteHeader(http.StatusOK)
}

func mustNew(t *testing.T, v auth.TokenVerifier, opts ...authmw.Option) authmw.Middleware {
	t.Helper()
	mw, err := authmw.New(v, opts...)
	if err != nil {
		t.Fatalf("authmw.New: %v", err)
	}
	return mw
}

func newRequest(authHeaders ...string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Del("Authorization")
	for _, h := range authHeaders {
		r.Header.Add("Authorization", h)
	}
	return r
}

func serve(mw authmw.Middleware, next http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mw(next).ServeHTTP(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) httpx.Body {
	t.Helper()
	var b httpx.Body
	if err := json.NewDecoder(rec.Body).Decode(&b); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return b
}

func TestNew_ValidToken_PassesIdentityToNext(t *testing.T) {
	fake := &fakeVerifier{id: auth.Identity{Subject: "user-1", Roles: []string{"admin"}}}
	next := &recordingHandler{}
	mw := mustNew(t, fake)

	rec := serve(mw, next, newRequest("Bearer abc.def.ghi"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !next.called {
		t.Fatal("next handler was not called")
	}
	if !next.hadID {
		t.Fatal("downstream handler could not read Identity from context")
	}
	if next.gotID.Subject != "user-1" {
		t.Fatalf("Identity.Subject = %q, want user-1", next.gotID.Subject)
	}
	if fake.calls != 1 {
		t.Fatalf("Verify called %d times, want 1", fake.calls)
	}
	if fake.lastTok != "abc.def.ghi" {
		t.Fatalf("Verify token = %q, want abc.def.ghi", fake.lastTok)
	}
}

func TestBearerParsing_RejectedRequestsAre401_NextNotCalled(t *testing.T) {
	cases := []struct {
		name    string
		headers []string
	}{
		{"missing header", nil},
		{"wrong scheme", []string{"Basic dXNlcjpwYXNz"}},
		{"empty token", []string{"Bearer "}},
		{"no separator", []string{"Bearer"}},
		{"double space leading whitespace", []string{"Bearer  x"}},
		{"trailing space", []string{"Bearer x "}},
		{"tab inside token", []string{"Bearer a\tb"}},
		{"newline inside token", []string{"Bearer a\nb"}},
		{"multiple authorization headers", []string{"Bearer a", "Bearer b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeVerifier{}
			next := &recordingHandler{}
			mw := mustNew(t, fake)

			rec := serve(mw, next, newRequest(tc.headers...))

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			if next.called {
				t.Fatal("next handler must not run on a rejected request")
			}
			if fake.calls != 0 {
				t.Fatalf("Verify called %d times, want 0 (extraction failed before Verify)", fake.calls)
			}
		})
	}
}

func TestBearerParsing_SchemeCaseInsensitive(t *testing.T) {
	for _, header := range []string{"bearer abc", "BEARER abc", "BeArEr abc"} {
		t.Run(header, func(t *testing.T) {
			fake := &fakeVerifier{id: auth.Identity{Subject: "u"}}
			next := &recordingHandler{}
			mw := mustNew(t, fake)

			rec := serve(mw, next, newRequest(header))

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if fake.calls != 1 || fake.lastTok != "abc" {
				t.Fatalf("Verify calls=%d token=%q, want 1 and abc", fake.calls, fake.lastTok)
			}
		})
	}
}

func TestCustomExtractor_EmptyTokenNoError_TreatedAsMissing(t *testing.T) {
	fake := &fakeVerifier{}
	next := &recordingHandler{}
	extractor := func(*http.Request) (string, error) { return "", nil }
	mw := mustNew(t, fake, authmw.WithTokenExtractor(extractor))

	rec := serve(mw, next, newRequest("Bearer ignored"))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if fake.calls != 0 {
		t.Fatalf("Verify called %d times, want 0 (empty token must not reach Verify)", fake.calls)
	}
	if next.called {
		t.Fatal("next handler must not run")
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
		t.Fatalf("WWW-Authenticate = %q, want Bearer", got)
	}
}

func TestVerifyError_MapsTo401_NextNotCalled(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		wantMsg string
	}{
		{"invalid", auth.ErrTokenInvalid, "auth: token invalid"},
		{"expired", auth.ErrTokenExpired, "auth: token expired"},
		{"wrapped invalid", fmt.Errorf("%w: jwt parse: %w", auth.ErrTokenInvalid, errors.New("signature mismatch")), "auth: token invalid"},
		{"wrapped expired", fmt.Errorf("%w: %w", auth.ErrTokenExpired, errors.New("exp=123")), "auth: token expired"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeVerifier{err: tc.err}
			next := &recordingHandler{}
			mw := mustNew(t, fake)

			rec := serve(mw, next, newRequest("Bearer abc"))

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			if next.called {
				t.Fatal("next handler must not run after a failed Verify")
			}
			body := decodeBody(t, rec)
			if body.Code != errorsx.CodeUnauthorized {
				t.Fatalf("body.Code = %q, want unauthorized", body.Code)
			}
			if body.Message != tc.wantMsg {
				t.Fatalf("body.Message = %q, want %q", body.Message, tc.wantMsg)
			}
		})
	}
}

func TestVerifyError_UncodedSanitizedTo500_NoLeak(t *testing.T) {
	secret := "boom token=eyJhbGciOi.secret.value"
	fake := &fakeVerifier{err: errors.New(secret)}
	next := &recordingHandler{}
	mw := mustNew(t, fake)

	rec := serve(mw, next, newRequest("Bearer abc"))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if next.called {
		t.Fatal("next handler must not run after a failed Verify")
	}
	body := decodeBody(t, rec)
	if body.Message != "internal error" {
		t.Fatalf("body.Message = %q, want \"internal error\"", body.Message)
	}
	if strings.Contains(rec.Body.String(), "boom") || strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("response body leaked the uncoded error: %s", rec.Body.String())
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != "" {
		t.Fatalf("WWW-Authenticate = %q, want absent on 500", got)
	}
}

func TestWWWAuthenticate_Challenge(t *testing.T) {
	const secret = "leak-eyJ-claim-detail"
	cases := []struct {
		name       string
		headers    []string
		verifyErr  error
		wantStatus int
		wantChal   string // "" means header must be absent
	}{
		{"missing token", nil, nil, http.StatusUnauthorized, "Bearer"},
		{"invalid token", []string{"Bearer abc"}, fmt.Errorf("%w: %w", auth.ErrTokenInvalid, errors.New(secret)), http.StatusUnauthorized, `Bearer error="invalid_token"`},
		{"expired token", []string{"Bearer abc"}, fmt.Errorf("%w: %w", auth.ErrTokenExpired, errors.New(secret)), http.StatusUnauthorized, `Bearer error="invalid_token"`},
		{"uncoded error", []string{"Bearer abc"}, errors.New(secret), http.StatusInternalServerError, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeVerifier{err: tc.verifyErr}
			mw := mustNew(t, fake)

			rec := serve(mw, &recordingHandler{}, newRequest(tc.headers...))

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			got := rec.Header().Get("WWW-Authenticate")
			if got != tc.wantChal {
				t.Fatalf("WWW-Authenticate = %q, want %q", got, tc.wantChal)
			}
			if strings.Contains(got, secret) {
				t.Fatalf("WWW-Authenticate leaked error detail: %q", got)
			}
		})
	}
}

func TestNew_NilVerifier(t *testing.T) {
	t.Run("literal nil interface", func(t *testing.T) {
		mw, err := authmw.New(nil)
		if !errors.Is(err, authmw.ErrNilVerifier) {
			t.Fatalf("err = %v, want ErrNilVerifier", err)
		}
		if mw != nil {
			t.Fatal("middleware must be nil when New errors")
		}
	})
	t.Run("nil TokenVerifierFunc", func(t *testing.T) {
		mw, err := authmw.New(auth.TokenVerifierFunc(nil))
		if !errors.Is(err, authmw.ErrNilVerifier) {
			t.Fatalf("err = %v, want ErrNilVerifier", err)
		}
		if mw != nil {
			t.Fatal("middleware must be nil when New errors")
		}
	})
	t.Run("nil options fall back to defaults", func(t *testing.T) {
		fake := &fakeVerifier{id: auth.Identity{Subject: "u"}}
		next := &recordingHandler{}
		mw, err := authmw.New(fake, authmw.WithTokenExtractor(nil), authmw.WithErrorResponder(nil))
		if err != nil {
			t.Fatalf("New with nil options: %v", err)
		}
		rec := serve(mw, next, newRequest("Bearer abc"))
		if rec.Code != http.StatusOK || !next.called {
			t.Fatalf("nil options should keep defaults: status=%d called=%v", rec.Code, next.called)
		}
	})
}

func TestCustomExtractorAndResponder_AppliedAndCtxPropagated(t *testing.T) {
	type ctxKey struct{}
	wantVal := "ctx-marker"

	fake := &fakeVerifier{err: auth.ErrTokenInvalid}
	extractor := func(*http.Request) (string, error) { return "fixed-token", nil }
	responder := func(w http.ResponseWriter, _ *http.Request, _ error) { w.WriteHeader(http.StatusTeapot) }
	mw := mustNew(t, fake,
		authmw.WithTokenExtractor(extractor),
		authmw.WithErrorResponder(responder),
	)

	req := newRequest()
	req = req.WithContext(context.WithValue(req.Context(), ctxKey{}, wantVal))
	rec := serve(mw, &recordingHandler{}, req)

	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want 418 (custom responder)", rec.Code)
	}
	if fake.lastTok != "fixed-token" {
		t.Fatalf("custom extractor not applied: Verify token = %q", fake.lastTok)
	}
	if got, _ := fake.lastCtx.Value(ctxKey{}).(string); got != wantVal {
		t.Fatalf("Verify received ctx value %q, want %q (request ctx must propagate)", got, wantVal)
	}
}
