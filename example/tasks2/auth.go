package tasks2

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

// Caller is who a request claims to be — deliberately minimal, an API key
// and nothing else, since the point here is the wiring, not a real auth
// scheme. A real one swaps what RequireAuthForWrites puts in the context;
// nothing downstream changes.
type Caller struct {
	APIKey string
}

type callerKey struct{}

// CallerFrom reads the Caller a middleware stored in ctx — the same seam
// sqlb.TxFrom uses for a transaction. A Do func or a hook only ever receives
// a plain context.Context, so anything upstream wants visible downstream has
// to go through it.
func CallerFrom(ctx context.Context) (Caller, bool) {
	c, ok := ctx.Value(callerKey{}).(Caller)
	return c, ok
}

// RequireAuthForWrites is the "group": every operation whose method is not
// GET needs an API key; every GET does not.
//
// There is no router-level grouping primitive to reach for here —
// rest.Resource, rest.Mutation, rest.Query and rest.Action all mount onto
// the one huma.API this runs against (rest.NewServer's Server wraps a plain
// net/http.ServeMux, not a router with sub-trees), so the group is one
// middleware registration and the membership test is the condition inside
// it, the way a chi sub-router's path prefix would otherwise be.
//
// Gating on Method rather than a path prefix or a tag is deliberate: reads
// staying public and writes requiring a caller is exactly the line Query
// and Mutation already split the schema along, so the group and the
// declaration agree without either naming the other.
func RequireAuthForWrites(api huma.API) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		if ctx.Operation().Method == http.MethodGet {
			next(ctx)
			return
		}
		key := ctx.Header("X-API-Key")
		if key == "" {
			_ = huma.WriteErr(api, ctx, http.StatusUnauthorized, "X-API-Key is required for this operation")
			return
		}
		next(huma.WithValue(ctx, callerKey{}, Caller{APIKey: key}))
	}
}
