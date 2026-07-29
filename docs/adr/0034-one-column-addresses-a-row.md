# ADR-0034: A row is addressed by one column, and a composite key becomes a unique index

- **Status:** Working — `schema.Validate` has refused a second `PrimaryKey()`
  since before `v0.1.0`, and `rest.Resource` refuses to mount a read, update or
  delete on a model without one. What is new here is the record, not the
  behaviour
- **Confidence:** High on the REST half, where the constraint is a wire format
  decision and the freeze makes a guess expensive; Medium on the rest, because
  the tuple work everywhere else is mechanical and merely unbuilt, and nobody
  has yet paid the surrogate-key migration this record tells them to pay
- **Decided:** 2026-07-29
- **Last reviewed:** 2026-07-29

## Context

The refusal exists and has never been explained. `schema.Validate` reports
`%d primary keys declared, expected at most one (use UniqueIndex for composite
keys)` — a message that names the workaround and not the reason, which is the
one thing [ADR-0011](0011-actionable-errors.md) asks an error not to do.

An outside reader noticed. The second evaluation
([review-adoption-multi-app.md](../review-adoption-multi-app.md)) asks for
"composite primary keys, **or** a documented 'surrogate key required' stance" —
a polite way of saying it could not tell which of the two it was looking at, and
therefore could not tell whether the cost it was about to pay was permanent. It
is about to pay a real one: twenty-six composite primary keys across fifteen
files, on tables chosen that way deliberately.

### Six places assume one column, and five of them could stop

| Site | What it does with the key |
|---|---|
| [`filter/filter.go`](../../filter/filter.go) | prepends it to every list's sort, so the order is total ([ADR-0027](0027-keyset-pagination.md)) |
| [`cursor.go`](../../cursor.go) | breaks ties on it, and the cursor payload holds its value |
| [`expand.go`](../../expand.go) | joins on it and aggregates the expanded rows by it |
| [`rest/rest.go`](../../rest/rest.go) | refuses read, update and delete without one |
| [`schema/registry.go`](../../schema/registry.go) | a `Ref` defaults to its target's key, and a target without one is rejected |
| [`schema/lint.go`](../../schema/lint.go) | warns that a table accepting creates cannot address what it created |

Five of those six have a mechanical answer that nobody has written. A tuple
cursor is a row-value comparison — `(created_at, a, b) > ($1, $2, $3)` — which
Postgres indexes and plans as well as the single-column form. An expansion
aggregates on a tuple as easily as on a scalar. The sort tiebreak appends two
columns instead of one. A `Ref` carrying two columns is the composite foreign
key [`references.md`](../schema/references.md) already describes as a hand-
written `migrate.Change`.

The sixth has no mechanical answer, and it is the whole decision.

### The blocker is the URL, not the SQL

`rest.Resource` serves `/resource/{id}`. A composite key has to become a path,
and every spelling is a commitment:

- `/tasks/{workspace}/{id}` collides with the sub-resource paths a router also
  serves, and the collision depends on which resources an application happens to
  mount — so it is a conflict that appears at someone else's mount site.
- `/tasks/{a},{b}` invents an encoding, and then needs an escape for a key value
  containing a comma, and then a rule for what an unescaped one means.

Either becomes wire format on the first response, alongside the cursor payload
that would now carry a tuple and the cache-key factories in `client.gen.ts`,
which map a table and a row key to one array
([ADR-0028](0028-typescript-client.md)). [ADR-0025](0025-expansion-is-one-statement.md)
is this project's record of learning that the wire half is the expensive half,
and it learned it by paying.

So the question is not whether sqlb *could* carry a composite key. It is whether
a guess at how a row is spelled in a URL is worth freezing before anyone has
asked for it.

### What happens today, precisely

More gracefully than the evaluation assumed, and worth stating because it
changes how urgent this is. [`introspect`](../../introspect/) reads a composite
primary key, **reports** it — "composite primary key; the DSL declares at most
one primary key column (a composite unique index is the nearest thing)" — and
imports the table without one. The table is not lost, and the reason is named at
the moment of import rather than discovered when `Diff` comes back non-empty.

The failure is already actionable. What is missing is the stance behind it.

## Decision

**One column addresses a row. A table may have no primary key at all, and what
it may then do is bounded by that choice rather than by a refusal.**

| The table is | It needs | Because |
|---|---|---|
| queried through the builder only | **nothing** | `Stable` leaves a keyless model alone; offset paging works |
| cursor-paged, or expandable | **one column** | the cursor breaks ties on it; an expansion aggregates by it |
| exposed with read, update or delete | **one column** | `/resource/{id}` is one path segment |

The first row is the one worth noticing, because it is wider than the error
message suggests: a join table with a genuine composite key, queried through
`sqlb.Query` and never exposed, needs no surrogate at all. `UniqueIndex("a",
"b")` states the real key, Postgres enforces it, and nothing in sqlb objects.
The constraint bites on *addressing*, and only there.

**The composite key does not disappear; it changes job.** A table that must be
addressed carries both: a surrogate `UUIDv7` for identity, and the
`UniqueIndex` that keeps the real key real. That pairing is already in
[`example/tasks`](../../example/tasks/taskschema/schema.go), declared there for
a different reason — a composite foreign key needs a unique constraint covering
exactly the columns it references — which is evidence the shape is natural
rather than a workaround invented here.

### What is refused is a composite key *as the primary key*

