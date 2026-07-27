# ADR-0025: Expansion is one statement, and Hidden survives the join

- **Status:** Working — one level of `?expand` is built and running against a
  real Postgres
- **Confidence:** Medium — the correctness argument is settled and tested three
  ways; what is unsettled is whether nesting can be added without reopening the
  one-statement choice
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

`?expand=author` had been parsed since the filter package existed and refused by
every surface that could have answered it. Refusing was the right interim
behaviour — a silently dropped expansion is a 200 with a field missing, which a
client cannot distinguish from a null reference — but it left the largest
declared-and-unbuilt feature in the project.

Building it forced three decisions. Two are about what the SQL says; the third
turned out to be about whether it is SQL at all.

### One statement, or two

The obvious implementation is two queries: read the page, collect the foreign
keys, issue `SELECT … WHERE id IN (…)`, stitch the results in Go. It avoids the
join entirely, it is what an ORM without a query builder usually does, and it
has an appealing property — the second query is a plain single-table read, so
every hook, every filter and every capability check that applies to the target
model applies to it unchanged.

It also cannot be made consistent. The second query runs at a later snapshot, so
between the two a row can be deleted and the caller gets a null expansion for a
reference the database still holds — not a stale answer, a *contradictory* one:
`author_id` is set, `author` is null, and both came from the same response.
Wrapping the pair in a repeatable-read transaction fixes it and makes every list
request take a transaction, which is a cost paid by every caller to fix a
problem only expansion has.

### Whether the target's columns can be trusted wholesale

Given a join, the target has to arrive as one value or its columns have to be
aliased apart from the base table's and unaliased again at scan time —
`posts.id` and `authors.id` are both `id`. Building an object is the simpler
half of that: the row arrives as JSON and scanning it is a `json.Unmarshal` into
a type that already knows its own shape.

`row_to_json(t.*)` does that in one call. It also takes every column, and
`Hidden` is a capability the blog example uses for `authors.password_hash`. A
hidden column that survives a join makes `?expand` a way to read a column the
resource refuses to serve directly — not a leak in a corner case, a leak in the
shipped example, reachable by a request the manifest advertises.

### And then Postgres refused the statement

The join, the JSON column and the scan were built, tested in three packages, and
green. They were also not valid SQL. `filter.Apply` builds its projection from
unqualified column references, which is correct and readable while one table is
named:

```sql
SELECT "id", "org_id", … FROM "posts"
```

Add the join and it is not a query that returns the wrong row. It is not a query:

```
ERROR: column reference "id" is ambiguous (SQLSTATE 42702)
```

It was broader than the projection — `?sort=created_at` and `?search=` were
ambiguous too, wherever the two tables shared a column name. The in-memory
driver the engine's tests use accepts any string the builder hands it, so the
golden tests proved what somebody expected rather than what Postgres accepts.
This is [ADR-0016](0016-guards-proven-both-ways.md) arriving from the other
direction: not a guard that had never failed, but a test suite that could not.

## Decision

**One statement. A `LEFT JOIN` per relation, and a `json_build_object` over the
target's non-hidden columns, in the query that reads the page.**

```sql
SELECT "posts"."id", …,
       CASE WHEN "__ex_author"."id" IS NULL THEN NULL
            ELSE json_build_object('id', "__ex_author"."id", …) END AS "__expand_author"
FROM "posts"
LEFT JOIN "authors" AS "__ex_author" ON "__ex_author"."id" = "posts"."author_id"
```

Three things follow from it, and they are the decision as much as the shape is.

**The target's columns are listed explicitly, never `row_to_json`.** `Hidden`
holds across a join. This is the one failure here that would be a security bug
rather than a broken feature, and it is asserted against a real Postgres by
reading the JSON the database produced — not the decoded struct, which drops the
key either way and would pass with the hash sitting in the answer.

**A `LEFT JOIN` that matches nothing yields `NULL`, not an object of nulls.**
"There is no related row" and "there is one and every field is empty" are
different answers and a client can act on the difference.

**An unqualified column resolves to the statement's base table — but only once
something is joined.** Every name a request can write, from `?select`, `?sort`
or a filter, names a column of the model being queried, so the base table is
what it meant. Single-table SQL keeps its bare column names, because that is
what a person reading a logged query wants to see and there is nothing ambiguous
about it.

**One level.** A relation expands to its row; that row's relations do not expand
in turn, and there is no `?expand=author.org`.

## Consequences

**What this buys.** The expansion is consistent by construction — one snapshot,
so `author_id` and `author` cannot disagree — and it costs no transaction. The
projection stays exactly as wide as `T`: an expanded relation arrives in its own
result column, so `?select` still names columns and nothing about the row shape
changes when a relation is added. Because the target's model comes from the Go
type rather than a second declaration, its columns and its hidden ones cannot
drift from what the target's own endpoint serves.

**What this costs.** Three things, and the first is the one to watch.

