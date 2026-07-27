// Package auth is the demo's authentication: HS256 JSON Web Tokens, password
// hashing, and the middleware that turns a bearer token into request-scoped
// claims the data layer can read.
//
// # Why this is written out rather than imported
//
// Signing and verifying an HS256 token is about a hundred lines of standard
// library, and writing them here keeps the example runnable with no
// dependency of its own — which matters because the point of the demo is the
// data layer, and a reader should be able to see the whole path from
// Authorization header to WHERE clause without leaving the module.
//
// It is not a general-purpose JWT library and should not be lifted into one.
// It handles exactly one algorithm and one token shape. A real service verifying
// tokens it did not mint — from Auth0, Keycloak, Cognito, Clerk — needs key
// rotation, a JWKS fetch and RS256/ES256 verification, and should use
// github.com/golang-jwt/jwt or github.com/go-jose/go-jose rather than extending
// this.
//
// What is *not* simplified is the checking. The three mistakes that turn a JWT
// implementation into an authentication bypass are all made in the verify path,
// and each is defended against explicitly below: the algorithm is pinned before
// the signature is used, the comparison is constant-time, and expiry is
// mandatory rather than checked only when present.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Claims is the token payload.
//
// It carries the workspace, not just the user, because the workspace is what
// scopes every query: a token is a claim to act *as* someone *in* somewhere. A
// user who belongs to three workspaces gets three different tokens and switches
// between them, rather than one token whose scope the server has to work out on
// every request.
type Claims struct {
	// Subject is the user id — "sub" by JWT convention.
	Subject string `json:"sub"`
	Email   string `json:"email"`

	// Workspace is the workspace id the token is scoped to, and Role is the
	// user's role in it, copied from the membership at issue time.
	//
	// Copying the role into the token is the usual trade: it saves a query on
	// every request and goes stale until the token expires. With a 24-hour TTL
	// that is too long for a demotion to take effect, which is a thing to
	// notice rather than to hide — see the note on Signer.TTL.
	Workspace string `json:"wsp"`
	Role      string `json:"role"`

	Issuer    string `json:"iss,omitempty"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
}

// Errors a caller may want to distinguish. Everything else is a malformed
// token, which is not worth enumerating for the client: a 401 either way, and
// telling an attacker *which* part of their forgery failed is free help.
var (
	ErrMalformed = errors.New("auth: malformed token")
	ErrAlgorithm = errors.New("auth: unexpected signing algorithm")
	ErrSignature = errors.New("auth: signature does not verify")
	ErrExpired   = errors.New("auth: token has expired")
)

// alg is the only algorithm this package accepts, in either direction.
const alg = "HS256"

// header is the fixed JOSE header. It is a constant rather than something
// marshalled per token so that signing cannot accidentally emit an algorithm
// verification does not expect.
type header struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

// Signer mints and verifies tokens against one secret.
type Signer struct {
	secret []byte
	issuer string
	ttl    time.Duration

	// now is injectable so that expiry can be tested without sleeping. Tests
	// that verify time-dependent behaviour by waiting are slow and flaky; ones
	// that cannot control the clock usually end up not verifying it at all.
	now func() time.Time
}

// NewSigner returns a Signer. The secret must be at least 32 bytes: HS256's
// security rests entirely on it, and a short one is brute-forceable offline by
// anyone holding a single token.
func NewSigner(secret []byte, issuer string, ttl time.Duration) (*Signer, error) {
	if len(secret) < 32 {
		return nil, fmt.Errorf("auth: signing secret is %d bytes, want at least 32", len(secret))
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("auth: token TTL must be positive, got %s", ttl)
	}
	return &Signer{secret: secret, issuer: issuer, ttl: ttl, now: time.Now}, nil
}

// WithClock replaces the Signer's clock. For tests.
func (s *Signer) WithClock(now func() time.Time) *Signer {
	c := *s
	c.now = now
	return &c
}

// TTL is how long an issued token stays valid.
//
// There is no refresh token and no revocation list, so this is also the window
// in which a logout, a password change or a removed membership has no effect.
// A production service closes that gap with short access tokens plus a refresh
// endpoint that checks the membership still exists; the demo does not, and says
// so rather than implying the problem does not exist.
func (s *Signer) TTL() time.Duration { return s.ttl }

// Sign returns a signed token for the claims. Subject, Workspace, IssuedAt,
// ExpiresAt and Issuer are filled in here, so a caller cannot mint a token that
// never expires by leaving a field zero.
func (s *Signer) Sign(c Claims) (string, error) {
	if c.Subject == "" || c.Workspace == "" {
		return "", errors.New("auth: a token needs both a subject and a workspace")
	}
	now := s.now()
	c.Issuer = s.issuer
	c.IssuedAt = now.Unix()
	c.ExpiresAt = now.Add(s.ttl).Unix()

	h, err := json.Marshal(header{Alg: alg, Typ: "JWT"})
	if err != nil {
		return "", err
	}
	p, err := json.Marshal(c)
	if err != nil {
		return "", err
	}

	signing := encode(h) + "." + encode(p)
	return signing + "." + encode(s.mac(signing)), nil
}

// Verify checks a token and returns its claims.
//
// The order of the checks is the part that matters. The algorithm is read and
// pinned *before* the signature is computed, so a token claiming `"alg":"none"`
// — or claiming RS256 in the hope that the verifier will treat the HMAC secret
// as a public key — is rejected without either being tried. Both are the
// classic JWT bypasses, and both are bypasses of the verifier rather than of
// the cryptography.
func (s *Signer) Verify(token string) (Claims, error) {
	var zero Claims

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return zero, ErrMalformed
	}

	rawHeader, err := decode(parts[0])
	if err != nil {
		return zero, ErrMalformed
	}
	var h header
	if err := json.Unmarshal(rawHeader, &h); err != nil {
		return zero, ErrMalformed
	}
	if h.Alg != alg {
		return zero, fmt.Errorf("%w: %q", ErrAlgorithm, h.Alg)
	}

	sig, err := decode(parts[2])
	if err != nil {
		return zero, ErrMalformed
	}
	// hmac.Equal, not bytes.Equal: a comparison that returns early on the first
	// differing byte leaks, through timing, how much of a guess was right,
	// which is enough to forge a signature one byte at a time.
	if !hmac.Equal(sig, s.mac(parts[0]+"."+parts[1])) {
		return zero, ErrSignature
	}

	rawClaims, err := decode(parts[1])
	if err != nil {
		return zero, ErrMalformed
	}
	var c Claims
	if err := json.Unmarshal(rawClaims, &c); err != nil {
		return zero, ErrMalformed
	}

	// Expiry is required, not merely respected when present. A claims struct
	// with no "exp" unmarshals to zero, and treating zero as "no expiry
	// requested" would make an omitted field into a token that never dies.
	if c.ExpiresAt == 0 {
		return zero, ErrMalformed
	}
	if !s.now().Before(time.Unix(c.ExpiresAt, 0)) {
		return zero, ErrExpired
	}
	if c.Subject == "" || c.Workspace == "" {
		return zero, ErrMalformed
	}
	return c, nil
}

func (s *Signer) mac(signing string) []byte {
	m := hmac.New(sha256.New, s.secret)
	m.Write([]byte(signing))
	return m.Sum(nil)
}

// JWT uses base64url without padding — RawURLEncoding, not URLEncoding. The
// padded variant produces "=" characters, which are legal in a URL query but
// not in a JWT segment, and a token built with them is rejected by every other
// implementation.
func encode(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func decode(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }
