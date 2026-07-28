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
