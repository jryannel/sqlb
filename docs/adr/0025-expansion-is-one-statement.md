# ADR-0025: Expansion is one statement, and Hidden survives the join

- **Status:** Working — one level of `?expand` is built on both the list and the
  item endpoint, and running against a real Postgres
- **Confidence:** Medium — the correctness argument is settled and tested three
  ways; what is unsettled is whether nesting can be added without reopening the
  one-statement choice
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-28

## Context

`?expand=author` had been parsed since the filter package existed and refused by
every surface that could answer it. Building it forced three decisions.

**One statement, or two.** The obvious implementation reads the page, collects
the foreign keys, and issues `SELECT … WHERE id IN (…)`. It inherits every hook,
filter and capability check on the target for free — and it cannot be made
consistent. The second query runs at a later snapshot, so a row deleted between
the two produces a *contradictory* answer: `author_id` set, `author` null, both
in one response. Fixing that with a repeatable-read transaction makes every list
request take one, to solve a problem only expansion has.

**Whether the target's columns can be trusted wholesale.** `row_to_json(t.*)`
builds the object in one call and takes every column — including `Hidden` ones
like `authors.password_hash` in the blog example. A hidden column surviving a
join makes `?expand` a way to read what the resource refuses to serve directly:
not a corner case, a leak in the shipped example.

**And then Postgres refused the statement.** The join, the JSON column and the
scan were built, tested in three packages, and green — and not valid SQL, because
`filter.Apply` builds unqualified column references: `column reference "id" is
ambiguous`. It was broader than the projection; `?sort` and `?search` were
ambiguous too. The in-memory driver accepts any string the builder produces, so
the golden tests proved what somebody expected rather than what Postgres accepts.
That is [ADR-0016](0016-guards-proven-both-ways.md) from the other direction: not
a guard that had never failed, but a test suite that could not.

## Decision

**One statement. A `LEFT JOIN` per relation, and a `json_build_object` over the
target's non-hidden columns.**

```sql
SELECT "posts"."id", …,
       CASE WHEN "__ex_author"."id" IS NULL THEN NULL
            ELSE json_build_object('id', "__ex_author"."id", …) END AS "__expand_author"
FROM "posts"
LEFT JOIN "authors" AS "__ex_author" ON "__ex_author"."id" = "posts"."author_id"
```

- **Columns are listed explicitly, never `row_to_json`.** `Hidden` holds across
  the join, asserted against a real Postgres by reading the JSON the database
  produced — not the decoded struct, which drops the key either way and would
  pass with the hash sitting in the answer.
- **A `LEFT JOIN` that matches nothing yields `NULL`, not an object of nulls.**
- **An unqualified column resolves to the base table, but only once something is
  joined.** Single-table SQL keeps bare column names, which is what a person
  reading a logged query wants.
- **One level.** No `?expand=author.org`.

## Consequences

**Buys.** Consistency by construction — one snapshot, so `author_id` and `author`
cannot disagree — at no transaction cost. The projection stays as wide as `T`, so
`?select` still names columns. The target's model comes from the Go type, so its
hidden columns cannot drift from what its own endpoint serves.

**Costs.** *Hooks follow the join, and getting them there cost something.* The
registry stores a type-erased view of each hook set, and predicates are rewritten
onto the join alias before being spliced into the `ON` clause, so a soft-delete
filter on List applies to `?expand=list`. The bill is a restriction: a predicate
that cannot be requalified with certainty — a `RawPred`, or a column naming an
unjoined table — fails the query rather than being dropped, because a dropped
scope predicate is the same leak arriving silently by another route.

*The join is unconditional per named relation*, with no budget beyond each
joining on a primary key; `Lint` warns on an unindexed expandable foreign key.
*Qualification is a property of the statement* now, restored around nested
compilation, and a bug there would be a wrongly qualified column rather than an
obvious failure.

## What would change our mind

- **Nesting.** `?expand=author.org` under a depth limit is more joins, which is
  fine, or an aggregation, which may not be. If the natural implementation is a
  second query, the one-statement argument has to be re-made — and it is weaker
  for a nested relation, whose inconsistency window a client can barely observe.
- **The reverse direction** was never this decision and now has its own
  ([ADR-0022](0022-references-declare-their-inverse.md)): a correlated subquery,
  because `json_agg` over a joined set changes the row count unless grouped. Still
  one statement, and `Hidden` still holds.
- **A hook that can only be expressed as a `BeforeQuery` on the target** and
  cannot move into the schema.
- **A measured cost.** If expansion is the slow query on a real screen, the
  batched second query becomes worth its inconsistency, per resource.

## Cost of change

Asymmetric, split between the SQL and the wire format. The SQL is cheap — one
file, compiled fresh per statement, covered in three packages plus `pgtest`. The
wire format is expensive: `?expand=author` puts an `author` object in the body
and an `Author *Author` field on the generated model, so removing or reshaping
either breaks every client at once. That is why the relation is named by its json
tag rather than its column — the name is part of the wire format from the first
response. Turning expansion *off* for a resource is free: drop `.Expandable()`
and the parameter is refused with the list of what would have worked.

## Revisions

- 2026-07-27 — Written after the feature landed, deliberately late: the ambiguity
  failure is the most useful thing here and was not knowable until a real database
  saw the SQL.
- 2026-07-28 — Reviewed against the code. Every checkable claim held. One
  narrowing: `?expand` reached the item endpoint reusing `Builder.Expand`
  unchanged, so this reads "the row or the page".
- 2026-07-28 — The reverse direction landed in ADR-0022, the split this record
  predicted. The prediction that mattered was the diagnosis — a collection
  changes the row count unless grouped — which sent that design to a subquery.
- 2026-07-30 — Condensed.
