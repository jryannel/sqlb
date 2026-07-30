// This file is hand-written; everything else in this package is generated.
//
// It lives here rather than in a module of its own because both things it
// provides are properties of the schema: the migration history that builds the
// tables, and the REST surface generated from the same declaration. A separate
// package would have to import this one for Register and the migrations
// package for the files, and would add a name without adding a boundary.

package store

import (
	"github.com/danielgtaylor/huma/v2"
	"go.uber.org/fx"

	"github.com/jryannel/sqlb"
	"github.com/jryannel/sqlb/example/fxapp/dbbase"
	"github.com/jryannel/sqlb/example/fxapp/httpkit"
	"github.com/jryannel/sqlb/example/fxapp/migrations"
)

var Module = fx.Module("store",
	fx.Provide(
		fx.Annotate(provideMigrations, fx.ResultTags(`group:"migrations"`)),
		fx.Annotate(provideOperations, fx.ResultTags(`group:"http-operations"`)),
	),
)

// provideMigrations registers this schema's history with dbbase.
//
// One set, where core-style platforms have one per module. The reason is a
// constraint worth knowing before splitting a schema up: sets are applied in
// alphabetical order by module name, and notes.space_id references spaces.id,
// so a "notes" set and a "spaces" set would apply in exactly the wrong order.
// Independent histories need independent tables — which is what ADR-0015's
// module prefixes are for, and what a platform that forbids cross-module
// foreign keys is buying.
func provideMigrations() dbbase.MigrationSet {
	return dbbase.MigrationSet{
		Module: "notes",
		FS:     migrations.FS(),
		Dir:    ".",
	}
}

// provideOperations mounts the generated resources.
//
// Two things about this three-line function are the example's argument.
//
// The handle it takes is the scoped one — the plain *sqlb.DB, not the
// `name:"unscoped"` one — so every generated handler queries through the hooks
// the feature modules registered. Nothing in rest_gen.go mentions a space, and
// nothing has to.
//
// And the error is returned rather than logged. Register refuses to mount a
// resource whose schema declares a Scoped column when no hook backs it
// (ADR-0030), so a module list that dropped `notes.Module` produces a boot
// failure naming store.Note. That refusal is only worth anything if it reaches
// fx, which is why httpkit.OperationSet.Register has an error in its
// signature at all.
func provideOperations(db *sqlb.DB) httpkit.OperationSet {
	return httpkit.OperationSet{
		Module:   "store",
		Register: func(api huma.API) error { return Register(api, db) },
	}
}
