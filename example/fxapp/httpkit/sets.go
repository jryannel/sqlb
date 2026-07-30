package httpkit

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

// MiddlewareSet is the value-group element a module contributes to wrap every
// request.
//
//	fx.Provide(fx.Annotate(provideMiddleware, fx.ResultTags(`group:"http-middleware"`)))
type MiddlewareSet struct {
	// Module names the contributor, for the boot log.
	Module string

	// Order decides where in the chain this sits: lower runs first, and ties
	// break on Module so the chain is the same on every boot.
	//
	// An explicit number rather than the order the group happened to arrive
	// in, because value-group order is not something fx promises — and because
	// middleware order is a correctness question. Authentication that ran
	// after the handler would be decoration.
	Order int

	// Wrap is the middleware.
	Wrap func(http.Handler) http.Handler
}

// OperationSet is the value-group element a module contributes to put
// endpoints on the shared API.
//
//	fx.Provide(fx.Annotate(provideOperations, fx.ResultTags(`group:"http-operations"`)))
//
// Register returning an error is what carries a refused mount out to the boot:
// the generated Register reports a resource whose declared scope has no hook
// behind it, and this group is how that reaches fx instead of being logged and
// stepped over.
type OperationSet struct {
	// Module names the contributor, for the boot log and the error.
	Module string

	// Register is called once, with the API every module shares.
	Register func(huma.API) error
}
