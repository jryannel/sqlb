# Queries and hooks

## A query is a value

Nothing runs when you build a query. That is the whole design: predicates can be
added on a branch, which is what static query generators structurally cannot
express.

```go
q := sqlb.Query[Post]().Where(sqlb.F("status").Eq("published"))
if search != "" {
    q = q.Where(sqlb.F("title").Contains(search))
}
posts, err := q.OrderBy(sqlb.F("created_at").Desc()).Limit(50).All(ctx, db)
```

Methods mutate the builder and return it, so a query can be assembled across
branches without reassignment gymnastics — and so a hook can amend a query it is
handed. Use `Clone()` before sharing a partially built query between goroutines
or request scopes.

`Where` skips the zero `Pred`, so `If` removes the surrounding statement
entirely when a filter is optional:

```go
q.Where(
    sqlb.F("status").Eq("published"),
    sqlb.If(minViews > 0, sqlb.F("view_count").Gte(minViews)),
)
```

### Terminal methods

| Method | Returns |
|---|---|
| `All(ctx, db)` | Every matching row |
| `One(ctx, db)` | The single match; `ErrNotFound` if none, an error if more than one |
| `First(ctx, db)` | The first match; pair it with `OrderBy` to be deterministic |
| `Count(ctx, db)` | Row count, ignoring pagination; group count for a grouped query |
| `Exists(ctx, db)` | Whether anything matched |
| `SQL()` | The statement and its bind parameters, executing nothing |

`One` fetches two rows so it can tell you the result was ambiguous rather than
silently returning the first — a caller asking for one row is asserting only one
exists.

The builder is cloned before hooks run, so running the same builder twice does
not accumulate their predicates.

### Predicates

