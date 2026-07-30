# example/fxapp — sqlb under a dependency-injection container

The example about *wiring*. `example/blog` is the shortest path from a schema
to a server and `example/tasks` is what that machinery looks like at
application scale; this one answers a different question, which people building
on [uber-go/fx](https://github.com/uber-go/fx) ask first: where do the sqlb
pieces go when the application is assembled by a container rather than by a
`main` that news everything up in order?

The schema is deliberately small — two tables, one of them a tenant — because
it is not the subject. It has exactly one property that matters: `notes.space_id`
and `spaces.id` are `Scoped`, so the generated resources refuse to mount unless
the hooks that confine them are registered ([ADR-0030](../../docs/adr/0030-declared-scope-is-required.md)).
That refusal is what makes the container's ordering guarantee worth stating,
and it is what [`app_test.go`](app_test.go) asserts by taking a module away.

It is a module of its own, like `pgtest` and `example/tasks`, so fx costs the
engine nothing. `mise run deps-check` still reports **standard library only**.

## Running it

```bash
docker run --rm -e POSTGRES_PASSWORD=postgres -p 5432:5432 postgres:18
```

```bash
export FXAPP_DATABASE_URL='postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable'
export FXAPP_SPACE_KEYS="acme=$(head -c 24 /dev/urandom | base64)"
go run ./cmd/server
```

Then <http://localhost:8080/docs>. The migrations apply at startup and the
configured spaces are created, so an empty database is enough.

```bash
export KEY='...the acme key...'
curl -X POST localhost:8080/notes -H "authorization: Bearer $KEY" \
  -H 'content-type: application/json' \
  -d '{"title":"Quarterly plan","body":"Ship the thing.","status":"published"}'

curl "localhost:8080/notes?status=eq.published&sort=-created_at" -H "authorization: Bearer $KEY"
curl localhost:8080/insights/notes -H "authorization: Bearer $KEY"
```

**Postgres 18 or newer**, for the reason `example/tasks` gives: the migration
uses the built-in `uuidv7()` so the DDL applies to a stock install with no
extension.

## What to read, and in what order

| | |
|---|---|
| [`cmd/server/main.go`](cmd/server/main.go) | The whole binary. One call. |
| [`app.go`](app.go) | The module list, and why its order does not matter. |
| [`sqlbkit/handles.go`](sqlbkit/handles.go) | The point of the example: a hook registry assembled from a value group, and the two handles that come out of it. |
| [`store/module.go`](store/module.go) | The generated resources, mounted on the scoped handle. Three lines and two arguments. |
| [`notes/hooks.go`](notes/hooks.go) | The space boundary: one registration per statement kind, and no handler that knows about spaces. |
| [`dbbase/migrations.go`](dbbase/migrations.go) | Why "the migrations have run" is a value rather than an `fx.Invoke`. |
| [`app_test.go`](app_test.go) | Every claim here, asserted against a real Postgres — including the composition that must fail. |

## The shape of it

```
logs ── dbbase ── Migrated ─┬─ unscoped handle ── spaces.Directory ─┐
                            │                                       │
                            └───────────── scoped handle ◀── hooks ─┘
                                                 │
                            httpkit.API ◀── operations ── store (generated)
                                                       └── notes  (hand-written)
```

Four things in that picture are the argument.

**The hook registry is a value group.** A module contributes its rules with
a `group:"hooks"` result tag, and the handle everything queries through
cannot be constructed until every contributor has run. In a hand-written `main`
that ordering is a matter of writing the lines in the right order; here it is a
dependency edge, which is the version that still holds for a module written
next year by someone who never read this file.

**A refused mount is a boot failure.** `store.Register` returns the error sqlb
raises when a resource declares a scope no hook backs, `httpkit.OperationSet`
carries it out, and fx reports it instead of listening. Delete `notes.Module`
from the list and the server does not start:

```
rest: /notes exposes create|read|update|delete|list, and nothing confines store.Note
  create: BeforeCreate is not registered (space_id is Scoped)
  list and read: BeforeQuery is not registered (space_id is Scoped)
  ...
```

That is asserted, in `TestResourcesRefuseToMountWithoutHooks`. A guard nobody
has watched refuse is a claim rather than a check
([ADR-0016](../../docs/adr/0016-guards-proven-both-ways.md)).

**Ordering is a type, not a position.** `dbbase.Migrated` is an empty struct
that means "every registered migration set has been applied". The handle takes
one, so every query in the process is downstream of the migrations by
construction — no module list can be written in an order that queries a table
that does not exist yet. The same trick puts the operations after the API and
the API after the handle.

**Two handles, one connection.** `sqlbkit` provides the scoped handle everything
uses and a `name:"unscoped"` one for the two jobs that cannot be scoped because
they run before there is anything to scope by: provisioning the configured
spaces at boot, and resolving a slug to the id the hooks then filter on. It is
two values rather than one value and a "skip the hooks" flag, because a flag is
something a caller passes and the set of callers allowed to pass it is the whole
point. `grep -r 'name:"unscoped"'` lists every consumer.

## The modules

The first four would be the same in any application; the last four are this one.
That split is what a platform repository makes into two packages — an
`appbase.Standard()` every product composes, and the product.

| | |
|---|---|
| `logs` | The process logger, and `fx.WithLogger` so fx's own boot events go through it rather than to stderr in a second format. |
| `dbbase` | `*sql.DB`, the pool settings, and the migration runner over the `migrations` value group. Owns the driver import; sqlb never opens a connection ([ADR-0040](../../docs/adr/0040-the-driver-is-a-dependency.md)). |
| `sqlbkit` | The hook registry and the two handles. |
| `httpkit` | chi, a Huma API, the server's lifetime, and three value groups: `http-middleware`, `http-operations`, plus the ordering edge that keeps them apart. |
| `store` | The generated package, plus one hand-written file contributing its migrations and its resources. |
| `access` | Which space a request speaks for: a bearer key per space, verified in constant time. |
| `spaces` | The tenant — provisioning, the slug-to-id directory the hooks resolve against, and the rule that confines the table itself. |
| `notes` | The feature: the space boundary for notes, and the one endpoint the generator does not write. |

## What this example is not

**It is not an authentication system.** A space presents a shared secret from
the configuration. There are no users, no sessions and no revocation, and a
leaked key is a leaked tenant until the configuration changes. That is the
smallest thing that is still a boundary rather than a convention — the
alternative an example is tempted by, a plain `X-Space` header, would let any
caller name any tenant and make every hook here decorative.
[`example/tasks`](../tasks/) is where authentication lives: registration, login,
PBKDF2, and an HS256 verifier with the three checks that make one safe. Copy
that for the identity half and this one for the wiring.

**It is not a claim that sqlb needs fx.** Nothing in the engine knows what a
container is, and `example/tasks` assembles the same pieces with a function and
an argument. What the container buys is that the ordering constraints become
edges the compiler and the boot enforce, and the cost is a layer of indirection
between a constructor and its caller. Both are real; which one is worth it
depends on how many modules the application has.

**Migrations at startup suit a demo and a single-instance service.** They do not
suit a rolling deploy, where several new instances race to apply the same
migration and the old code briefly runs against the new schema. `dbbase` is
where that changes: drop the provider that applies them, and have `Migrated`
assert a version instead of producing one.

## Testing

```bash
mise run test-fx               # needs Docker, except for the graph validation
```

Against a real Postgres in a container, one database per test, given to the
application empty — so every boot in the suite is also a run of the checked-in
migration history. The container starts on first use rather than in `TestMain`,
so `go test -run TestGraphIsValid .` needs no Docker; that is not a skip, and a
test that needs Postgres and cannot have it still fails.

`TestGraphIsValid` is the cheap half worth copying into any fx application:
`fx.ValidateApp` resolves every dependency and constructs nothing, which catches
the mistakes a container introduces and a compiler cannot see — a misspelled
group tag, a missing provider, a cycle.

## Regenerating

```bash
sqlb generate ./noteschema     # models, typed columns, REST bodies, manifest
go run ./cmd/migrate -force    # migrations, from the schema
```

`go generate ./...` runs the first line. The second is a baseline plus one
hand-written trigger — `updated_at` is otherwise set by its column default and
by nothing afterwards — and `migrate.Write` refuses to overwrite, because a
migration already applied somewhere must not change under the runner's feet.
`-force` is the development escape, and it deletes only the files it is about
to write.
