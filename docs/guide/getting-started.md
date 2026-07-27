# Getting started

By the end of this page you have a schema, generated models, and a query
running against Postgres.

sqlb needs Go 1.25 or newer and Postgres. Nothing else: the engine depends on
the standard library alone, so importing it costs you no transitive
dependencies. Only the `rest` package pulls in [Huma](https://huma.rocks), and
only if you use it.

```bash
go get github.com/jryannel/sqlb
```

## Two ways in

sqlb works in either direction, and you can start with one and move to the
other:

- **Schema-first** — declare tables as Go values, generate models, migrations
  and REST handlers from them. This is the path below, and the one the rest of
  the guide assumes.
- **Structs-first** — you already have model structs, from another generator or
  written by hand. Skip to [Using your own
  structs](#using-your-own-structs); nothing here requires the DSL.

## 1. Declare a schema

The schema is ordinary Go, in a package of its own. It lives apart from the
generated models because the two share names — `blogschema.Post` is the table
declaration, `blog.Post` is the row struct — and keeping them separate is what
lets both be called `Post`.

```go
// blogschema/schema.go
package blogschema

import "github.com/jryannel/sqlb/schema"

var Author = schema.Table("authors",
    schema.UUIDv7("id").PrimaryKey(),
    schema.Text("email").Unique().Searchable(),
    schema.Text("name").Searchable().Sortable(),
    schema.Text("password_hash").Hidden(),
    schema.Timestamps(),
)

var Post = schema.Table("posts",
    schema.UUIDv7("id").PrimaryKey(),
    schema.Ref("author", Author).OnDelete(schema.Restrict),
    schema.Text("title").Searchable().Sortable(),
    schema.Enum("status", "draft", "review", "published").
        Default(schema.Value("draft")).
        Filterable().
        Sortable(),
    schema.Timestamps(),
).
    Index("author_id").
    Expose(schema.REST{Ops: schema.CRUD | schema.OpList, MaxPageSize: 100})
```

Two things are doing the work here.

**Capabilities are opt-in.** `Filterable`, `Sortable`, `Searchable`, `Hidden`. A
column that does not declare a capability cannot be reached through it — ever.
`password_hash` is readable by your Go code and absent from every REST response,
filter vocabulary and rejection message. This is the difference between sqlb and
exposing your database.

**`Expose` is what publishes a table.** Without that call, `authors` above is
reachable from Go and has no HTTP surface at all.

See [Schema](schema.md) for the full column vocabulary.

## 2. Generate

Codegen is a normal Go program that imports your schema package for its side
effects — declaring a table registers it — and writes the artefacts.

```go
// blogschema/gen/main.go
package main

import (
    "github.com/jryannel/sqlb/codegen"
    "github.com/jryannel/sqlb/schema"

    _ "yourmodule/blogschema"
)

func main() {
    codegen.Must(codegen.Generate(codegen.Options{
        Registry: schema.DefaultRegistry(),
        Dir:      "blog",
        Package:  "blog",
    }))
}
```

```bash
go run ./blogschema/gen
```

That writes four files into `blog/`:

| File | Contents |
|---|---|
| `models_gen.go` | The row structs, with `db` and `sqlb` tags |
| `columns_gen.go` | The typed column facade, and typed update statements |
| `rest_gen.go` | Request bodies and a `Register` function, one call per exposed table |
| `sqlb.json` | The manifest: every column, its capabilities, the operator vocabulary |

Wire it to `go generate` with a directive in the schema file, and add
`codegen.Check` to CI — generated code is committed, so it drifts the first time
someone edits a schema and forgets to regenerate.

## 3. Query

```go
db, err := sql.Open("pgx", os.Getenv("DATABASE_URL"))
if err != nil {
    return err
}

posts, err := sqlb.Query[blog.Post]().
    Where(sqlb.F("status").Eq("published")).
    OrderBy(sqlb.F("created_at").Desc()).
    Limit(50).
    All(ctx, db)
```

A query is a **value**, not a statement that runs when you build it. That is the
point: a predicate can be added on a branch, which is exactly what static query
generators cannot express.

```go
q := sqlb.Query[blog.Post]().Where(sqlb.F("status").Eq("published"))
if search != "" {
    q = q.Where(sqlb.F("title").Contains(search))
}
posts, err := q.All(ctx, db)
```

`SQL()` renders the statement and its bind parameters without running anything,
which is the inspection point — log it, diff it in a test, paste it into
`EXPLAIN`:

```go
sql, args, err := q.SQL()
// SELECT "posts"."id", ... FROM "posts" WHERE ("status" = $1) AND ("title" ILIKE $2)
// [published %postgres%]
```

Values never reach the SQL text. Every user-supplied value becomes a bind
parameter, and only identifiers validated against the model are interpolated.

Prefer the generated typed columns to `sqlb.F` where you can — `blog.PostCols`
puts column names and comparand types back under the compiler:

```go
q := sqlb.Query[blog.Post]().
    Where(blog.PostCols.Status.Eq(blog.PostStatusPublished)).
    OrderBy(blog.PostCols.CreatedAt.Desc())
```

`PostCols.Titel` does not compile. Neither does `PostCols.ViewCount.Eq("x")`,
nor `PostCols.ViewCount.Contains("x")`. Hidden columns are not in the struct at
all.

## 4. Serve

`rest` takes a `huma.API` rather than building a router, so your router and its
middleware stay yours:

```go
router := chi.NewRouter()
router.Use(middleware.RequestID, middleware.Recoverer, yourAuth)

api := humachi.New(router, huma.DefaultConfig("Blog", "1.0.0"))
if err := blog.Register(api, db); err != nil {   // generated
    return err
}
http.ListenAndServe(":8080", router)
```

You now have list, read, create, patch and delete for every exposed table, with
filtering, sorting, search, pagination and an OpenAPI document built from each
column's capabilities. See [REST](rest.md).

## 5. Scope every read

Before this is safe to deploy multi-tenant, register the constraint once:

```go
sqlb.On[blog.Post]().BeforeQuery(func(ctx context.Context, q *sqlb.Builder[blog.Post]) error {
    org, ok := auth.OrgFrom(ctx)
    if !ok {
        return auth.ErrNoTenant
    }
    q.Where(sqlb.F("org_id").Eq(org))
    return nil
})
```

`BeforeQuery` receives the query itself, so this one registration applies to
every read of the model — including the reads the generated REST handlers
issue. Tenant scoping stops being something each call site has to remember, and
a hook returning an error aborts the operation. See [Queries and
hooks](queries-and-hooks.md).

## Using your own structs

The schema DSL and codegen are both optional. Point sqlb at structs you already
have and describe them at startup:

```go
type Invoice struct {
    ID         string    `json:"id"`
    CustomerID string    `json:"customerId"`
    AmountDue  int64     `json:"amountDue"`
    Memo       string    `json:"-"`
    CreatedAt  time.Time `json:"createdAt"`
}

func init() {
    sqlb.Describe[Invoice]().
        PrimaryKey("id").
        Defaulted("id").
        Timestamps("created_at").
        Filterable("customer_id", "amount_due").
        Sortable("amount_due", "created_at").
        Hidden("memo")
}
```

Column names are derived from field names (`CustomerID` → `customer_id`), so the
query builder works with no metadata at all. What `Describe` adds is what
reflection cannot infer: which column is the key, which have database defaults
(without this, an insert writes `""` over your generated uuid), and which
capabilities are open. Naming a column that does not exist panics at startup and
lists the ones that do.

Call it from `init`. It mutates the cached model and does not lock, so it is
only safe before any query has been built against that model.

## Where to next

- [Schema](schema.md) — the full column vocabulary, references, linting
- [Queries and hooks](queries-and-hooks.md) — mutations, transactions, `AfterCommit`
- [REST](rest.md) — the filter grammar and what the OpenAPI document says
- [Migrations](migrations.md) — turning a schema edit into files your runner applies
