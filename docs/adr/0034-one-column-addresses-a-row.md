# ADR-0034: A row is addressed by one column, and a composite key becomes a unique index

- **Status:** Working — `schema.Validate` has refused a second `PrimaryKey()`
  since before `v0.1.0`, and `rest.Resource` refuses to mount a read, update or
  delete without one. What is new here is the record, not the behaviour
- **Confidence:** High on the REST half, where the constraint is a wire format
  decision and a guess is expensive; Medium on the rest, since the tuple work
  elsewhere is mechanical and merely unbuilt
- **Decided:** 2026-07-29
- **Last reviewed:** 2026-07-30 (the first trigger fired; see Revisions)

## Context

The refusal predates `v0.1.0` and went unexplained: the error named the
workaround and not the reason, which is the one thing
[ADR-0011](0011-actionable-errors.md) asks an error not to do.

An outside evaluation asked for "composite primary keys, **or** a documented
'surrogate key required' stance" — a polite way of saying it could not tell which
it was looking at, and therefore whether the cost it was about to pay was
permanent. It is a real cost: twenty-six composite primary keys across fifteen
files, on tables chosen that way deliberately.

**Six places assume one column, and five of them could stop.** The sort tiebreak,
the cursor, the expansion aggregation, a `Ref`'s default target and the lint all
have mechanical answers nobody has written — a tuple cursor is a row-value
comparison Postgres plans as well as the scalar form.

**The sixth has no mechanical answer, and it is the whole decision: the URL.**
`/tasks/{workspace}/{id}` collides with sub-resource paths, and which collision
depends on what an application mounts — a conflict appearing at someone else's
mount site. `/tasks/{a},{b}` invents an encoding, then needs an escape for a key
containing a comma, then a rule for what an unescaped one means. Either becomes
wire format on the first response, alongside a tuple cursor payload and the cache
keys in `client.gen.ts`. So the question is not whether sqlb *could* carry a
composite key — it is whether a guess at how a row is spelled in a URL is worth
freezing before anyone has asked.

**What happens today is more graceful than assumed.** `introspect` reads a
composite primary key, *reports* it, and imports the table without one. The
failure is already actionable; what was missing is the stance behind it.

## Decision

**One column addresses a row. A table may have no primary key at all, and what it
may then do is bounded by that choice rather than by a refusal.**

| The table is | It needs | Because |
|---|---|---|
| queried through the builder only | **nothing** | `Stable` leaves a keyless model alone; offset paging works |
| cursor-paged, or expandable | **one column** | the cursor breaks ties on it; an expansion aggregates by it |
| exposed with read, update or delete | **one column** | `/resource/{id}` is one path segment |

The first row is wider than the error message suggests: a join table with a
genuine composite key, queried through `sqlb.Query` and never exposed, needs no
surrogate. `UniqueIndex("a", "b")` states the real key and Postgres enforces it.
The constraint bites on *addressing*, and only there.

**The composite key does not disappear; it changes job.** A table that must be
addressed carries both — a surrogate `UUIDv7` for identity and the `UniqueIndex`
that keeps the real key real. `example/tasks` already pairs them for a different
reason, which is evidence the shape is natural rather than invented here.

**What is refused is a composite key *as the primary key*, not composite keys.**
[ADR-0030](0030-declared-scope-is-required.md)'s answer to the `?expand` tenant
hole *depends* on a composite foreign key existing in the database, and this
record refuses none of it. One is about how a row is named from outside; the
other is about what the database will let a row point at.

## Consequences

**Buys.** A row has exactly one spelling — the same in the URL, the cursor
payload, the `?expand` aggregation and the generated cache key. Each is a wire
format under [compatibility.md](../compatibility.md)'s freeze, and each would
otherwise need a tuple encoding of its own, decided independently and permanent
from its first response.

**Costs.** *Adopters pay a migration this project does not* — twenty-six columns
across fifteen tables in the one codebase that counted. The DDL is cheap and the
data is not, and the backfill is a hand-written goose file interleaved with
generated ones.

*A table gains a column its domain has no use for.* A surrogate on a join table
is an identifier nothing in the business reads. That is a real objection and the
reason those schemas were written the other way.

*The refusal is wider than its own justification.* The registry refuses a second
`PrimaryKey()` everywhere, while the argument only reaches tables that are
addressed, paged by cursor or expanded.

## What would change our mind

- **A real schema wanting a composite key on a table that is not exposed,
  expandable or cursor-paged** — the refusal is in the wrong place, and the fix
  is to move it into `rest.Resource`'s mount check and `keysetTerms`.

  **This has happened, and it did less damage than expected.** The multi-app
  port's `llmcatalog` has exactly that table and reports it "didn't block me",
  because `OnConflictUpdate` takes a column list and the upsert names the
  composite conflict target itself. So the table loses the ability to *declare*
  the key it has, not the ability to work. The narrowing stays the right first
  change and stops being urgent; it is additive and can land after 1.0. What
  should not wait is saying so in the schema documentation, since today a reader
  finds out one affordance at a time, at the call site.
- **The surrogate-key migration stopping an adoption** — the constraint is being
  paid by the wrong party, and the tuple work stops being theoretical.
- **A URL spelling that survives the freeze.** Confidence in the REST half is
  High because of the cost of guessing, not because the design space is
  exhausted.
- **A second consumer needing tuple cursors anyway** — the marginal cost of
  composite keys falls to the URL alone.

## Cost of change

**Widening is additive and mostly cheap**: every schema declares one column
today, and a one-element tuple is the current behaviour. **Except at the wire,
where it is permanent** — a URL spelling is frozen the first time a client builds
a request against it, as is the cursor payload and the row-key array.
**Narrowing is impossible**: once a composite-keyed row has a URL, there is no
route back that does not break every deployed client.

So the asymmetry runs the useful way — the same shape as
[ADR-0017](0017-enums-as-text-and-check.md)'s reason for starting an enum from
text.

One alternative is under-rated and worth reaching for first in practice: **leave
those tables on sqlc.** [with-sqlc.md](../with-sqlc.md) already draws the line
where this needs it, and a join table with a natural composite key, queried by
two static queries and never exposed, is on sqlc's side of it by every criterion
except habit.

## Revisions

- 2026-07-29 — Written. Writing it surfaced that the registry refuses more than
  the argument justifies, so the narrowing is recorded as the first thing that
  should change rather than as a defence of what is there.
- 2026-07-29 — `schema.Validate`'s message rewritten to carry the reason and both
  halves of the fix. A record that names a bad error message and leaves it in
  place is a note, not a decision.
- 2026-07-30 — The first trigger fired and the record survives it in better shape:
  the constraint costs a *declaration* rather than a capability, so
  `release-1.0.md` moves this row from blocker to non-blocking.
- 2026-07-30 — Condensed.
