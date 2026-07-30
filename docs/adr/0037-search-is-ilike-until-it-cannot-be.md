# ADR-0037: `?search` is ILIKE, and a `tsvector` column is not in 1.0

- **Status:** Working as a decision, unbuilt as a feature — `?search` ships and
  is ILIKE; full-text search is refused for 1.0 and this says why and what would
  change it
- **Confidence:** High that ILIKE is the right default and that the refusal is
  the right pre-1.0 answer. Low on the shape full text should take when it is
  built, which is why no shape is proposed here
- **Decided:** 2026-07-29
- **Last reviewed:** 2026-07-29

## Context

`?search=ada` fans out across every `Searchable` column as a disjunction of
`ILIKE '%ada%'`, with the term's metacharacters escaped. That is what ships, and
it is not full-text search.

Both adoption evaluations name `tsvector` as a gap. The first counts it among
the constructs that keep tables outside the registry — alongside arrays, since
closed, and composite primary keys, still open — and puts it in the list of
things that would have to change before a full adoption is discussable. A
`tsvector` column today cannot be declared, cannot be rendered by `migrate`, and
makes `introspect` refuse the table it is on, so a module carrying one is in the
position arrays were in before [ADR-0033](0033-array-columns.md): not "adopts
with a gap" but "cannot be adopted".

That is the same argument that got arrays built, and the reason it does not get
full text built is that the two are not the same size.

## Decision

**`?search` stays ILIKE. A `tsvector` column is not in 1.0.**

### Why ILIKE is the right *default*, not merely the cheap one

A substring match is what a filter box does. It needs no index to be correct, no
configuration to be predictable, and no dictionary to explain itself: a user who
types `ada` gets rows containing `ada`, including `Nowlada` and `ADA-2`, which is
occasionally wrong and never surprising.

Full text is a different operation wearing a similar name. It stems, it drops
stop words, it depends on a text-search configuration that is a property of the
*deployment* rather than the column, and it ranks. A user who types `running`
matches `run`; a user who types `the` matches nothing. Those are better answers
for a search box over prose and worse ones for a filter over identifiers, and
which of the two a given `Searchable` column wants is not something the schema
currently says.

So the honest position is not "ILIKE is a placeholder for full text". It is that
they are two capabilities, and sqlb has one of them.

### Why the missing one is not in 1.0

**It is not one feature.** Arrays needed a type, a DDL arm, an `introspect`
mapping and a codec. Full text needs a type, a DDL arm, an `introspect` mapping,
a GIN index requirement, a *text-search configuration* the schema has to name, a
decision about generated-versus-trigger-maintained columns, a query operator, a
ranking function, and a position on whether `?search` switches to it silently.
The last of those is a wire-format question ([ADR-0036](0036-the-wire-is-the-column-name.md)),
and the two before it are ones this project has no evidence to answer.

**The generated-column problem is the sharp one.** A `tsvector` is almost always
maintained by the database — a `GENERATED ALWAYS AS (to_tsvector(...)) STORED`
column, or a trigger. `migrate.Diff` renders neither: generated columns are
already on the list of constructs it does not express, and a trigger is further
outside it still. So a `tsvector` feature that stopped at the column type would
declare something the migration layer could not maintain, which is worse than
not declaring it — it is the shape of a feature without the substance.

**Nobody has asked for a specific one.** Both evaluations name the gap; neither
names the query it wants to write. `?search=` with stemming, a separate
`?q=` with ranking, and a `Filterable` `@@` operator are three different features
and the census does not distinguish them.

### What a schema with a `tsvector` column does today

The same thing it does with any construct the DSL cannot express, and this is
the part that makes the refusal survivable rather than fatal:

- **The column stays out of the registry**, and `introspect` says so rather than
  dropping it — the report names it, exactly as it names a generated column or a
  composite foreign key.
- **The query is written by hand**, through `Raw` or through sqlc beside sqlb,
  which is the coexistence [with-sqlc.md](../with-sqlc.md) is about.
