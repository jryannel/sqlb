# A query is a value

Nothing runs when you build a query. `sqlb.Query[Post]()` returns a builder; the
statement is compiled and sent only when a terminal method — `All`, `One`,
`Count`, `Exists` — is called. That single property is where most of the design
comes from.

```go
q := sqlb.Query[Post]().Where(sqlb.F("status").Eq("published"))
if search != "" {
    q = q.Where(sqlb.F("title").Contains(search))
}
posts, err := q.OrderBy(sqlb.F("created_at").Desc()).Limit(50).All(ctx, db)
```

## The problem it solves

Modern application screens are dynamic. One data view filters on several
columns, sorts by whichever one the user clicked, runs a free-text search, and
paginates. The set of queries such a screen can issue is combinatorial.

A static query generator compiles a fixed SQL string into a typed function,
which is exactly the right tool for a known query and exactly the wrong one for
a view whose shape depends on what the user does. There is no way to write *"and
this `WHERE` clause exists only when the search box has something in it"* as a
fixed string. Teams hit this within weeks, and the standard workaround —
building the dynamic parts by concatenation — gives back every guarantee the
typed generator was adopted for.

Making the query a value is the answer. A predicate is data, so adding one is an
ordinary Go branch.

## Three things follow from it

**It can be inspected without being run.** `SQL()` renders the statement and its
bind parameters and executes nothing, which is the seam for logging, for a test
that diffs the SQL, and for pasting into `EXPLAIN`. `sqlb.Explain` goes further
and plans it against the live database — which also catches the migration that
was written and never applied, something a compile-time column check
structurally cannot.

**A hook can amend it.** `BeforeQuery` receives the builder itself, so a tenant
predicate is written once and applies to every read of the model — including the
reads generated REST handlers issue. If a query were a string by the time a hook
saw it, the only options would be to re-parse it or to trust each call site.

**Absent means absent.** `Where` skips the zero `Pred`, so an optional filter can
be expressed inline without a surrounding `if`:

```go
q.Where(
    sqlb.F("status").Eq("published"),
    sqlb.If(minViews > 0, sqlb.F("view_count").Gte(minViews)),
)
```

`And`, `Or` and `Not` all skip zero predicates too, and `Not` of a zero
predicate stays zero — so an absent filter stays absent rather than quietly
becoming always-false.

## The cost, stated

Methods mutate the builder and return it, rather than returning a fresh copy.
That is what lets a query be assembled across branches without reassignment
gymnastics, and what lets a hook amend one — but it means a partially built
query shared between goroutines or request scopes needs `Clone()`.

The builder *is* cloned before hooks run, so running the same builder twice does
not accumulate their predicates. That is the case that would otherwise be a
silent bug.

The other cost is that column names are strings at the `sqlb.F` level, so
`sqlb.F("titel")` is a runtime error rather than a compile error. That is what
[the typed column facade](../queries/typed-columns.md) exists to fix, and why it
is worth preferring where codegen is in play.

## Read next

- [Queries](../queries/README.md) — the builder in full: predicates, terminal
  methods, aggregates
- [One grammar, two producers](one-grammar.md) — the second half of the design
- [ADR-0002](../adr/0002-queries-are-values.md) — the decision record
