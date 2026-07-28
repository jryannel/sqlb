# Mounting resources

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
- [Rejections](errors.md) — what a refusal says and why
