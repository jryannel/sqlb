# Architecture

How sqlb fits together, and why the seams are where they are. For the reasoning
behind individual choices, see the [decision records](adr/). For where this is
going, see the [vision](vision.md).

*Last reviewed: 2026-07-28.*

## The shape of it

```
  blogschema/schema.go          ← you edit this
         │
         │  go generate ./...           (a generator main, not a CLI)
         ├──────────────▶ migrations/*.sql  DDL, diffed against the last state
         ├──────────────▶ models.go         db + sqlb struct tags
         ├──────────────▶ columns.go        typed column facade
         ├──────────────▶ rest_gen.go       request bodies + registration
         ├──────────────▶ sqlb.json         the manifest
         ├──────────────▶ client.gen.ts     TypeScript client   (ADR-0028)
         ├──────────────▶ client.gen.dart   Dart client         (ADR-0031)
         ├──────────────▶ cli/client/       Go client, stdlib only (ADR-0029)
         └──────────────▶ cli/              cobra tree over it     (ADR-0029)

  The last four are generated from the *schema*, not from openapi.json. The
  OpenAPI document cannot say what they need to say — `?status=eq.published`
  documents as `array<string>`, which is exactly the guarantee being sold —
  so the emitters read the same declaration everything else does.

                    ┌─────────────────────────────┐
   Go code ────────▶│                             │
                    │      predicate AST          │──▶ compiler ──▶ SQL + args
   HTTP query ─────▶│   (sqlb.Pred, sqlb.Expr)    │
     (filter)       └─────────────────────────────┘
                                  ▲
                                  │
                            BeforeQuery hooks
```

Two things carry most of the design. The first is that a **query is a value**,
so it can be built conditionally, handed to a hook to amend, and inspected
without being run ([ADR-0002](adr/0002-queries-are-values.md)). The second is
that there is **one predicate AST with two producers** — hand-written Go and the
URL filter grammar — so escaping, authorisation and hook application each happen
exactly once ([ADR-0003](adr/0003-one-ast-two-producers.md)).

Almost everything else follows from those two.

## Packages

| Package | Responsibility | Depends on |
|---|---|---|
| `schema` | The declarative DSL and its validation. Design-time only; nothing at runtime imports it. | nothing |
| `.` (`sqlb`) | AST, Postgres compiler, generic builder, model reflection, mutations, hooks, `Describe`. | stdlib only |
| `filter` | URL grammar → predicates, validated against model capabilities. | `sqlb` |
| `migrate` | Diffs two schemas into changes, renders them as Postgres DDL, and writes migration files for goose, golang-migrate or plain SQL. Does not apply them. | `schema` |
| `introspect` | Reads `pg_catalog` back into a `*schema.Registry`, and reports every construct the DSL cannot express. Design-time; connects through a `sqlb.Executor`, so the handle is the caller's. | `schema`, `sqlb` |
| `codegen` | Generates models, the typed column facade, the REST request bodies, the manifest, the TypeScript and Dart clients, and the cobra CLI. `Check` is the dry-run mode wired into CI. | `schema` |
| `rest` | Mounts a model on a Huma API: handlers, and an OpenAPI operation built from the model's capabilities. `Serve` wraps a whole server around it — pool, migrations, listen, graceful shutdown. | `sqlb`, `filter`, huma |
| `shadow` | Replays a checked-in migration history into an empty database, so the current side of a diff can come from the history rather than from a live schema. Design-time. | `schema`, `migrate` |
| `example/recipes` | One file per aspect, one point per example, each ending in output the test compares. The narrow-question counterpart to the worked applications, and the surface an agent greps. | all of the above |
| `example/blog` | A worked schema plus the artefacts codegen must produce. | all of the above |
| `example/tasks` | A multi-tenant task manager: hooks as the authorisation seam, JWT middleware feeding the context hooks read, and a migration history applied by goose. A separate module, like `pgtest`. | all of the above, `migrate` |
| `example/withsqlc` | The same schema rendered as DDL for sqlc, and a test that sqlb reads sqlc's structs. Proves [docs/with-sqlc.md](with-sqlc.md) rather than leaving it asserted. | `sqlb`, `filter`, stdlib |

