# Mutations and transactions

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
returns `ErrNotFound`. When any row is skipped, **no** caller struct is written
back — the returned slice is shorter than the rows that went in, so position no
longer identifies them, and writing back by position would hand one row's
generated id to another. The returned slice is the account of what was written.

## When the database refuses a write

A unique index, a foreign key or a check constraint refusing a write is usually
the caller's mistake rather than an outage, so it arrives as a value you can
branch on:

```go
if _, err := sqlb.InsertRows(&loan).One(ctx, tx); err != nil {
    var c *sqlb.ConstraintError
    if errors.As(err, &c) && c.Kind == sqlb.ConstraintUnique {
        return "you already have a copy of that book out"
    }
    return err
}
```

`errors.Is(err, sqlb.ErrConstraint)` is the cheap test for the class.
`ConstraintError.Kind` is always set — `ConstraintUnique`, `ConstraintForeignKey`,
`ConstraintCheck`, `ConstraintNotNull`, `ConstraintExclusion`. The generated REST
handlers use exactly this to answer 409 for a conflict and 422 for the rest,
instead of the 500 an unrecognised error would otherwise become.

`Constraint` — the name of the index that refused — needs a driver to read it,
because every driver exposes it as a struct field rather than as a method and
this library depends on the standard library alone. Register a classifier once
at startup and the field is filled in:

```go
sqlb.SetErrorClassifier(func(err error) (sqlb.ConstraintError, bool) {
    var pg *pgconn.PgError
    if !errors.As(err, &pg) {
        return sqlb.ConstraintError{}, false
    }
    kind, ok := sqlb.ConstraintKindOf(pg.SQLState())
    if !ok {
        return sqlb.ConstraintError{}, false
    }
    return sqlb.ConstraintError{
        Kind:       kind,
        Constraint: pg.ConstraintName,
        Table:      pg.TableName,
        Column:     pg.ColumnName,
        Detail:     pg.Detail,
    }, true
})
```

Then `c.Constraint == "loans_one_open_per_book_per_borrower"` is a comparison
against a value rather than a `strings.Contains` on a message — which is what
the same code looks like without it, and what no rename survives.

This is the mechanism behind [where domain logic
goes](../concepts/domain-logic.md): the check constraint is the guarantee, and
the classifier is what turns its refusal into a sentence a caller can act on.

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

## Compute in the database, not in Go

`SetExpr` writes an expression rather than a value, which is how a counter moves
without a read-modify-write:

```go
u.Stmt().SetExpr("view_count", sqlb.Raw{SQL: "view_count + ?", Args: []any{n}})
```

The difference matters under concurrency. `available_copies = available_copies -
1 WHERE id = $1 AND available_copies >= 1` is evaluated by Postgres under a row
lock, so twenty simultaneous requests for the last copy produce one success and
nineteen refusals whose transactions roll back. Reading the row first and
deciding in Go is wrong, and passes every test that runs one request at a time.

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

### Row locking

A transaction on its own does not stop two requests reading the same row,
deciding the same thing and both writing. `ForUpdate` is what does:

```go
stock, err := sqlb.Query[Stock]().
    Where(sqlb.F("sku").Eq(sku)).
    ForUpdate().
    One(ctx, tx)
```

The lock is held until the transaction ends. `ForShare` takes the weaker form,
and `SkipLocked` — valid only with one of the two — steps over rows another
transaction already holds, which is what makes a queue consumer work.

Two rules make the difference between code that passes its tests and code that
survives load.

**Take locks in a consistent order.** Two transactions locking the same rows in
opposite orders deadlock, and the test that would have found it is the one
nobody writes.

**In a hook, take the lock in `BeforeCreate`, not `AfterCreate`.** See
[Locking order](hooks.md#locking-order) — it is invisible in the Go code and
scales with concurrency.

## Sharing the transaction with another library

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
one failure mode that looks like success. [Using sqlb with sqlc](../with-sqlc.md)
covers the pairing in full: who owns the schema, and which queries go where.

## Next

- [Hooks](hooks.md) — the registrations these statements run
- [Inspecting and tracing](inspecting.md) — seeing the SQL before it runs
- [Migrations](../migrations/README.md) — changing the schema underneath all this
