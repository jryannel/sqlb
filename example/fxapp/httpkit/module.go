// Package httpkit owns the router, the Huma API mounted on it, and the
// server's lifetime.
//
// Modules contribute middleware to the "http-middleware" group and operations
// to the "http-operations" group. Neither group is ordered by module list
// position: middleware carries an explicit Order, and operations are
// registered on an API that cannot exist before the handle they query has been
// built.
//
// rest mounts onto a huma.API the application builds rather than handing one
// back, which is what makes this module possible at all: the router is chi
// because this application wants chi's middleware, the API is humachi, and
// none of that is a decision sqlb participates in.
package httpkit

import (
	"net/http"

	"go.uber.org/fx"
)

var Module = fx.Module("httpkit",
	fx.Provide(
		NewConfig,
		fx.Annotate(NewRouter, fx.ParamTags(``, `group:"http-middleware"`)),
		fx.Annotate(NewAPI, fx.ParamTags(``, ``, ``, `group:"http-operations"`)),
		NewServer,
	),
	// Force the server — and with it the API, the router, and every registered
	// operation — to be constructed.
	//
	// fx is lazy. Without this line nothing depends on *http.Server, so a
	// process would boot, apply its migrations, and listen on nothing at all.
	fx.Invoke(func(*http.Server) {}),
)