The dependency direction matters: `schema` is a leaf that nothing imports at
runtime, and `sqlb` has no dependency on `schema`. That is deliberate. It is
what makes [ADR-0010](adr/0010-codegen-is-optional.md) possible — the engine
cannot quietly grow a dependency on the schema DSL, because it cannot see it.
Capabilities reach the runtime as struct tags or `Describe` calls, never as a
schema import.

`migrate`, `introspect` and `codegen` sit on the other side of that line: all
three are design-time tools that read or write `schema`, and none is reachable
from the request path. `migrate` is the only package that renders DDL, which is
why the Postgres type mapping lives there rather than beside the query compiler
— a `Format` decides what a *runner* wants a file to look like, and the DDL
layer decides what the *database* wants a statement to look like.

`introspect` is the same mapping pointed backwards, and it is a separate package
because it connects to a database and `migrate` deliberately does not. That
separation is what keeps `migrate` a pure function over two data structures, and
it is why the two can be checked against each other: render a schema, apply it,
read it back, and the diff between what went in and what came out must be empty.

`sqlb` depends on pgx and nothing else, and neither does anything else on the
request path. `rest` is the single exception: it depends on huma, and nothing
depends on `rest`. `mise run deps-check` proves this per package rather than per
module — the allowed set is computed from what pgx itself pulls in, so it cannot
go stale — and it ends by checking that it can still see huma in `rest` and that
it still *refuses* huma everywhere else. A guard that cannot fail is worse than
no guard ([ADR-0016](adr/0016-guards-proven-both-ways.md)).

`Executor` is the two-method subset of pgx that the engine needs — `Query` and
`Exec` — so a `*pgxpool.Pool`, a `*pgx.Conn` and any instrumenting wrapper all
work unchanged. So does a `pgx.Tx`, which is the point of taking pgx at all:
sqlb writes join a transaction the application opened
([ADR-0040](adr/0040-the-driver-is-a-dependency.md)). `sqlb.DB` is a handle over
an `Executor`, adding `WithTx` and a scoped hook registry; it satisfies
`Executor` itself, which is what lets it be adopted without touching call sites
([ADR-0020](adr/0020-transaction-scoped-handle.md)). `DB.Tx` reaches the
underlying `pgx.Tx`, which is how a unit of work is shared with code wanting
more than two methods — `CopyFrom`, `SendBatch`, or sqlc's generated `DBTX`.
`rest` takes a `huma.API`, not a router, so the choice of chi, gin, echo or
`net/http` — and all of that router's middleware — stays the application's. It
wraps each generated write in a transaction, which is what gives a hook a commit
to be after; reads are left alone, since one `SELECT` is atomic already
([ADR-0021](adr/0021-hooks-receive-an-event.md)).

## Request path

A list request through `rest.Resource`:

1. **Parse.** `filter.Parse` reads the query string against the model. Unknown
   parameters, undeclared capabilities and uncoercible values are collected into
   a `filter.Errors` — all of them, not the first
   ([ADR-0011](adr/0011-actionable-errors.md)). Values become typed Go values
   here; nothing downstream sees strings.
2. **Apply.** `filter.Apply` writes predicates, ordering, projection and limits
   onto a `*sqlb.Builder[T]`. It owns the projection and defaults to non-hidden
   columns, so a handler cannot leak a `Hidden` column by forgetting to project.
3. **Hook.** The terminal method clones the builder, then runs `BeforeQuery`.
   Cloning is what stops a hook's predicates accumulating when the same query
   value runs twice. A hook that returns an error aborts before any SQL is
   issued, so a missing tenant fails closed
   ([ADR-0008](adr/0008-hooks-as-domain-seam.md)). Which registry the hooks come
   from is read off the executor: a `*sqlb.DB` carries one, and anything else
   carries none, so a statement issued against a bare pool runs unconfined
   ([ADR-0047](adr/0047-no-default-hook-registry.md)).
