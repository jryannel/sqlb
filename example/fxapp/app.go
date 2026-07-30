// Package fxapp is the composition: the list of modules this server is made
// of, and nothing else.
//
// It is a library rather than a main so that the tests build the same
// application the binary builds. A demo whose tests exercise a different
// assembly than the one that ships is testing the tests — and with a container
// the risk is sharper than usual, because the assembly *is* the program.
//
// # What to read, and in what order
//
//	noteschema/schema.go   the source of truth: two tables, one of them a tenant
//	sqlbkit/handles.go     the hook registry, assembled from a value group
//	store/module.go        the generated resources, mounted on the scoped handle
//	notes/hooks.go         the space boundary, one registration per statement kind
//	app_test.go            the claims above, asserted — including the one that fails
package fxapp

import (
	"go.uber.org/fx"

	"github.com/jryannel/sqlb/example/fxapp/access"
	"github.com/jryannel/sqlb/example/fxapp/dbbase"
	"github.com/jryannel/sqlb/example/fxapp/httpkit"
	"github.com/jryannel/sqlb/example/fxapp/logs"
	"github.com/jryannel/sqlb/example/fxapp/notes"
	"github.com/jryannel/sqlb/example/fxapp/spaces"
	"github.com/jryannel/sqlb/example/fxapp/sqlbkit"
	"github.com/jryannel/sqlb/example/fxapp/store"
)

// Modules is the whole application.
//
// The order of this list does not matter, and that is worth stating rather
// than leaving to be discovered: fx resolves by type, so what runs before what
// is decided by the parameters each constructor declares. The list is grouped
// and commented for a reader, not for the container.
//
// What the grouping does say is where the seams are. The first group knows
// nothing about notes or spaces and would be the same in any application; the
// second is this one. That is the split a platform repository makes into two
// modules — see the studio-apps/core layout, where the first group is an
// appbase.Standard() every product composes and the second is the product.
func Modules() fx.Option {
	return fx.Options(
		// Platform. Reusable as-is: a logger, a connection and a migration
		// runner, the sqlb handles, an HTTP surface with three value groups
		// hanging off it.
		logs.Module,
		dbbase.Module,
		sqlbkit.Module,
		httpkit.Module,

		// This application. The schema's generated surface, who may speak for
		// a space, the tenant, and the feature.
		store.Module,
		access.Module,
		spaces.Module,
		notes.Module,
	)
}

// Run boots the application and blocks until it is signalled.
//
// It is the entire body of cmd/server's main. A binary that needed a flag of
// its own would call fx.New(Modules(), ...) directly rather than growing an
// argument here.
func Run(opts ...fx.Option) {
	fx.New(append([]fx.Option{Modules()}, opts...)...).Run()
}
