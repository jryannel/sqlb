# ADR-0044: The container is an adapter, and the contribution types are the contract

- **Status:** Exploring
- **Confidence:** Medium
- **Decided:** 2026-07-31
- **Last reviewed:** 2026-07-31

## Context

`example/fxapp` answers the question people building on
[uber-go/fx](https://github.com/uber-go/fx) ask first — where do the sqlb
pieces go when a container assembles the application — and it answers it with
roughly four hundred lines of glue that every fx adopter re-writes:
`dbbase` (the pool, the migration runner over a value group, the `Migrated`
token), `sqlbkit` (the hook registry assembled from a value group, the
scoped/unscoped handle pair), and `httpkit` (chi, a Huma API, the server's
lifetime, the middleware and operation groups).

Three things about that glue are worth more than an example can deliver:

1. **The group names and element types are a de-facto contract with no
   owner.** A module contributes `sqlbkit.HookSet` to `group:"hooks"` — but
   `hooks` is a name the example happened to pick, `HookSet` is a type the
   example happened to define, and a third-party module (an auth module, an
   audit module) has nothing stable to compile against. Pluggability needs the
   contract to be published, not copied.

2. **The boot-refusal guarantee stops at the example's edge.** sqlb's
   distinctive check — a `Scoped` resource refuses to mount without its
   confining hook (ADR-0030) — reaches fx only because the example's
   `OperationSet.Register` returns an error. Whether a given application's
   copy of the glue preserves that path is a matter of how carefully it was
   copied.

3. **The seam between "who is calling" and "what confines the query" is a
   per-app convention.** `access` writes a slug into the context under a
   private key; `spaces.Directory.Current` reads it back. Swapping the auth
   mechanism means touching that convention everywhere it is spelled. A
   published contract for the principal is what would make auth modules
   interchangeable.

The engine deliberately knows nothing about containers, and
[ADR-0040](0040-the-driver-is-a-dependency.md)'s dependency stance —
pgx and nothing else, enforced by `deps-check` — rules out fx (which brings
`dig` and `multierr`) ever entering the engine module.

## Decision

**`sqlbfx` is a separate Go module** — `github.com/jryannel/sqlb/sqlbfx`,
beside `pgtest` and the example modules — so the engine's dependency gate does
not move. It depends on fx, chi, goose and huma, and that is the point: it is
the *opinionated assembly*, and the opinions (chi for the router, humachi for
the API, goose for the runner, `log/slog` for the log) are the same ones every
example in this repository already holds. An application that holds different
ones does not fight the kit; it writes its own, and the kit's source — four
small files — is the reference for what it must preserve.

**The contribution types are the contract.** Four value-group element types,
promoted from the example with their group names namespaced so a library
cannot collide with an application's own groups:

| Type | Group | A module contributes |
|---|---|---|
| `HookSet` | `sqlbfx.hooks` | its query/mutation rules, per registry |
| `MigrationSet` | `sqlbfx.migrations` | its embedded migration history |
| `MiddlewareSet` | `sqlbfx.middleware` | request wrapping, with an explicit `Order` |
| `OperationSet` | `sqlbfx.operations` | its endpoints, with an error path that reaches the boot |

`Provide*` helpers (`sqlbfx.ProvideHooks(ctor)`, …) wrap the `fx.Annotate`
incantation so the group strings are typed once, in this package.

**The unscoped handle is a type, not a name tag.** The example's
`name:"unscoped"` becomes `sqlbfx.Unscoped` — a struct embedding `*sqlb.DB` —
because a type is grep-able the same way and cannot be misspelled in a string
tag that only `fx.ValidateApp` will catch. The scoped handle remains the plain
`*sqlb.DB`, so the default spelling is the safe one.

**The principal seam ships, and it is container-free.** `WithPrincipal(ctx,
p)` / `PrincipalFrom[T](ctx)` — a context contract, no fx types in its
signature. An auth module's middleware resolves credentials and stores the
principal; scoping hooks read it back by type. That one seam is what makes an
HS256 module, an API-key module and an OIDC module interchangeable without
touching a hook. It lives in `sqlbfx` because that is where its first
consumers are, and its signature is the part most likely to move into the
engine later (see below).

**Configuration is the application's, still.** `DBConfig` and `HTTPConfig`
are plain structs the application provides — from env, from flags, from
wherever — because ADR-0040 already decided that how the pool is sized and
where its DSN comes from is no part of the library's business. The kit reads
no environment variable and freezes no variable name. The logger is an
optional dependency: if the graph provides a `*slog.Logger` the kit uses it,
otherwise `slog.Default()` — the kit never provides one, so it can never
collide with the application's.

**The kit splits where a platform layer already owns the ground.** The
reference fx codebase (studio-apps/core) composes an `appbase.Standard()`
whose `dbbase` owns the pool and consumes its own `group:"migrations"` (17
contributors), and whose `httpkit` owns the router. An application in that
world takes none of `sqlbfx.Module()`'s infrastructure — it takes
`sqlbfx.Handles()` alone, over the platform's pool, and supplies the
`Migrated` fact its platform's runner established: `fx.Supply(sqlbfx.Migrated{})`
is the application asserting what the kit cannot know. So the kit is five
composable options — `Pool`, `Migrations`, `Handles`, `HTTP`, and `Module` as
the sum — and `Handles` is the one every composition includes. The namespaced
group names are also this scenario's requirement: a published library
claiming `group:"migrations"` beside a platform that owns that name would
collide; `sqlbfx.migrations` cannot. The principal seam mirrors the shape
that codebase already proved (`tenancy.WithScope` / `FromContext` /
`Require`), which is independent confirmation the contract is
container-shaped, not fx-shaped.

**What the kit preserves, stated as obligations rather than habits:** a
refused mount is a boot failure carrying the module's name; `Migrated` is a
value, so everything that touches a table is downstream of the migrations by
construction; middleware order is an explicit integer, not group arrival
order; and every boot log line is deterministic (contributions sorted by
module name).

## Consequences

**Buys.** An fx application drops from four glue packages to a module list.
The contracts third-party modules need — the four contribution types and the
principal seam — have an owner, a version and a changelog. The ADR-0030
boot-refusal guarantee becomes a property of the kit rather than of a
faithful copy. `example/fxapp` shrinks to what it should be: a worked
application, not a wiring tutorial.

**Costs.** The group names, element types, `Unscoped`, `Migrated` and the
principal signature become compatibility surfaces of a published module —
pre-1.0 they may move, but each move now breaks consumers rather than an
example. The kit's opinions (chi, humachi, goose, slog) are load-bearing:
an application on echo or golang-migrate writes its own kit and tracks the
obligations above by hand. And a second module means a second `go.mod`,
a second tidy-check, and a `replace` in every example that consumes it.

**What this is not.** Not a claim that sqlb needs fx — the engine still knows
nothing about containers, `example/tasks` still assembles the same pieces
with a function and an argument, and `deps-check` still proves the engine
takes pgx alone. Not codegen — a generated `store.Module()` (the natural
follow-up) waits until these group names have survived a real consumer.

## What would change our mind

- **A non-fx consumer wants the principal seam.** That is the signal it was
  never fx-shaped, and it moves to the engine (or `rest`), with `sqlbfx`
  re-exporting aliases. The seam's signature is deliberately free of fx types
  so this move is cheap.
- **A second container matters** (wire, do-it-yourself DI at scale). If the
  contribution types survive a port to it, they were the right contract; if
  they do not, the kit was fx-specific and should say so in its name.
- **The opinion set blocks real adopters** — an application that wants the
  kit but not goose, or not chi. The answer is a small interface at exactly
  the blocked seam (a `Runner`, a router abstraction), added then, not
  speculatively now.
- **Codegen wants in.** When two schemas have hand-written the same
  `store.Module()` shape, `codegen.Options.FX` earns its place, and the
  generated module becomes the fifth contract surface.

## Cost of change

Low while `sqlbfx` is pre-1.0 and versioned separately from the engine: a
renamed group or a reshaped element type is a minor-version note in a module
with few consumers. The expensive half is the principal seam, because hooks —
application code, the part nobody regenerates — spell it directly; moving it
later costs every consumer an import rewrite, which is why it ships with the
smallest possible signature now.

## Revisions

- 2026-07-31 — Written, with `example/fxapp` as the prototype being promoted
  and its port to the kit as the first proof.
