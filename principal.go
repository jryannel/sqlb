package sqlb

import "context"

// The principal seam: the one contract between "who is calling" and "what
// confines the query", so that swapping the authentication mechanism touches
// no hook.
//
// A BeforeQuery hook is handed a context and a builder and nothing else, which
// is what lets one registration apply to every read of a model (see Hooks).
// The price is that whatever the hook needs must already be in the context,
// put there by whoever knows: the middleware that verified the request. That
// hand-off is a convention every application on sqlb ends up inventing —
// example/tasks wrote one, example/fxapp wrote another — and two conventions
// for one seam is one too many, because a hook written against either is a
// hook that cannot move.
//
// So the seam is here, in the engine, next to the hooks that read it. It costs
// the engine nothing: context is standard library, and ADR-0040's dependency
// stance is untouched.
//
// The principal is whatever type the application chooses — a tenant id, a
// claims struct, a user record. sqlb never inspects it; it only carries it,
// and hands it back to whoever asks for that type.

type principalKey struct{}

// WithPrincipal returns a context carrying p as the request's principal.
//
// Middleware calls this once, after verifying whatever the request presented.
// Storing an unverified value here defeats every hook that trusts it: a
// boundary the caller can name is a convention, not a boundary.
func WithPrincipal(ctx context.Context, p any) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the principal as a T, and whether one of that type was
// stored.
//
// The two failure modes are deliberately one answer: no principal stored, and
// a principal of a different type, both report false. A hook that needs to
// tell them apart is coupling itself to which middleware ran — the thing this
// seam exists to prevent.
//
// What a hook must not do is treat false as "no restriction". That turns every
// path which forgot to authenticate into a read across every tenant, which is
// the failure the whole arrangement exists to make impossible. Fail closed:
// return an error, and let the caller see it.
func PrincipalFrom[T any](ctx context.Context) (T, bool) {
	p, ok := ctx.Value(principalKey{}).(T)
	return p, ok
}
