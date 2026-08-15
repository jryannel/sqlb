// Package authworkos verifies WorkOS AuthKit access tokens — JWTs WorkOS
// signs and hands to a client after login, presented back to this
// application as a bearer credential — against WorkOS's per-client JWKS
// endpoint.
//
// It implements sqlb.Verifier[T] (see auth.go in the sqlb module), so it
// plugs into sqlb.Middleware[T] exactly like this repository's other
// worked auth examples: example/tasks/auth's hand-rolled HS256 JWT and
// example/fxapp/access's shared-secret bearer keys. This one verifies
// tokens WorkOS minted rather than tokens the application mints itself,
// which is why it needs a real JWT/JWKS library
// (github.com/golang-jwt/jwt/v5, github.com/MicahParks/keyfunc/v3) instead
// of the ~100 lines example/tasks/auth writes by hand for a single fixed
// HS256 secret.
//
// # Why a separate module
//
// sqlb core is dependency-locked to pgx only; this package needs WorkOS's
// SDK plus a JWT/JWKS library, none of which sqlb's own go.mod may carry.
// It lives under example/ with its own go.mod for exactly that reason —
// see docs/architecture.md's "A Verifier composes with the principal
// seam" decision.
//
// # Why Verify never returns sqlb.TransientError
//
// golang-jwt/jwt/v5 wraps any error the Keyfunc callback returns as
// jwt.ErrTokenUnverifiable, and keyfunc's own top-level error
// (keyfunc.ErrKeyfunc) does not distinguish "the JWKS was fetched but
// doesn't contain this key" from "the JWKS could not be fetched at all" —
// both wrap the same way. There is no reliable sentinel to tell a WorkOS
// outage apart from an unrecognized signing key, and string-matching an
// error message to guess would be worse than not guessing. So every
// rejection Verify makes, including an unknown key, answers as a plain
// error (401 via sqlb.Middleware). The one place a WorkOS-unreachable
// failure surfaces cleanly is New, which fetches the JWKS synchronously
// at construction and returns an ordinary error if that fails — handled
// as a startup failure, not a per-request one.
package authworkos

import (
	"context"
	"errors"
	"fmt"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
	"github.com/workos/workos-go/v10"
)

// issuer is fixed across every WorkOS environment and client — WorkOS does
// not scope it per application — so it is a constant here rather than a
// configuration field.
const issuer = "https://api.workos.com"

// Claims is what Verify extracts from a validated WorkOS access token,
// named after the fields WorkOS documents as stable
// (https://workos.com/docs/reference/authkit/session-tokens). The token's
// registered claims — iss, exp, iat — are checked during verification and
// not carried forward: a caller that already trusts a Claims value has no
// use for re-inspecting them.
type Claims struct {
	// Subject is the WorkOS user id — "sub".
	Subject string
	// SessionID is the WorkOS session id — "sid".
	SessionID string
	// ClientID is checked against the Verifier's configured client id
	// during verification; it is carried into Claims for a mapper that
	// wants to log or assert it, not because a caller needs to re-check it.
	ClientID string
	// OrgID is the organization the token is scoped to — "org_id". WorkOS
	// is organization-centric, so this is usually the field an
	// application's principal type is built around.
	OrgID string
	// Role is the caller's role in OrgID — "role".
	Role string
	// Roles is the same information as Role, pluralised — "roles". WorkOS's
	// docs show both; which one an application reads is the mapper's
	// choice, not this package's.
	Roles []string
	// Permissions are the fine-grained grants attached to the token —
	// "permissions".
	Permissions []string
}

// Verifier implements sqlb.Verifier[T]: it verifies a WorkOS AuthKit
// access token and maps the result to T via the mapper supplied to New.
type Verifier[T any] struct {
	jwks     keyfunc.Keyfunc
	clientID string
	mapper   func(Claims) T
}

// New returns a Verifier that checks tokens against clientID's JWKS
// endpoint (https://api.workos.com/sso/jwks/<clientID>, built by
// workos.GetJWKSURL) and maps a verified token's Claims to T via mapper.
//
// ctx should be long-lived — an application's own root context, not a
// per-request one — because keyfunc ties its background key-set refresh
// to it: cancelling ctx after New returns stops that refresh, not just
// the initial fetch.
func New[T any](ctx context.Context, clientID string, mapper func(Claims) T) (*Verifier[T], error) {
	if clientID == "" {
		return nil, errors.New("authworkos: clientID is empty")
	}
	if mapper == nil {
		return nil, errors.New("authworkos: mapper is nil")
	}
	jwksURL := workos.GetJWKSURL("", clientID)
	jwks, err := keyfunc.NewDefaultCtx(ctx, []string{jwksURL})
	if err != nil {
		return nil, fmt.Errorf("authworkos: fetching JWKS from %s: %w", jwksURL, err)
	}
	return NewWithKeyfunc(jwks, clientID, mapper), nil
}

// NewWithKeyfunc builds a Verifier from an already-constructed
// keyfunc.Keyfunc, skipping New's network fetch. It exists for tests that
// need a Verifier backed by an in-memory key set
// (keyfunc.NewJWKSetJSON) rather than a live JWKS URL — production code
// should call New.
func NewWithKeyfunc[T any](jwks keyfunc.Keyfunc, clientID string, mapper func(Claims) T) *Verifier[T] {
	return &Verifier[T]{jwks: jwks, clientID: clientID, mapper: mapper}
}

// Verify checks cred as a WorkOS AuthKit access token: signature against
// the client's JWKS, issuer, and expiry (via jwt.WithIssuer and
// jwt.WithExpirationRequired), then that the token's own client_id claim
// matches the Verifier's configured client — belt and suspenders
// alongside the JWKS URL already being client-scoped, so a question about
// whether WorkOS ever shares signing keys across clients does not have to
// be answered to trust this check.
func (v *Verifier[T]) Verify(ctx context.Context, cred string) (T, error) {
	var zero T

	claims := jwt.MapClaims{}
	_, err := jwt.ParseWithClaims(cred, claims, v.jwks.Keyfunc,
		jwt.WithIssuer(issuer),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return zero, fmt.Errorf("authworkos: %w", err)
	}

	clientID, _ := claims["client_id"].(string)
	if clientID != v.clientID {
		return zero, fmt.Errorf("authworkos: token client_id %q does not match configured client %q", clientID, v.clientID)
	}

	return v.mapper(Claims{
		Subject:     stringClaim(claims, "sub"),
		SessionID:   stringClaim(claims, "sid"),
		ClientID:    clientID,
		OrgID:       stringClaim(claims, "org_id"),
		Role:        stringClaim(claims, "role"),
		Roles:       stringSliceClaim(claims, "roles"),
		Permissions: stringSliceClaim(claims, "permissions"),
	}), nil
}

func stringClaim(claims jwt.MapClaims, key string) string {
	s, _ := claims[key].(string)
	return s
}

func stringSliceClaim(claims jwt.MapClaims, key string) []string {
	raw, ok := claims[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
