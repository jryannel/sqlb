# sqlb

A schema-first data layer for Go: declare your tables once, get typed
composable queries, a validated REST filter grammar, and domain hooks — without
hand-writing the HTTP-to-SQL layer for every dynamic view.

## Why

Static query generators (sqlc and friends) cannot express *"this WHERE clause
exists only when the user typed something in the search box."* The usual
workaround is string concatenation, which is why the HTTP layer of a
filter/sort/search page is mostly boilerplate.

PostgREST solves that by making the database the API, but there is then nowhere
to put Go domain logic, and the whole schema sits one policy mistake away from
being public.

sqlb takes the middle path. A query is a **value**, so predicates can be added
conditionally:

```go
q := sqlb.Query[Post]().Where(sqlb.F("status").Eq("published"))
if search != "" {
    q = q.Where(sqlb.F("title").Contains(search))
}
posts, err := q.OrderBy(sqlb.F("created_at").Desc()).Limit(50).All(ctx, db)
```

and the REST filter grammar compiles into that **same** predicate AST. One
compiler, one bind-parameter discipline, one set of hooks — two producers.

## Design

**Schema is Go values.** One file is the source of truth for migrations,
models, REST handlers and OpenAPI.

```go
var Post = schema.Table("posts",
    schema.UUIDv7("id").PrimaryKey(),
    schema.Ref("author", Author).OnDelete(schema.Restrict).Expandable(),
    schema.Text("title").Searchable().Sortable(),
    schema.Enum("status", "draft", "review", "published").Filterable().Sortable(),
    schema.Text("password_hash").Hidden(),
    schema.Timestamps(),
    schema.SoftDelete(),
).
    Index("org_id", "status").
    Expose(schema.REST{Ops: schema.CRUD | schema.OpList, MaxPageSize: 100})
```

**Capabilities are opt-in per column.** `Filterable`, `Sortable`, `Searchable`,
`Expandable`, `Hidden`. A column that does not declare a capability cannot be
reached through it — ever, and the failure is a 400 rather than a leak. This is
the difference between this and exposing the database.

**Rejections are actionable.** A bad parameter reports what was wrong *and what
would have been accepted*:

```
filter: sort=body: column is not sortable (allowed: title, status, view_count, published_at, created_at)
```

Every problem in a request is reported at once, not one per round trip.

**Hooks are where domain logic lives.** `BeforeQuery` is the load-bearing one:
it receives the query itself, so one registration constrains every read of a
model — including the ones generated REST handlers issue.

```go
sqlb.On[Post]().BeforeQuery(func(ctx context.Context, q *sqlb.Builder[Post]) error {
    org, ok := auth.OrgFrom(ctx)
    if !ok {
        return auth.ErrNoTenant
    }
    q.Where(sqlb.F("org_id").Eq(org), sqlb.F("deleted_at").IsNull())
    return nil
})
```

Multi-tenancy and soft deletes stop being something each call site has to
remember.

## Without code generation

The schema DSL and codegen are optional. Bring your own structs — `json` tags
and `db` tags are independent — and describe them at startup:

```go
type Invoice struct {
    ID         string    `json:"id"`
    CustomerID string    `json:"customerId"`
    AmountDue  int64     `json:"amountDue"`
    Paid       bool      `json:"paid"`
    Memo       string    `json:"-"`
    CreatedAt  time.Time `json:"createdAt"`
}

func init() {
    sqlb.Describe[Invoice]().
        PrimaryKey("id").
        Defaulted("id").          // insert leaves it to the database
        Timestamps("created_at"). // defaulted + read-only + sortable
        Filterable("customer_id", "paid", "amount_due").
        Sortable("amount_due", "created_at").
        Hidden("memo")
}
```

Column names are derived from field names (`CustomerID` → `customer_id`), so
the query builder works with no metadata at all. What `Describe` adds is what
the builder cannot infer:

