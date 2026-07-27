# Architecture

How sqlb fits together, and why the seams are where they are. For the reasoning
behind individual choices, see the [decision records](adr/). For where this is
going, see the [vision](vision.md).

*Last reviewed: 2026-07-27.*

## The shape of it

```
  blogschema/schema.go          ← you edit this
         │
         │  sqlb generate                                    (not built yet)
         ├──────────────▶ migrations/*.sql
         ├──────────────▶ models.go        db + sqlb struct tags
         ├──────────────▶ columns.go       typed column facade
         ├──────────────▶ rest.go          per-resource handlers
         └──────────────▶ openapi.json ──▶ TypeScript client

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
| `codegen` | Generates models, the typed column facade and the manifest. `Check` is the dry-run mode wired into CI. | `schema` |
| `example/blog` | A worked schema plus the artefacts codegen must produce. | all of the above |

The dependency direction matters: `schema` is a leaf that nothing imports at
runtime, and `sqlb` has no dependency on `schema`. That is deliberate. It is
what makes [ADR-0010](adr/0010-codegen-is-optional.md) possible — the engine
cannot quietly grow a dependency on the schema DSL, because it cannot see it.
Capabilities reach the runtime as struct tags or `Describe` calls, never as a
schema import.

`migrate` and `codegen` sit on the other side of that line: both are design-time
tools that read `schema` and emit text, and neither is reachable from the
request path. `migrate` is the only package that renders DDL, which is why the
Postgres type mapping lives there rather than beside the query compiler — a
`Format` decides what a *runner* wants a file to look like, and the DDL layer
decides what the *database* wants a statement to look like.

`sqlb` has no third-party dependencies. `Executor` is the two-method subset of
`*sql.DB` that the engine needs, so pgx works through its stdlib adapter and any
instrumenting wrapper works unchanged.

## Request path

A list request through a generated handler:

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
   ([ADR-0008](adr/0008-hooks-as-domain-seam.md)).
4. **Compile.** The AST renders to SQL with `$N` placeholders. Values are always
   bind parameters. Identifiers are validated against the model and quoted.
   `LIMIT`/`OFFSET` are literals so the planner can see them — safe because both
   are range-checked ints.
5. **Scan.** Result columns are matched to struct fields by name. Unmatched
   columns are read and discarded, so a query selecting extra expressions still
   scans into the model.

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
| **Stable** | `Query`/`Builder`, `F`/`Pred`/`And`/`Or`/`Not`/`If`, `Field` and its operators, `Col`/`TextCol`/`Typed`/`TextColumn`, `Order`, the aggregates, `InsertRows`/`UpdateRows`/`DeleteRows`, `On`/`Hooks`, `Describe`, `Collect`, `Executor`, `ErrNotFound`/`ErrUnscoped`, all of `filter` and `schema` | Changes are breaking changes and are treated as such |
| **Provisional** | `Model`, `ColumnInfo`, `ModelOf`, `Selectable`, `Selection`, `Dialect`, `Postgres` | Public because `filter` and generated code need them across a package boundary. Expect movement |
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
| A change with no `Down` | Renders an explanation, not an empty section that looks like a working rollback |
| Filter names an unknown or uncapable column | 400 listing what would have been accepted |
| Schema authoring mistake | Every problem reported at once, each with the fix |

Two deliberate exceptions, both documented where they happen: a page size above
the maximum is capped rather than rejected, since a client asking for too much
should get the maximum rather than an error; and `Builder.All` tolerates
unfilled fields, because a partial projection is exactly what `?select=id,name`
is. `Collect` is strict precisely because its destination type was written to
match the projection.

## Testing

The engine's tests run against an in-memory `database/sql` driver that records
statements and replays canned rows, so hooks, scanning and the mutation paths
are covered end to end without a live Postgres. SQL-string assertions cover the
compiler.

The typed facade is checked by attempting to compile the cases that should fail
and confirming they do — a test that passes vacuously if the facade stops
working is worse than no test, so those are exercised as real build attempts.

## Known gaps

- The generator does not exist. Everything under `example/blog` is hand-written
  to the exact shape it must produce, so it doubles as the generator's fixture.
- `?expand` validates relation names but does not perform the join.
- No migration diffing and no change feed. See the [vision](vision.md).
