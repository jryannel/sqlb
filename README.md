# sqlb

A schema-first data layer for Go: declare your tables once, get typed
composable queries, a validated REST filter grammar, and domain hooks — without
hand-writing the HTTP-to-SQL layer for every dynamic view.

New here? [**docs/guide/getting-started.md**](docs/guide/getting-started.md)
goes from `go get` to a running server. This page explains what sqlb is and why
it is shaped this way.

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
    // Delete is left out because this table soft-deletes: the generated delete
    // is a real DELETE. See "Hooks are where domain logic lives" below.
    Expose(schema.REST{
        Ops:         schema.OpCreate | schema.OpRead | schema.OpUpdate | schema.OpList,
        MaxPageSize: 100,
    })
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
remember — but not something *someone* has to write. `schema.SoftDelete()` adds
the `deleted_at` column and nothing else; the registration above is what makes a
row with a non-null value disappear from every read, generated handlers
included. Nothing in the runtime reads the column's name.

The same goes the other way: a resource exposing `OpDelete` hard-deletes, and
`BeforeDelete` receives a `*Delete` it cannot turn into an `UPDATE`. A table
whose deletes are meant to be soft should leave `OpDelete` out of its `Expose`
and serve the endpoint itself.

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
| `rest` | Mounts a model on a Huma API, with an OpenAPI operation built from its capabilities |
| `codegen` | Emits the models, the typed facade, the REST bodies and the manifest — and renders a registry back to schema source |
| `migrate` | Diffs two registries, renders the changes as Postgres DDL, and writes the files a runner applies |
| `introspect` | Reads a live schema out of `pg_catalog` into a registry, reporting what the DSL cannot express |
| `shadow` | Replays a migration history into an empty database, so the current schema comes from the history rather than from production |
| `describe.go` | Runtime column metadata, for using sqlb without the schema DSL or codegen |
| `example/blog` | A worked schema, everything codegen emits from it, and an assembled server |
| `example/tasks` | The larger example: a multi-tenant task manager with JWT auth, a runnable server, a generated migration history and a suite against a real Postgres. A module of its own, so its dependencies cost the engine nothing |

## The REST server

`rest` takes a `huma.API`, not a router, so chi, gin, echo or `net/http` — and
all of that router's middleware — stays your choice:

```go
router := chi.NewRouter()
router.Use(middleware.RequestID, middleware.Recoverer, yourAuth)

api := humachi.New(router, huma.DefaultConfig("Blog", "1.0.0"))
blog.RegisterHooks()                             // hand-written: the domain rules
if err := blog.Register(api, db); err != nil {   // generated from the schema
    return err
}
blog.RegisterPostSoftDelete(api, db)             // hand-written: DELETE as an UPDATE
http.ListenAndServe(":8080", router)
```

`blog.Register` is one `rest.Resource` call per exposed table. The handlers are
not generated — `rest.Resource[T, C, U]` is one generic function — but the
OpenAPI document *is* per resource, built from each column's capabilities:

```yaml
# GET /posts, from the schema above
parameters:
  - name: status              # declared Filterable
    schema: {type: array, items: {type: string}}
    description: "Filter on `status`. Written `operator.value`, or a bare value
                  for equality. Operators: eq, ne, gt, gte, lt, lte, in, nin,
                  between, like, ilike, contains, startswith, endswith."
  - name: sort                # enumerated, in both directions
    schema: {type: array, items: {enum: [title, -title, status, -status, ...]}}
```

`password_hash` is `Hidden`, so it appears nowhere: not as a parameter, not in
the response schema, and not in the allow-list of a rejection.

A rejection says what would have worked ([ADR-0011](docs/adr/0011-actionable-errors.md)),
as data rather than prose:

```json
{
  "title": "Bad Request", "status": 400,
  "detail": "one or more query parameters were rejected",
  "errors": [{
    "message": "column is not sortable",
    "location": "query.sort", "value": "body",
    "allowed": ["title", "status", "view_count", "published_at", "created_at"]
  }]
}
```