4. **Compile.** The AST renders to SQL with `$N` placeholders. Values are always
   bind parameters. Identifiers are validated against the model and quoted.
   `LIMIT`/`OFFSET` are literals so the planner can see them — safe because both
   are range-checked ints.
5. **Scan.** Result columns are matched to struct fields by name. Unmatched
   columns are read and discarded, so a query selecting extra expressions still
   scans into the model.

A write takes the same path with a transaction around it: `BEGIN`, the hooks and
the statement, `COMMIT`, then the `AfterCommit` callbacks — outside the
transaction, since there is nothing left to join. A callback that fails does not
fail the request, because the row is already durable and a retry would write it
twice; `rest` logs it and returns the success it achieved.

## Where safety lives

Four independent mechanisms, each covering what the others cannot:

**Bind parameters.** Values never reach SQL text. There is one `bind` method on
the compiler and no way to interpolate a value around it.

**Identifier validation.** Column names are checked against the reflected model
before compilation. `Raw` is the documented escape hatch and is the one place
this does not apply — which is why raw fragments are parenthesised as operands,
since their contents are opaque and could otherwise re-associate a surrounding
predicate.

**Opt-in capabilities.** A column that does not declare `Filterable` cannot be
filtered, ever ([ADR-0006](adr/0006-capabilities-are-opt-in.md)). `Hidden` goes
further: the column is reported as unknown rather than as forbidden, so its
existence cannot be probed, and `Hidden` plus `Filterable` is a schema
validation error because a filterable secret can be recovered a character at a
time.

**Query hooks.** Tenant scoping applies to every read of a model, including
reads issued by generated handlers, because both go through the same builder.

Two smaller rails worth knowing: `Update` and `Delete` without a `WHERE` return
`ErrUnscoped` until `Everything()` is called explicitly, and LIKE
metacharacters in user input are escaped so a search for `50%` searches for the
literal string.

## Model metadata

The engine needs to know four things a Go struct does not say: which column is
the key, which columns the database defaults, which capabilities each column
exposes, and which columns are hidden.

That metadata arrives by one of two routes, which merge:

- **Struct tags** — `db:"email"` for the column name, `sqlb:"filter,sort"` for
  capabilities. This is what codegen emits.
- **`sqlb.Describe[T]()`** — the same information supplied at startup, for
  structs you did not generate and would rather not edit.

Without either, the builder still works — column names derive from field names —
but no column is filterable, so the REST layer exposes nothing. That default is
the point.

## API surface

There are no `internal/` packages, and the layout is flat
([ADR-0013](adr/0013-no-internal-split.md)). The genuinely internal machinery —
the compiler, scanning, model building, escaping — is already unexported within
package `sqlb`, which is a finer-grained boundary than `internal/` can express.

What is exported falls into three tiers. They are a convention, not a compiler
check, and they exist because the module is `v0`:

| Tier | What | Promise |
|---|---|---|
| **Stable** | `Query`/`Builder`, `F`/`Pred`/`And`/`Or`/`Not`/`If`, `Field` and its operators, `Col`/`TextCol`/`Typed`/`TextColumn`, `Order`, the aggregates, `InsertRows`/`UpdateRows`/`DeleteRows`, `On`/`Hooks`, `Describe`, `Collect`, `Executor`, `DB`/`New`/`WithTx`, `ErrNotFound`/`ErrUnscoped`, all of `filter` and `schema` | Changes are breaking changes and are treated as such |
| **Provisional** | `Model`, `ColumnInfo`, `ModelOf`, `Selectable`, `Selection`, `Dialect`, `Postgres`, `Registry`/`On`/`WithHooks`, `Beginner`, `TxFrom` | Public because `filter` and generated code need them across a package boundary, or — for the registry surface — because they are new enough that no one has used them in anger yet |
| **Escape hatch** | `Expr` and the node types: `Raw`, `Binary`, `Unary`, `Call`, `Cast`, `BetweenExpr`, `List`, `Param`, `Column` | Use `Raw`, `RawPred`, `RawSel`. The rest is the compiler's vocabulary and will change without ceremony |

