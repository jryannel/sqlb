package sqlbfx

import (
	"net/http"

	"go.uber.org/fx"
)

// Pool is the pgx pool alone: opened from the application's DBConfig, closed
// by fx. Take it separately when the platform owns everything else.
func Pool() fx.Option {
	return fx.Module("sqlbfx.pool", fx.Provide(newPool))
}

// Migrations is the runner over the migrations group, producing Migrated.
func Migrations() fx.Option {
	return fx.Module("sqlbfx.migrations",
		fx.Provide(runMigrations),
		// Force the migrations to run even when nothing depends on Migrated.
		//
		// fx is lazy: a provider is called only if something asks for its
		// result. Everything downstream of a handle does ask — but a module
		// list that dropped every consumer would otherwise start a process
		// that connects to the database and applies nothing, which is the
		// kind of quiet difference between two module lists that is worth
		// spending one line to rule out.
		fx.Invoke(func(Migrated) {}),
	)
}

// Handles is the sqlb layer alone: the hook registry assembled from the hooks
// group, the scoped handle, and the Unscoped one.
//
// It consumes a *pgxpool.Pool and a Migrated wherever they come from. In a
// standalone application that is Pool() and Migrations(); in an application
// whose platform layer already owns the pool and applies migrations its own
// way — the studio-apps/core shape, where dbbase is a module of the
// platform's — the platform provides the pool and the application states the
// fact the platform established:
//
//	platform.DBModule,                  // provides *pgxpool.Pool, runs its own migrations
//	fx.Supply(sqlbfx.Migrated{}),       // "and they have run before anything queries"
//	sqlbfx.Handles(),
//
// Supplying Migrated is an assertion, and it is the application's to make:
// the kit cannot know what the platform's runner guarantees.
func Handles() fx.Option {
	return fx.Module("sqlbfx.handles",
		fx.Provide(
			newUnscoped,
			newScoped,
		),
	)
}

// DB is the database half of the kit: the pool with its lifetime, the
// migration runner over the migrations group, and the two handles. Take it
// alone for a process with no HTTP surface — a worker, a migration job.
//
// The application must provide a DBConfig (fx.Supply, or a constructor that
// reads whatever the application reads).
func DB() fx.Option {
	return fx.Options(Pool(), Migrations(), Handles())
}

// HTTP is the HTTP half: chi with the middleware group installed, a Huma API
// with the operations group registered on it, and a server whose lifetime fx
// manages.
//
// The application must provide an HTTPConfig.
func HTTP() fx.Option {
	return fx.Module("sqlbfx.http",
		fx.Provide(
			newRouter,
			newAPI,
			newServer,
		),
		// Force the server — and with it the API, the router, and every
		// registered operation — to be constructed. fx is lazy; without this
		// line nothing depends on *http.Server, so a process would boot,
		// apply its migrations, and listen on nothing at all.
		fx.Invoke(func(*http.Server) {}),
	)
}

// Module is the whole kit: DB and HTTP together. The application provides
// DBConfig and HTTPConfig, contributes to the four groups, and lists this
// beside its own modules.
func Module() fx.Option {
	return fx.Options(DB(), HTTP())
}
