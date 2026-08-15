package authworkos_test

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"testing"
	"time"

	"github.com/MicahParks/jwkset"
	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jryannel/sqlb/example/auth-workos"
)

// testKeyID is the "kid" every token mintToken signs carries, and the same
// id the keyset newTestKeyfunc builds publishes — keyfunc looks a token up
// by this id, so a mismatch here would silently fall through to "unknown
// key" rather than the case a test meant to exercise.
const testKeyID = "test-key-1"

// newTestRSAKey generates a fresh 2048-bit RSA key pair. Real key
// generation, not a shared fixture: a fixture reused across tests risks
// two tests interfering if one is ever changed to mutate its key, and
// 2048-bit generation is fast enough (single-digit milliseconds) that
// nothing is bought by sharing one.
func newTestRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	return key
}

// newTestKeyfunc builds a keyfunc.Keyfunc from key's public half,
// published under testKeyID — the same shape keyfunc.NewDefaultCtx would
// build from a live JWKS URL, but from an in-memory JSON document rather
// than an HTTP fetch, so most tests need no network and no
// httptest.Server.
func newTestKeyfunc(t *testing.T, key *rsa.PrivateKey) keyfunc.Keyfunc {
	t.Helper()
	jwk, err := jwkset.NewJWKFromKey(key.Public(), jwkset.JWKOptions{
		Metadata: jwkset.JWKMetadataOptions{KID: testKeyID},
	})
	if err != nil {
		t.Fatalf("jwkset.NewJWKFromKey: %v", err)
	}

	storage := jwkset.NewMemoryStorage()
	ctx := t.Context()
	if err := storage.KeyWrite(ctx, jwk); err != nil {
		t.Fatalf("storage.KeyWrite: %v", err)
	}
	marshalled, err := storage.Marshal(ctx)
	if err != nil {
		t.Fatalf("storage.Marshal: %v", err)
	}
	raw, err := json.Marshal(marshalled)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	kf, err := keyfunc.NewJWKSetJSON(raw)
	if err != nil {
		t.Fatalf("keyfunc.NewJWKSetJSON: %v", err)
	}
	return kf
}

// mintToken signs claims as a WorkOS-shaped RS256 access token with key,
// under testKeyID, and returns the compact JWT string.
func mintToken(t *testing.T, key *rsa.PrivateKey, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = testKeyID
	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatalf("token.SignedString: %v", err)
	}
	return signed
}

// validClaims returns a claim set Verify should accept outright. Every
// later test starts here and overrides exactly the field under test, so a
// test failing on an unrelated field is a bug in validClaims, not in the
// test — the values themselves are WorkOS's own documented example claims
// (https://workos.com/docs/reference/authkit/session-tokens), not
// invented ids.
func validClaims() jwt.MapClaims {
	now := time.Now()
	return jwt.MapClaims{
		"iss":         "https://api.workos.com",
		"sub":         "user_01HBEQKA6K4QJAS93VPE39W1JT",
		"sid":         "session_01HQSXZGF8FHF7A9ZZFCW4387R",
		"client_id":   "client_test123",
		"org_id":      "org_01HRDMC6CM357W30QMHMQ96Q0S",
		"role":        "member",
		"roles":       []string{"member"},
		"permissions": []string{"posts:read", "posts:write"},
		"iat":         now.Unix(),
		"exp":         now.Add(time.Hour).Unix(),
	}
}

// TestHarness_MintedTokenVerifiesAgainstItsOwnKeyset is not a test of
// authworkos — it is a test of the harness above, run once so a broken
// harness fails here with a clear name rather than as a mysterious
// failure in every later task's tests.
func TestHarness_MintedTokenVerifiesAgainstItsOwnKeyset(t *testing.T) {
	key := newTestRSAKey(t)
	kf := newTestKeyfunc(t, key)
	token := mintToken(t, key, validClaims())

	parsed, err := jwt.Parse(token, kf.Keyfunc)
	if err != nil {
		t.Fatalf("jwt.Parse: %v", err)
	}
	if !parsed.Valid {
		t.Fatal("token did not parse as valid")
	}
}

type testPrincipal struct {
	UserID string
	OrgID  string
	Role   string
}

// newTestVerifier builds a Verifier[testPrincipal] wired directly to a
// keyfunc.Keyfunc built by the harness, via NewWithKeyfunc rather than
// New — every test in this package uses this path. New's own successful
// path (workos.GetJWKSURL plus a real keyfunc.NewDefaultCtx
// fetch) is never exercised against a live endpoint anywhere in this
// suite, on purpose: the Global Constraints forbid a live WorkOS call in
// CI, and NewWithKeyfunc exists specifically so the rest of the package's
// tests do not need one. What New adds beyond NewWithKeyfunc — building
// the URL and making the fetch — has no branch of its own for a test to
// exercise without either a live endpoint or refactoring the URL into an
// injectable parameter, which the spec's illustrative constructor
// signature (New(ctx, clientID, mapper)) does not have room for. New's
// two validation checks (empty clientID, nil mapper) return before any
// network call and are tested below.
func newTestVerifier(t *testing.T, key *rsa.PrivateKey, clientID string) *authworkos.Verifier[testPrincipal] {
	t.Helper()
	kf := newTestKeyfunc(t, key)
	v := authworkos.NewWithKeyfunc(kf, clientID, func(c authworkos.Claims) testPrincipal {
		return testPrincipal{UserID: c.Subject, OrgID: c.OrgID, Role: c.Role}
	})
	return v
}

func TestVerify_Accepts(t *testing.T) {
	key := newTestRSAKey(t)
	v := newTestVerifier(t, key, "client_test123")
	token := mintToken(t, key, validClaims())

	principal, err := v.Verify(t.Context(), token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	want := testPrincipal{
		UserID: "user_01HBEQKA6K4QJAS93VPE39W1JT",
		OrgID:  "org_01HRDMC6CM357W30QMHMQ96Q0S",
		Role:   "member",
	}
	if principal != want {
		t.Fatalf("principal = %+v, want %+v", principal, want)
	}
}

func TestNew_RejectsEmptyClientID(t *testing.T) {
	_, err := authworkos.New(t.Context(), "", func(c authworkos.Claims) testPrincipal {
		return testPrincipal{}
	})
	if err == nil {
		t.Fatal("New accepted an empty clientID")
	}
}

func TestNew_RejectsNilMapper(t *testing.T) {
	_, err := authworkos.New[testPrincipal](t.Context(), "client_test123", nil)
	if err == nil {
		t.Fatal("New accepted a nil mapper")
	}
}
