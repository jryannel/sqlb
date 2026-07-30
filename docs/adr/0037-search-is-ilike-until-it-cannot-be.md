# ADR-0037: `?search` is ILIKE, and a `tsvector` column is not in 1.0

- **Status:** Working as a decision, unbuilt as a feature — `?search` ships and
  is ILIKE; full-text search is refused for 1.0
- **Confidence:** High that ILIKE is the right default and that the refusal is the
  right pre-1.0 answer; Low on the shape full text should take, which is why no
  shape is proposed here
- **Decided:** 2026-07-29
- **Last reviewed:** 2026-07-29

## Context

`?search=ada` fans out across every `Searchable` column as a disjunction of
`ILIKE '%ada%'`, with metacharacters escaped. That is what ships, and it is not
full-text search.

Both adoption evaluations name `tsvector` as a gap. A `tsvector` column today
cannot be declared, cannot be rendered by `migrate`, and makes `introspect`
refuse the table it is on — the position arrays were in before
[ADR-0033](0033-array-columns.md): not "adopts with a gap" but "cannot be
adopted". That is the argument that got arrays built, and the reason it does not
get full text built is that the two are not the same size.

## Decision

**`?search` stays ILIKE. A `tsvector` column is not in 1.0.**

**ILIKE is the right default, not merely the cheap one.** A substring match is
what a filter box does: no index to be correct, no configuration to be
predictable, no dictionary to explain itself. A user who types `ada` gets rows
containing `ada`, including `Nowlada` — occasionally wrong and never surprising.

Full text is a different operation wearing a similar name. It stems, drops stop
words, depends on a text-search configuration that is a property of the
*deployment* rather than the column, and ranks. `running` matches `run`; `the`
matches nothing. Those are better answers for prose and worse ones for
identifiers, and which a given `Searchable` column wants is not something the
schema says. So the honest position is not that ILIKE is a placeholder — they are
two capabilities, and sqlb has one of them.

**Why the missing one is not in 1.0:**

- **It is not one feature.** Arrays needed a type, a DDL arm, an `introspect`
  mapping and a codec. Full text needs all of those plus a GIN requirement, a
  text-search configuration the schema must name, a decision about
  generated-versus-trigger-maintained columns, a query operator, a ranking
  function, and a position on whether `?search` switches silently — the last of
  which is a wire-format question.
- **The generated-column problem is the sharp one.** A `tsvector` is almost
  always maintained by the database, and `migrate.Diff` renders neither generated
  columns nor triggers. A feature that stopped at the column type would declare
  something the migration layer cannot maintain, which is worse than not
  declaring it: it looks complete.
- **Nobody has asked for a specific one.** `?search=` with stemming, a separate
  `?q=` with ranking, and a `Filterable` `@@` operator are three different
  features, and the census does not distinguish them.

**What a schema with a `tsvector` column does today:** the column stays out of
the registry and `introspect` *says so* rather than dropping it; the query is
written by hand through `Raw` or sqlc beside sqlb; and `?search` still works over
the text columns the schema does declare. The table cannot be fully schema-first,
and the module adopts with an asterisk rather than not at all — a weaker
statement than the arrays case, which is why it ranks below it.

## Consequences

**A gap named by both evaluations goes into 1.0 open, deliberately.** 1.0 is
about finding the mistakes that are expensive to keep, not about completeness,
and an absent feature is additive — adding full text in 1.1 breaks nobody.

**`Searchable` keeps its current meaning, now written down.** A schema that
wanted full text and got ILIKE will get the answers ILIKE gives, and nobody
should have to discover that from behaviour.

**The refusal is checkable rather than silent** — `introspect` already reports a
type it cannot map.

## What would change our mind

- **A port that needs it to complete** — not "would have liked it" but a module
  that cannot be adopted because its search box is a `tsvector` query. That is
  the arrays argument.
- **A specific query, named** — "we need `?search` to stem" or "we need results
  ranked". Each is smaller than the general feature, and either could ship alone.
- **`migrate` learning generated columns** — the dependency, and useful on its
  own; one evaluation counted ten, none of them full text.
- **ILIKE becoming a performance problem before an expressiveness one.** A
  leading-wildcard `ILIKE` cannot use a btree index, and the answer there is a
  trigram index — a different, smaller feature this record should not be read as
  refusing.

## Cost of change

**Free today, and the freeze does not bind it.** No schema declares a `tsvector`.
A new operator is additive to the filter grammar by compatibility.md's own terms,
and a new column type is additive to the DSL.

**The one expensive direction is changing what `?search` means.** If full text
later took over the existing parameter, every deployed search box would change
behaviour without its request changing. So whatever ships will be a new spelling,
not a redefinition — worth recording now while it is free.

Two alternatives are worth naming. Building it before 1.0 loses on the
generated-column dependency. And declaring `tsvector` as an opaque passthrough is
`schema.Opaque` again, rejected in [ADR-0026](0026-vectors-declare-their-index.md)
and [ADR-0033](0033-array-columns.md) for the same reason — the slot is the small
half of the feature. Three records reaching the same place is itself evidence.

## Revisions

- 2026-07-29 — Written as a decision rather than a design, for
  [the road to 1.0](../release-1.0.md)'s Phase 1. The reason it is a refusal
  rather than a plan is the generated-column dependency, which was not obvious
  until the two features were written side by side.
- 2026-07-30 — Condensed.
