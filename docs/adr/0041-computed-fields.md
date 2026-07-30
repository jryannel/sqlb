# ADR-0041: A computed field is an expression in the row, and the parameterised ones oblige a hook

- **Status:** Exploring — the shape is decided and nothing is built. Additive, so
  it does not block the tag; [the road to 1.0](../release-1.0.md) already names it
  the strongest candidate for 1.1
- **Confidence:** High that the slot is needed — §13.3 of the adoption review
  ranked it first of six, and it stands unmet. High that `ColumnInfo` is where it
  goes, because the render path was traced and there is exactly one interception
  point. High that the parameterised case is real and that
  [#17](https://github.com/jryannel/sqlb/issues/17) as written does not cover it.
  Medium on the correlated-subquery tier being worth `Filterable()` at all. Low on
  `FromGo`, which is the tier most likely to be cut
- **Decided:** 2026-07-30
- **Last reviewed:** 2026-07-30

## Context

**One derived field pushes an entity off the generated path entirely.** sqlb's
model *is* the row, so there is no slot for a value the row does not store. The
adoption review measured the consequence — a 416-line hand-written view, and the
TypeScript type by hand on top of it — and ranked closing it first of six
([review §13.3](../review-adoption-existing-app.md), [#17](https://github.com/jryannel/sqlb/issues/17)).

**The six fields a real application actually asks for do not want one feature.**
Read against the evaluated codebase, they stratify:

| Field | How the application produces it today | What it needs here |
|---|---|---|
| `totalTasks`, `completedTasks` on work packages | real columns, trigger-maintained | **nothing** — `ReadOnly` covers it |
| `totalTasks`, `completedTasks`, `openTasks` on projects | two extra aggregate queries per list, joined in Go by map | correlated subquery |
| `isOverdue` | a Go function over dates and counts | row-local expression |
| `progress %` | `completed*100/total` in the response loop | expression over other computed fields |
| `isStarred` | a per-member favourites table, listed and mapped per request | **a bind supplied per request** |

`isStarred` is the one [#17](https://github.com/jryannel/sqlb/issues/17) does not
reach. `FromSQL("…")` is a static string; whether a row is starred depends on who
is asking. Shipping the proposal as written leaves that field hand-written, and
the entity stays off the generated path — which was the point of building it.

**The mechanism is smaller than the feature sounds,** because every consumer
already resolves through `*ColumnInfo`, and every one of them renders through a
single function. The filter parser builds predicates as `sqlb.F(col.Name)`
(`filter/filter.go:492`, and again at 900 and 952), sorts the same way, and
`rest` gates on the same struct (`rest/params.go:31`). All of it lands in
`compile.go:145`, `(*compiler).column`.

**What it costs today is written out and tested** in
[`example/computed`](https://github.com/jryannel/sqlb/tree/main/example/computed),
which produces all six values with `RawSel`, `RawPred` and a hand-written
migration. It compiles to correct, parameterised SQL in one round trip — this
record is not claiming the SQL cannot be written. What that example shows instead
is the two things a raw fragment cannot buy: the `isStarred` expression is
written twice and its bind supplied twice, with nothing keeping the copies
agreeing, and none of the three derived fields exists to the filter grammar, the
TypeScript type, the CLI flags or the OpenAPI document. The gap is in what sqlb
can *declare*, not in what it can express, and the declaration is what the
emitters read.

**Half the mechanism is already in the tree, and it is not reachable.**
[ADR-0026](0026-vectors-declare-their-index.md)'s `Near` names one vector in the
projection, the threshold and the ordering, and binds it once — `sharedValue` in
`near.go`, which the compiler resolves to a placeholder on first sight and reuses
after. That is exactly what a parameterised computed field wants, and it is
unexported and welded to `Nearness`. A declared computed field is the general
case of a facility vector search already proved worth having.

**What the review's own proposal got right and left open.** It correctly refuses
to let the Go form pretend to be filterable, and it correctly reaches for
[ADR-0030](0030-declared-scope-is-required.md)'s obligation shape to stop a
declared-but-never-computed field from being a permanently-zero JSON key. This
record keeps both and applies the second one to a case the review did not have:
an unbound `isStarred` renders `member_id = NULL`, returns `false` for every row
forever, and looks exactly like a working feature.

## Decision

**A computed field is a `*ColumnInfo` carrying an `Expr` instead of only a name.**
The compiler substitutes the expression wherever that column is rendered, so
`WHERE`, `ORDER BY`, `?select` and expansion follow from one change rather than
four. The projection emits `(expr) AS name`, which the name-matching scan already
handles unchanged.

Four declaration forms, and what each is allowed to claim:

```go
schema.Computed("is_overdue", schema.TypeBool,
    schema.FromSQL("due_date < current_date AND open_tasks > 0")).
    Filterable()                                        // 1 · row-local

schema.Computed("total_tasks", schema.TypeInt,
    schema.FromSQL("(SELECT count(*) FROM tasks t WHERE t.project_id = projects.id)"))
                                                        // 2 · correlated: projection only

schema.Computed("is_starred", schema.TypeBool,
    schema.FromSQL("EXISTS (SELECT 1 FROM project_stars s "+
        "WHERE s.project_id = projects.id AND s.member_id = ?)")).
    Needs("viewer").Filterable()                        // 3 · parameterised

schema.Computed("next_due_date", schema.TypeDate,
    schema.FromGo(nextDue))                             // 4 · projection only
```

- **Row-local `FromSQL`** may be `Filterable()` and `Sortable()`. It is an
  expression the compiler emits into a predicate, which is
  [ADR-0003](0003-one-ast-two-producers.md) working as specified.
- **Correlated `FromSQL`** is projection-only unless `Filterable()` is written
  explicitly, because a subquery in `WHERE` runs once per row. The declaration is
  the acknowledgement.
- **`Needs(key)`** takes [ADR-0030](0030-declared-scope-is-required.md)'s shape:
  the declaration writes no value, and `rest.Resource` refuses to mount until a
  `BeforeQuery` hook supplies the bind. Same idiom as `Scoped`, same failure mode
  closed at startup rather than in production.
- **`FromGo`** is projection-only, obliges a hook the same way, and is never
  filterable or sortable.

Three declaration-time errors, each closing a failure that is otherwise silent:

- **`Sortable()` on a volatile expression.** [ADR-0027](0027-keyset-pagination.md)
  keysets on the sort column; an expression reading `now()` is not stable across
  pages, so page 1 and page 50 can disagree about the same row.
- **`Searchable()` on any computed field.** `?search` fans out over text columns
  ([ADR-0037](0037-search-is-ilike-until-it-cannot-be.md)); there is no coherent
  reading of it here.
- **`Needs` with no hook behind it,** at mount.

**No DDL, in either direction.** A computed field emits nothing from `migrate`,
and `Diff` does not see one. Writes exclude it — `mutate.go:193` and the `UPDATE`
set — while `RETURNING` (`mutate.go:550`) *keeps* it as an expression, so a
`POST` response carries the derived fields without a second read.

**What is deliberately not a computed field:** a counter a trigger maintains. That
is a real column, it is `ReadOnly` today, and it is what the evaluated application
does for work packages. This record should not be read as an argument to move it.

**Postgres `GENERATED ALWAYS AS … STORED` is out of scope.** It would make a
computed field indexable, which is the honest answer to filtering tier 2 at scale
— but it requires `IMMUTABLE`, so it cannot express the `now()`-dependent fields
that motivated this, and it puts computed fields back into `Diff`. Deferred as its
own decision rather than smuggled in as a flag.

## Consequences

**Buys.** The field lands in the row type, the JSON, the TypeScript and Dart
types and the CLI column set — one declaration, five artefacts, which is where
the value is and what hand-writing costs today. Capabilities keep working
unchanged, because a computed field is a `ColumnInfo`: `Hidden` hides it,
`Filterable` gates it, and a rejection still names the accepted set
([ADR-0011](0011-actionable-errors.md)). The two aggregate round-trips the
evaluated application issues per list collapse into the statement. And the
parameterised tier means the per-viewer fields — the ones every product grows —
stay on the generated path instead of forking it.

**Costs.** `FromSQL` is raw SQL in the schema, which
[ADR-0024](0024-no-annotation-slot.md)'s bar admits only because there is now a
consumer: it is unvalidated until a query runs, and `Explain` becomes the only
thing that catches a typo. Correlated subqueries make a list's cost a function of
its page size in a way `?select` must respect — an unselected computed field must
not be projected, or narrowing a response gets more expensive rather than less.
The schema gains a second kind of thing a table can hold, and `FieldDesc` grows a
variant that most of its fields do not apply to. `Needs` adds a third obligation
to the mount check, and obligations that accumulate faster than they are
consolidated become a startup failure nobody can read.

## What would change our mind

- **Tier 2 is never filtered in practice.** If applications project counters and
  filter on stored ones, the correlated-subquery form collapses to projection-only
  and the `Filterable()` opt-in is dead weight to remove.
- **`FromGo` is not reached for.** It is the tier with the least mechanism behind
  it and the most obligation attached; if the first two applications express
  everything in SQL, cut it and keep the record honest about why.
- **The stratification does not hold on a second codebase.** One application's six
  fields decided four tiers. A second that needs a fifth means the taxonomy is
  wrong rather than incomplete.
- **`Needs` starts carrying request state generally.** If it becomes the way
  applications smuggle context into queries, it has outgrown this record and needs
  its own.

## Cost of change

Asymmetric, and the cheap direction is the one that matters. **Adding it** is
additive: a schema that declares no computed field compiles to the same SQL, so
nothing existing moves — which is why it is a 1.1 item rather than a 1.0 blocker.
**Removing a tier** later is cheap while the field is projection-only and
expensive once it is filterable, because a filter expression a client can send is
part of the REST contract that [compatibility.md](../compatibility.md) freezes:
withdrawing `?filter=totalTasks.gt.5` is a break, and withdrawing the JSON key is
a bigger one.

That asymmetry argues for shipping tiers 1 and 3 first and holding tier 2's
`Filterable()` until an application asks for it by name — projection is
reversible, a filter grammar is not.

## Revisions

- 2026-07-30 — Written, against [#17](https://github.com/jryannel/sqlb/issues/17)
  and the six fields the evaluated application actually derives. Two things the
  issue did not have: the parameterised tier, which its `FromSQL` cannot express,
  and the trace showing one interception point rather than four.
- 2026-07-30 — `example/computed` added, which turns the baseline from an
  assertion into a compiled statement this record can be read against. It also
  narrowed one claim: techniques 1, 2 and 4 there survive this ADR unchanged, so
  `schema.Computed` is an addition to the options and not a replacement for
  them — a generated column is still the only one that can be indexed.
