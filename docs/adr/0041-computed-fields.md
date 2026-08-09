# ADR-0041: A computed field is an expression in the row, and the parameterised ones oblige a hook

- **Status:** Working — tiers 1 to 3 are built. **Tier 4, `FromGo`, is cut**:
  this record's own trigger for cutting it fired on 2026-08-01, and the section
  below records on what. Additive, so it landed rather than waiting for 1.1: a
  schema that declares no computed column compiles to the same SQL it did before
- **Confidence:** High that the slot is needed — §13.3 of the adoption review
  ranked it first of six. High that `ColumnInfo` is where it goes: the trace said
  one interception point and the implementation found exactly one,
  `(*compiler).column`. High that the parameterised case is real and that
  [#17](https://github.com/jryannel/sqlb/issues/17) as written does not cover it.
  Medium overall, because nothing outside this repository has used it yet.
  Medium on the correlated-subquery tier being worth `Filterable()` at all. Low on
  `FromGo`, which was the tier most likely to be cut and has now been cut — the
  one prediction in this list that has since been settled by evidence
- **Decided:** 2026-07-30
- **Last reviewed:** 2026-08-01 — `FromGo` cut

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
    schema.FromGo(nextDue))                             // 4 · cut, see below
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
- **`FromGo`** was to be projection-only, obliging a hook the same way and never
  filterable or sortable. It was never built and is now cut; the tier survives in
  this list because a taxonomy that quietly loses a row stops being a record of
  what was considered.

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
- ~~**`FromGo` is not reached for.** It is the tier with the least mechanism
  behind it and the most obligation attached; if the first two applications
  express everything in SQL, cut it and keep the record honest about why.~~
  **This fired on 2026-08-01** — see *`FromGo` is cut* below.
- **The stratification does not hold on a second codebase.** One application's six
  fields decided four tiers. A second that needs a fifth means the taxonomy is
  wrong rather than incomplete.
- **`Needs` starts carrying request state generally.** If it becomes the way
  applications smuggle context into queries, it has outgrown this record and needs
  its own.

## `FromGo` is cut

The trigger above set a condition and the condition is met, so the tier goes
rather than sitting in the tracker as work nobody asked for.

**The evidence, which is two applications expressing everything in SQL:**

- `example/computed` works the six values this record was written against. Three
  are stored counters and need no tier at all; the other three — `is_overdue`,
  `progress`, `is_starred` — are `schema.Computed`, and every one of them is
  `FromSQL`. `next_due_date`, the value the proposal wrote `FromGo` for, is not
  among them: it came from the evaluated application and no example carries it;