`sqlb.F("column")` is the untyped reference; the generated `PostCols.Title` is
the typed one, and worth preferring — see [the typed
facade](#the-typed-facade).

Comparison: `Eq`, `Neq`, `Gt`, `Gte`, `Lt`, `Lte`, `Between`, `NotBetween`,
`OneOf`, `NotOneOf`, `IsNull`, `NotNull`, `EqField`.

Text: `Contains`, `StartsWith`, `EndsWith`, `Like`, `ILike`.

`Contains`, `StartsWith` and `EndsWith` escape LIKE metacharacters, so a user
typing `50%` searches for that literal string. `Like` and `ILike` do not — use
them only for patterns your own code wrote.

`Eq(nil)` becomes `IS NULL` rather than `= NULL`, which is never true and is
never what the caller meant.

Combine with `And`, `Or` and `Not`. All three skip zero predicates, and `Not` of
a zero predicate stays zero, so an absent filter stays absent rather than
becoming always-false.

### Aggregates and other shapes

`Collect[R]` scans into a type other than the model, which is how grouped
queries are read:

```go
type Revenue struct {
    Status string  `db:"status"`
    Total  float64 `db:"revenue"`
}

rows, err := sqlb.Collect[Revenue](ctx, db,
    sqlb.Query[Order]().
        GroupBy(sqlb.F("status")).
        Select(sqlb.F("status"), sqlb.Sum(sqlb.F("total")).As("revenue")))
```

Query hooks still run, so tenant scoping applies to aggregates too. Unlike
`All`, `Collect` requires every field of `R` to be filled by some result column:
`R` was written to match this projection, so an unfilled field is a mistyped
alias rather than a deliberate partial select — and a mistyped alias on a `Sum`
would otherwise report zero revenue silently.

`Raw` and `RawPred` are the escape hatch for expressions the builder cannot
model. Their contents are not validated; their `?` placeholders are renumbered
by the compiler.

### Paging a whole result set

`Limit` and `Offset` are there, and for walking a whole set they are the wrong
tool: `OFFSET k` makes Postgres produce `k + n` rows and throw `k` away, and a
row inserted mid-walk shifts every later page so a row is read twice or not at
all. `After` names the position instead.

```go
var cursor sqlb.Cursor
for {
    q := sqlb.Query[Post]().
        Where(sqlb.F("status").Eq("published")).
        OrderBy(sqlb.F("created_at").Desc()).
        After(cursor).
        Limit(500)

    batch, err := q.All(ctx, db)
    if err != nil || len(batch) == 0 {
        return err
    }
    process(batch)
    if len(batch) < 500 {
        return nil
    }
    if cursor, err = q.CursorFor(batch[len(batch)-1]); err != nil {
        return err
    }
}
```

An empty cursor means "start at the beginning", so the first pass through the
loop needs no special case.

`After` and `CursorFor` both call `Stable()` first, which appends the primary
key unless the ordering already contains it. That is what makes a page boundary
nameable at all — without it, two rows with the same `created_at` cannot be told
apart, and the boundary between them is ambiguous. Call `Stable()` yourself if
you want the total order without a cursor.

A cursor is only valid for the ordering it was issued under; using one against a
different `OrderBy` fails with an error wrapping `sqlb.ErrBadCursor` that names
both orderings. `Count` ignores the boundary, so it answers how large the result
set is rather than how much of it is left.

For the best plans, index the ordering: `(created_at DESC, id DESC)` lets
Postgres answer the boundary as a single index seek.
[ADR-0027](../adr/0027-keyset-pagination.md) has the details, including how
NULLs in a sortable column are handled.

## The typed facade

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

## Mutations

```go
post := Post{Title: "Hello", Status: "draft"}
stored, err := sqlb.InsertRows(&post).One(ctx, db)
```

Rows are passed as pointers so hooks and returned database values can be written
back into them. Columns carrying a database default are omitted when their Go
value is the zero value, so a generated uuid comes from the database rather than
being overwritten with `""`. Every statement returns the stored rows, so
generated values land back in your structs without a follow-up read.

`OnConflictDoNothing(target...)` and `OnConflictUpdate(target, update...)` cover
upserts. A row skipped by do-nothing is simply absent from the result, so `One`
returns `ErrNotFound`.

```go
_, err := sqlb.UpdateRows[Post]().
    Set("status", "archived").
    Where(sqlb.F("published_at").Lt(cutoff)).
    Exec(ctx, db)

n, err := sqlb.DeleteRows[Post]().Where(sqlb.F("id").Eq(id)).Exec(ctx, db)
```

An update or delete with no `Where` is **rejected rather than run**:

```
sqlb: statement would affect every row; add a Where clause or call Everything to confirm
```

`Everything()` confirms it when that is genuinely what you meant. `Set` checks
the column name against the model, and the generated `UpdatePost()` wrapper
checks the value types too — worth using, since `Set(string, any)` checks
neither.

## Transactions

`sqlb.New(pool)` returns a `*sqlb.DB`: a handle carrying an executor and the
hook registry its queries resolve against. It satisfies `Executor` itself, so it
goes wherever a `*sql.DB` went.

```go
db := sqlb.New(pool)

err := db.WithTx(ctx, func(ctx context.Context, tx *sqlb.DB) error {
    order, err := sqlb.InsertRows(&o).One(ctx, tx)
    if err != nil {
        return err
    }
    _, err = sqlb.UpdateRows[Stock]().
        Set("reserved", true).
        Where(sqlb.F("sku").Eq(order.SKU)).
        Exec(ctx, tx)
    return err
})
```

Commits if `fn` returns nil, rolls back otherwise. A panic rolls back and is
re-raised, so a transaction is never left open by one.

`fn` receives a context carrying the transaction — pass **that** ctx onward, not
the enclosing one, or `TxFrom` will not find it inside your hooks.

Nesting **joins** rather than nests: `WithTx` on a handle already in a
transaction runs `fn` on that same transaction and leaves the commit to the
outermost call, so a function that opens a transaction stays callable from
inside one. Savepoints are the alternative and are a larger promise; nothing
needs them yet.

`WithTxOptions` takes an isolation level. Asking for stricter isolation than an
enclosing transaction already has is an error rather than a silent downgrade.

### Sharing the transaction with another library

`Executor` is deliberately two methods, which is what keeps every wrapper and
pool adapter valid — but it means a library wanting more than that cannot be
handed a `*sqlb.DB`. sqlc's generated `DBTX` wants four. `DB.Tx()` reaches the
underlying `*sql.Tx` so both sides land on one unit of work:

```go
err := db.WithTx(ctx, func(ctx context.Context, tx *sqlb.DB) error {
    post, err := sqlb.InsertRows(&p).One(ctx, tx)
    if err != nil {
        return err
    }
    sqlTx, ok := tx.Tx()
    if !ok {
        return errors.New("expected a transaction")
    }
    return sqlcgen.New(sqlTx).RecordPublication(ctx, post.ID)
})
```

It reports false when the executor is a pool, or a wrapper that does not expose
the transaction it holds.

**Do not commit or roll back the returned `*sql.Tx` yourself.** `WithTx` owns
that boundary, and taking it over leaves the after-commit callbacks unrun — the
one failure mode that looks like success. [docs/with-sqlc.md](../with-sqlc.md)
covers the pairing in full: who owns the schema, and which queries go where.

## Hooks

Hooks are where domain logic lives. Register once at startup, typically from
`init` or `main`; they run in registration order, and one returning an error
aborts the operation with the error reaching the caller unwrapped.

### BeforeQuery is the load-bearing one

It receives the query itself, so **one registration constrains every read of the
model** — including the reads the generated REST handlers issue.

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
remember. This is also why REST registration is generic over the model rather
than reflective: hooks are keyed by type, and a reflective dispatcher could not
run them.

Returning an error is how "no tenant in this context" becomes impossible to
forget rather than merely documented — no statement runs at all.

### Say it in the schema, so the missing hook is the one that is caught

The hook above cannot be forgotten at a call site. It can be forgotten
entirely, and an unscoped model serves every tenant's rows with a `200` next to
them. So the table declares what it expects:

```go
schema.Ref("org", Org).Filterable().ReadOnly().Scoped()
```

`Scoped` writes no predicate — it is inert in exactly the way `SoftDelete`'s
column is. What it does is oblige the resource: `rest.Resource` refuses to
mount a model whose declarations no hook satisfies, and names every missing
registration at once. The obligation follows the operations, because a
`BeforeQuery` hook says nothing about what a request can overwrite by id — an
exposed update needs `BeforeUpdate`, a delete needs `BeforeDelete`, and a
create needs `BeforeCreate` to supply the tenant column that `ReadOnly` kept
out of the request body.

The check proves a hook exists, not that it is right. That is worth knowing
before relying on it, and it catches the case that actually happens: the table
somebody added last week. [ADR-0030](../adr/0030-declared-scope-is-required.md)
has the reasoning, including why the predicate is not generated for you.

[`example/tasks/app/hooks.go`](../../example/tasks/app/hooks.go) is this taken
as far as it goes: one file, a little over two hundred lines, confining six
models across twenty-five endpoints. Two details there are worth stealing. The
scoping is one generic function used four times rather than four near-copies,
which is only possible because every table in that schema names the column
`workspace_id` — a convention kept deliberately so the boundary can be written
once. And reads and writes are scoped by *separate* registrations, because a
`BeforeQuery` predicate constrains what a request can see and says nothing about
what it can overwrite by id.

### The rest

| Hook | Receives | Use for |
|---|---|---|
| `BeforeCreate` | `*T` | Normalising an email, deriving a slug, stamping an owner |
| `AfterCreate` | `*T`, with defaults populated | Validation |
| `BeforeUpdate` | `*Update[T]` | Forcing a column, narrowing affected rows |
| `AfterUpdate` | `[]T` | Validation |
| `BeforeDelete` | `*Delete[T]` | Narrowing, or refusing |
| `AfterDelete` | `int64` | Validation |

All of these run **inside** the caller's transaction. That is right for
validation — an error rolls the write back — and wrong for anything the outside
world can observe.

The write hooks are narrower than `BeforeQuery`: they receive the row or the
statement rather than a handle. They can still reach the database, but only
where a transaction is — `rest` wraps every generated write in one, so
`sqlb.TxFrom(ctx)` finds it and a hook can query, as
[Reading your own writes](#reading-your-own-writes) below shows. On a read, or
under `Options.DisableTransactions`, there is nothing to find.

The gap that remains is narrower and deliberate: `BeforeUpdate` cannot read the
assignments it was handed, so a rule that depends on what a column is *becoming*
belongs in a `BEFORE` trigger.
[ADR-0021](../adr/0021-hooks-receive-an-event.md) records why the event types
that would have closed it are not being built.

What has landed from that record is the transaction: `rest.Resource` wraps every
generated create, update and delete in one, so `AfterCommit` is reachable from a
generated write. Set `Options.DisableTransactions` to opt out, and read the next
section before you do.

### AfterCommit, for side effects

Publishing an event, enqueuing a job, invalidating a cache: none of these may
happen if the write does not. `AfterCreate` running inside the transaction means
the transaction can still abort after the hook has already told the world it
succeeded.

```go
sqlb.On[Order]().AfterCreate(func(ctx context.Context, o *Order) error {
    id := o.ID
    return sqlb.AfterCommit(ctx, func(ctx context.Context) error {
        return events.Publish(ctx, OrderPlaced{ID: id})
    })
})
```

Callbacks run in registration order once `Commit` returns nil, and not at all if
it rolls back. The context they receive carries no transaction — there is
nothing left to join, and handing back a committed one would be a trap.

A failing callback does not stop the others; the failures are joined under
`ErrAfterCommit`. That sentinel matters, because the two cases need opposite
responses:

```go
if err := db.WithTx(ctx, placeOrder); err != nil {
    if errors.Is(err, sqlb.ErrAfterCommit) {
        // The order exists. Something downstream of it did not fire.
        log.Error("order placed, notification failed", "err", err)
    } else {
        return err // The order does not exist.
    }
}
```

Outside a transaction, `AfterCommit` is an error rather than an immediate call:
under autocommit sqlb cannot say when the commit happened, so the callback would
fire before the insert or after it depending on which hook called it.

**From a generated handler there is always a transaction**, because
`rest.Resource` opens one per write. The two ways to end up without one are a
write you issue yourself outside `WithTx`, and a resource that set
`Options.DisableTransactions`. The second is worth stating plainly: turning it on
does not disable `AfterCommit`, it makes every registration fail at request time.
That is loud rather than silent, which is the point — but it means the option is
a decision about the resource's hooks, not only about its latency.

This is in-process and at-most-once. A callback that never ran because the
process died leaves no trace — that is what a transactional outbox is for, and
it is not built ([ADR-0012](../adr/0012-change-feed-outbox.md)).

### Reading your own writes

A hook that needs to see rows written earlier in the same transaction must read
through the transaction handle. Reading through the pool would miss them,
because they are not committed yet:

```go
sqlb.On[Post]().BeforeCreate(func(ctx context.Context, p *Post) error {
    tx, ok := sqlb.TxFrom(ctx)
    if !ok {
        return errors.New("posts must be created inside a transaction")
    }
    n, err := sqlb.Query[Post]().Where(sqlb.F("slug").Eq(p.Slug)).Count(ctx, tx)
    …
})
```

### Scoping and tests

`On[T]()` reaches a process-wide registry, which is what you want at startup. A
test needs isolation, and has two options: `Reset()` in a defer, or
`sqlb.NewRegistry()` plus `db.WithHooks(r)`, which needs no teardown. The second
is also how two tenants' worth of differing domain rules coexist in one process.

A scoped registry earns its keep outside tests too. `example/tasks` builds two
handles over one pool: one resolving against its hooks, and one against an empty
registry, used by exactly the two endpoints that have to read a user *before*
there is a tenant to scope the read to. Two values, one of which never leaves
its own file, is harder to misuse than one handle and a "skip the hooks"
flag — a flag is something a caller can pass, and the set of callers allowed
to pass it is the whole question.

## Inspecting and tracing

`q.SQL()` renders without executing. `sqlb.Explain(ctx, db, q)` plans it against
the live database without executing, which answers both halves of "did I break
something":

```go
plan, err := sqlb.Explain(ctx, db, q)
if err != nil {
    t.Fatal(err)   // the query is not valid against this database
}
if d := plan.Diagnostics(); len(d) > 0 {
    t.Errorf("plan regressed:\n%s", sqlb.Diagnostics(d))
}
```

`ExplainAnalyze` gives real timings but **executes** the statement — on a
mutation that means it writes. Use it inside a transaction you roll back.

Tracing needs no API from sqlb. `Executor` is two methods, so a wrapper observes
every statement and reaches OpenTelemetry, slog or a test double without sqlb
depending on any of them:

```go
type tracer struct{ inner sqlb.Executor }

func (t tracer) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
    start := time.Now()
    rows, err := t.inner.QueryContext(ctx, q, args...)
    slog.InfoContext(ctx, "sqlb", "sql", q, "dur", time.Since(start), "err", err)
    return rows, err
}
// ExecContext likewise, then pass the wrapper wherever you passed the *sql.DB.
```

If your wrapper should also support `WithTx`, implement `Beginner` on it —
`BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)` — alongside
`Executor`. It is asserted for rather than required, which is what keeps
`Executor` two methods.

## Next

- [REST](rest.md) — the same predicates, produced from a URL
- [Migrations](migrations.md) — changing the schema underneath all this
