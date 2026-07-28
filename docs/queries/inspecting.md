# Inspecting and tracing

Nothing runs unasked. Three separate things can be asked of a query without
executing it, and they answer different questions.

## SQL(), which executes nothing

```go
sql, args, err := q.SQL()
// SELECT "posts"."id", ... FROM "posts" WHERE ("status" = $1) AND ("title" ILIKE $2)
// [published %postgres%]
```

This is the inspection point — log it, diff it in a test, paste it into
`EXPLAIN`. It is also how a reader confirms the claims made elsewhere in this
documentation: values are always bind parameters, and only identifiers validated
against the model are interpolated.

## Explain, which plans without running

```go
plan, err := sqlb.Explain(ctx, db, q)
if err != nil {
    t.Fatal(err)   // the query is not valid against this database
}
if d := plan.Diagnostics(); len(d) > 0 {
    t.Errorf("plan regressed:\n%s", sqlb.Diagnostics(d))
}
```

`Explain` plans the statement against the live schema without executing it,
which answers both halves of "did I break something": whether the query is valid
against *this* database, and whether the plan regressed.

The first half is worth dwelling on. It fails on the migration that was written
and never applied — which a compile-time column check structurally cannot,
because the column exists in the schema file either way.

`ExplainAnalyze` gives real timings but **executes** the statement — on a
mutation that means it writes. Use it inside a transaction you roll back.

## Tracing needs no API

`Executor` is two methods, so a wrapper observes every statement and reaches
OpenTelemetry, slog or a test double without sqlb depending on any of them:

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

That two-method surface is the same reason pgx works through its stdlib adapter,
and why a connection pooler in the path needs nothing from sqlb: the query path
is tested through a real PgBouncer in transaction pooling, because that is the
deployed topology ([ADR-0019](../adr/0019-pgbouncer-in-the-path.md)).

## Checking the schema, not the query

Two more inspections belong to design time rather than to a request, and they
are on their own pages:

- **`schema.Lint()`** reports what will behave badly in production — an
  unindexed filterable column, a list endpoint with nothing sortable. See
  [Declaring tables](../schema/README.md#checking-your-work).
- **`migrate.Diff` against a replayed history** answers whether the database and
  the migration files agree. See [Adopting a database](../migrations/adopting.md).

## Next

- [Hooks](hooks.md) — what amends a query between `SQL()` and the wire
- [Queries](README.md)