The tiers exist because the obvious extraction does not work: `Expr` and `Raw`
are the documented escape hatch and appear in `SetExpr`, `GroupByExpr`,
`Coalesce` and `OrderByDesc`, so they cannot be hidden. Hiding the rest of the
node set alone would buy little and would leave `Pred.Expr()` returning a type
callers cannot name.

The dialect is not among them. It is package-level but unexported and
unsettable: a mutable global read on the compile path of every query is a data
race with no legitimate trigger, since sqlb targets Postgres only. `UseDialect`
overrides it per statement, which is scoped and race-free.

## Failing loudly

Where sqlb cannot do the right thing, it says so rather than guessing. The rule
is that a wrong answer must never be quieter than no answer.

| Situation | Behaviour |
|---|---|
| `Collect[R]` has a field no result column fills | Error naming the field and both names — a mistyped `As("revenu")` would otherwise scan as a real-looking `0` |
| `Describe` called after a statement was built | Panic; mutating the cached model then would race and half-apply |
| `Describe` names a column that does not exist | Panic at startup, listing the columns that do |
| `Update`/`Delete` with no `WHERE` | `ErrUnscoped` until `Everything()` is called |
| Destructive migration | Rendered commented out with the reason stated |
| A change over a column a commented-out change adds | Commented out with it, naming what it waits for. Emitting it live makes the file fail partway through instead of being the no-op the guard intends |
| A column or table that was renamed | A drop and an add, unless `RenamedFrom` says otherwise — inferring a rename from a similar name would destroy data whenever the guess was wrong |
| A migration that rewrites or scans a table | Emitted live with the lock it takes and the sequence to use instead named above it. Not commented out: whether a scan matters depends on a row count the schema does not have. `migrate.Unblock` writes the sequence when the remedy is mechanical |
| A change with no `Down` | Renders an explanation, not an empty section that looks like a working rollback |
| Filter names an unknown or uncapable column | 400 listing what would have been accepted |
| Schema authoring mistake | Every problem reported at once, each with the fix |
| A resource over a `Scoped` or soft-deleting model with no hook confining it | Refused at mount, listing every missing registration and the declaration that asked for it. Serving it would answer 200 with another tenant's rows, which is the quietest wrong answer in the system |

Two deliberate exceptions, both documented where they happen: a page size above
the maximum is capped rather than rejected, since a client asking for too much
should get the maximum rather than an error; and `Builder.All` tolerates
unfilled fields, because a partial projection is exactly what `?select=id,name`
is. `Collect` is strict precisely because its destination type was written to
match the projection.

## Testing

The engine's tests run against an in-memory `Executor` that records statements
and replays canned rows, so hooks, scanning and the mutation paths are covered
end to end without a live Postgres. The pgx shapes that stands on —
`pgx.Rows` and `pgx.Tx` — are in `internal/pgfake`, written once and used by
every test package that needs them. SQL-string assertions cover the compiler.

What a fake cannot cover, `pgtest` does, and the driver flip made that split
sharper rather than softer: both bugs ADR-0040's port introduced were cases
where pgx hands back exactly what Postgres sent, and neither was reachable from
a canned result set.

The typed facade is checked by attempting to compile the cases that should fail
and confirming they do — a test that passes vacuously if the facade stops
working is worse than no test, so those are exercised as real build attempts.

## Known gaps

- `introspect` produces a registry from a live database and
  `codegen.RenderSchema` renders it back as `schema.go`, so adoption is a closed
  loop, and `shadow.Build` replays a migration history into an empty database so
  the current side of a diff can come from what the history builds. What it
  cannot reproduce is a destructive change: those render commented out, so the
  checked-in file is not the SQL that ran, and the shadow will differ from
  production wherever one was uncommented by hand.
- No change feed, and no MCP server over the manifest. See the
  [vision](vision.md). The TypeScript client, the Dart client and the CLI have
  since landed ([ADR-0028](adr/0028-typescript-client.md),
  [ADR-0031](adr/0031-dart-client.md), [ADR-0029](adr/0029-go-cli.md)); all
  three read the schema rather than the OpenAPI document, for the reason the
  diagram above gives.
