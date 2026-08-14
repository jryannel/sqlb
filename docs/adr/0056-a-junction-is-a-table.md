# ADR-0056: A junction is a table, and many-to-many is not a declaration

- **Status:** Working — the shape ships and is exercised; this records a position
  that was implicit rather than changing behaviour
- **Confidence:** High that a junction is a table; Medium that no sugar is worth
  it, which is the half an adopter is most likely to push back on
- **Decided:** 2026-08-13
- **Last reviewed:** 2026-08-13

## Context

Every ORM in this space has a many-to-many keyword. bun registers a junction
model and reads `bun:"m2m:order_to_items,join:Order=Item"`; ent, GORM and
Drizzle each have their own spelling. sqlb has none: a reference is a column,
and [ADR-0022](0022-references-declare-their-inverse.md) gives it a reverse
direction, so relations are one hop and forward-or-inverse.

The position was implicit and split across three documents that each said part
of it. [ADR-0034](0034-one-column-addresses-a-row.md) says a join table with a
genuine composite key, queried through the builder and never exposed, needs no
surrogate. `docs/best-practices.md` repeats it. `docs/schema/references.md`
mentions `ON DELETE RESTRICT` on both sides of one. None of them says what a
caller does to get from a post to its tags, which is the question somebody
arriving from bun actually asks — and `docs/comparisons.md` answered it wrongly,
listing many-to-many beside reverse expansion as things "ent has and sqlb does
not" when reverse expansion had shipped a fortnight earlier.

## Decision

**A junction table is an ordinary table with two references, and the far side is
reached by querying the junction.** There is no `m2m` tag and no plan for one.

```go
// which tags this post carries, one statement
sqlb.Query[PostTag]().Where(sqlb.F("post_id").Eq(id)).Expand("tag")

// which posts carry this tag, without reading the junction rows
tagged := sqlb.Query[PostTag]().Select(sqlb.F("post_id")).Where(sqlb.F("tag_id").Eq(id))
sqlb.Query[Post]().Where(sqlb.F("id").InQuery(tagged))
```

Both forms are exercised in `manytomany_test.go`, which exists so that the
recommendation has a check behind it rather than only a paragraph.

**Why no sugar.** A junction is almost never empty. It carries `added_at`, `role`,
`position`, `added_by` — the columns that are the reason the relationship is a
table rather than an array — and every one of them is filterable, sortable and
projectable the moment the junction is a model. An `m2m` tag hides the table
that holds them, and then needs a way to say "but let me at the junction row
after all", which is the table it just hid. Modelling it as what it is costs one
struct and buys the whole grammar.

The second reason is [ADR-0006](0006-capabilities-are-opt-in.md). A declared
`m2m` traversal is a capability nobody wrote down: it would make the far table
reachable through the near one, and the scope obligations of
[ADR-0030](0030-declared-scope-is-required.md) would apply to a path no
declaration names.

**What this does not give.** A post cannot reach its tags in one hop. `Expand`
names a relation of the model being queried, expansion is one level
([ADR-0025](0025-expansion-is-one-statement.md)), and `Expand("tagged.tag")` is
an error. Expanding the junction from the post gives the junction rows, which
carry the far-side foreign key, and the second hop is a second query or the
subquery form above.

## Consequences

**Buys.**

- No new vocabulary, and no second way for a relation to exist.
- The junction's own columns are first-class: `?added_at=gte.…` works on it
  because it is a resource like any other.
- Both directions come from one declaration, since the junction references both
  sides.

**Costs.**

- A caller arriving from bun or ent writes a struct where they expected a tag,
  and reads a paragraph to learn why.
- A post's tags in one response is two queries, or one query from the junction
  whose rows are shaped as junction rows rather than as tags.
- Nothing stops a schema declaring a junction and never mounting it, which is
  usually right and is never checked.

## What would change our mind

- If nested expansion (`?expand=tagged.tag`) is built, the second hop stops
  being a second query and this record's "what it does not give" section is the
  thing to revisit — not the decision itself, which stays.
- If several adopters model a junction with no columns of its own beyond the two
  keys, the argument that a junction is always more than a link is weaker than
  stated here, and sugar over the two-hop is worth costing.
- If the scope obligation across a junction turns out to be commonly forgotten —
  a mounted junction whose far side is confined and whose own rows are not —
  that is an argument for a declaration precisely so the check has something to
  attach to.

## Cost of change

Cheap to add, impossible to remove. An `m2m` declaration would be additive: the
junction stays a table and the tag becomes a second way to reach it. Removing
one after clients depend on the generated field is a wire break, which is why
this record says no now rather than later.

## Open questions I had to answer myself

- **Whether `docs/comparisons.md` was stale or stating a narrower claim.** Stale.
  It listed reverse expansion as absent after ADR-0022 shipped it, so the
  many-to-many claim beside it could not be trusted either; both are corrected
  with this record.
- **Whether the junction needs a surrogate key.** No, and ADR-0034 already says
  so — the composite is the real key and `UniqueIndex("a","b")` states it. A
  surrogate is needed only if the junction row is addressed, which is a decision
  about the REST mount rather than about the relationship.
- **Whether `InQuery` over the junction should be the recommended primary form.**
  Not primary. It is the right answer when the caller wants the far rows and not
  the junction rows, and querying the junction is the right answer when they
  want either the junction's own columns or both sides. Both are shown rather
  than ranked.

## Revisions

- 2026-08-13 — Written.