- **`PrimaryKey`** — without it there is no `One()`-by-id and no REST row addressing.
- **`Defaulted`** — without it an insert writes `""` into your uuid column and a
  zero timestamp over the database default.
- **`Filterable` / `Sortable` / `Searchable`** — capabilities are opt-in, so an
  undescribed model exposes *nothing* to the REST layer.
- **`Hidden`** — omits the column from responses, filters, sorts and projections.
- **`Column(field, name)`** — for when the derived name is not the real one.

Naming a column that does not exist panics at startup, listing the ones that do.
Descriptions merge onto struct tags, so a partly tagged model can be completed
here. Call it from `init`; it mutates the cached model and does not lock, since
a mutex there would tax every query to pay for something that happens once.

This is also the incremental-adoption path: point sqlb at structs another
generator already produced, without editing them.

## The typed facade

The query engine is reflective, so `sqlb.F("titel")` is a runtime error. Since
codegen is already emitting models, it also emits a typed column set per table —
which puts column names and comparand types back under the compiler at a
fraction of the cost of generating a whole builder API:

```go
q := sqlb.Query[Post]().
    Where(blog.PostCols.Status.Eq(blog.PostStatusPublished)).
    Where(blog.PostCols.Title.Contains(search)).
    OrderBy(blog.PostCols.ViewCount.Desc())
```

| | |
|---|---|
| `PostCols.Titel.Eq(…)` | does not compile — misspelled column |
| `PostCols.ViewCount.Eq("x")` | does not compile — wrong comparand type |
| `PostCols.ViewCount.Contains("x")` | does not compile — text operator on an integer |
| `AuthorCols.PasswordHash` | does not exist — hidden columns are omitted |

Three decisions make this work:

- **`Col[T]` does not embed `Field`.** Embedding would promote every operator
  onto every column, so `Contains` would be callable on an integer: it compiles,
  reaches the database, and fails there. The pattern operators live on
  `TextCol[T ~string]` instead.
- **Nullable columns are typed as their base type.** `published_at` is
  `*time.Time` on the model but `Col[time.Time]` here, so the comparand is a
  `time.Time` and NULL is expressed with `IsNull` rather than by comparing
  against a pointer.
- **The select builder is not wrapped.** Twenty-odd chainable methods would each
  need their return type re-wrapped, and the column set already covers where
  mistakes happen. Update statements *are* wrapped, because `Set(string, any)`
  checks neither the column name nor the value type.

Predicates stay untyped (`sqlb.Pred`, not `Pred[T]`), so a column from the wrong
table still compiles. Binding predicates to a model was considered and rejected:
join conditions span two tables, so they cannot be `Pred[T]` for any single `T`,
and the escape hatch that would require reopens the hole anyway.

## Layout

| Package | Contents |
|---|---|
| `schema` | The declarative DSL, plus validation that reports every authoring mistake at once |
| `.` (`sqlb`) | Expression AST, Postgres compiler, generic query builder, model reflection, mutations, hooks |
| `filter` | URL query grammar → the same predicate AST, validated against capabilities |
| `describe.go` | Runtime column metadata, for using sqlb without the schema DSL or codegen |
| `example/blog` | A worked schema, the models codegen will emit from it, and an end-to-end list handler |

## Filter grammar

```
?status=eq.published          operator form
?email=alice@example.com      shorthand (a dotted value is not read as an operator)
?age=gte.18&age=lt.65         repeated parameters conjoin
?tag=in.a,b,c                 value lists, quotable: in."a,b",c
?deleted_at=isnull            null tests
?views=between.10,20          ranges
?or=(status.eq.draft,age.lt.18)   explicit disjunction, nestable
?sort=-created_at,title       "-" for descending; created_at.desc also works
?select=id,title              projection (the primary key is always kept)
?search=ada                   fan-out over searchable columns
?page=2&per_page=50           pagination, capped by the schema
```

