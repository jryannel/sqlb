# Testing an application on sqlb

sqlb's own inner loop is `go test ./...` with no Docker and no Postgres: the
engine's suite runs against an in-memory pgx double, so hooks, scanning and the
mutation paths are all covered in a couple of seconds. `sqlbtest` is that double,
exported, so an application gets the same loop.

```go
db := sqlbtest.New(
    sqlbtest.Reply{Cols: []string{"id", "title"}, Rows: [][]any{{"p1", "Hello"}}},
)
handle := sqlb.New(db).WithHooks(hooks)

if _, err := ListPosts(ctx, handle, req); err != nil {
    t.Fatal(err)
}
if !strings.Contains(db.LastStatement(), `"tenant_id" = $1`) {
    t.Errorf("the scoping hook did not reach the statement:\n%s", db.LastStatement())
}
```

## What it is for, and what it is not for

`sqlbtest.DB` is not a Postgres. It does not parse SQL, it does not evaluate a
predicate, and it does not know that the `WHERE` clause your hook just added
would have excluded the row it is about to hand back. It answers whatever the
script says.

Its value is in what it **records**: the statements your code produced, and the
values it bound. That makes it right for the questions a unit test actually
asks —

- did the hook's predicate reach the statement?
- did the handler bind the tenant id from the request rather than from the body?
- did the write run inside a transaction, and did a failure roll it back?
- does the generated handler keep the hidden column out of its projection?

— and wrong for *does this query return the right rows*, which needs a database.

Both are worth having, and the split is worth keeping. sqlb runs its own
round-trip suite against containers in a module of its own (`pgtest`) precisely
so that the fast half stays fast. An application adopting sqlb should expect the
same shape: most tests here, a smaller suite against a real Postgres for the
answers only Postgres can give.

## Scripting

A `Reply` matches by substring, so a test can tell the page query from the count
query without parsing SQL. Replies are tried in order and the first match wins,
so the specific ones go first:

```go
db := sqlbtest.New(
    sqlbtest.Reply{Match: "count(", Cols: []string{"count"}, Rows: [][]any{{int64(42)}}},
    sqlbtest.Reply{Cols: postColumns, Rows: postRows},
)
```

A statement no `Reply` matches **fails**, with an error quoting it. That is one
place the double is stricter than it has to be, and deliberately: answering an
unscripted read with an empty result set hands back zero columns, and the scan
then fails several frames later with a message about your model's `db` tags
rather than about the missing reply. A `Reply` with an empty `Match` is the
catch-all.

Two failure modes, because they are genuinely different:

| Field | When it fails | What it stands for |
|---|---|---|
| `Err` | when the statement is sent | a syntax error, a dead connection |
| `RowsErr` | while the result is read | a constraint violation |

The second is not an academic distinction. pgx reports a constraint violation on
the extended protocol *after* `Query` has returned, so code that only checks
`Query`'s error misses it entirely. Put a `*pgconn.PgError` in `RowsErr` to reach
sqlb's constraint classification — and, through it, the REST layer's 409 and 422.

## Asserting

| Method | Answers |
|---|---|
| `LastStatement()` | the compiled SQL of the most recent statement |
| `LastArgs()` | its bind parameters |
| `Statements()` | every statement in order, including `BEGIN`/`COMMIT`/`ROLLBACK` |
| `Args()` | the bind parameters of each, aligned with `Statements()` |
| `Reset()` | clears the log, keeps the script |

`LastStatement` and `LastArgs` skip the transaction markers, because a generated
write is wrapped by default and no assertion about SQL has ever been about
`COMMIT`. A test asking whether a write was wrapped reads `Statements()`.

The two halves matter together. The statement text says a predicate was added;
the args say what value it was given — which is the half that catches a hook
reading the tenant from the request body instead of from the verified claim.

```go
if args := db.LastArgs(); len(args) != 1 || args[0] != "acme" {
    t.Errorf("bound %#v, want the caller's workspace", args)
}
```

## Transactions

`sqlbtest.DB` can begin one, so generated writes — which wrap themselves by
default — run against it unchanged, and the boundary lands in the statement log:

```go
err := handle.WithTx(ctx, func(ctx context.Context, tx *sqlb.DB) error {
    _, err := sqlb.InsertRows(&note).One(ctx, tx)
    return err
})

got := strings.Join(db.Statements(), " | ")
// BEGIN | INSERT INTO "notes" ... | COMMIT
```

Give the insert an `Err` and the same assertion reads `… | ROLLBACK`, which is
how you pin that a failing unit of work leaves nothing behind.

## The other half

What this cannot answer is whether the SQL is *valid* — whether the column
exists, whether the cast is legal, whether the index is used. For that, see
[Inspecting and tracing](inspecting.md): `Explain` runs against a real database
and fails when the query does not plan, which is the cheapest test that catches a
statement Postgres would refuse.