- **`?search` still works** over the text columns the schema does declare.

What this costs is that the table cannot be *fully* schema-first, and the module
containing it adopts with an asterisk rather than not at all. That is a weaker
statement than the arrays case, and it is why this ranks below it.

## Consequences

**A gap named by both evaluations goes into 1.0 open, deliberately.** That is
worth being plain about: [the road to 1.0](../release-1.0.md) says 1.0 is about
finding the mistakes that are expensive to keep, not about completeness, and an
absent feature is additive. Adding full text in 1.1 breaks nobody.

**`Searchable` keeps its current meaning, and that meaning is now written down.**
It is a substring fan-out. A schema that wanted full text and got ILIKE will get
the answers ILIKE gives, and the docs should not let anyone discover that from
behaviour.

**The refusal is checkable rather than silent.** `introspect` already reports a
type it cannot map; nothing needs building for a `tsvector` column to be
*noticed*.

## What would change our mind

- **A port that needs it to complete.** Not "would have liked it" — a module
  that cannot be adopted because the search box is a `tsvector` query and there
  is nowhere to put it. That is the arrays argument, and it would move this the
  way it moved that.
- **A specific query, named.** The strongest version of this ask is not "support
  tsvector", it is "we need `?search` to stem" or "we need results ranked". Each
  is a smaller feature than the general one, and either could ship without the
  other.
- **`migrate` learning generated columns.** That is the dependency, and it is
  useful on its own — ten of them were counted in one evaluation, none of them
  full text. If it lands, the expensive half of this is already paid.
- **If ILIKE turns out to be a performance problem before it is an expressiveness
  one.** A leading-wildcard `ILIKE` cannot use a btree index, and the answer to
  that is a trigram index rather than full text — a different, smaller feature
  that this record should not be read as refusing.

## Cost of change

**Free today, and the freeze does not bind it.** No schema declares a
`tsvector`, so nothing has to be reversed. The 1.0 wire freeze does not close
this door either: a new operator is additive to the filter grammar by
`compatibility.md`'s own terms, and a new column type is additive to the DSL.

**The one expensive direction is changing what `?search` means.** If full text
later took over the existing parameter, every deployed client's search box would
change behaviour without its request changing — which is precisely the kind of
break the grammar is frozen to prevent. So whatever ships will be a new
spelling, not a redefinition of this one, and that constraint is worth recording
now while it is free.

## Alternatives considered

**Build it before 1.0 because both evaluations named it.** The strongest case
against this record, and it loses on the generated-column dependency: a
`tsvector` sqlb can declare but not maintain is a worse offer than one it does
not declare, because the first looks complete.

**Declare `tsvector` as an opaque passthrough type** — render the DDL, no Go
type, no capability. This is `schema.Opaque` again, considered and rejected in
both [ADR-0026](0026-vectors-declare-their-index.md) and
[ADR-0033](0033-array-columns.md) for the same reason: the slot is the small half
of the feature, and a column with no Go type cannot be scanned, filtered or
typed on the wire. Three records reaching the same place is itself evidence.

**Make `?search` use `to_tsvector(...) @@ plainto_tsquery(...)` without a stored
column.** Expressible today with `Raw`, and it is a sequential scan with a
function call per row — the failure mode ADR-0026 exists to name, arriving
without even a declaration to hang the warning off.

**Say `Searchable` is deprecated in favour of a future full-text capability.**
Deprecating a shipped, working, well-scoped feature in favour of an unbuilt one
is how a project acquires two half-answers. ILIKE search is correct for what it
does.

## Revisions

- 2026-07-29 — Written as a decision rather than a design, for
  [the road to 1.0](../release-1.0.md)'s Phase 1: every ADR is Working or
  explicitly out of scope with the reason, and this was one of four decisions
  with no record at all. The reason it is a refusal rather than a plan is the
  generated-column dependency, which was not obvious until the two features were
  written side by side.
