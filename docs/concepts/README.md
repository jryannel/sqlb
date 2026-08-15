# How it fits together

Five short pages, one idea each. Together they are the reasoning the rest of the
documentation assumes; the [guide](../start/quickstart.md) is how to use it, the
[decision records](../architecture.md#decisions) are the long arguments, and this is the middle.

| | |
|---|---|
| [A query is a value](queries-are-values.md) | Nothing runs when you build a query, so predicates compose on a branch and a hook can amend one |
| [One grammar, two producers](one-grammar.md) | The URL filter grammar compiles into the same predicate AST your Go code produces |
| [Capabilities](capabilities.md) | Every column opts in to what the outside world may do with it, and the failure is a 400 rather than a leak |
| [Where domain logic goes](domain-logic.md) | Hooks are the seam, and what belongs below them in the database |
| [Generated, not hidden](generated-not-hidden.md) | What codegen emits, why it is committed, and where the hand-written half attaches |

## The shape of it

```
  blogschema/schema.go          ← you edit this
         │
         │  go generate ./...           (a generator main, not a CLI)
         ├──────────────▶ models.go        db + sqlb struct tags
         ├──────────────▶ columns.go       typed column facade
         ├──────────────▶ rest_gen.go      request bodies + registration
         ├──────────────▶ client.gen.ts    TypeScript client
         └──────────────▶ cli_gen.go       cobra command tree

                    ┌─────────────────────────────┐
   Go code ────────▶│                             │
                    │      predicate AST          │──▶ compiler ──▶ SQL + args
   HTTP query ─────▶│   (sqlb.Pred, sqlb.Expr)    │
     (filter)       └─────────────────────────────┘
                                  ▲
                                  │
                            BeforeQuery hooks
```

Two things carry most of the design, and almost everything else follows from
them: a query is a value, and there is one predicate AST with two producers.

## The layers, and which way they depend

| Package | Responsibility | Depends on |
|---|---|---|
| `schema` | The declarative DSL and its validation. Design-time only; nothing at runtime imports it | nothing |
| `.` (`sqlb`) | AST, Postgres compiler, generic builder, model reflection, mutations, hooks, `Describe` | stdlib only |
| `filter` | URL grammar → predicates, validated against model capabilities | `sqlb` |
| `rest` | Mounts a model on a Huma API: handlers, and an OpenAPI operation built from the model's capabilities | `sqlb`, `filter`, huma |
| `codegen` | Models, the typed column facade, REST bodies, the manifest, the TypeScript client, the Go CLI | `schema` |
| `migrate` | Diffs two schemas into changes and renders Postgres DDL. Does not apply anything | `schema` |
| `introspect` | Reads `pg_catalog` back into a registry, reporting what the DSL cannot express | `schema`, stdlib |

The dependency direction is the load-bearing part: **`sqlb` has no dependency on
`schema`**. That is what keeps codegen optional — the engine cannot quietly grow
a dependency on the schema DSL, because it cannot see it. Capabilities reach the
runtime as struct tags or `Describe` calls, never as a schema import.

`migrate`, `introspect` and `codegen` sit on the other side of that line: all
three are design-time tools, and none is reachable from the request path.

`sqlb` has no third-party dependencies and neither does anything else on the
request path. `rest` is the single exception — it depends on
[Huma](https://huma.rocks), and nothing depends on `rest`, so importing the
engine still costs nothing. `mise run deps-check` proves this per package, and
ends by checking it can still *see* huma in `rest`: a guard that cannot fail is
worse than no guard ([ADR-0016](../architecture.md#guards-proven-both-ways)).

## The request path

A list request through `rest.Resource`:

1. **Parse.** `filter.Parse` reads the query string against the model. Unknown
   parameters, undeclared capabilities and uncoercible values are collected into
   a `filter.Errors` — all of them, not the first
   ([ADR-0011](../architecture.md#actionable-errors)). Values become typed Go values
   here; nothing downstream sees strings.
2. **Apply.** `filter.Apply` writes predicates, ordering, projection and limits
   onto a `*sqlb.Builder[T]`. It owns the projection and defaults to non-hidden
   columns, so a handler cannot leak a `Hidden` column by forgetting to project.
3. **Hook.** The terminal method clones the builder, then runs `BeforeQuery`.
   Cloning is what stops a hook's predicates accumulating when the same query
   value runs twice. A hook returning an error aborts before any SQL is issued,
   so a missing tenant fails closed.
4. **Compile.** The AST renders to SQL with `$N` placeholders. Values are always
   bind parameters; identifiers are validated against the model and quoted.
   `LIMIT`/`OFFSET` are literals so the planner can see them — safe because both
   are range-checked ints.
5. **Scan.** Result columns are matched to struct fields by name. Unmatched
   columns are read and discarded, so a query selecting extra expressions still
   scans into the model.

A write takes the same path with a transaction around it: `BEGIN`, the hooks and
the statement, `COMMIT`, then the `AfterCommit` callbacks — outside the
transaction, since there is nothing left to join.

## Where safety lives

Four independent mechanisms, each covering what the others cannot. This is worth
reading as a set, because no one of them is the answer:

**Bind parameters.** Values never reach SQL text. There is one `bind` method on
the compiler and no way to interpolate a value around it.

**Identifier validation.** Column names are checked against the reflected model
before compilation. `Raw` is the documented escape hatch and is the one place
this does not apply.

**Opt-in capabilities.** A column that does not declare `Filterable` cannot be
filtered, ever. See [Capabilities](capabilities.md).

**Query hooks.** Tenant scoping applies to every read of a model, including
reads issued by generated handlers, because both go through the same builder.

Two smaller rails: `Update` and `Delete` without a `WHERE` return `ErrUnscoped`
until `Everything()` is called explicitly, and LIKE metacharacters in user input
are escaped, so a search for `50%` searches for the literal string.

## Where it fails loudly

The rule is that **a wrong answer must never be quieter than no answer**. The
full table is in [Architecture](../architecture.md#failing-loudly); the four
worth knowing before you write anything:

| Situation | Behaviour |
|---|---|
| `Update`/`Delete` with no `WHERE` | `ErrUnscoped` until `Everything()` is called |
| A filter names an unknown or uncapable column | 400 listing what would have been accepted |
| A destructive migration | Rendered commented out, with the reason stated |
| A resource over a `Scoped` or soft-deleting model with no hook confining it | Refused at mount, naming every missing registration — serving it would answer 200 with another tenant's rows, which is the quietest wrong answer in the system |

## Next

- [A query is a value](queries-are-values.md) — start here
- [Architecture](../architecture.md) — the same material at full length,
  including the API stability tiers and the known gaps
- [Decision records](../architecture.md#decisions) — why each choice was made, and what would change it
