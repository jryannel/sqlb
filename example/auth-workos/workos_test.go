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
