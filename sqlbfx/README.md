# sqlbfx — sqlb under uber-go/fx

The opinionated assembly of sqlb inside an [fx](https://github.com/uber-go/fx)
application, and the contracts that make modules pluggable
([ADR-0044](../docs/adr/0044-the-container-is-an-adapter.md)). A separate Go
module, so the engine's dependency gate does not move: fx, chi, goose and huma
live here and reach nothing that does not import this package.

```go
fx.New(
    fx.Supply(
        sqlbfx.DBConfig{DSN: os.Getenv("DATABASE_URL")},
        sqlbfx.HTTPConfig{Title: "Notes", Version: "1.0.0"},
    ),
    sqlbfx.Module(),

    store.Module,   // contributes migrations + generated operations
    auth.Module,    // contributes middleware, stores the principal
    notes.Module,   // contributes hooks that read the principal
).Run()
```

A feature module contributes to four value groups and provides nothing anybody
imports:

| Contribution | Helper | What it is |
|---|---|---|
| `HookSet` | `sqlbfx.ProvideHooks` | the module's query/mutation rules |
| `MigrationSet` | `sqlbfx.ProvideMigrations` | its embedded migration history |
| `MiddlewareSet` | `sqlbfx.ProvideMiddleware` | request wrapping, explicitly ordered |
| `OperationSet` | `sqlbfx.ProvideOperations` | its endpoints — with an error path that reaches the boot |

Two properties are the point. **A refused mount is a boot failure**: the error
sqlb raises for a `Scoped` resource with no confining hook (ADR-0030) travels
out through `OperationSet.Register` and stops the process, naming the module.
**Ordering is a dependency edge**: `Migrated` means "every registered migration
set has been applied", the handles take one, and nothing can query a table
that does not exist yet — in any module-list order.

The kit is five composable options. `Module()` is the sum for a standalone
application; a codebase whose platform layer already owns the pool, the
migrations and the router takes `Handles()` alone over the platform's
`*pgxpool.Pool` and asserts the platform's guarantee with
`fx.Supply(sqlbfx.Migrated{})`:

| Option | Provides |
|---|---|
| `Pool()` | `*pgxpool.Pool` from `DBConfig`, closed by fx |
| `Migrations()` | goose over the migrations group → `Migrated` |
| `Handles()` | the hook registry from the hooks group → scoped `*sqlb.DB` + `Unscoped` |
| `HTTP()` | chi + Huma over the middleware and operations groups, server lifetime |
| `Module()` | all of the above |

The auth seam is `WithPrincipal` / `PrincipalFrom[T]`: middleware verifies the
request and stores the principal; scoping hooks read it back by type. Neither
end names the other, which is what makes an auth mechanism swappable without
touching a hook.

[`example/fxapp`](../example/fxapp/) is the worked application — a
tenant-scoped notes API on this kit, with the boot-refusal claim asserted
against a real Postgres by removing a module and requiring the server not to
start.
