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

Since sqlb generates code anyway, the query builder could have been
generated per table — `Users.Age.Gte(18)` as real Go, the way ent does it.
That trades compile-time safety against generated-code volume: a generated
builder catches a hallucinated column name at compile time, which matters
when an agent is writing the queries, but adds hundreds of lines of API
surface per table.

Instead the query engine is generic and reflective: `sqlb.Query[T]()` builds
a model from `db` and `sqlb` struct tags, cached per type, and column
references are strings at the core (`sqlb.F("age")`). Compile-time safety is
recovered separately by a thin generated facade over the same model rather
than by generating the builder itself. This keeps the engine one small
package instead of a template that has to produce a correct API per table,
and it means the engine works on any tagged struct, including ones sqlb
never generated — which is what makes optional codegen possible. A new
builder feature benefits every table at once, with no regeneration needed.

The trade shows up as `sqlb.F("titel")` compiling fine and failing at
runtime, and as the engine having to validate column names itself rather
than getting that from the type system; reflective scanning is also slower
than generated field access, though that has not been measured against real
query latency. Revisit if the typed facade proves insufficient and
column-name typos keep reaching production — then generate the builder after
all — or if profiling shows reflective scanning is a meaningful share of
request time on realistic result sets, in which case generate scan
functions while keeping the builder itself generic.

### Capabilities are opt in

Exposing a table over a dynamic filter API means deciding what a client may
ask about. PostgREST's model — everything in the exposed schema is fair
game, with row-level security as the guard — puts the whole schema one
policy mistake away from public, and turns "which columns can a client
filter on" into a question answered by reading policies rather than the
table. sqlb instead makes every capability opt-in per column: `Filterable`,
`Sortable`, `Searchable`, `Expandable`, `Hidden`. A column that does not
declare a capability cannot be reached through it, and the request is
rejected with a 400 rather than silently ignored.

`Hidden` goes further than the others: a hidden column is reported as
*unknown* rather than *not filterable*, so its existence cannot be probed
from the rejection, and declaring a column both `Hidden` and `Filterable` is
a schema validation error, because a filterable secret can be recovered a
character at a time. `filter.Apply` owns the projection and defaults to
non-hidden columns, so a handler that forgets to specify one cannot leak a
hidden column by omission.

The payoff is that the blast radius of exposing a table is legible from the
schema file alone — adding a column never silently widens the API — and an
index can be guaranteed for every filterable column, because the set is
finite and declared. The cost is friction by design: every new filter needs
a schema edit and a regeneration. That cost is deliberately asymmetric in
the other direction too — loosening the default later is nearly
irreversible, since clients would come to depend on filters that opt-in
never granted, and tightening after that breaks them in ways that are hard
to see coming. Tightening from here stays cheap, because nothing is exposed
that was not declared. Revisit if the declare-and-regenerate loop becomes
the dominant complaint from people building views, which would argue for a
per-resource permissive mode that still excludes `Hidden`.

### Generated rest handlers