- `?expand` resolves one level. A relation expands to its row; that row's own
  relations do not expand in turn, and there is no `?expand=list.workspace`.
  One level is a join per relation and a bounded statement; nesting is where a
  depth limit and a cost model have to be argued for, and neither has been.

## Decisions

Decisions that shaped this codebase, folded in one at a time from a former
`docs/adr/` directory of individually numbered records. Each subsection below
used to be its own file; the reasoning now lives here, and its history lives
in this file's git history rather than in a separate directory — `git log --follow -p -L /^### <heading>/,/^### /:docs/architecture.md` finds the commit that made or last revised a given decision. A change to a
decision below gets its own commit, and the commit message carries the *why*,
the way an ADR's body used to.

### Postgres only

sqlb targets Postgres, and only Postgres. The `Dialect` interface exists for
placeholder style and identifier quoting, not for pretending to be portable.
The features the design leans on are not evenly available elsewhere:
`LISTEN/NOTIFY` for the change feed, jsonb aggregation for relation expansion,
`RETURNING` on every mutation, `ON CONFLICT`, `SKIP LOCKED`, partial and GIN
indexes, `ILIKE`. Supporting a second dialect would mean either dropping to
the intersection of what both support or carrying per-dialect branches
through the compiler, the schema DSL and the migration generator — and either
way, no feature could assume the best primitive available anymore. Targeting
one database is what keeps the compiler one small set of rules and lets
mutations return their rows in a single round trip.

The cost is asymmetric by design: narrowing further is free, but widening to
a second dialect later is close to a restart, since every compiler assumption
would need re-auditing and `LISTEN/NOTIFY` and jsonb expansion would need
replacements. Revisit only if a concrete project on MySQL or SQLite needs
supporting and cannot move, or if the `Dialect` seam turns out to leak beyond
placeholder rendering and quoting.

### Queries are values

A query is a value built up incrementally, not a statement assembled in one
shot. `Where` appends rather than replacing, the zero `Pred` is a no-op that
gets skipped, construction is separate from execution, and `SQL()` renders
without running anything:

```go
q := sqlb.Query[Post]().Where(sqlb.If(search != "", sqlb.F("title").Contains(search)))
```

Static query generators cannot express "this clause exists only when the user
typed something" without a combinatorial explosion of SQL strings, and teams
that hit that wall tend to fall back to concatenating SQL by hand — which
reintroduces every problem the typed builder was adopted to avoid. Making the
query a value sidesteps the explosion: conditional filters need no branching
at the call site, and the same value can be amended by a hook, produced by
the REST filter parser, inspected in a test, or printed for `EXPLAIN`. Hooks,
the filter grammar and query introspection all rest on this one property.

The cost is that builder methods mutate in place and return themselves, so a
shared base query can be aliased by accident — `Clone` exists but has to be
remembered — and errors are sticky, surfacing at the terminal method rather
than at the call that caused them. Revisit if aliasing bugs show up in
practice, which would justify the extra allocation of switching to
copy-on-write.

### One ast two producers

A query gets built two ways — a developer writes it in Go, or a client sends
filter parameters over HTTP — and giving each its own path would mean two
escaping strategies, two places to enforce authorisation, and two things to
keep in sync whenever a column changes. That is exactly where an injection
risk would live. So there is one predicate AST. Go code produces it through
`sqlb.F(...)`; the `filter` package produces it by parsing a request; both
feed the same builder, compiler and hooks, and the filter parser never emits
SQL text, only `sqlb.Pred` values. `filter` reads two wire formats — the URL
grammar and a JSON expression tree — but they are two frontends over one
compiler: both hand typed operands to a single internal `applyOp`, and a test
asserts that equivalent filters compile to byte-identical statements.