Values are always bind parameters. Identifiers are validated against the model
before they reach the compiler, and quoted when they get there. LIKE
metacharacters in user input are escaped, so a search for `50%` searches for
the literal string.

## Status

Working and tested:

- Schema DSL, capabilities, references, indexes, checks, REST exposure
- Schema validation (16 distinct authoring mistakes, all reported together)
- Expression AST and Postgres compiler — predicates, groups, joins, aggregates,
  ordering, pagination, locking, raw-SQL escape hatch with placeholder renumbering
- Generic query builder over struct tags; `Collect[R]` for aggregate shapes
- Typed column facade (`Col[T]`, `TextCol[T]`) and a typed update wrapper
- Code generation for models, the typed facade and the manifest (`codegen`).
  The blog example is generated from its schema, so every behaviour test in it
  is a test of the generator's output
- Insert (with default-omission and upsert), Update, Delete, all with a guard
  against unscoped mutations
- Hooks: BeforeQuery / Before+AfterCreate / Before+AfterUpdate / Before+AfterDelete
- Filter grammar with capability enforcement, type coercion and cost limits
- Schema manifest (JSON), schema linting, and `Explain` with plan diagnostics
- `sqlb.Describe[T]()` for runtime-only use: the schema DSL and codegen are
  optional, and sqlb can be layered over structs it did not generate

Not built yet, in the order they matter:

1. **Migrations** — `migrate.Diff` computes the changes between two schemas and
   renders them as Postgres DDL, and `migrate.Write` emits the files. What is
   missing is where the *current* schema comes from: `sqlb import` reading
   `pg_catalog`, or a shadow database replaying the existing history. Until one
   of those exists, both sides of a diff have to be hand-written registries.
2. **REST handlers and OpenAPI** — the generator emits models and the typed
   facade today, not handlers.
3. **`?expand`** — the grammar validates relation names; the joins are not
   performed yet.
4. **Change feed** — transactional outbox written in the same transaction as
   the mutation, tailed via `LISTEN/NOTIFY` and fanned out to `AfterCommit`
   hooks and SSE/WebSocket subscribers.
5. **Agent affordances** — a `sqlb.json` manifest and generated `AGENTS.md`, and
   `sqlb explain` to print the SQL a query compiles to without running it.

Postgres only. `LISTEN/NOTIFY`, jsonb aggregation for expansions and `RETURNING`
are all load-bearing; multi-dialect support would cost the best features.

### Go 1.27 generic methods

Three parts of the current API are shaped around methods not being able to
declare type parameters. Go 1.27 lifts that for concrete types, and each one
becomes an additive change — the existing functions stay as wrappers, so no
call site breaks:

| Today | With 1.27 |
|---|---|
| `sqlb.Collect[R](ctx, db, b)` | `b.Collect[R](ctx, db)` |
| `filter.Apply(b, q)` | `q.ApplyTo(b)` |
| `sqlb.Query[T]()` + `db` on every terminal call | `db.Query[T]().…All(ctx)` |

The third is the substantial one: a concrete `*sqlb.DB` handle can carry the
executor, the dialect and a scoped hook registry, which removes both the `db`
threading and the process-global `sqlb.On[T]()`.

Interface methods still cannot declare type parameters, so `Executor` stays a
plain interface and anything generic hangs off the concrete `*DB`.

Adopting this means requiring Go 1.27 of every consumer, so it is worth doing
once the toolchain is widely available rather than at release.

## Inspection and vetting

The loop is `go test`, not a CLI: an agent already runs it, and every check
below is reachable from a test.

| Question | Answer |
|---|---|
| What can I query on this resource? | `schema.BuildManifest()` — JSON: every column, its capabilities, the operator vocabulary, and worked example requests. Hidden columns are omitted entirely |
| Is my schema well-formed? | `schema.Validate()` — every authoring error at once |
| Will my schema behave badly in production? | `schema.Lint()` — unindexed filters, search without a trigram index, list endpoints with no stable sort order |
| What SQL does this query produce? | `q.SQL()` — text and args, without executing |
| Is this query still valid against the real database? | `sqlb.Explain(ctx, db, q)` — fails with the database's own complaint, without executing |
| Does it still use the index? | `plan.UsesIndex(…)`, `plan.UsesSeqScan(…)`, `plan.Diagnostics()` |
| Did the plan regress? | Assert on `plan.Diagnostics()` in a test |

