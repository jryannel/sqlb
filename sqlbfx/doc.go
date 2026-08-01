// Package sqlbfx assembles sqlb inside a uber-go/fx application, and owns the
// contracts that make modules pluggable.
//
// It is the promotion of example/fxapp's hand-written glue into a module with
// an owner (ADR-0044): the pool and its lifetime, the migration runner over a
// value group, the hook registry assembled from what feature modules
// contribute, the scoped and unscoped handles, and an HTTP surface — chi, a
// Huma API, a server whose lifetime fx manages.
//
// The contract is four value-group element types. A feature module provides
// some subset of them and nothing else:
//
//	sqlbfx.ProvideHooks(func(dir *spaces.Directory) sqlbfx.HookSet { ... })
//	sqlbfx.ProvideMigrations(func() sqlbfx.MigrationSet { ... })
//	sqlbfx.ProvideMiddleware(func(cfg Config) sqlbfx.MiddlewareSet { ... })
//	sqlbfx.ProvideOperations(func(db *sqlb.DB) sqlbfx.OperationSet { ... })
//
// Two properties the kit preserves are the reason it exists rather than being
// copied per application. A refused mount is a boot failure: OperationSet's
// Register returns an error, and the error sqlb raises for a Scoped resource
// with no confining hook (ADR-0030) reaches fx and stops the process, naming
// the module. And ordering is a dependency edge: the Migrated value means
// "every registered migration set has been applied", the handles take one,
// so nothing can query a table that does not exist yet — in any module-list
// order.
//
// The kit is opinionated where the engine is not: chi for the router, humachi
// for the API, goose for the migration runner, log/slog for the log. An
// application that holds different opinions writes its own kit; this
// package's source is small and states what such a kit must preserve.
//
// Configuration is the application's business. DBConfig and HTTPConfig are
// plain structs the application provides (fx.Supply, or a constructor that
// reads whatever the application reads); the kit reads no environment
// variable. The logger is optional: a *slog.Logger in the graph is used,
// otherwise slog.Default() — the kit never provides one.
package sqlbfx