Bind-parameter discipline is enforced in exactly one place this way, and a
`BeforeQuery` hook constrains HTTP-driven and hand-written queries
identically, so tenant scoping cannot be bypassed by going through REST
instead of Go. A new builder feature reaches the filter grammar for free, and
adding a second wire format cost a parser rather than a second compiler. The
price is that the AST has to serve both producers — it carries nodes the
filter grammar will never emit and cannot be tuned narrowly for either side —
and the filter parser must coerce types up front, since the AST holds typed
Go values rather than strings.

This is cheap to keep and expensive to reverse: splitting the producers is
easy to do and hard to undo, since it reintroduces two escaping paths and two
authorisation points, and the resulting bugs are security bugs that surface
late. Revisit if the filter grammar needs an expression the builder cannot
represent — it may need its own compilation step, but it must still
terminate in `Pred` values.

### Schema as go dsl

Something has to be the single source of truth for table structure, since
every derived artefact — migrations, models, REST handlers, OpenAPI, three
clients — has to agree with it. The candidates were a Go DSL that generates
migrations (the ent/Convex approach) or introspection of an existing
database (the sqlc/PostgREST approach). DDL has nowhere to record
*capabilities*: there is no way to say "this column may be filtered on" in
`CREATE TABLE`, so an introspecting tool needs a side-car config file, and
now there are two sources of truth that can drift apart.

The schema is a Go DSL in its own package, and `sqlb generate` reads it to
emit migrations, models, typed column sets, REST handlers and OpenAPI. One
file is edited; everything else is derived. Capabilities, REST exposure,
comments and relations live next to the column they describe, which is a
large part of why this is pleasant to drive with an agent, and authoring
mistakes are caught before any SQL is generated. The cost lands on adoption:
bringing sqlb into an existing project means importing the current schema
and handing DDL control over to it, which is a real migration rather than an
afternoon's work.

Cost of change rises steadily once it's in use — after a production database
has been migrated by generated DDL, reversing course means reconciling a
generated history against a hand-managed one. Revisit if migration diffing
against a live database proves substantially harder than expected (invert to
introspection-first, keeping the DSL for capabilities), or if a generated
migration ever produces a destructive diff that wasn't intended — that would
be a stop-the-line signal for the whole approach.

### Runtime query engine

_(pending merge from `docs/adr/0005-runtime-query-engine.md`)_

### Capabilities are opt in

_(pending merge from `docs/adr/0006-capabilities-are-opt-in.md`)_

### Generated rest handlers

_(pending merge from `docs/adr/0007-generated-rest-handlers.md`)_

### Hooks as domain seam

_(pending merge from `docs/adr/0008-hooks-as-domain-seam.md`)_

### Typed column facade

_(pending merge from `docs/adr/0009-typed-column-facade.md`)_

### Codegen is optional

_(pending merge from `docs/adr/0010-codegen-is-optional.md`)_

### Actionable errors

_(pending merge from `docs/adr/0011-actionable-errors.md`)_

### Change feed outbox

_(pending merge from `docs/adr/0012-change-feed-outbox.md`)_

### No internal split

_(pending merge from `docs/adr/0013-no-internal-split.md`)_

### Migrations and import

_(pending merge from `docs/adr/0014-migrations-and-import.md`)_

### Module isolation

_(pending merge from `docs/adr/0015-module-isolation.md`)_

### Guards proven both ways

_(pending merge from `docs/adr/0016-guards-proven-both-ways.md`)_

### Enums as text and check

_(pending merge from `docs/adr/0017-enums-as-text-and-check.md`)_

### Tooling scoped to tracked files

_(pending merge from `docs/adr/0018-tooling-scoped-to-tracked-files.md`)_

### Pgbouncer in the path

_(pending merge from `docs/adr/0019-pgbouncer-in-the-path.md`)_

### Transaction scoped handle

_(pending merge from `docs/adr/0020-transaction-scoped-handle.md`)_

### Hooks receive an event

_(pending merge from `docs/adr/0021-hooks-receive-an-event.md`)_

### References declare their inverse

_(pending merge from `docs/adr/0022-references-declare-their-inverse.md`)_

