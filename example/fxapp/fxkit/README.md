# fxkit — this example's sqlb-under-fx glue

The opinionated assembly of sqlb inside an [fx](https://github.com/uber-go/fx)
application. **Copy it, don't import it**: it is a package of `example/fxapp`,
not a published module, and
[ADR-0044](../../../docs/architecture.md#the-container-is-an-adapter) records why
the published version was reversed. Its opinions — chi, humachi, goose,
`log/slog` — are load-bearing, so a library could only offer them
take-it-or-leave-it where a file you own can be adapted.

```go
fx.New(
    fx.Supply(
        fxkit.DBConfig{DSN: os.Getenv("DATABASE_URL")},
        fxkit.HTTPConfig{Title: "Notes", Version: "1.0.0"},
    ),
    fxkit.Module(),

    store.Module,   // contributes migrations + generated operations
    auth.Module,    // contributes middleware, stores the principal
    notes.Module,   // contributes hooks that read the principal
).Run()
```

A feature module contributes to four value groups and provides nothing anybody
imports:

| Contribution | Helper | What it is |
|---|---|---|
| `HookSet` | `fxkit.ProvideHooks` | the module's query/mutation rules |
| `MigrationSet` | `fxkit.ProvideMigrations` | its embedded migration history |
| `MiddlewareSet` | `fxkit.ProvideMiddleware` | request wrapping, explicitly ordered |
| `OperationSet` | `fxkit.ProvideOperations` | its endpoints — with an error path that reaches the boot |

## What a copy must preserve

Four properties, stated as obligations rather than habits. `doc.go` says why
each is a decision; this is the checklist.

1. **A refused mount is a boot failure, naming the module.** The error sqlb
   raises for a `Scoped` resource with no confining hook (ADR-0030) travels out
   through `OperationSet.Register` and stops the process. Logging it instead of
   returning it turns the loudest safety check into a warning nobody reads.
2. **`Migrated` is a value, so ordering is a dependency edge.** The handles take
   one, so nothing can query a table that does not exist yet — in any
   module-list order.
3. **Middleware order is an explicit integer**, because fx value groups have no
   defined order, and auth that runs after its handler is not auth.
4. **The boot log is deterministic** — contributions sorted by module name — so
   a diff between two boots means something.

`TestResourcesRefuseToMountWithoutHooks` in [`../app_test.go`](../app_test.go)
asserts the first against a real Postgres, by removing a module and requiring
the server not to start. It is the assertion worth copying along with the code.

## The five options

`Module()` is the sum for a standalone application; a codebase whose platform
layer already owns the pool, the migrations and the router takes `Handles()`
alone over the platform's `*pgxpool.Pool` and asserts the platform's guarantee
with `fx.Supply(fxkit.Migrated{})`:

| Option | Provides |
|---|---|
| `Pool()` | `*pgxpool.Pool` from `DBConfig`, closed by fx |
| `Migrations()` | goose over the migrations group → `Migrated` |
| `Handles()` | the hook registry from the hooks group → scoped `*sqlb.DB` + `Unscoped` |
| `HTTP()` | chi + Huma over the middleware and operations groups, server lifetime |
| `Module()` | all of the above |

## The auth seam is not here

`sqlb.WithPrincipal` / `sqlb.PrincipalFrom[T]` are in the engine: middleware
verifies the request and stores the principal, scoping hooks read it back by
type, and neither end names the other. That seam was never fx-shaped —
[`example/tasks/auth`](../../tasks/auth/context.go) had hand-rolled the same
thing with no container in sight, which is what moved it. See
[`../access/middleware.go`](../access/middleware.go) for this application's end.
