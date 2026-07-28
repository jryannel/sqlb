# Filtering and search

```
?status=eq.published          operator form
?email=alice@example.com      shorthand (a dotted value is not read as an operator)
?age=gte.18&age=lt.65         repeated parameters conjoin
?tag=in.a,b,c                 value lists, quotable: in."a,b",c
?labels=has.urgent            an array column contains this element
?labels=hasany.a,b            overlaps these; hasall.a,b contains all of them
?metadata=hasdoc.{"lang":"de"}  a jsonb document contains this one
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

## Array columns take containment, and nothing else

A column declared `.Array()` accepts a vocabulary of its own:

| Request | Means |
|---|---|
| `?labels=has.urgent` | the array contains that one element |
| `?labels=hasany.a,b` | the array overlaps the list |
| `?labels=hasall.a,b` | the array contains every member of the list |
| `?labels=eq.a,b` | the whole array, compared element by element |
| `?labels=isnull` | the column is NULL — which is *not* the same as empty |

The ordering operators, `in` and `between` are refused: Postgres will order
arrays, but that is not an ordering an API should offer, and a list of arrays
has no spelling in this grammar.

`contains` is refused too, and that one is deliberate rather than incidental.
It is the case-insensitive substring operator for text, and giving it a second
meaning on array columns would put an ambiguity into the one vocabulary whose
whole purpose is that there is none. The refusal names `has` instead, since that
is what a request spelling it that way meant:

```
GET /tasks?labels=contains.urgent
400 — operator "contains" does not apply to the array column labels
      (allowed: eq, has, hasall, hasany, isnull, ne, neq, notnull)
```

An array column cannot be `Sortable` or `Searchable`, and a `Filterable` one has
to carry a GIN index — `schema.Validate` reports all three. The index is not a
suggestion: an array filter without one still returns the right rows, by
scanning the table for them, so nothing would ever report it
([ADR-0033](https://github.com/jryannel/sqlb/blob/main/docs/adr/0033-array-columns.md)).

## Document columns take containment, and nothing else

A `jsonb` column is the one place where the useful filter cannot be declared in
advance, because the keys a caller attaches are the point of having it. So it
gets one operator:

| Request | Means |
|---|---|
| `?metadata=hasdoc.{"lang":"de"}` | the document contains that key and value, whatever else it holds |
| `?metadata=isnull` | the column is NULL — not the same as `{}` |

`hasdoc` compiles to Postgres's `@>`, which is subset containment rather than
equality, and it is the operator a GIN index over the column serves.

It is spelled `hasdoc` rather than `contains` for the reason ADR-0033 gives
about arrays: `contains` is already the case-insensitive substring operator for
text, and a third meaning dispatched on column type is exactly the ambiguity the
generated clients exist to remove. `hasdoc` joins the `has` family, which is how
containment is already spelled here.

There is no bare-value shorthand — `?metadata={"lang":"de"}` is refused, because
the `eq` it would infer is not an operator the column takes — and the ordering
and pattern operators are refused too. Comparing documents by Postgres's
ordering rule means something almost nobody intends, and a pattern would match
against a serialisation whose key order and whitespace are Postgres's to choose;
both would answer, which is worse than refusing:

```
GET /docs?metadata=startswith.x
400 — operator "startswith" does not apply to the JSON document column metadata
      (allowed: hasdoc, isnull, notnull)
```

The same filter can arrive through the JSON tree, where the document is a value
rather than text — `{"field":"metadata","op":"hasdoc","value":{"lang":"de"}}` —
and binds the identical parameter.

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

**This is a substring match, not full-text search**, and the distinction is
worth knowing before relying on it. A user who types `ada` matches `Nowlada`; a
user who types `running` does *not* match `run`, because nothing stems, drops
stop words or ranks. That is the right default for a filter box over identifiers
and the wrong one for a search box over prose, and sqlb has the first
([ADR-0037](https://github.com/jryannel/sqlb/blob/main/docs/adr/0037-search-is-ilike-until-it-cannot-be.md),
which also says why `tsvector` is not in 1.0 and what a schema carrying one does
today).

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