Not composite keys. The distinction is load-bearing and has already confused one
reader, so it is stated rather than implied:
[ADR-0030](0030-declared-scope-is-required.md)'s answer to the `?expand` tenant
hole **depends** on a composite foreign key existing in the database — `tasks
(workspace_id, list_id)` against `lists (workspace_id, id)` — and this record
refuses none of it. The referenced side is a `UniqueIndex` the DSL writes; the
two-column `FOREIGN KEY` is a hand-written `migrate.Change`
([`references.md`](../schema/references.md)).

One is about how a row is named from outside. The other is about what the
database will let a row point at. They are consistent, and the consistency is
not obvious from the error message that started this.

## Consequences

**What this buys.** A row has exactly one spelling, and it is the same spelling
in the URL, in the cursor payload, in the `?expand` aggregation and in the
generated cache key. Each of those is a wire format under
[compatibility.md](../compatibility.md)'s freeze, and each would otherwise need
a tuple encoding of its own, decided independently, and permanent from its first
response.

**What this costs, and it is not free.**

*Adopters pay a migration this project does not.* Twenty-six columns across
fifteen tables in the one codebase that has counted. On a join table the DDL is
cheap and the data is not: add the column, backfill it, add the unique index on
the old pair, move the constraint. And `migrate.Diff` renders DDL only, so the
backfill is a hand-written goose file interleaved with generated ones — the
asterisk on "one declaration is the source of truth" that
[ADR-0014](0014-migrations-and-import.md) already carries.

*A table gains a column its domain has no use for.* A surrogate key on a join
table is a row identifier nothing in the business reads. That is a real
objection, it is the reason those schemas were written the other way, and no
amount of "it is conventional" answers it.

*The refusal is currently wider than its own justification.* The registry
refuses a second `PrimaryKey()` on every table, while the argument above only
reaches tables that are addressed, paged by cursor or expanded. That gap is
recognised here rather than defended, and the trigger for closing it is the
first item below.

## What would change our mind

- **A real schema wanting a composite key on a table that is not exposed, not
  expandable and not cursor-paged.** The refusal is then in the wrong place, and
  the fix is to move it: out of `schema.Validate`, into `rest.Resource`'s mount
  check and `keysetTerms`, where the assumption actually lives. This is the
  cheapest revision available and the one most likely to be right. It has not
  been made yet only because one rule in one place beats three rules in three
  places until somebody needs the third.
- **The surrogate-key migration stopping an adoption.** If a team walks away at
  this step rather than paying it, the constraint is being paid by the wrong
  party and the tuple work stops being theoretical.
- **A URL spelling that survives the freeze.** The `/tasks/{a},{b}` family is
  rejected above on the escaping problem. Somebody may have a better answer;
  this record's confidence in the REST half is High because of the cost of
  guessing, not because the design space is exhausted.
- **A second consumer needing tuple cursors anyway.** If row-value keyset
  comparison arrives for some other reason, the marginal cost of composite keys
  falls to the URL alone, and the trade above is recomputed with a smaller
  number on one side.

## Cost of change

**Widening is additive and mostly cheap.** Allowing a second `PrimaryKey()`,
tupling the cursor terms, the expansion key and the sort tiebreak breaks no
existing schema: every one of them declares one column today, and a one-element
tuple is the current behaviour. The work is real and the compatibility risk is
close to zero.

**Except at the wire, where it is permanent.** Whatever URL spelling a composite
key gets is frozen the first time a client builds a request against it, as is
the cursor payload's shape and the row-key array the generated clients hand to
TanStack. Those cannot be taken back by a minor version.

**Narrowing is impossible.** Once a composite-keyed row has a URL, there is no
route back to "address rows by one column" that does not break every deployed
client.

So the asymmetry runs the useful way: refusing now and allowing later costs the
tuple work; allowing now and refusing later costs a wire format nobody can
migrate off. This is the same shape as
[ADR-0017](0017-enums-as-text-and-check.md)'s reason for starting an enum from
text, and [ADR-0033](0033-array-columns.md)'s reason for refusing `Sortable` on
an array.

## Alternatives considered

**Support composite primary keys properly.** The honest version of this
alternative costs five mechanical changes and one irreversible guess, and it is
the guess that decides it. If the URL question had an obvious answer this record
would go the other way — the SQL is not the hard part and never was.

**Move the check to the sites that need it**, allowing composite keys on tables
that are never addressed. Genuinely close; close enough that it is the first
trigger in *What would change our mind* rather than an alternative that lost on
the merits. It loses today on nothing but simplicity, and it wins the moment one
real schema wants it.

**A `schema.Key("a", "b")` declaration**, separate from `PrimaryKey()`, stating
the tuple without claiming it can address a row. It renders the constraint and
nothing consumes it — which is [ADR-0024](0024-no-annotation-slot.md)'s argument
arriving again, and the same place [ADR-0026](0026-vectors-declare-their-index.md)
sent an opaque type escape hatch. `UniqueIndex` already renders the constraint,
so this buys a synonym.

**Leave those tables on sqlc.** The most under-rated option and the one to reach
for first in practice. [with-sqlc.md](../with-sqlc.md) already draws the line
where this needs it — sqlb owns the CRUD and list surface, sqlc owns what it
cannot express — and a join table with a natural composite key, queried by two
static queries and never exposed as a REST resource, is on sqlc's side of that
line by every criterion except habit. It does not answer the case where such a
table *must* be exposed, which is why it is an alternative rather than the
decision.

## Revisions

- 2026-07-29 — Written. The behaviour predates `v0.1.0`; what prompted the
  record was an evaluation asking whether the refusal was a decision or a gap,
  which it could not tell from the error message. Writing it surfaced that the
  registry refuses more than the argument justifies — the constraint is about
  addressing, and only tables that are addressed, expanded or cursor-paged need
  it — so the narrowing is recorded as the first thing that should change rather
  than as a defence of what is there.
