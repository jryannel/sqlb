package sqlbfx

import "context"

// The principal seam: the one contract between "who is calling" and "what
// confines the query", so that swapping the authentication mechanism touches
// no hook.
//
// An auth module's middleware resolves credentials to a principal — whatever
// type the application chooses; a tenant id, a user struct — and stores it
// with WithPrincipal. Scoping hooks read it back by type with PrincipalFrom.
// The two ends never name each other; they name the type.
//
// The contract is deliberately free of fx types. If a non-fx consumer wants
// it, it moves to the engine and this package re-exports it (ADR-0044).

type principalKey struct{}

// WithPrincipal returns a context carrying p as the request's principal.
//
// Middleware calls this once, after verifying whatever the request presented.
// Storing an unverified value here defeats every hook that trusts it, which
// is the same property the example's access module states about a plain
// X-Tenant header: a boundary a caller can name is a convention, not a
// boundary.
func WithPrincipal(ctx context.Context, p any) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the principal as a T, and whether one of that type
// was stored.
//
// The two failure modes are deliberately one answer: no principal stored, and
// a principal of a different type, both report false. A hook that needs the
// distinction is coupling itself to which middleware ran — the thing this
// seam exists to prevent.
func PrincipalFrom[T any](ctx context.Context) (T, bool) {
	p, ok := ctx.Value(principalKey{}).(T)
	return p, ok
}
