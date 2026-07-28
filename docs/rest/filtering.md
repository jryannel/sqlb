# Filtering and search

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
?page=2&per_page=50           offset pagination, capped by the schema
?cursor=eyJrIjpb…            keyset pagination: resume after a position
```

Values are always bind parameters. Identifiers are validated against the model
before they reach the compiler, and quoted when they get there. LIKE
metacharacters in user input are escaped, so a search for `50%` searches for the
literal string.

Every operator, and which column types accept it, is in the
[filter grammar reference](https://jryannel.github.io/sqlb/reference/filter-grammar/).

## It is the same builder

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
remembers — and a table that declared `Scoped` does not mount at all until the
registration exists, so it is not something the *schema* has to remember
either. See [Hooks](../queries/hooks.md).

Those two functions are also the extension point. A hand-written handler can
call `Parse` and `Apply` itself and then add whatever it likes; a generated
resource is exactly that with nothing added.

## Search

`?search=` fans out over every `Searchable` column as a disjunction, with the
input escaped:

```sql
WHERE ("title" ILIKE $1) OR ("body" ILIKE $2)
-- args: %50\%% %50\%%
```

Which columns join that fan-out is a privacy decision as much as an API one — an
address column that is filterable but not searchable answers "find my own
record" and refuses to answer "who here uses example.com". See
[Capabilities](../schema/capabilities.md#choosing-between-them).

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

The primary key is always kept, even when `?select` drops it, because an item
that cannot be addressed is not much use to a caller. That is also what lets the
[TypeScript client](../typescript/README.md) narrow a response type on `select`
and still promise the key is there.

If you want a custom projection, apply `Where`, `Order` and the limits from the
`Query` fields yourself rather than calling `Apply`.

## Limits

`MaxPageSize` is a hard ceiling, not a hint — a client asking for more gets the
maximum. `MaxFilters` bounds how many predicates one request may carry, which
bounds the cost of a single query. Both default to the `filter` package's values
when left zero, and both are worth setting per resource in the schema's
`Expose`.

Nesting in `?or=(…)` is bounded too, so a deeply nested group is a rejection
rather than a stack the parser walks.

## Next

- [Pagination](pagination.md) — where `?page` and `?cursor` differ
- [Rejections](errors.md) — what happens when a column has not opted in
- [One grammar, two producers](../concepts/one-grammar.md) — why this is the
  same code as the Go path
