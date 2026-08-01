# Refactoring a sqlc endpoint to sqlb

[with-sqlc.md](with-sqlc.md) argues that sqlb and sqlc are good at opposite
things and says which queries belong on which side. This is the other half of
that: what it actually takes to move one endpoint across, in four steps, with a
stopping point after each.

The worked version is [example/withsqlc](../example/withsqlc), one file per
stage, and the claims below that can be tested are tested rather than asserted —
[refactor_test.go](../example/withsqlc/refactor_test.go) for what each stage
sends and refuses, [pgtest/refactor_test.go](../pgtest/refactor_test.go) for the
part that needs a real planner.

## This is not a migration away from sqlc

Only one population of queries is worth moving: the ones whose `WHERE` clause
depends on what a request happened to contain. sqlc cannot express those, and
the [documented workarounds](https://dizzy.zone/2024/07/03/SQLC-dynamic-queries/)
are `sqlc.narg` with `coalesce`, chains of `(@x::text IS NULL OR col = @x)`, or
"use a query builder".

Everything else should stay where it is. The window function and the grouped
aggregate in [query.sql](../example/withsqlc/query.sql) are still there after
all four stages, because nothing about them benefits from being built at
runtime and sqlc types them at build time, which sqlb cannot.

So the thing being refactored here is one list endpoint, and the end state still
runs sqlc.

## The four stages

| | What owns the query | What owns the capabilities | Dependencies |
|---|---|---|---|
| **1** | `query.sql`, static | the handler | pgx, sqlc |
| **2** | the builder, at runtime | `Describe` + a map in the handler | ＋ sqlb |
| **3** | the builder, at runtime | the schema declaration | the same |
| **4** | the generated resource | the schema declaration | ＋ huma |

Each is a place to stop. Stage 2 is worth doing on its own; stage 4 is not the
point of stages 1 through 3.

### Stage 1 — the shape being replaced

```sql
SELECT * FROM posts
WHERE org_id = @org_id
  AND deleted_at IS NULL
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text)
  AND (sqlc.narg('min_views')::bigint IS NULL OR view_count >= sqlc.narg('min_views')::bigint)
  AND (sqlc.narg('search')::text IS NULL OR title ILIKE '%' || sqlc.narg('search')::text || '%')
ORDER BY published_at DESC
LIMIT @page_limit;
```

sqlc does the typed part of this well: `ListPostsParams` is checked against the
real schema, and a column that does not exist fails `sqlc generate` rather than
a request. What it cannot do is anything about the shape.

Three costs, and only the first is cosmetic:

- **Every predicate is sent on every request**, each behind a NULL check.
  [The test](../example/withsqlc/refactor_test.go) asserts all three arms go to
  Postgres on a request that filtered nothing.
- **The sort is in the query text.** `ORDER BY view_count ASC` is a second entry
  in `query.sql`, a second generated function and a branch to choose between
  them, so *n* sortable columns in two directions is 2*n* queries. Stage 1
  returns `ErrSortUnavailable` for anything but the ordering it was generated
  for, which is that limitation made visible rather than worked around.
- **The handler is the security boundary and nothing marks it as one.** That
  `status` is filterable and `password_hash` is not is a fact about which lines
  someone wrote in [stage1.go](../example/withsqlc/stage1.go), so reviewing the
  API surface means reading handlers ([ADR-0006](adr/0006-capabilities-are-opt-in.md)).

One detail worth noticing, because it is the kind of thing that survives a
review: the `ILIKE` above interpolates the search term without escaping `%` or
`_`, so a user typing `50%` gets a wildcard. sqlb's `Contains` escapes them.

### Stage 2 — the builder, over the structs sqlc generated

Nothing in `sqlcgen` changes, and nothing in it learns that sqlb exists.
`sqlb.Describe[sqlcgen.Post]()` attaches the metadata at startup, and stock sqlc
output maps cleanly because sqlb falls back to snake_case field names when there
is no `db` tag — the claim [adopt_test.go](../example/withsqlc/adopt_test.go)
exists to check.

```go
q := sqlb.Query[sqlcgen.Post]().
	Where(sqlb.F("org_id").Eq(orgID), sqlb.F("deleted_at").IsNull())

if v := query.Get("status"); v != "" {
	q = q.Where(sqlb.F("status").Eq(v))
}
```

What this buys: only what was asked reaches Postgres, and the sort is a value
rather than a query. What it does not buy is anything about *who decides* — the
request-to-predicate translation is hand-written, and so is the allow-list the
sort is checked against:

```go
var stage2Sortable = map[string]bool{"published_at": true, "view_count": true}
```

That map says the same thing the `Sortable("published_at", "view_count")` call
above it says, and nothing makes the two agree. **This is the stage's real
cost**, and it is why stage 2 is the only step here that makes the code longer.

One thing that does not change: both sides still share a transaction. A
`pgx.Tx` satisfies `sqlb.Executor` and sqlc's `DBTX` at once
([ADR-0040](adr/0040-the-driver-is-a-dependency.md)), so the dashboard query and
this list can run in one unit of work throughout.

### Stage 3 — the schema owns the capabilities

The type changes from `sqlcgen.Post` to a model generated from the schema
declaration, and that is the whole move. The declaration already states which
columns are filterable, sortable and searchable, so the endpoint becomes:

```go
parsed, err := filter.Parse(query, filter.Options{
	Model:           sqlb.ModelOf[blog.Post](),
	DefaultPageSize: defaultPageLimit,
	MaxPageSize:     maxPageLimit,
})
if err != nil {
	return nil, err
}

q := sqlb.Query[blog.Post]().Where(
	blog.PostCols.OrgID.Eq(orgID),
	blog.PostCols.DeletedAt.IsNull(),
)
return filter.Apply(q, parsed).All(ctx, db)
```

- **The allow-list is gone.** `?sort=body` is refused because `body` never
  declared `Sortable`, and the rejection names the columns that would have
  worked ([ADR-0011](adr/0011-actionable-errors.md)) — which stage 2's map
  could not do without a second list.
- **The per-parameter unpacking is gone**, operators included.
  `?view_count=gte.100` needed a branch of its own in both earlier stages.
- **A misspelled column stops compiling.** `blog.PostCols.OrgID` is a
  `Col[string]`; `sqlb.F("org_id")` was a string that had to be right
  ([ADR-0009](adr/0009-typed-column-facade.md)).

Where the declaration comes from, if you do not have one: `introspect.Registry`
reads `pg_catalog` and `codegen.RenderSchema` turns it into the `schema.go` you
edit from then on — [Adopting a database](migrations/adopting.md). Your existing
`schema.sql` becomes the starting point rather than something to throw away, and
after this it is the schema declaration that renders the DDL sqlc reads.

### Stage 4 — no handler at all

```go
srv := rest.NewServer(rest.Config{Title: "Blog", Version: "1.0.0"})
err := blog.Register(srv.API, db)
```

`Register` is generated from the declaration and mounts every resource the
schema exposes, with the filter grammar, sorting, search, pagination and the
OpenAPI document derived from the capabilities the columns declared
([ADR-0007](adr/0007-generated-rest-handlers.md)). The list endpoint stage 3
hand-wrote is one of them.

The part worth more than the deleted handler is the hook:

```go
sqlb.On[blog.Post]().BeforeQuery(func(ctx context.Context, q *sqlb.Builder[blog.Post]) error {
	org, ok := ctx.Value(orgContextKey{}).(string)
	if !ok || org == "" {
		return ErrNoOrg
	}
	q.Where(blog.PostCols.OrgID.Eq(org), blog.PostCols.DeletedAt.IsNull())
	return nil
})
```

The tenant scope has been an argument threaded through every call site since
stage 1. One registration constrains every read of `Post` instead — the
generated list, the generated read, the queries the expand machinery issues, and
anything written by hand later ([ADR-0008](adr/0008-hooks-as-domain-seam.md)).
Stage 3's handler could have forgotten the org predicate, compiled, and tested
green against a single-tenant fixture.

It is thorough enough to be worth knowing about before you rely on it: once that
hook is registered, a query on a context nothing scoped **fails** rather than
returning every tenant's rows. The pgtest suite had to put the org on the
context for stage 3 as well, which is the hook keeping its promise rather than an
awkwardness of the test.

**The honest cost of this stage**: `rest` is an adapter onto huma, so this is the
step where a project accepts a web framework it did not choose. A project that
stops at stage 3 keeps sqlb's engine on pgx and nothing else.

## How much code

Lines of Go per stage, comments, blanks, package clause and imports excluded:

| Stage | Lines | What they are |
|---|---|---|
| 1 | 36 ＋ 8 of SQL | parameter unpacking, bounds, the sort refusal |
| 2 | 59 | ＋ the `Describe` block and the sortable map |
| 3 | 15 | parse, two predicates, apply |
| 4 | 26 | the hook and the mount — **none of it a list handler** |

Stage 2 is bigger than stage 1, and pretending otherwise would misrepresent the
trade. What it buys is not brevity but the sort, and the end of the three
always-sent predicates. The drop happens at stage 3, when the capabilities stop
being restated. Stage 4's 26 lines are not this endpoint's — they mount every
resource in the schema.

## What changes on the wire

Two things, and both are release notes rather than internal details.

**The query string changes at stage 3.** Stages 1 and 2 read whatever spelling
the handler invented (`?status=published&min_views=100&limit=20`); from stage 3
it is the documented filter grammar
(`?status=eq.published&view_count=gte.100&per_page=20`). The rows are the same —
[pgtest/refactor_test.go](../pgtest/refactor_test.go) runs six scenarios through
all four stages against one Postgres and requires them to agree row for row —
but a client has to change.

**`?search` widens at stage 3.** Stages 1 and 2 search the title, because that is
what the SQL and the branch said. Stage 3 searches every column the schema
declared `Searchable`, which for posts is title *and* body. That is the
declaration doing what it says, and it is the one behaviour the four stages do
not share; there is a test named after it.

If neither is acceptable yet, [`restcompat`](rest/README.md) and stopping at
stage 2 are both real answers.

## Where to stop

- **Stage 2** if the win you wanted was the filter and sort. It costs one
  dependency and no schema work, and it is reversible in an afternoon.
- **Stage 3** if you have — or are willing to generate — a schema declaration.
  This is where the duplicate capability lists disappear, and it is the largest
  single improvement in the sequence.
- **Stage 4** if you want the REST surface generated too, and accept huma.

Going backwards is cheap at every step, which is the point of doing it in four.