*Hooks do not follow the join.* `BeforeQuery` is keyed by model type and runs for
the query being executed, so a hook on the target does not apply to an
expansion. In `example/tasks` this means a soft-deleted list still expands: the
hook filters `deleted_at` for `GET /lists` and cannot for `?expand=list`. Where
a hook enforces a boundary the expansion must respect, the schema has to enforce
it too — that example keeps a task and its list in the same workspace with a
composite foreign key, not with the hook, and that is why it is safe. This is
the cost of the two-query alternative inverted: it would have got hooks for free
and consistency never.

*The join is unconditional per named relation.* An `?expand` naming three
relations is three `LEFT JOIN`s in one statement, and there is no budget for
them beyond the fact that each joins on a primary key. `Lint` warns when an
expandable foreign key is unindexed, which is the only pressure applied today.

*Qualification is now a property of the statement rather than of each
reference.* The compiler carries a base table while compiling a joined query.
It is restored around nested compilation, and a bug there would be a wrongly
qualified column rather than an obvious failure.

## What would change our mind

- **Nesting, if it is ever asked for.** `?expand=author.org` under a depth limit
  is more joins in the same statement, which is fine, or an aggregation, which
  may not be. If the natural implementation of nesting is a second query, the
  one-statement argument has to be re-made rather than assumed — and it is a
  weaker argument for a nested relation, whose inconsistency window a client is
  less able to observe.
- **The reverse direction.** A collection of children rather than a single
  parent — `?expand=comments` on a post — is not this decision at all.
  `json_agg` over a joined set changes the row count unless it is grouped, and
  that is the design this record does not cover.
- **A hook that has to follow the join.** One concrete case where a boundary can
  only be expressed as a `BeforeQuery` on the target, and cannot be moved into
  the schema, is the trigger to revisit. The fix would be applying the target
  model's hooks to the joined subexpression, which is a real feature and is not
  built on speculation.
- **A measured cost.** If expansion shows up as the slow query on a real screen,
  the batched second query becomes worth its inconsistency and this becomes a
  choice per resource rather than a property of the feature.

## Cost of change

**Asymmetric, and the split is between the SQL and the wire format.**

The SQL is cheap to change. It is generated in one file, compiled fresh for each
statement, and covered by tests in three packages plus `pgtest`. Swapping
`json_build_object` for something else, or the join for a batched second query,
touches `expand.go` and nothing a caller wrote.

The wire format is expensive. `?expand=author` puts an `author` object into the
response body and a typed `Author *Author` field onto the generated model.
Removing either breaks every client and every consumer of the generated code at
once; changing the shape of the object — nesting the key differently, renaming
it away from the relation name — is the same bill. This is why the relation is
named by its json tag rather than by the column: the name is part of the wire
format from the first response, so it had better be the name the payload uses.

Turning expansion *off* for a resource is free at any time: drop `.Expandable()`
and the parameter is refused with the list of what would have worked.

## Alternatives considered

**Two queries with a batched `WHERE id IN (…)`.** Genuinely close, and the
better answer on the one axis this record concedes: it would inherit hooks,
filters and capability checks on the target for free, which is exactly the hole
in the chosen design. It loses on consistency, and consistency is the property a
caller cannot restore for themselves — they can work around a missing hook by
moving the rule into the schema, and they cannot work around a response that
contradicts itself. If a hook that has to follow the join ever turns up, this is
the alternative to weigh again.

**`row_to_json(t.*)`.** One call instead of a column list, and it was rejected
on `Hidden` alone. Worth recording that the failure mode is quiet: it produces a
correct-looking expansion containing an extra key, which no test asserting on a
decoded struct can see, because the Go type drops the key. The check that finds
it has to read the database's answer.

**Joining the target's columns in directly, aliased.** `authors.id AS
__ex_author_id` and so on, unaliased at scan time. It avoids depending on
Postgres's JSON functions and it makes the projection legible in a log. It also
means the scanner has to reconstruct a struct from a flat column set it
assembled the naming convention for, which is the work `json.Unmarshal` already
does against a type that knows its own shape.

**Merging the expansion in the REST layer instead.** Read the page, then let the
handler fetch and attach. Rejected because it makes expansion a REST-only
feature: `sqlb.Query[Post]().Expand("author")` would not work, and the engine is
meant to be usable without the REST layer ([ADR-0010](0010-codegen-is-optional.md)
is the same principle applied to codegen). It also puts the target's `Hidden`
enforcement in the layer least able to check it.

**Qualifying every column in every statement.** The simplest version of the
ambiguity fix, and it makes every logged query noisier to read for a problem
only joined statements have. Rejected for that, and the narrower rule is
asserted in both directions — a joined query qualifies, a single-table one does
not.

## Revisions

- 2026-07-27 — Written, after the feature landed in three commits. Recorded late
  on purpose: the ambiguity failure is the most useful thing in here and it was
  not knowable until a real database saw the SQL, so a record written at design
  time would have argued the first two points confidently and missed the one
  that actually broke.
