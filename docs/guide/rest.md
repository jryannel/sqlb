# REST

The premise: the HTTP-to-SQL layer of a filter/sort/search page is mostly
boilerplate, and it is boilerplate you should not have to write. What makes that
safe rather than reckless is that the URL grammar compiles into the *same*
predicate AST your Go code produces — so one compiler, one bind-parameter
discipline, and one set of hooks cover both.

## Mounting

`rest` takes a `huma.API` rather than building a router, so chi, gin, echo or
`net/http` — and all of that router's middleware — stays your choice:

```go
router := chi.NewRouter()
router.Use(middleware.RequestID, middleware.Recoverer, yourAuth)

api := humachi.New(router, huma.DefaultConfig("Blog", "1.0.0"))
if err := blog.Register(api, db); err != nil {   // generated from the schema
    return err
}
http.ListenAndServe(":8080", router)
```

`blog.Register` is one `rest.Resource` call per exposed table. Written out, one
of them looks like this:

```go
rest.Must(rest.Resource[blog.Post, blog.PostCreate, blog.PostPatch](api, db, rest.Options{
    Path: "/posts",
    Ops:  rest.CRUD | rest.OpList,
}))
```

`T` is the row type, `C` the create body and `U` the update body. A resource
exposing neither create nor update passes `rest.None[T]` for both. Registration
is the startup path, so failures are returned rather than panicked — a mistake
should name the resource that caused it.

The handlers are **not** generated: `rest.Resource[T, C, U]` is one generic
function serving every resource. What is per-resource is the OpenAPI document,
built from each column's capabilities.

[`example/tasks/app/app.go`](../../example/tasks/app/app.go) is this assembled
for real: authentication middleware, six generated resources mounted in one
call, and seven hand-written endpoints on the same router and in the same
OpenAPI document. The thing to notice is what the generated half does *not*
contain — no mention of tenants, tokens or roles anywhere in it, because the
hooks cover those for every read the handlers issue.

### Request bodies

Codegen emits `PostCreate` and `PostPatch` because two problems need types
rather than reflection.

`PostCreate` omits read-only columns — the database or a `BeforeCreate` hook
owns those — and makes defaulted columns optional, so leaving one out means the
database supplies the value rather than a zero overwriting it. Its `Row()`
method builds the row to insert; returning an error there is a 422, which is
where cross-field validation belongs.

Both halves of "the database or a hook" are live: the handler clears every
read-only field before inserting, so a hand-written `Row()` cannot set one, and
a hook still can. That is what makes a tenant id expressible as a column no
request may name — see [`example/tasks`](../../example/tasks/app/hooks.go),
where four models are stamped that way.

`PostPatch` has every field as a pointer and reports which ones the request
actually carried. A typed struct cannot tell "absent" from "zero", which is the
whole difficulty of PATCH, so the body reports its change set explicitly. An
empty change set is a 400 rather than a no-op update, because it almost always
means the client sent the wrong shape. Immutable columns are absent entirely.

## The filter grammar

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
metacharacters in user input are escaped, so a search for `50%` searches for the
literal string.

Here is that end to end, from `filter/example_test.go`:

```go
values, _ := url.ParseQuery("status=eq.published&views=gte.100&sort=-views&per_page=10")
q, _ := filter.Parse(values, filter.Options{Model: sqlb.ModelOf[Article]()})
sql, args, _ := filter.Apply(sqlb.Query[Article](), q).SQL()
```

```sql
SELECT "id", "title", "body", "status", "views", … FROM "articles"
WHERE ("status" = $1) AND ("views" >= $2) ORDER BY "views" DESC LIMIT 10 OFFSET 0
-- args: published 100
```

That is the same builder your Go code uses, so a `BeforeQuery` hook applies to
it. Tenant scoping is a startup registration, not something each handler
remembers.

### Search

`?search=` fans out over every `Searchable` column as a disjunction, with the
input escaped:

```sql
WHERE ("title" ILIKE $1) OR ("body" ILIKE $2)
-- args: %50\%% %50\%%
```

## Rejections say what would have worked

A column that does not declare a capability cannot be reached through it, and
the rejection is data rather than prose ([ADR-0011](../adr/0011-actionable-errors.md)):

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

Every problem in a request is reported at once, not one per round trip — a
malformed request takes one round trip to fix rather than one per mistake. The
caller most likely to read this is a program assembling requests against a
schema it only partly knows, and "column is not sortable" is a dead end where
the same message plus the sortable columns is a fix.

In Go, reach the structured form with `filter.AsErrors`, which unwraps as it
goes. Prefer it to a type assertion, which panics the moment a middleware wraps
the error:

```go
if errs, ok := filter.AsErrors(err); ok {
    errs.WriteHTTP(w)
    return
}
```

A `Hidden` column appears nowhere: not as a parameter, not in the response
schema, and not in that `allowed` list. It cannot be recovered by probing.

## Projections cannot leak

`filter.Apply` owns the projection. Given `?select` it uses those columns;
otherwise it projects every non-hidden column — deliberately *not* falling back
to the builder's default of "all mapped columns", because that would put a
`Hidden` column into a response any time a handler forgot to project.

```go
q, _ := filter.Parse(url.Values{}, filter.Options{Model: sqlb.ModelOf[Article]()})
sql, _, _ := filter.Apply(sqlb.Query[Article](), q).SQL()
// SELECT "id", "title", "body", "status", "views", … — internal_note is absent
```

If you want a custom projection, apply `Where`, `Order` and the limits from the
`Query` fields yourself rather than calling `Apply`.

## Responses

A list response pages without counting. `has_more` comes from reading one row
beyond the page, so a request for `per_page=5` reaches the database as `LIMIT 6`
— the probe is added by the handler, on top of the `LIMIT` that `filter.Apply`
produced:

```json
{"items": [...], "page": 1, "per_page": 20, "has_more": true}
```

`total` costs a second query and so is opt-in, with `?count=exact`.

## Limits

`MaxPageSize` is a hard ceiling, not a hint — a client asking for more gets the
maximum. `MaxFilters` bounds how many predicates one request may carry, which
bounds the cost of a single query. Both default to the `filter` package's values
when left zero, and both are worth setting per resource in the schema's
`Expose`.

## What is not built

`?expand` validates relation names but does not perform the join yet. Rather
than accepting the parameter and answering without it, every surface refuses:
`rest.Resource` rejects a non-empty `Options.Expandable` at startup,
`filter.Apply` fails the builder rather than dropping a parsed `?expand`, and
the manifest reports neither the capability nor the relation names.
`schema.Ref(…).Expandable()` still parses, so schemas can declare the intent;
nothing acts on it.

A startup rejection is the only place this can be caught — at request time the
parameter parses cleanly, so the failure would be a correct-looking 200 with the
relation missing.

## Next

- [Queries and hooks](queries-and-hooks.md) — the hooks these handlers run
- [Migrations](migrations.md) — changing the schema behind the API
