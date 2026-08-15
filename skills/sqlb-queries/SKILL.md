---
name: sqlb-queries
description: Use when writing, reviewing or debugging a query with the sqlb builder in Go — a list endpoint, a filter or sort, an aggregate or GROUP BY rollup, a join, an upsert, a dashboard or report query — or when deciding whether a query belongs in sqlb at all versus sqlc, hand-written SQL, or sqlb.Raw. Covers where the builder ends, and four failure modes that compile, pass their tests, and are wrong at runtime.
---

# Writing queries with sqlb

sqlb builds SQL at runtime, which is what makes a filterable list endpoint
expressible and what makes its compile-time guarantee weaker than a static
generator's. The question is never *sqlb or sqlc*. It is **which query goes
where**, and this skill is the boundary.

Two failure modes, opposite directions, both common:

- **Bailing out too early.** Dropping to `Raw` for something the builder owns —
  joins, `GROUP BY`, `HAVING`, aggregates, jsonb containment, row locks. This
  throws away identifier quoting and hook-applied tenant scoping for no gain.
- **Torturing the builder too long.** Chaining `Raw` fragments until the query
  is SQL wearing a Go costume. Past a certain point the honest move is a hand-
  written statement or a sqlc query.

## The decision rule

Ask what the query reaches for. **sqlb degrades when the query or the response
reaches past the row.**

| The query is | Use |
|---|---|
| Rows of one table, filtered/sorted/paged — especially when the `WHERE` depends on which request params arrived | **sqlb.** This is the case a static generator structurally cannot express |
| The same scope predicate on every read of a model | **sqlb.** `BeforeQuery` constrains every query at once |
| A REST surface with an OpenAPI document | **sqlb.** Generated from declared capabilities |
| A rollup: `GROUP BY`, aggregates, `HAVING` | **sqlb, carefully.** It works — read *Trap 2* first |
| Window functions, `WITH RECURSIVE`, `UNION`, `DISTINCT ON` | **sqlc or hand-written SQL.** No builder spelling exists; `Raw` reaches them but you are writing SQL either way |
| A reporting or dashboard query typed end to end | **sqlc.** Its guarantee is stronger and the query text is fixed |

A good split: **sqlb owns the CRUD and list surface, sqlc owns the dashboard and
the reports.**

## What the builder actually owns

Check here before reaching for `Raw` — this list is wider than it looks.

- **Predicates.** `Eq` `Neq` `Gt` `Gte` `Lt` `Lte` `Between` `NotBetween`
  `OneOf` `NotOneOf` `IsNull` `NotNull` `EqField`; text `Contains` `StartsWith`
  `EndsWith` `Like` `ILike`; arrays `Has` `HasAny` `HasAll`; jsonb
  `ContainsJSON` / `NotContainsJSON` (renders `@> $1::jsonb`). Combine with
  `And` `Or` `Not` — all skip zero predicates, so an absent filter stays absent
  rather than becoming always-false.
- **Joins.** `Join(table, alias, on)` and `LeftJoin`. Note the cost: both take
  the table and alias as **strings**, so columns across the seam are `F("c.id")`
  — untyped, outside the generated facade, and unchecked until the database
  sees them.
- **Rollups.** `GroupBy` `GroupByExpr` `Having`, and `Sum` `Avg` `Min` `Max`
  `Count` `CountOf` `CountDistinct` `Coalesce`. Read into a non-model type with
  `Collect[R]`; hooks still run, so tenant scoping applies to aggregates too.
- **Locking.** `ForUpdate` `ForShare` `SkipLocked` — a claim queue where each
  row goes to exactly one worker is expressible and tested under contention.
- **Upserts, including arithmetic.** `OnConflictDoNothing(target…)` and
  `OnConflictUpdate(target []string, cols…)` — note `target` is a **slice**,
  the update columns are variadic. For an expression, chain `OnConflictSet`
  after a conflict clause (alone it errors, and says so):

  ```go
  sqlb.InsertRows(&m).
      OnConflictUpdate([]string{"bucket"}).
      OnConflictSet("hits", sqlb.Add(sqlb.Current("hits"), sqlb.Excluded("hits")))
  // → ON CONFLICT ("bucket") DO UPDATE SET "hits" = "meters"."hits" + EXCLUDED."hits"
  ```

  `Current` is the stored row, `Excluded` the proposed one — insert-or-increment
  has a spelling.
- **Escape hatches.** `Raw`, `RawPred`, `RawSel`. Contents are not validated;
  `?` placeholders are renumbered by the compiler.

Prefer the generated typed columns (`OrderCols.Status`) over `F("status")`
wherever they exist — a misspelling becomes a build error instead of a runtime
one.

## Four traps that compile, pass, and are wrong

These are the reason this skill exists. Each one produces code that builds,
returns no error, and satisfies tests written against populated data.

### Trap 1 — an aggregate over an empty set fails to scan

`Sum` over zero rows is SQL `NULL`, not `0`. Scanning it into an `int64` is a
**runtime error**, and the failing case is the one nobody fixtures: a dashboard
before the first sale, a new account, a stock with no trades.

