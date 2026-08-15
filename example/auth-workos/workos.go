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
package authworkos

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