`Explain` is the load-bearing one, because it answers both halves of "did I
break something" — correctness and performance — without running the statement:

```go
plan, err := sqlb.Explain(ctx, db, q)
if err != nil {
    t.Fatal(err)   // the query is not valid against this database
}
if d := plan.Diagnostics(); len(d) > 0 {
    t.Errorf("plan regressed:\n%s", sqlb.Diagnostics(d))
}
```

```
[seq-scan] Seq Scan on users: sequential scan over ~250000 rows filtering on (org_id = 'acme')
    fix: add an index covering the filtered columns on "users"
[external-sort] Sort: sort spilled to disk (external merge, Disk)
    fix: add an index matching the ORDER BY, or reduce the rows sorted before ordering
```

`ExplainAnalyze` gives real timings but *executes* the statement — on a
mutation that means it writes. Use it inside a transaction you roll back.

## Dry run and tracing

Nothing writes or executes without being asked, and every artefact can be
inspected first:

| | Inspect | Commit |
|---|---|---|
| Query | `q.SQL()` — text and args, nothing executed | `q.All(ctx, db)` |
| Query against the live schema | `sqlb.Explain(ctx, db, q)` — plans it, does not run it | `q.All(ctx, db)` |
| Schema change | `migrate.Diff(current, target)` — the changes, as values | — |
| Migration | `migrate.Render(m, opts)` — files in memory | `migrate.Write(dir, m, opts)` |
| Generated code | `codegen.Check(opts)` — what is stale | `codegen.Generate(opts)` |

`codegen.Check` is the one worth wiring into CI. Generated code is committed, so
it drifts: someone edits a schema, forgets to regenerate, and the committed
models describe a table that no longer exists. `mise run generate-check` fails
when they disagree.

**Tracing needs no API from sqlb.** `Executor` is two methods, so wrapping it
observes every statement — and reaches OpenTelemetry, slog or a test double
without sqlb depending on any of them:

```go
type tracer struct{ inner sqlb.Executor }

func (t tracer) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
    start := time.Now()
    rows, err := t.inner.QueryContext(ctx, q, args...)
    slog.InfoContext(ctx, "sqlb", "sql", q, "args", len(args), "dur", time.Since(start), "err", err)
    return rows, err
}
// ExecContext likewise, then pass the wrapper wherever you pass the *sql.DB.
```

## Documentation

| Document | Contents |
|---|---|
| [docs/vision.md](docs/vision.md) | What this is for, non-goals, and where it goes next |
| [docs/architecture.md](docs/architecture.md) | How the pieces fit, the request path, where safety lives, API tiers |
| [docs/adr/](docs/adr/) | Decision records — what was decided, why, and what would change our mind |

The decision records are living documents: they describe current understanding,
not settled law, and they are meant to be edited when that understanding moves.
Each one carries both a **What would change our mind** section and a **Cost of
change** section, so revising a decision is a trade someone can weigh rather
than a rule they have to argue against.

## Development

Tool versions are pinned in `mise.toml`, so local runs and CI use the same Go
and the same linter.

```
mise run test          # the inner loop; no Docker or Postgres needed
mise run ci            # the full gate, same as .github/workflows/ci.yml
mise run bisect-check  # verify every commit builds on its own
mise tasks             # everything else
```

The engine's tests run against an in-memory `database/sql` driver, so hooks,
scanning and the mutation paths are covered end to end without a live database.

The engine's tests run against an in-memory `database/sql` driver, so hooks,
scanning and the mutation paths are covered end to end without a live database.
