# Using sqlb with sqlc

[vision.md](vision.md) says sqlb "should stay useful alongside sqlc rather than
demanding all of a codebase." This is what that looks like in practice.

The worked version is [example/withsqlc](../example/withsqlc), and the claims
below that can be tested are tested there rather than asserted here.

## Why this is not a competition

They are good at opposite things, and the reason is the same in both directions.

sqlc reads your SQL at build time and types it exactly. That is a strong
guarantee, and it is only available because the query text is fixed before the
program runs. A `WHERE` clause that depends on which query parameters a request
happened to include cannot be typed that way — not because sqlc is missing a
feature, but because the query does not exist yet at the moment sqlc runs.

sqlb builds the query at runtime, which is what makes a filterable list endpoint
expressible, and it pays for that with a weaker compile-time guarantee: column
names are strings ([ADR-0009](adr/0009-typed-column-facade.md)). What it offers
instead is [`Explain`](#instead-of-compile-time-column-checking).

So the question is never *which one*. It is *which queries go where*.

| Work | Use | Why |
|---|---|---|
| Static queries, typed end to end | **sqlc** | Its whole guarantee. sqlb's is weaker by design |
| Reporting, window functions, recursive CTEs | **sqlc** | sqlb sends these to `Raw`, which is an escape hatch, not a feature |
| A filter/sort/search list endpoint | **sqlb** | sqlc structurally cannot express a conditional `WHERE` |
| Multi-tenant scoping across every read | **sqlb** | `BeforeQuery` constrains every query of a model at once ([ADR-0008](adr/0008-hooks-as-domain-seam.md)) |
| A REST surface with an OpenAPI document | **sqlb** | Generated from the schema's declared capabilities ([ADR-0007](adr/0007-generated-rest-handlers.md)) |
| A multi-statement unit of work | **either** | `WithTx` hands you a handle; one `pgx.Tx` satisfies sqlc's `DBTX` too ([ADR-0020](adr/0020-transaction-scoped-handle.md)) |

A useful split for a typical application: sqlb owns the CRUD and list surface,
sqlc owns the dashboard and the reports.

Which leaves the question this page does not answer — what moving one of those
queries actually costs, and whether you have to move all of it.
[Refactoring a sqlc endpoint](refactoring-from-sqlc.md) is the worked version:
four stages over one list endpoint, with a stopping point after each, and a test
that requires all four to return the same rows. And
[Surveying an existing codebase](surveying-a-codebase.md) is the count that
turns this table into a number — how many of your named queries fall in each
row of it, measured rather than estimated.

## Who owns the schema

**sqlb does**, and there is one declaration rather than two.

`migrate.Diff` renders the schema declaration as DDL, and that DDL is what sqlc
reads as its `schema.sql`. The example wires this up literally:

```
example/blog/blogschema/schema.go     the one declaration you edit
  → go run ./example/withsqlc/gen     renders it to DDL
  → example/withsqlc/schema.sql       what sqlc reads
  → sqlc generate                     types its queries against it
```

`mise run generate-check` fails if `schema.sql` has drifted from the
declaration, which is the part that makes this an arrangement rather than a
convention — otherwise someone edits the schema, forgets to re-render, and sqlc
types its queries against a database that no longer exists in that shape.

### Regenerating, and which half is automatic

The first arrow is a `go:generate` directive. The second is not:

```bash
go generate ./example/withsqlc/...      # renders schema.sql from the declaration
cd example/withsqlc && sqlc generate    # retypes sqlcgen against it
```

`sqlc` is deliberately not in `mise.toml`'s pinned toolchain, so behind a
directive the second step broke `go generate ./...` — and with it `mise run
heal`, the command [CONTRIBUTING.md](../CONTRIBUTING.md) puts in front of a new
contributor — on every checkout that had not installed sqlc separately. Pinning
a fourth toolchain would have bought that back by making sqlc a build dependency
of a library whose whole argument is that it imposes none. That is the same
trade `generate-check` already refused above.

So it is a manual step after a schema change, and the honest cost is that
nothing catches a stale `sqlcgen` — not a gate, and not the tests, which assert
the mapping against a column list written alongside the structs. That cost is
unchanged by making the step manual; no gate ever covered it. What the gate does
cover is the file the two tools share, `schema.sql`, and that is still rendered
by the directive.

The reverse direction also works, and is how an existing sqlc project adopts
sqlb: `introspect.Registry` reads `pg_catalog` into a registry and
`codegen.RenderSchema` turns it into the `schema.go` you edit from then on. Your
hand-written `schema.sql` becomes the starting point rather than something to
throw away.

## Can sqlb read sqlc's structs?

**Yes, including stock sqlc output with no db tags.** This is the claim the
README makes about incremental adoption, and it is the one most worth checking
rather than believing, so
[example/withsqlc](../example/withsqlc/adopt_test.go) tests it against real
generated code.

sqlb maps a Go field to a column by its `db` tag, and falls back to the field
name in snake_case when there is none. sqlc's default naming lines up with that,
including the case that trips naive conversions:

```go
// sqlc generated this. No tags, no sqlb import, no knowledge of sqlb at all.
type Post struct {
    ID          string
    OrgID       string        // → org_id, not org_i_d
    ViewCount   int64         // → view_count
    PublishedAt pgtype.Timestamptz // a Scanner/Valuer, so nullables need nothing special
}
```

```go
// sqlb reads them, and you say what the API may do with them.
sqlb.Describe[sqlcgen.Post]().
    PrimaryKey("id").
    Filterable("status", "author_id").
    Sortable("published_at", "view_count").
    Searchable("title", "body")
```

The example deliberately leaves `emit_db_tags` **off** in its `sqlc.yaml`.
Turning it on would make the test pass for a reason that does not generalise to
the sqlc projects people already have.

**What this costs.** Capabilities cannot be read from a struct that never
declared them, so what the schema DSL states once has to be restated in
`Describe`. That is not a papercut, it is the price of this path: a column is
not filterable until you say so ([ADR-0006](adr/0006-capabilities-are-opt-in.md)),
and adopting over existing structs must not widen the API by accident. `Describe`
checks the names against the struct, so a typo fails rather than quietly
disabling a filter.

## Sharing a transaction

Both sides take an interface, and since [ADR-0040](adr/0040-the-driver-is-a-dependency.md)
they are interfaces over the same types: `sqlb.Executor` is `Query` and `Exec`
over pgx, and a pgx-generated `DBTX` is those two plus `QueryRow`. `Executor` is
a strict subset, so a `pgx.Tx` satisfies both and `DB.Tx` reaches it:

```go
err := db.WithTx(ctx, func(ctx context.Context, tx *sqlb.DB) error {
    post, err := sqlb.InsertRows(&p).One(ctx, tx)
    if err != nil {
        return err
    }
    pgTx, ok := tx.Tx()
    if !ok {
        return errors.New("expected a transaction")
    }
    // The same transaction, through sqlc's generated interface.
    return sqlcgen.New(pgTx).RecordPublication(ctx, post.ID)
})
```

Both statements land on one connection and commit or roll back together, and
`WithTx` keeps its rollback-on-panic and its `AfterCommit` callbacks. Do not
commit or roll back the returned `pgx.Tx` yourself — `WithTx` owns that
boundary.

The other direction works too, and is the one an existing sqlc codebase reaches
for first: if your code already opened the transaction, hand it to sqlb rather
than the other way round.

```go
tx, err := pool.Begin(ctx)
if err != nil {
    return err
}
defer tx.Rollback(ctx)

if err := sqlcgen.New(tx).DebitAccount(ctx, id); err != nil {
    return err
}
if _, err := sqlb.InsertRows(&entry).Exec(ctx, sqlb.New(tx)); err != nil {
    return err
}
return tx.Commit(ctx)
```

`sqlb.New(tx)` knows it is inside a transaction, so hooks see `InTx` and a
`WithTx` within joins rather than opening a second one. It does not take over
the boundary — you opened the transaction and you commit it — so `AfterCommit`
refuses on that handle rather than queueing a callback behind a commit sqlb will
never perform. Use `WithTx` when you want that guarantee.

### If your sqlc is generated for database/sql

Everything above holds because both sides speak pgx. Under
`sql_package: database/sql` they do not: `DBTX` is
`ExecContext`/`QueryContext`/`QueryRowContext`/`PrepareContext` over
`*sql.Rows` and `sql.Result`, which no sqlb handle satisfies and no adapter
fixes — the two libraries can share a pool through `stdlib.OpenDBFromPool` and
cannot share a transaction ([the driver](compatibility.md#the-driver)).

Two shapes are honest, and only one of them scales:

- **Disjoint tables.** sqlb owns tables sqlc never writes, and no unit of work
  spans both. This is the right first move: it costs nothing, needs no
  regeneration, and answers whether the list surface is worth the rest. It stops
  being viable the moment one module's filterable list and its reports read the
  same table inside one transaction.
- **Regenerate sqlc with `sql_package: pgx/v5`.** Then one `pgx.Tx` carries
  both and the split above is a split rather than a fracture. It is mechanical
  and it is not free: your models change type — `sql.NullTime` becomes
  `pgtype.Timestamptz` and so on down the column list — and any `overrides` need
  re-checking against the pgx type set. `CopyFrom` appears rather than
  disappears, which is the direction that at least argues for itself.

Read that as an end state and not as a first step. Proving the list surface on
disjoint tables costs days and can make the regeneration unnecessary; doing the
regeneration first is the largest mechanical change available and proves nothing
on its own.

**This subsection used to say the opposite**, and the reversal is the whole of
what ADR-0040 changed here: pgx-generated sqlc was the incompatible case, and
regenerating for `database/sql` was the move that bought a shared transaction.
Anyone who read that advice and waited was told to. `example/withsqlc` is
generated for pgx and asserts the compatibility above at compile time, so this
document cannot drift from it again.

What this document assumed for a long time and now barely needs: pgx's `pgtype`
values scan through sqlb because they implement `sql.Scanner` and
`driver.Valuer`, which pgx still honours as its last-resort plan.
`pgtest/pgtype_test.go` covers it in both directions including NULLs, with
compile-time assertions that fail the build if a pgx release ever drops those
interfaces. It mattered most when the two sides were on different drivers; it is
kept because "point sqlb at your existing sqlc structs" still rests on it.

## Instead of compile-time column checking

The honest gap: `sqlb.F("titel")` compiles and fails at runtime, where sqlc
would have caught it at build time. Pretending otherwise would be the wrong way
to make this argument.

The answer is not that it rarely happens. It is `Explain`, run as a test:

```go
func TestEveryQueryShapePlans(t *testing.T) {
    for name, q := range shapes {
        if _, err := sqlb.Explain(ctx, db, q); err != nil {
            t.Errorf("%s does not plan against the live schema: %v", name, err)
        }
    }
}
```

`Explain` asks Postgres to plan the statement without executing it, so a column
that does not exist or a type that no longer matches fails there. It catches
strictly more than a column check does — it validates against the *live schema*
rather than against a second model of it, so it also catches the migration that
was written but not applied.

[pgtest/explain_test.go](../pgtest/explain_test.go) does this for every shape
the blog example's three resources can produce, mutations included, and ends by
pointing the same check at a misspelled column to prove it fires
([ADR-0016](adr/0016-guards-proven-both-ways.md)).

Two things it does not give you: the failure arrives at test time rather than
compile time, and it needs a database. Both are real, and both are cheaper than
they sound if you already run integration tests — which, for anything with a
schema, you should.
