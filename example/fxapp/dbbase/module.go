// Package dbbase owns the database connection and the migration runner.
//
// It provides *sql.DB and nothing sqlb-specific: sqlb takes an Executor and
// never opens a connection of its own, so which driver is here and how the
// pool is sized is this application's business and no part of the library's
// (ADR-0040). The sqlb handle is built one layer up, in sqlbkit.
//
// Modules that own tables contribute a MigrationSet to the "migrations" value
// group. dbbase applies every registered set at startup and returns a
// Migrated value, which is how anything that touches a table states that it
// must not run first.
package dbbase

import "go.uber.org/fx"

var Module = fx.Module("dbbase",
	fx.Provide(
		NewConfig,
		NewDB,
		runMigrations,
	),
	// Force the migrations to run even when nothing depends on Migrated.
	//
	// fx is lazy: a provider is called only if something asks for its result.
	// Everything in this application does ask — the sqlb handle takes Migrated
	// — but a module list that dropped the HTTP surface would otherwise start
	// a process that connects to the database and applies nothing, which is
	// the kind of quiet difference between two module lists that is worth
	// spending one line to rule out.
	fx.Invoke(func(Migrated) {}),
)
