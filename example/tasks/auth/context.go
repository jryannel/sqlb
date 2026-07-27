package auth

import (
	"context"
	"errors"
)

// The context is the only channel between HTTP middleware and a sqlb hook.
//
// A BeforeQuery hook is handed a context and a query builder and nothing else,
// which is deliberate: it is what lets one registration apply to every read of
// a model — the ones generated REST handlers issue, the ones a background job
// issues, the ones a hand-written endpoint issues — without any of them being
// aware of it. The price is that whatever the hook needs must be in the
// context, put there by whoever knows: here, the middleware that verified the
// token.

type claimsKey struct{}

// ErrNoClaims is what a hook returns when it is asked to scope a query and the
// context carries no identity.
//
// It is an error rather than an empty scope on purpose. The alternative —
// treating "no tenant" as "no restriction" — turns every path that forgets to
// authenticate into a full table scan across every tenant, which is the failure
// this whole arrangement exists to make impossible. Failing closed means a
// missing token produces an error the caller sees, not a leak nobody sees.
var ErrNoClaims = errors.New("auth: request carries no authenticated identity")

// WithClaims returns a context carrying the verified claims.
func WithClaims(ctx context.Context, c Claims) context.Context {
	return context.WithValue(ctx, claimsKey{}, c)
}

// FromContext returns the claims the middleware verified, if any.
func FromContext(ctx context.Context) (Claims, bool) {
	c, ok := ctx.Value(claimsKey{}).(Claims)
	return c, ok
}

// Require returns the claims, or ErrNoClaims. It is what hooks call.
func Require(ctx context.Context) (Claims, error) {
	c, ok := FromContext(ctx)
	if !ok {
		return Claims{}, ErrNoClaims
	}
	return c, nil
}

// WorkspaceOf returns just the workspace id, which is what most hooks want.
func WorkspaceOf(ctx context.Context) (string, error) {
	c, err := Require(ctx)
	if err != nil {
		return "", err
	}
	return c.Workspace, nil
}

// Role names, ordered by what they may do. Roles are compared by rank rather
// than by set membership so that a new role can be slotted in without every
// call site being revisited.
const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"
)

var rank = map[string]int{RoleMember: 1, RoleAdmin: 2, RoleOwner: 3}

// AtLeast reports whether the claims carry role or better.
func (c Claims) AtLeast(role string) bool { return rank[c.Role] >= rank[role] }