The REST surface could be one generic handler dispatching on the path, or a
generated handler per resource with a typed filter struct and a precise
OpenAPI operation. The apparent trade was boilerplate against client-side
typing — a generic handler supposedly cannot describe itself precisely,
because the filter grammar is compositional. But the grammar is
compositional and the *columns are not*: they are finite, known at
registration, and each admits a documented operator vocabulary. One query
parameter per filterable column describes the surface exactly without
describing the grammar, and [Huma](https://huma.rocks) makes this
buildable — it keeps explicitly-set operation parameters and hands an input
struct's `Resolve` the raw query values, so `filter.Parse` still owns
validation.

So sqlb uses one generic handler, instantiated per resource through
generics: `rest.Resource[T, C, U]` registers the exposed operations for a
model on a `huma.API`, and the OpenAPI operation is built per resource from
`sqlb.Model`. Generics rather than reflection, specifically, because hooks
are keyed by type — a reflective dispatcher holding a `reflect.Type` cannot
call `On[T]()`, which is how tenant scoping stops being something each
handler has to remember. Codegen emits only what generics cannot express:
the request bodies. Create, patch and row are three different JSON schemas
over one table, and no single Go type serves all three honestly, so
`rest_gen.go` holds two body types per writable resource plus one
`rest.Resource` call per exposed table. `rest` takes a `huma.API` rather
than building a router, so the application keeps its own router and
middleware.

The result is end-to-end typing into the frontend — a filter that does not
exist fails at the client's compile step, not as a runtime 400 — and adding
a table costs one generated registration, with response schema, parameter
list and rejection allow-list all deriving from the same capability flags so
they cannot disagree. The cost is a dependency on Huma's shape, and on huma
itself: it sets the module's Go floor at 1.25 for every consumer, which was
weighed against the module graph cost and accepted, since sqlb had already
given up "importing it costs nothing" the moment the driver became pgx
rather than `database/sql`. Moving off Huma later would cost only the `rest`
package — the engine, filter grammar, generated bodies and generated
clients all read `sqlb.Model`, never the OpenAPI document — but the response
and error shape (`{items, page, per_page, has_more, total}` and an RFC 9457
problem document) is the genuinely expensive surface to change, since a
generated client or an agent's retry logic depends on its exact structure.
Revisit if the per-column parameter list gets unwieldy at realistic column
counts — a fifty-column table documenting fifty parameters — which would
argue for collapsing the rare ones behind a single `filter` parameter with
looser typing.

### Hooks as domain seam

A generated data layer has to leave somewhere for domain logic to live, or
teams route around it. The common failure is that generated CRUD is
all-or-nothing: as soon as one endpoint needs to normalise an email or stamp
an owner, it gets written by hand and the generated version is abandoned.
Multi-tenant scoping is the sharpest case of this — `WHERE org_id = $1` has
to be on every read, and forgetting it once is a cross-tenant data leak.

sqlb registers hooks per model — `BeforeQuery`, `Before`/`AfterCreate`,
`Before`/`AfterUpdate`, `Before`/`AfterDelete`, and `AfterDeleteRows` —
and `BeforeQuery` is the load-bearing one. It receives the `*Builder` and
may amend it, so one registration constrains every read of that model,
including reads issued by generated REST handlers. Terminal methods clone
the builder before running hooks, so a hook's predicates cannot accumulate
across repeated executions of the same query value, and a hook that returns
an error aborts the operation before any SQL runs. This turns tenant scoping
and soft-delete filtering into one registration each, instead of a rule
every call site has to remember.

The cost is that hooks are action-at-a-distance — reading a query does not
tell you what will execute, and hook order is registration order. Two limits
worth naming: registration is default-*open*, where row-level security is
default-*deny*, so an unregistered model serves every tenant's rows with no
failure signal — this is closed only where handlers are generated, by a
schema declaration the mount checks, and not for queries written by hand in
Go. And write hooks were originally a thinner seam than intended:
`BeforeCreate` receives a bare row and `BeforeUpdate` cannot read its own
assignments; wrapping generated writes in a transaction closed most of that
gap by giving a hook something to query against, but a hook on an ordinary
read still has no executor. `AfterDeleteRows` exists alongside `AfterDelete`
rather than changing its signature, because the rows it carries are not
free — they arrive via `DELETE ... RETURNING`, so the clause is only added
when a rows-kind hook is actually registered, keeping the cost visible at
registration instead of charged to every delete in the process.

Removing hooks entirely would be the expensive direction: tenant scoping
would move back to individual call sites, losing the guarantee that it
cannot be forgotten. Revisit if people need to bypass a hook for a
legitimate admin path — the likely answer is an explicit unscoped builder,
not a way to disable hooks globally — or if hook ordering starts to matter
enough that registration order needs to become explicit priorities.

### Typed column facade

Making the query engine reflective means `sqlb.F("titel")` is a runtime
error, and that's the design's largest cost — it bites hardest in the
workflow sqlb targets, where an agent is writing the queries and a compile
error is a fast correction signal while a runtime error is a slow one.
Since codegen already emits models, a typed facade over them is nearly
free: sqlb generates a typed column set per table, so `PostCols.Status` is a
`sqlb.Col[PostStatus]` and `PostCols.Title` is a `sqlb.TextCol[string]`.
Predicate construction is type-checked; the builder underneath stays
generic.

A few choices sharpen the facade. `Col[T]` does not embed `Field`, because
embedding promoted every operator onto every column and made `Contains`
callable on an integer column — pattern operators live only on
`TextCol[T ~string]`. Nullable columns are typed as their base type, so the
comparand is a plain value and NULL is expressed with `IsNull` rather than
threading through the type parameter. Hidden columns are omitted from the
generated set entirely, so a predicate against one cannot even be written.
Update statements are wrapped too, since `Set(string, any)` checks neither
name nor type, but the select builder itself is not — its twenty-odd
chainable methods would each need a re-wrapped return type for safety the
column set already provides elsewhere.

Misspelled columns, wrong comparand types, and text operators on non-text
columns all fail at compile time now, for one small generated file per
table. What's given up is that predicates stay untyped — `sqlb.Pred`, not
`Pred[T]` — so a column from the wrong table still compiles and only fails
at the database, and the facade is a second artefact the generator has to
keep in step with the model. Removing it later is cheap, since it's purely
additive; `Pred[T]` would be the expensive direction, touching the AST,
every combinator, and the filter package's intermediate representation.
Revisit if cross-table column mixing turns out to be a common mistake
rather than a theoretical one — though `Pred[T]` still has to answer how a
join condition, which references two tables, could ever be `Pred[T]` for a
single `T`.

### Codegen is optional

Making the Go schema DSL the source of truth is a good end state and a poor
starting position: adopting it in an existing project would mean importing
the schema, handing over DDL control, and regenerating models that already
exist. The engine needs none of that — it reflects over struct tags and
derives column names from field names when no tag is present — so the
schema DSL and the generator are both optional. Metadata the builder cannot
infer is supplied at runtime instead:

```go
sqlb.Describe[Invoice]().
    PrimaryKey("id").
    Defaulted("id").
    Filterable("customer_id", "paid").
    Sortable("amount_due").
    Hidden("memo")
```

Descriptions merge onto struct tags, so a partly tagged model can be
completed, and naming a column that does not exist panics at startup,
listing the ones that do. Every capability the generator can emit has a
runtime form, including relations — `Relation("Customer", "customer_id")`
is the no-codegen half of `?expand` — which is the test this decision has
to keep passing: a capability reachable only from generated tags would
quietly make the generator mandatory again. This is what lets sqlb be
layered over structs another generator produced, without editing them,
turning adoption into something incremental rather than a migration; it
also keeps the engine honest, since anything it needs must be expressible
without importing the schema package at all.

The two routes can disagree, and nothing checks either against the
database. One consequence of allowing `Describe` at runtime was a real data
race: an early guard flag was read when a `Description` was constructed but
the writes happened in the chained calls after that, so a query built in
between could pass the guard and race the writes to the fields the request
path reads to decide what a caller may see. The fix keeps this decision's
constraint of no lock on the read path by inverting where the cost lands —
`Describe` now copies the model, writes the copy, and publishes it into the
model cache, so a published `*Model` is never written again and a statement
in flight always sees a consistent snapshot. Describing costs a copy once,
at startup; reading costs nothing. Revisit if the two routes drift
confusingly in practice — the fix would be having the generator emit
`Describe` calls rather than tags, collapsing to one mechanism.

### Actionable errors

Because capabilities are opt-in, requests get rejected routinely — that's
the design working as intended. The caller most likely to hit a rejection
is a program assembling a request against a schema it only partly knows: a
frontend, a client library, or an agent. For all three, `400 column is not
sortable` on its own is a dead end that costs a round trip and a guess. So
every rejection names both what was wrong and what would have worked:

```
filter: sort=body: column is not sortable (allowed: title, status, view_count, published_at, created_at)
```

Parsing collects every problem in a request rather than stopping at the
first, so a malformed request takes one round trip to fix rather than one
per mistake, and schema validation follows the same rule. The exception is
`Hidden` columns, which are reported as unknown and never listed in an
allow-list — the diagnostic must not become an oracle for probing what
exists.

This lets a caller correct itself from the response alone, and the
allow-list doubles as discovery, reducing how much schema a client needs up
front — part of why the API is pleasant to drive with an agent. The cost is
that error responses are larger and disclose the shape of the resource,
which is fine for something meant to be exposed but makes the exposure
decision itself carry more weight. The response shape is not free to change
later, either: a generated client's or an agent's retry logic depends on
the current structure, so renaming or renesting fields is a breaking change
even on an error path. Revisit if disclosing the filterable column set
turns out to be unacceptable for some resource — add a per-resource terse
mode rather than making terse the default — or if allow-lists get long
enough to be unhelpful, in which case truncate with a count and a pointer
to the OpenAPI document rather than dropping them outright.

### Change feed outbox

Dynamic data views need to know when their data changed, and the reference
points — Convex especially — set the expectation that a view can be live
rather than polled. Firing a notification from an in-process `AfterCommit`
hook loses events when the process dies between commit and publish, and
delivers phantom events when a transaction commits the notification but
rolls back the data. So every mutation that goes through sqlb writes a row
to an outbox table in the *same* transaction as the change. A dispatcher
tails that table — woken by `LISTEN/NOTIFY` rather than polling — and fans
out to subscribers. Subscribers receive invalidation events (table plus row
key), not recomputed results; clients refetch. The fan-out endpoint, event
shape and reconnection contract are a separate decision behind a
`rest.Source` seam; what belongs here is the outbox table, the trigger that
wakes the dispatcher, and the ordering guarantee underneath both.

The outbox row *is* the event; `NOTIFY` is only a doorbell carrying no
payload, which is why a lost notification degrades to latency rather than
lost data — the dispatcher also polls on a slow fallback interval, which is
what keeps the feed correct behind a connection pooler that silently
swallows `LISTEN`. An `AFTER INSERT` trigger on the outbox table rings that
doorbell, deliberately not a call from sqlb's own mutation path: issuing
`NOTIFY` from Go is one fewer database object but is forgettable — a new
mutation path that writes the outbox and omits the notify would work in
tests and lag in production. It's also deliberately not a trigger on every
domain table, since that captures row changes rather than domain events and
floods during backfills.

The hardest problem only appeared once there was something to be correct
about rather than describe: a bigserial primary key does not promise commit
order. Two transactions can take ids 5 and 6 and commit in the other order,
and a dispatcher reading `id > cursor ORDER BY id` would see 6, advance past
5, and lose it silently — exactly the failure the whole design exists to
prevent, arriving from inside the mechanism meant to prevent it. The fix is
`pg_advisory_xact_lock`, held from the outbox insert until commit, so id
order *is* commit order by construction and the dispatcher needs no
reasoning about visibility at all. The alternative considered and rejected
was gating the tail on a snapshot watermark (`pg_snapshot_xmin`), which has
no write-path cost but is wrong in a way that took a while to see: the xid
is assigned at a transaction's *first* write while the sequence value is
assigned at the outbox insert, so a transaction can hold an earlier id and
a later xid, and the watermark then admits the higher id first. Repairing
that means dispatching in `(xid, id)` order, which is no longer an order a
client's `Last-Event-ID` can name — the lock buys a position that is a
plain row id, and a row id is what makes replay across a restart possible
at all.

That correctness has a stated cost: writes to published models serialise
from the outbox insert to roughly the commit, bounding write throughput on
those models at about one transaction per commit latency — a real ceiling,
even though it's the same order Postgres's own WAL flush already imposes.
An application that publishes a write-heavy table pays for a feature its
clients may not even subscribe to, and the only remedy today is not
publishing that model. Retention (24 hours by default) is a delivery
guarantee rather than a disk setting — a subscriber resuming from a pruned
position gets a reset rather than a replay — and it's the piece of the
design chosen with the least confidence, since it was picked without a real
consumer. The dispatcher probes its own `LISTEN` at startup, ringing the
doorbell from a separate connection and reporting if it never hears back:
a pooled `LISTEN` is silently accepted and useless, which otherwise leaves
the feed correct, slow, and looking fine to everyone — the exact shape of
failure that earns a check rather than a paragraph. This is distinct from
`sqlb.AfterCommit`, which is in-process and at-most-once — fine for
invalidating a local cache, silently lossy as a change feed.

What's bought is at-least-once delivery that survives a process restart,
where only the dispatcher itself needs to be highly available; replay that
survives a rolling deployment, since a reconnecting client is caught up out
of the table rather than needing a full refetch; and two dispatchers over
one table both delivering, which is the horizontal-scaling story. Revisit
if the advisory lock turns out to be the binding constraint on write
throughput — the likeliest reason this gets revised, and unmeasured either
way — trading it against the `(xid, id)` dispatch order, which costs
exactly the row-id position that makes replay-across-a-restart possible.
Also revisit if outbox write volume becomes a measurable drag, which points
at logical replication (`pgoutput`, no write cost, but a replication slot
and decoded rows instead of typed domain events) — and if retention proves
the wrong knob, where the fix is likely a cheaper reset rather than a
bigger window.

### No internal split

Package `sqlb` exports a large number of identifiers: some are the daily
API, some are public only because another package needs them across a
boundary, some are the compiler's own vocabulary. Go offers `internal/` to
make that distinction compiler-enforced, and two facts decided against
using it. The genuinely internal machinery — compiler, scanning, model
building, escaping — is already unexported within the package, so
`internal/` would restate a boundary that already holds. And the obvious
extraction fails on its own terms: `Expr` and `Raw` have to stay public as
the documented escape hatch, so hiding the remaining node types would buy
only field renaming while forcing `Pred.Expr()` to return a type callers
cannot name.

So there is no `internal/` package. The layout stays flat, and the
distinction is expressed as documented tiers plus a `v0` version instead:
stable (the query builder, predicates, the typed facade, hooks, mutations,
`Describe`, `filter`, `schema` — changes here are breaking changes and
treated as such), provisional (`Model`, `ColumnInfo`, `Dialect` and similar
— public because `filter` and generated code cross a package boundary, and
expected to move), and escape hatch (`Expr` and the node types — use `Raw`,
`RawPred`, `RawSel` instead; the rest is compiler vocabulary that will
change without ceremony). This avoids import gymnastics and a premature
boundary in a library still moving pre-1.0, and tiers communicate intent
per identifier where `internal/` can only work per package. The cost is
that tiers are convention, not a compiler check — someone can depend on a
node type like `Binary` and be broken with only a doc comment to point at,
and a reader cannot tell the tiers apart without consulting the docs.

Introducing `internal/` later stays mechanically cheap inside the module,
but the cost lands entirely on external users: anything they imported that
moves becomes uncompilable with no deprecation path, since `internal/` is
absolute. If it's going to happen, it should happen before there are
external users to break. Revisit properly at v1.0 — promote provisional
identifiers to stable, or hide them — or sooner if someone outside the
module depends on a node type and is broken by a compiler change, which
would be the trigger to extract the AST behind `internal/` and accept the
`Pred.Expr()` awkwardness that comes with it.

### Migrations and import

Making the Go DSL the source of truth means something has to turn a schema
edit into DDL, and something has to turn an existing database into a
schema. A wrong answer here is destructive: a diff that mistakes a rename
for a drop-and-add loses a column of production data, and it cannot tell
the two apart from the schema alone. So migrations are generated, not
applied — sqlb emits files and stops; it does not own a runner, track
applied versions, or connect to a database to migrate anything. Goose is
the default output format, with golang-migrate and plain SQL selectable and
`Plain` as the escape hatch for runners sqlb doesn't ship. The format isn't
cosmetic: goose's `NO TRANSACTION` directive is file-level, so a migration
containing `CREATE INDEX CONCURRENTLY` would strip the rollback guarantee
from every unrelated change sharing its file — which is why index changes
get their own migration file, versioned to sort immediately after the one
they depend on.

The diff itself runs between two registries, not between a registry and a
live database. Introspection produces the same `*schema.Registry` the DSL
does, so `Diff(current, target) []Change` is a pure function, testable
without a database — and the same machinery works pointed in either
direction. Current state comes from replaying the checked-in migration
history into a scratch database and introspecting *that*, which validates
the history and catches drift as a side effect. Destructive changes are
opt-in: dropping a column or table, narrowing a type, or adding `NOT NULL`
without a default all render commented out, with the reason stated. A
change that depends on one of those commented-out changes is commented out
too — carrying `DependsOn` rather than `Destructive`, because it's
premature rather than dangerous — since without that, a commented `ADD
COLUMN` followed by a live `ADD CONSTRAINT` would fail the file partway
through instead of being the no-op the guard intends. Lock hazards, by
contrast, are stated rather than gated: a statement that rewrites or scans
a table is emitted live, with the lock it takes and an expand/contract
sequence named above it, because unlike a destructive change this is only
*occasionally* slow, and how slow depends on a row count the schema doesn't
have. `migrate.Unblock` can rewrite the lock-brief sequence — a scanning
`ADD CONSTRAINT` into `NOT VALID` plus `VALIDATE`, for instance — but the
caller decides whether to apply it, because the sequences aren't equivalent
under failure: they can leave a binding-but-unvalidated constraint or an
invalid index behind, where the plain statement leaves nothing.

Renames are declared, never inferred — `.RenamedFrom("old")` for one
release, and without it a rename is rendered as a drop and an add: lossy,
but never silently wrong. Adoption is `sqlb import`, which reads
`pg_catalog` and emits a `schema.go` with no capabilities, so the result
describes the database exactly and exposes nothing over REST until
capabilities are added by a deliberate edit; what import cannot represent,
it reports, and an empty report is the claim that the registry describes
the database completely. Reading the catalog is a separate package
(`introspect`) from writing DDL (`migrate`), which is what keeps the diff a
pure function, and formats are rendered in code rather than translated by
an agent — the variation between runners is only about fourteen lines of
syntax each, but what they share is semantics (file splitting, `Down`
reversing `Up`, destructive statements staying commented, multi-statement
delimiting), and a translation step would have to re-derive all of that and
get it right *most* of the time. A wrong migration is applied once, often
irreversibly, and nothing type-checks it — so agents are better spent
reviewing a destructive migration or supplying rename hints than generating
SQL text. No `USING` clause is ever generated for a type change, either:
Postgres refusing an implicit cast is the correct outcome, and a generated
cast nobody reviewed would truncate data silently instead.

The round trip is proven, not assumed: `pgtest` runs render, apply, read
back, diff against real Postgres in CI, and a stricter *fixpoint* —
import, re-render, apply, re-import, diff — is asserted unconditionally and
is empty, which is what makes adoption actually trustworthy rather than
merely plausible. Cost of change rises sharply once the first generated
migration is applied anywhere real: before that the diff engine is a pure
function and freely rewritable, but after, the migration history is
permanent, and the file format is the single most expensive thing here to
change later. Revisit if the shadow database proves too heavy for the inner
loop (replay into an in-memory model instead, losing validation against a
real parser), if people start uncommenting destructive changes without
reading them (meaning the guard isn't working and needs to become a
separate reviewed file rather than a comment), or if import silently drops
a construct that matters — the failure mode this design watches for
hardest, and the fix would be a raw-DDL passthrough.

### Module isolation

A target codebase arranged as independent fx modules — `auth`, `billing`,
`tenants`, `rag` — with a rule that no module imports another, each owning
its own migration directory, and cross-module foreign keys forbidden
outright, collided with sqlb in three ways: `schema.Table` registered into
one global registry, so two modules couldn't both own a table called
`events`; table names had no namespace, leaving prefixing to a discipline
that had already drifted; and `Ref` took a `*TableDef`, requiring exactly
the Go import the architecture forbids.

The fix makes a registry the unit of module isolation:
`schema.NewModule("billing")` returns a registry whose tables are prefixed
with the module name, while declarations use the local name —
`Table("invoices")` — so the prefix can never be forgotten and moving a
table between modules is a one-line change. Prefixing uses plain names
(`billing_invoices`), not Postgres schemas, and there's no abstraction
layered over the two: a Postgres schema is a deployment model, not a
rendering strategy, since only one of its four practical requirements is
about how a name renders — the others are `search_path` management,
ordering `CREATE SCHEMA` ahead of each module's first migration, and
per-schema goose version tables. A strategy interface covering rendering
alone would suggest switching between the two is just configuration, while
the hard parts stay entirely unbuilt. The prefix stays a storage concern
and never reaches the URL — a REST path defaults to the local name, so
moving a table between modules isn't a breaking API change — and
cross-module relationships are declared rather than enforced:
`ExternalRef("tenant", "tenants.id")` produces the column and a join index
but no `FOREIGN KEY`, with the target left as free text because resolving
it would require the very dependency this design avoids. A reference built
this way can't be `Expandable`.

Modules get to migrate and deploy independently this way, and either side
of a soft reference can move to its own database without dropping a
constraint; the relationship still shows up in the manifest as
`enforced: false`, so tooling and readers can see what the database itself
cannot. The cost is that referential integrity becomes the application's
job — nothing stops a `tenant_id` pointing at a deleted tenant, and no
cascade cleans it up — prefixed names run longer, and `ExternalRef` targets
are unchecked strings that rot silently when the other side renames its
table. Namespacing is the expensive half to reverse: adding a prefix to an
existing table is a rename, meaning a migration per table and a
coordinated deploy, though it's free before a module's tables exist.
Revisit if orphaned rows become a real operational problem (the answer is
likely a periodic reconciliation job per module, not foreign keys, which
would reintroduce the coupling this avoids), if a module needs to move to
a genuinely separate database (at which point prefixes stop helping and
Postgres schemas become worth their operational cost — the compiler
already renders qualified names, but `search_path`, schema-creation
ordering and per-schema version tables would still need building), or if
`ExternalRef` targets rot often enough to matter, which would justify a
lint pass checking them against a per-module manifest without adding a
compile-time dependency.

### Guards proven both ways

Three guards in this repository reported success while checking nothing, and
each was written deliberately and looked right on review: a dependency check
that grepped package paths for a dot and matched the standard library's own
vendored code, filtering everything away; a later version of the same check
that let `go list -m all` fail to stderr, so empty output read as "no
dependencies"; and a bisect check running under `set -e`, where the first
commit — legitimately without Go packages — killed the script before it
printed anything. A guard's failure path runs far less often than its success
path, so it can go unexercised until the day it matters, and a guard that
cannot fail is worse than no guard: absent tooling prompts caution, broken
tooling prevents it.

So a guard is not trusted until it has been observed failing on purpose.
Before a check joins the gate, both directions get demonstrated: it passes on
a clean tree, and it fails — naming the problem — on a tree broken in exactly
the way it exists to catch. Where the broken state is cheap to construct, a
test constructs it, so the failing branch runs on every CI run rather than
only once at review time; the migration diff engine's destructive-change guard
and codegen's dry-run check both work this way. Two narrower rules follow from
the specific failures above: a command whose own failure would empty its
result must have its exit status checked, since silence is not evidence of
cleanliness, and under `set -e` an expected failure must be guarded by `if`,
not read from `$?` afterward.

This buys the only real evidence that a green pipeline means something — every
guard's failing branch has run at least once under conditions someone chose —
at the cost of a slower add-a-check workflow, since the demonstration is
manual wherever the broken state isn't cheap to construct. Mechanically it's
cheap to abandon, since it's a practice rather than a structure; what dropping
it costs is confidence, and only gradually, because a guard that rots into
uselessness is invisible by construction. Revisit if a guard is found silently
passing despite this — the manual demonstration isn't working on its own, and
guards need a shared harness that constructs the failure for them.

### Enums as text and check

`schema.Enum("status", "draft", "live")` declares a column constrained to a
fixed set, and Postgres has a native type for exactly this — the obvious
choice right up to the point the list has to change, which is one of the most
ordinary schema edits there is. A native enum value cannot be removed; the
route is a replacement type, a rewrite of every column using the old one, and
a drop. Adding a value cannot happen in the same transaction that reads it, so
a change needing that drags every unrelated change sharing its migration file
out of its transaction too. And the type is schema-level, not table-level, so
under this project's module prefixing two modules declaring their own
`status` enum collide in a namespace neither owns. Against that, the native
type buys storage compactness, a defined sort order, and type-level rejection
at every call site.

So an enum column compiles to `text` with a named `CHECK` constraint —
`CHECK ("status" IN ('draft', 'live'))` — rather than a native type. Changing
the list becomes an ordinary `DROP CONSTRAINT` plus `ADD CONSTRAINT`, which
the diff engine already does for every other constraint: no special case, no
new object type, no transaction exception. Removing a value from the list
isn't marked destructive, because it can't lose data — Postgres rejects the
whole `ADD CONSTRAINT` if any existing row would violate it — so it renders
live, with a comment naming the values no longer permitted, since the fix
lives in the rows rather than the migration.

The cost is more storage than a native enum's four bytes, no implicit sort
order — `ORDER BY status` sorts alphabetically, and declaration order needs an
explicit `CASE` — and a bad value rejected at insert time by the constraint
rather than at parse time by a type system. The direction of that cost is
deliberately the cheap one to be wrong in: nothing outside the DDL renderer
knows the representation, so moving to a native enum later is confined and
mechanical, while moving away from one would mean rewriting every table that
used it. Revisit if declaration-order sorting is needed in more than one or
two places (the likely answer is an explicit ordinal column, not a native
enum), if a consumer's tooling reads `pg_enum` and can't be taught to read
`pg_constraint` instead, or if the text column's width becomes measurably
significant — at which point the right escalation is a lookup table, which is
also the natural move once a value list acquires attributes of its own.

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