- the multi-app adoption declared five, described in
  [#92](https://github.com/jryannel/sqlb/issues/92) as *"one per ADR-0041 tier:
  three aggregates over another table, one row-local predicate, and one that
  depends on who is asking"*. Tiers 1 to 3 again, and it reports them working
  against a real database — including a cancelled task counting as open, which
  is exactly the domain rule `FromGo` existed to catch;
- there is no `FromGo` in the tree, and `codegen/schemasrc.go` — which renders a
  schema back out of an introspected database — only knows how to emit
  `FromSQL`. A tier the round trip cannot express is a tier nothing can adopt.

**And the space it would occupy has since narrowed.**
[#93](https://github.com/jryannel/sqlb/issues/93) lifted the refusal of a
`Searchable` computed column, so a text expression over a related table is now
declarable — which is the shape a "render some related values into one field"
case would otherwise have reached for Go to do.
[#92](https://github.com/jryannel/sqlb/issues/92) made computed columns opt-in
per reader, which removed the cost argument for keeping derived work out of SQL.

**What this does not claim.** No application tried `FromGo` and found it
wanting; the finding is that none reached for it, which is weaker evidence and
is the evidence the trigger asked for. `next_due_date` over a recurrence rule —
the original motivating example — is still the case with the best claim to
needing Go, and it came from a codebase that has not adopted sqlb.

**Reopening is cheap and stays cheap.** Nothing was built, so nothing is being
removed; the cost-of-change note below still applies in the additive direction.
A second application reaching for it is the evidence this cut lacks, and
[#17](https://github.com/jryannel/sqlb/issues/17) is where it was tracked.

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
- 2026-07-31 — Built, and the record moves to Working. `schema.Computed` with
  `FromSQL` and `Needs`, `Builder.Bind`, `Describe(...).Computed` for models sqlb
  did not generate, a `ComputedColumns` method carried by the generated ones, the
  mount check extended to unsupplied binds, and `example/computed/declared.go`
  as the same three values written as declarations. Four things the design did
  not have, each found by building it:

  **The expression lives in a method, not the struct tag.** Every other thing the
  runtime knows about a column arrives in `sqlb:"…"`, which is a comma-separated
  list of words. A SQL expression contains commas, quotes and parentheses, so
  putting one there means inventing an escape and writing a parser for it.
  Codegen emits `func (T) ComputedColumns() []sqlb.Computed` instead — Go the
  compiler checks, and a reader can see what will run. The tag keeps everything
  else, so a computed column needs no second code path anywhere below it.

  **`FromGo` was not built.** The record already called it the tier most likely
  to be cut; nothing in `example/computed`'s six fields reaches for it, and the
  three that motivated the feature are all SQL. Adding it later is additive.
  This is the ADR's own "what would change our mind" answered by not needing it
  yet rather than by evidence against it.

  **An expansion does not carry a computed column, and that is a refusal rather
  than an omission.** `?expand=project` joins the target under `__ex_project`,
  and requalifying a raw fragment onto an alias is exactly what `qualify.go`
  refuses to do for a `RawPred` — text cannot be requalified with certainty, and
  a fragment that silently resolves to the wrong table is worse than an absent
  key. So an expanded row carries the target's stored columns and its derived
  ones come from its own endpoint. The design said "expansion follows from one
  change"; it does not, and this is the honest version.

  **A write's `RETURNING` keeps the row-local expressions and drops the
  parameterised ones.** The design said `RETURNING` keeps a computed column as an
  expression, which holds for a bind-free one. A bind is a property of who is
  asking, and the hooks a write runs receive the row rather than the statement,
  so there is nowhere to take one from. Rendering it against a missing bind is
  the failure this whole record exists to prevent, so the column is left out of
  the write's response and read back by the next query.

  Tier 2's `Filterable()` shipped as the opt-in the record specified, not as a
  default: a correlated subquery is projection-only unless the declaration says
  otherwise, and `Lint` reports the cost once per filterable computed column
  rather than letting the index rules fire on a column that cannot be indexed.

- 2026-08-01 — `FromGo` cut, and the record's own trigger is the reason. The
  condition it set was "if the first two applications express everything in SQL";
  they did, and *`FromGo` is cut* above names them. Two changes on the same day
  narrowed the space it would have occupied rather than widening it:
  [#93](https://github.com/jryannel/sqlb/issues/93) made a text expression over a
  related table declarable, and [#92](https://github.com/jryannel/sqlb/issues/92)
  made a computed column opt-in per reader, which removed the cost argument for
  keeping derived work out of SQL. The taxonomy keeps four rows because a record
  that quietly drops the option it rejected is not a record of the decision.

- 2026-08-05 — **Nullability inverted.** A computed column is now nullable
  unless the declaration writes `NotNull()`, where a stored one stays not-null
  unless it writes `Nullable()`. The record never stated a default, and the one
  it inherited was the one an expression cannot honour: a correlated subquery
  matching nothing is `NULL`, which for the reporting application was not an
  edge case but every row with no project plus every row pointing at a deleted
  one — a cross-module reference having no foreign key to prevent the second
  ([#147](https://github.com/jryannel/sqlb/issues/147)).

  What made it expensive is where it landed. A stored column reads its
  nullability off `NOT NULL` and the round trip checks it; a computed column has
  no DDL, so `generate` had no opinion and `Diff` correctly ignored a column that
  is not in the database. Both gates were green and the failure was a 500 at scan
  time — `cannot scan NULL into *string`, naming the generated model rather than
  the declaration that produced it — on data a fixture is unlikely to contain.

  Inference over the expression was considered and rejected for the reason the
  report gave: it is wrong in the unsafe direction wherever it is incomplete.
  `NotNull()` is a claim rather than a check, and it fails the other way, which
  is the direction this record already prefers everywhere else.

- 2026-08-08 — **Writes take the opt-in reads took, and a write's response says
  what it computed.** Two halves of the same sentence, from
  [#164](https://github.com/jryannel/sqlb/issues/164) and
  [#163](https://github.com/jryannel/sqlb/issues/163).

  This record decided `RETURNING` keeps a computed column "so a `POST` response
  carries the derived fields without a second read", and that predates
  [#92](https://github.com/jryannel/sqlb/issues/92) flipping reads to opt-in. The
  write path was never revisited, so the same aggregate a read had to ask for by
  name was evaluated by every `INSERT` and `UPDATE` of the table — and by every
  `DELETE` once an `AfterDeleteRows` hook was registered. An adopting application
  found the three consequences in ascending order: a per-write tax on aggregates
  nobody read; a create whose `participant_ids` counted rows the same transaction
  had not written yet, so the returned value was *always* wrong and the second
  read this clause exists to delete had to come back; and a subquery naming
  another module's table riding into every insert, which made the table
  unwritable unless that module's tables were present and failed a module's own
  isolation boot test. So `Insert`, `Update` and `Delete` grow `WithComputed`,
  defaulting to none, and `rest.Options.Computed` narrows the write's `RETURNING`
  as it already narrowed the read's projection — one decision covering both
  paths.

  The default flip is breaking for a caller relying on a `POST` response's
  derived fields, and it is the direction
  [#147](https://github.com/jryannel/sqlb/issues/147) chose for the same reason:
  the failure of the new default is a zero field the first test that looks
  catches, where the failure of the old one is a cost nothing reports and a value
  that can be silently wrong.

  The second half is smaller and is a plain contradiction of what this record
  says above. "The column is left out of the write's response" was true of the
  statement and false of the response: the mount serialised its whole read
  projection, so a `Needs` column came back at its Go zero value — a definite
  `false` where the truth is unknown, which is precisely the
  "declared-but-never-computed field as a permanently-zero JSON key" this record
  exists to prevent, arriving on the one path nobody looked at. The key is absent
  now, which is what a client should read as "not computed here".

- 2026-08-08 — **A caveat this record never wrote down: whose table the
  expression names.** From
  [#167](https://github.com/jryannel/sqlb/issues/167).

  `ExternalRef` refuses expansion with a stated reason — "expanding it would join
  a table this module does not own" — so the DSL takes module isolation seriously
  enough to make it a refusal. `Computed` + `FromSQL` accepts the identical
  coupling silently, because a correlated subquery is a static string and nothing
  resolves table names out of it. That asymmetry is not a defect: checking it
  would require exactly the dependency `ExternalRef`'s free-text target exists to
  avoid, and this record's own "nothing parses the SQL" is the reason. What was
  missing is that nothing *said* so.

  The footprint is what makes the two unequal, and it is the part a porter gets
  wrong: a `LEFT JOIN` lives in one query behind one handler, where a computed
  column is in the projection surface of every mount that opts in and in the
  `RETURNING` of every write that asks for it. A module that replaced the first
  with the second "on the reasoning that the coupling was identical" could not
  write its own table without the foreign module's tables present, and its
  isolation boot test failed on its own seed.

  So the rule is written on `FromSQL`, in the schema guide, and cross-referenced
  from `ExternalRef`: the question is not *is this a subquery* but *whose table
  does it name*. A subquery may name this module's tables — `participant_ids`
  over `chat_members` is correct and deletes an N+1. One naming another module's
  couples every statement of this table to that module's presence.