### Mixins carry behaviour

_(pending merge from `docs/adr/0023-mixins-carry-behaviour.md`)_

### No annotation slot

_(pending merge from `docs/adr/0024-no-annotation-slot.md`)_

### Expansion is one statement

_(pending merge from `docs/adr/0025-expansion-is-one-statement.md`)_

### Vectors declare their index

_(pending merge from `docs/adr/0026-vectors-declare-their-index.md`)_

### Keyset pagination

_(pending merge from `docs/adr/0027-keyset-pagination.md`)_

### Typescript client

_(pending merge from `docs/adr/0028-typescript-client.md`)_

### Go cli

_(pending merge from `docs/adr/0029-go-cli.md`)_

### Declared scope is required

_(pending merge from `docs/adr/0030-declared-scope-is-required.md`)_

### Dart client

_(pending merge from `docs/adr/0031-dart-client.md`)_

### Sqlb command

_(pending merge from `docs/adr/0032-sqlb-command.md`)_

### Array columns

_(pending merge from `docs/adr/0033-array-columns.md`)_

### One column addresses a row

_(pending merge from `docs/adr/0034-one-column-addresses-a-row.md`)_

### Type overrides

_(pending merge from `docs/adr/0035-type-overrides.md`)_

### The wire is the column name

_(pending merge from `docs/adr/0036-the-wire-is-the-column-name.md`)_

### Search is ilike until it cannot be

_(pending merge from `docs/adr/0037-search-is-ilike-until-it-cannot-be.md`)_

### Collections are flat

_(pending merge from `docs/adr/0038-collections-are-flat.md`)_

### A schema edit is an api edit

_(pending merge from `docs/adr/0039-a-schema-edit-is-an-api-edit.md`)_

### The driver is a dependency

_(pending merge from `docs/adr/0040-the-driver-is-a-dependency.md`)_

### Computed fields

_(pending merge from `docs/adr/0041-computed-fields.md`)_

### The exit is generated

_(pending merge from `docs/adr/0042-the-exit-is-generated.md`)_

### Declared actions

_(pending merge from `docs/adr/0043-declared-actions.md`)_

### The container is an adapter

_(pending merge from `docs/adr/0044-the-container-is-an-adapter.md`)_

### The stream is a seam

_(pending merge from `docs/adr/0045-the-stream-is-a-seam.md`)_

### A negation is sqls

_(pending merge from `docs/adr/0046-a-negation-is-sqls.md`)_

### No default hook registry

_(pending merge from `docs/adr/0047-no-default-hook-registry.md`)_

### Auto incrementing keys

_(pending merge from `docs/adr/0048-auto-incrementing-keys.md`)_

### The skill is generated

_(pending merge from `docs/adr/0049-the-skill-is-generated.md`)_

### Reachability is a property of the mount

_(pending merge from `docs/adr/0050-reachability-is-a-property-of-the-mount.md`)_

### A gap in the declaration is reported

_(pending merge from `docs/adr/0051-a-gap-in-the-declaration-is-reported.md`)_

### A singleton is an op that removes the id

_(pending merge from `docs/adr/0052-a-singleton-is-an-op-that-removes-the-id.md`)_

### The manifest describes what cannot be guessed

_(pending merge from `docs/adr/0053-the-manifest-describes-what-cannot-be-guessed.md`)_

### A named scope is releasable at the mount

_(pending merge from `docs/adr/0054-a-named-scope-is-releasable-at-the-mount.md`)_

### A nested query runs nobodys hooks

_(pending merge from `docs/adr/0055-a-nested-query-runs-nobodys-hooks.md`)_

### A junction is a table

_(pending merge from `docs/adr/0056-a-junction-is-a-table.md`)_

### A read is a query and a row scoped write is a mutation

_(pending merge from `docs/adr/0057-a-read-is-a-query-and-a-row-scoped-write-is-a-mutation.md`)_

### Serve owns the boilerplate mount is the seam

_(pending merge from `docs/adr/0058-serve-owns-the-boilerplate-mount-is-the-seam.md`)_