A list response pages without counting. `has_more` comes from reading one row
beyond the page; `total` costs a second query and so is opt-in, with
`?count=exact`:

```json
{"items": [...], "page": 1, "per_page": 20, "has_more": true}
```

Because reads go through `sqlb.Query[T]`, a `BeforeQuery` hook registered on the
model applies to them — which is why registration is generic over the model
rather than reflective. Tenant scoping is a startup registration, not something
each handler remembers.

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
- Code generation for models, the typed facade, the REST request bodies and the
  manifest (`codegen`). The blog example is generated from its schema, so every
  behaviour test in it is a test of the generator's output
- REST surface over [Huma](https://huma.rocks) (`rest`): list, read, create,
  patch and delete, driven by the schema's `Expose` declaration, with an OpenAPI
  document built from each column's capabilities
- Insert (with default-omission and upsert), Update, Delete, all with a guard
  against unscoped mutations
- Hooks: BeforeQuery / Before+AfterCreate / Before+AfterUpdate / Before+AfterDelete,
  registered process-wide with `On[T]()` or scoped to a handle with `OnIn[T]`.
  Those all run *inside* the transaction; `AfterCommit` registers work that must
  not happen if it aborts — publishing an event, enqueuing a job, invalidating a
  cache. It is in-process and at-most-once, so it is not a change feed
  ([ADR-0012](docs/adr/0012-change-feed-outbox.md)). `rest.Resource` wraps each
  generated write in a transaction so hooks can reach it; set
  `Options.DisableTransactions` to opt a resource out
  ([ADR-0021](docs/adr/0021-hooks-receive-an-event.md))
- `sqlb.DB` — a handle carrying an executor and a hook registry, with `WithTx`
  for multi-statement units of work. It satisfies `Executor`, so it goes
  wherever a `*sql.DB` went; `TxFrom(ctx)` is how a hook reads rows the same
  transaction has written but not yet committed
  ([ADR-0020](docs/adr/0020-transaction-scoped-handle.md))
- Filter grammar with capability enforcement, type coercion and cost limits
- Schema manifest (JSON), schema linting, and `Explain` with plan diagnostics
- `sqlb.Describe[T]()` for runtime-only use: the schema DSL and codegen are
  optional, and sqlb can be layered over structs it did not generate — including
  stock sqlc output with no `db` tags, which
  [example/withsqlc](example/withsqlc) tests against real generated code rather
  than asserting. [docs/with-sqlc.md](docs/with-sqlc.md) is the pairing story
- Migrations (`migrate`): `Diff` computes the changes between two schemas and
  renders them as Postgres DDL; `Render` and `Write` emit the files for goose,
  golang-migrate or plain SQL. Renames are declared with `.RenamedFrom("old")`
  on a column or a table, because a rename cannot be told from a drop and an add
  by looking at the two states. Destructive changes render commented out unless
  explicitly allowed, and so does anything depending on one. sqlb does not apply
  migrations and does not track which have run — your runner does that
- Lock-aware migration sequencing: `Migration.Blocking` lists the changes that
  hold a lock proportional to the size of the table, and `migrate.Unblock`
  rewrites the ones whose remedy is mechanical — a scanning `ADD CONSTRAINT`
  becomes `NOT VALID` plus a `VALIDATE` in a later file, a `UNIQUE` becomes a
  concurrent index build plus an `ADD CONSTRAINT … USING INDEX` that adopts it.
  A type change is left flagged, because rewriting a table has no in-place form.
  `migrate.Split` separates the changes that cannot share a file, since
  transaction control in both runners is per file rather than per statement
- Reading a schema back: `introspect.Registry` reads `pg_catalog` into a
  registry, reporting every construct the DSL cannot express rather than
  dropping it. `shadow.Build` replays a checked-in migration history into an
  empty database, which is the better source for the current side of a diff —
  it says what the *history* builds rather than what production happens to look
  like, so an edited or skipped migration surfaces instead of being baked into
  the next one. Drift detection needs no extra API: it is `migrate.Diff` between
  the two registries. `codegen.RenderSchema` turns a registry into the
  `schema.go` you edit from then on, which closes the adoption loop — CI
  compiles the rendered source and checks it declares the database it came from

  Adopting an existing database is therefore two calls:

  ```go
  reg, report, err := introspect.Registry(ctx, db, introspect.Options{})
  if !report.Empty() {
      // Constructs the DSL cannot express. Read them: the schema does not
      // describe the database completely until this is empty.
      log.Print(report)
  }
  src, err := codegen.RenderSchema(reg, codegen.SchemaOptions{Package: "blogschema"})
  os.WriteFile("blogschema/schema.go", src, 0o644)
  ```

  Everything imports with no capabilities and nothing exposed over REST, because
  neither can be read from DDL — widening it is a deliberate edit. Table names
  are not singularised (`orgs` becomes `var Orgs`), because guessing wrongly on
  *status* or *address* costs more than renaming a variable the compiler checks
  for you. [docs/guide/migrations.md](docs/guide/migrations.md) walks the whole
  path

Not built yet, in the order they matter:

1. **TypeScript client** — the OpenAPI document is generated and precise; the
   client derived from it is not written yet.
2. **`?expand`** — the grammar validates relation names; the joins are not
   performed yet. Until they are, every surface that would promise expansion
   refuses instead of accepting the parameter and answering without it:
   `rest.Resource` rejects a non-empty `Options.Expandable` at startup,
   `filter.Apply` fails the builder rather than dropping a parsed `?expand`,
   and the manifest reports neither the capability nor the relation names.
   `schema.Ref(…).Expandable()` still parses and validates, so schemas can
   declare the intent; nothing acts on it yet.
3. **Change feed** — transactional outbox written in the same transaction as
   the mutation, tailed via `LISTEN/NOTIFY` and fanned out to SSE/WebSocket
   subscribers. `sqlb.AfterCommit` already exists and covers the in-process,
   at-most-once half of this; what is missing is the durability — a callback
   that never ran because the process died leaves no trace, which is precisely
   what the outbox is for.
4. **Agent affordances** — the `sqlb.json` manifest ships and `Explain` answers
   what a query compiles to. What is missing is a generated `AGENTS.md` and a
   command-line entry point; today both are reachable only from Go, which suits
   an agent that already runs `go test` and suits nothing else.

Postgres only. `LISTEN/NOTIFY`, jsonb aggregation for expansions and `RETURNING`
are all load-bearing; multi-dialect support would cost the best features.

### Which Postgres

There is no hard minimum. The query engine and almost all generated DDL are
ordinary SQL that has been valid for a decade. Three places are version
sensitive, and each says so where you meet it:

- **`schema.GenUUIDv7`** emits `uuid_generate_v7()`, which needs the
  [`pg_uuidv7`](https://github.com/fboulnois/pg_uuidv7) extension — so by
  default a UUIDv7 primary key produces DDL that will *not* apply to a stock
  install. Postgres 18 has `uuidv7()` built in: pass
  `migrate.MinPostgres(18)` to `migrate.Diff` and it emits that instead, needing
  no extension. On an older server without the extension, use
  `schema.GenUUIDv4`, which is built in from Postgres 13.
- **`migrate.Unblock`'s `SET NOT NULL` sequence** is correct on any version but
  only *fast* from Postgres 12, which is where a validated `CHECK` lets the
  `SET NOT NULL` skip its scan.
- **Reading a schema back** with `introspect` handles the `NOT NULL` constraint
  rows Postgres 18 introduced, and ignores them on older versions.

CI runs the round trip against Postgres 18; the generated DDL has also been
applied and reversed by hand on 17.

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

The third looked like the substantial one, and turned out to be two separate
things. The *object graph* — a handle carrying the executor and a scoped hook
registry — needed no new language feature and is built:
[`sqlb.DB`](docs/adr/0020-transaction-scoped-handle.md). What 1.27 adds is only
the call syntax, so `db` stops being threaded through every terminal.

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
| Is this query still valid against the real database? | `sqlb.Explain(ctx, db, q)` — fails with the database's own complaint, without executing. This is the practice, not an option; see below |
| Does it still use the index? | `plan.UsesIndex(…)`, `plan.UsesSeqScan(…)`, `plan.Diagnostics()` |
| Did the plan regress? | Assert on `plan.Diagnostics()` in a test |

### Explain is the practice, not an optional nicety

Predicates are deliberately untyped, so `sqlb.F("titel")` compiles and fails at
runtime ([ADR-0009](docs/adr/0009-typed-column-facade.md)). That is a real gap
next to a tool that checks columns at build time, and the answer is not that it
rarely happens.

**The answer is a test that plans every query shape a resource can produce.**
Treat it the way you would treat compiling: something the gate does, not
something you remember to do.

```go
func TestEveryQueryShapePlans(t *testing.T) {
    for name, q := range shapes {   // the shapes filter.Parse can produce
        if _, err := sqlb.Explain(ctx, db, q); err != nil {
            t.Errorf("%s does not plan against the live schema: %v", name, err)
        }
    }
}
```

[pgtest/explain_test.go](pgtest/explain_test.go) does exactly this for the blog
example's three resources — every list filter, sort, projection and page, plus
read, insert, update and delete — and ends by pointing the same check at a
misspelled column to prove it fires. It runs in `mise run ci`.

This catches strictly more than a compile-time column check: it validates
against the *live schema*, so it also fails on the migration that was written
and never applied. What it costs is a database at test time, and a failure that
arrives at test time rather than compile time. See
[docs/with-sqlc.md](docs/with-sqlc.md) for the honest comparison.

`Explain` answers the performance half too, without running the statement:

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
| [docs/guide/](docs/guide/) | How to use it, in the order you do it: install, schema, queries, REST, migrations |
| [docs/vision.md](docs/vision.md) | What this is for, non-goals, and where it goes next |
| [docs/architecture.md](docs/architecture.md) | How the pieces fit, the request path, where safety lives, API tiers |
| [docs/compatibility.md](docs/compatibility.md) | What the current tag freezes, and which surfaces are expected to move |
| [docs/with-sqlc.md](docs/with-sqlc.md) | Using both: who owns the schema, which queries go where, and sharing a transaction |
| [docs/review-adoption-readiness.md](docs/review-adoption-readiness.md) | An outside read on what blocks adoption, and what would change the verdict |
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
mise run test-pg       # round trip and connection path against real containers
mise run ci            # the full gate, same as .github/workflows/ci.yml
mise run bisect-check  # verify every commit builds on its own
mise tasks             # everything else
```

The engine's tests run against an in-memory `database/sql` driver, so hooks,
scanning and the mutation paths are covered end to end without a live database.
That is deliberate, and it is what keeps `mise run test` fast enough to sit in
an edit loop.

What it cannot answer is whether the generated SQL is *valid* rather than merely
*expected*, since a golden test compares DDL against a string somebody wrote.
`mise run test-pg` answers that: it applies the generated schema to a real
Postgres, reads it back, and asserts the round trip and its fixpoint. It also
runs the query path through a real PgBouncer, because that is the deployed
topology ([ADR-0019](docs/adr/0019-pgbouncer-in-the-path.md)). It needs Docker,
takes about ten seconds, and is part of `mise run ci`.

It lives in `pgtest/`, which is a **separate Go module**. Everything there needs
a Postgres driver, and the engine depends on the standard library alone —
`deps-check` enforces that so a consumer importing sqlb inherits nothing. The
split is not tidiness: `go list -deps` does not report test-only imports, so a
driver added to the root module's tests would leave that gate reporting success
while covering nothing. See `pgtest/doc.go`.
