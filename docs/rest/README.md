# Mounting resources

The premise: the HTTP-to-SQL layer of a filter/sort/search page is mostly
boilerplate, and it is boilerplate you should not have to write. What makes that
safe rather than reckless is that the URL grammar compiles into the *same*
predicate AST your Go code produces — so one compiler, one bind-parameter
discipline, and one set of hooks cover both.

## Mounting

The batteries-included path is one call. `rest.NewServer` builds a huma API on
`net/http` — no third-party router — and has huma serve the OpenAPI document and
its docs page for you:

```go
srv := rest.NewServer(rest.Config{Title: "Blog", Version: "1.0.0"})
if err := blog.Register(srv.API, db); err != nil {   // generated from the schema
    return err
}
http.ListenAndServe(":8080", srv.Handler)   // wrap srv.Handler with your middleware
```

That is the default because importing sqlb should cost nothing: the engine and
the REST adapter reach a consumer's module graph as the standard library plus
huma, and no router beyond `net/http`.

### Bringing your own router

`rest` mounts on a `huma.API`, not a router it builds, so chi, gin, echo — and
all of that router's middleware — stay a first-class choice. Build the API with
the matching huma adapter and hand it to the same generated `Register`:

```go
router := chi.NewRouter()
router.Use(middleware.RequestID, middleware.Recoverer, yourAuth)

api := humachi.New(router, huma.DefaultConfig("Blog", "1.0.0"))
if err := blog.Register(api, db); err != nil {
    return err
}
http.ListenAndServe(":8080", router)
```

`rest.NewServer` is a convenience over this seam, not a replacement: whatever you
pass `blog.Register` — the server's `srv.API` or one you built yourself — is a
plain `huma.API`. The two examples show both paths:
[`example/blog`](../../example/blog) mounts on the `NewServer` default, and
[`example/tasks`](../../example/tasks) brings a chi router for its middleware.

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
call, and six hand-written endpoints on the same router and in the same
OpenAPI document. The thing to notice is what the generated half does *not*
contain — no mention of tenants, tokens or roles anywhere in it, because the
hooks cover those for every read the handlers issue.

## What each operation gives you

| Operation | Endpoint | Notes |
|---|---|---|
| `OpList` | `GET /posts` | Filtering, sorting, search, pagination, `?expand` |
| `OpRead` | `GET /posts/{id}` | `?expand` is its only query parameter |
| `OpCreate` | `POST /posts` | Body is `C`; returns the stored row |
| `OpUpdate` | `PATCH /posts/{id}` | Body is `U`; reports its own change set |
| `OpDelete` | `DELETE /posts/{id}` | A real `DELETE` |

An operation the schema does not expose has no endpoint — not a 405. That is
also true of the generated [TypeScript client](../typescript/README.md) and
[CLI](../cli/README.md): the function and the subcommand do not exist.

`OpDelete` issuing a real `DELETE` is why a soft-deleting table usually leaves
it out and serves the removal as an update instead; the pair is written out in
[Your first app](../start/first-app.md).

**A collection has one path, and a parent relationship is a filter.** The tasks
of a list are `GET /tasks?list_id=eq.<id>`, not `GET /lists/{id}/tasks` — so
sorting, projection, paging and `?expand` all work on it unchanged, and it is the
same request a capped `?expand` tells a caller to follow for the rest of the
children. The one real cost is that a parent which does not exist yields an empty
page rather than a 404
([ADR-0038](https://github.com/jryannel/sqlb/blob/main/docs/adr/0038-collections-are-flat.md)).

### Documenting the auth scheme

`Options.Security` puts an OpenAPI security requirement on every operation of a
resource:

```go
rest.Options{Path: "/posts", Ops: rest.CRUD | rest.OpList,
    Security: []map[string][]string{{"bearerAuth": {}}}}
```

It **documents**; it does not enforce — authentication is middleware on your
router and runs whether or not this is set. Leaving it empty produces operations
that are protected and do not say so, which is what every reader of the document
then has to guess about. Declare the scheme itself once on the API:

```go
api.OpenAPI().Components.SecuritySchemes = map[string]*huma.SecurityScheme{
    "bearerAuth": {Type: "http", Scheme: "bearer", BearerFormat: "JWT"},
}
```

The generated clients do not read this, and that is not an oversight: they are
built from the schema rather than from the document, and they take the credential
from the transport your project supplies.

## Request bodies

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

## Writes run in a transaction

`rest.Resource` wraps every generated create, update and delete in one, which is
what gives a hook a commit to be after and what lets a hook read its own writes
through `sqlb.TxFrom(ctx)`. Reads are left alone, since one `SELECT` is atomic
already.

`Options.DisableTransactions` opts out, and does not disable `AfterCommit` — it
makes every registration fail at request time. Loud rather than silent, which is
the point, but it makes the option a decision about the resource's hooks and not
only about its latency. See [Hooks](../queries/hooks.md#aftercommit-for-side-effects).

## A resource can refuse to mount

A model whose schema declares `Scoped`, or that carries a `SoftDelete` column,
does not mount until a hook confines it. `Register` returns an error naming
every missing registration and the declaration that asked for it.

Serving it instead would answer 200 with another tenant's rows, which is the
quietest wrong answer in the system. See
[Capabilities](../schema/capabilities.md#scoped-so-the-missing-hook-is-caught).

## Next

- [Filtering and search](filtering.md) — the grammar and what it compiles to
- [Pagination](pagination.md) — offset, cursors, and `total`
- [Expanding relations](expand.md) — `?expand`, both directions
- [Actions](actions.md) — a domain verb with a generated envelope
- [Rejections](errors.md) — what a refusal says and why
- [API compatibility](compatibility.md) — what a schema edit does to the contract