```go
// Breaks the moment the set is empty:
sqlb.Collect[Revenue](ctx, db, q.Select(sqlb.Sum(sqlb.F("total_cents")).As("cents")))

// The fix. Selection does not implement Expr — bridge with .Expr():
q.Select(sqlb.Coalesce(
    sqlb.Sum(sqlb.F("total_cents")).Expr(),
    sqlb.Raw{SQL: "0"},
).As("cents"))
// → SELECT coalesce(sum("total_cents"), 0) AS "cents"
```

The first attempt at `Coalesce(Sum(f), …)` fails to compile in a way that reads
as *"these do not compose"* rather than *"call `.Expr()`"*. They do compose.

### Trap 2 — a bind parameter in both the projection and GROUP BY is numbered twice

Postgres then sees two different expressions and refuses the query. The unit of
a `date_trunc` rollup must be a **literal**, not a bound value:

```go
// Wrong — $1 in the projection, $2 in the GROUP BY, args [day day]:
bucket := sqlb.Call{Name: "date_trunc", Args: []sqlb.Expr{
    sqlb.Val("day"), sqlb.F("created_at").Column()}}

// Right — the unit is a Raw literal, so both sides render identically:
bucket := sqlb.Call{Name: "date_trunc", Args: []sqlb.Expr{
    sqlb.Raw{SQL: "'day'"}, sqlb.F("created_at").Column()}}

q.Select(sqlb.Sel(bucket).As("bucket"), sqlb.Count()).GroupByExpr(bucket)
```

Also: the REST surface has **no aggregate shape**. A rollup is a Go query, not
a generated endpoint.

### Trap 3 — a day filter against `timestamptz` matches zero rows, silently

There is no cast on the bind parameter, so `F("at").Eq("2026-08-03")` returns
**no rows and no error**. `Field.Cast` renders the right operand but returns an
`Expr`, which a projection accepts and no comparison takes — every comparison
hangs off `Field`. So the cast is expressible in a `SELECT` list and not in a
`WHERE` clause.

```go
// Silently matches nothing:
q.Where(sqlb.F("at").Eq(day))

// The predicate that works, reachable only through Raw:
q.Where(sqlb.RawPred(`"at"::date = ?::date`, day))
```

Prefer a half-open range on the raw column when you can compute the bounds — it
uses the index, which `::date` on the column does not. Two predicates in one
`Where`, not a chain (a predicate has no comparison methods):

```go
q.Where(sqlb.F("at").Gte(start), sqlb.F("at").Lt(end))
// → WHERE ("at" >= $1) AND ("at" < $2)
```

### Trap 4 — `OnConflictDoNothing` picks the terminal for you

An idempotency key does not behave the way it reads. `DO NOTHING` skips the
row, so nothing is returned and there is no row for `One` to give back. Since
#146 the pairing is refused at the terminal rather than answered with
`ErrNotFound`, but the choice is still yours to make:

```go
// Refused, with a message naming both of the following:
sqlb.InsertRows(&p).OnConflictDoNothing("idem_key").One(ctx, db)

// "Make sure it exists" — empty slice, nil error, on the conflict:
sqlb.InsertRows(&p).OnConflictDoNothing("idem_key").Exec(ctx, db)

// "Give me the row either way" — a write that changes nothing is still a
// written row, and a written row is a returned one:
sqlb.InsertRows(&p).OnConflictUpdate([]string{"idem_key"}, "idem_key").One(ctx, db)
```

## Escape hatch discipline

When you do need `Raw`, keep it as narrow as the expression that needs it.
`RawPred("\"at\"::date = ?::date", day)` gives up checking for one predicate;
`RawSel("COALESCE(SUM(\"total_cents\"), 0)")` gives it up for the whole
projection when `Coalesce(…​.Expr(), …)` would not have. Reach for `Raw{SQL:}`
for a *literal* inside an otherwise-built expression — that is the narrowest
form and the one Trap 2 needs.

One positional gotcha: `RawSel` reaching `DISTINCT ON` only works as the
**first** projection item, and nothing enforces that — getting it wrong is a
syntax error at the database rather than a build error in Go.

## Verifying instead of guessing

- **`q.SQL()`** returns the statement and its bind parameters and executes
  nothing. Use it to check numbering — it is what catches Trap 2.
- **`Explain` / `ExplainAnalyze`** plans without running, with `UsesIndex` and
  `UsesSeqScan` for assertions. This is the answer to sqlb's weaker
  compile-time guarantee: check the query against the database rather than
  hoping.
- **A test over an empty table** catches Trap 1, and nothing else will.

## Not expressible — stop and use SQL

Composite primary keys (a row is addressed by one column; the refusal names
`UniqueIndex` as the workaround), `vector(n)` columns, range types and
`EXCLUDE USING gist`, `tsvector` full-text search (`?search` is `ILIKE`),
self-referencing `parent_id` as a typed reference, and generated
columns/triggers. These are decisions with records behind them, not gaps
awaiting a patch — check architecture.md's Decisions section before proposing one.
