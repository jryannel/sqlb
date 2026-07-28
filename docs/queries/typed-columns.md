# Typed columns

The engine is reflective, so `sqlb.F("titel")` is a runtime error. Since codegen
is already emitting models, it also emits a typed column set:

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

The last two are why `Col[T]` does not embed `Field`: embedding would promote
every operator onto every column, so `Contains` on an integer would compile,
reach the database, and fail there. Pattern operators live on `TextCol[T
~string]` instead.

Nullable columns are typed as their base type — `published_at` is `*time.Time`
on the model but `Col[time.Time]` here — so the comparand is a `time.Time` and
NULL is expressed with `IsNull` rather than by comparing against a pointer.

An `Enum` column emits a named Go string type with one constant per value, so
`blog.PostStatusPublished` exists and a typo does not compile. That is also what
carries the enum's values into the generated
[TypeScript client](../typescript/README.md) and the
[CLI's `--help`](../cli/README.md).

## Typed update statements

The same generation covers writes. `UpdatePost()` returns a statement with a
setter per writable column, typed by that column:

```go
_, err := blog.UpdatePost().
    SetStatus(blog.PostStatusPublished).
    Where(blog.PostCols.ID.Eq(id)).
    Exec(ctx, db)
```

Worth using, since the untyped `Set(string, any)` checks the column name against
the model but not the value's type.

A `ReadOnly` column has no setter, which is the mechanism rather than a
convention. Where the operation the schema forbade is one you do want from Go —
incrementing a counter in the database rather than read-modify-write — the
extension goes in a hand-written file beside the generated one; see
[the seam](../concepts/generated-not-hidden.md#the-seam).

## When it is not available

The facade is generated, so it exists only on the schema-first path. With
[structs you described yourself](../start/structs-first.md), `sqlb.F("column")`
is the vocabulary, and column names are checked against the model at build time
rather than at compile time — a real difference, and the main thing codegen buys
on the query side.

## Testing that the refusals hold

A facade that stopped refusing would be invisible to an ordinary test, because
the cases that matter are the ones that must *not* compile. The repository
checks them by attempting to compile each and confirming it fails: a test that
passes vacuously when the facade breaks is worse than no test.

The generated TypeScript client is checked the same way, with `@ts-expect-error`
over the requests that must not type — see
[`example/tasks/web/src/refusals.ts`](../../example/tasks/web/src/refusals.ts).

## Next

- [Queries](README.md) — the builder these columns feed
- [Mutations and transactions](mutations.md) — the typed update statement in
  context
- [ADR-0009](../adr/0009-typed-column-facade.md) — why the facade is generated
  rather than reflective
