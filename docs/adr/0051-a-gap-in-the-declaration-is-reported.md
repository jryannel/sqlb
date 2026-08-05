# ADR-0051: A gap below the declaration is reported, not silent

- **Status:** Working
- **Confidence:** Medium
- **Decided:** 2026-08-05
- **Last reviewed:** 2026-08-05

## Context

The schema is meant to be the whole truth about a table: migrations, models, REST
handlers, the OpenAPI document and four clients are all derived from it. Below it
sit three layers that can each say something the declaration cannot — the mount,
the migration, and the caller. That is by design; the layers exist because not
everything belongs in a declaration.

Five reports in two weeks turned out to be the same shape, and the shape is not
"the declaration is missing a field".

| | The layer said it | The declaration could not | What reported the gap |
|---|---|---|---|
| [#148](https://github.com/jryannel/sqlb/issues/148) | `rest.Options` narrowed a mount's columns | one model per table, one `Expose` | nothing |
| [#151](https://github.com/jryannel/sqlb/issues/151) | `rest.Options.MaxOffset` bounded a scan | no field on `schema.REST` | nothing |
| [#153](https://github.com/jryannel/sqlb/issues/153) | a `BeforeQuery` hook scoped a read | — | nothing; `SQL()` rendered the unscoped statement and `Explain` planned it |
| [#154](https://github.com/jryannel/sqlb/issues/154) | a migration deferred a constraint | no deferrability on `Unique` | nothing; the drift gate was green |
| [#155](https://github.com/jryannel/sqlb/issues/155) | `sqlb.F` predicated on a hidden column | the facade omitted it | the compiler, which is the one case that *was* loud |

The missing spelling is the cheap half, and each of these was individually minor
— every one had a workaround, and the reporter said so. What they share is the
expensive half: **nothing said the gap was there.** A schema-first resource
silently took a package default two orders of magnitude past what its row count
justified. A hand-altered constraint passed `sqlb check` because the declaration
and the database were blind to the same property, so the fixpoint held for the
wrong reason. An inspection point rendered a statement that was not the one the
database would run, under documentation offering it as the way to confirm that
scoping works.

That last pair is the general form. A tool reporting *no difference* is making a
claim, and a tool that cannot see a property reports no difference about it
whether or not one exists. ADR-0014 named silent dropping as the failure mode to
watch for hardest, and ADR-0016 requires a guard to have failed on purpose —
both are about the same thing one level down: a check that cannot see a property
is not a weak check, it is a check that answers a question it was never asked.

## Decision

When a layer below the declaration can express something the declaration cannot,
close the gap where that is cheap — and where it is not, **make the gap visible
rather than leaving it inferable**.

Visible has three concrete forms, in order of preference:

1. **A refusal at the boundary.** The declaration gains the word, and something
   fails when it is absent or wrong. `MaxOffset` on `schema.REST`, and a
   negative ceiling refused rather than resolved to the loosest available bound.
2. **A report from the tool that reads the database.** `introspect` reads
   deferrability for every constraint kind and lists as a `Skip` the ones the DSL
   cannot declare it on, with the definition attached. The registry does not
   describe the database, and saying so is the entire value — the alternative is
   a green gate over a divergence nobody can see.
3. **A sentence where the reader is standing.** `Builder.SQL`'s own doc comment
   says what it does not render, beside the method, rather than in a topic
   pointer four screens below.

What this rules out is the fourth option, which is what each of these had: the
behaviour is correct, the workaround exists, and the only thing missing is that
the two facts are documented in different files from each other.

The corollary for the schema is narrower than "declare everything". A declaration
that cannot say a thing is acceptable; a *tool* that cannot see it is not, when
that tool's job is to report differences. Deferrability is declarable on `UNIQUE`
alone and read everywhere.

## Consequences

**Buys.** The gates mean what they claim. A drift check that reports nothing now
means the database matches the declaration in every property either side can
read, rather than in every property both happen to model. An inspection point
shows the statement that runs. And a declaration that cannot express something
says so at the moment it is read, which is what turns a workaround from a thing
somebody discovered by experiment into a thing the tooling stated.

**Costs.** Reporting a gap makes previously-green things red. Adopting a database
that defers a foreign key now produces report entries where it produced none —
that is the change working, and it is still a new obligation for a consumer whose
schema had been quietly fine. Each report is a decision someone now has to make,
and the failure mode of this ADR taken too far is a report nobody reads, which is
the state a `//nolint` comment describes.

**And the pull is toward declaring everything**, which is not the decision. The
DSL stays narrow on purpose — `schema.Exclusion` takes hand-written SQL,
`Touches` is unenforced, `rest.Options.Columns` is a mount-time argument and
ADR-0050 says plainly that it is the weaker answer. This record does not reverse
any of those. It says that where the weaker answer is taken, the gap it leaves is
reported rather than left to be found.

## What would change our mind

- **The reports become noise.** If an ordinary adoption produces a dozen entries
  a consumer routinely ignores, the reporting has stopped distinguishing "you
  must decide about this" from "here is a fact", and the answer is a severity on
  `Skip` rather than more entries.
- **A gap is closed by declaring it and the declaration goes unused.** If
  `Unique.Deferrable` is set by nobody but the importer, the case for declaring
  rather than only reporting was weaker than it looked, and the next one of these
  should stop at the report.
- **Something below the declaration turns out to be the right home.** The mount
  really is where per-resource reachability belongs (ADR-0050). If a second
  property lands there and reads well, "the declaration is the whole truth" is
  the premise to revisit rather than the gaps.

## Cost of change

**Reversing the reporting is free and immediate** — deleting a `rep.add` call
restores the old silence, which is exactly what makes this worth writing down:
the property costs nothing to lose and is invisible when it goes.

**Reversing a closed gap is a deprecation.** `MaxOffset`, `Unique.Deferrable`,
`Field.LookupKey` and `Builder.Resolved` are public surface under
[compatibility.md](../compatibility.md), and each is additive — a schema that
sets none of them generates what it generated before.

## Revisions

- 2026-08-05 — Written, against
  [#151](https://github.com/jryannel/sqlb/issues/151),
  [#153](https://github.com/jryannel/sqlb/issues/153),
  [#154](https://github.com/jryannel/sqlb/issues/154) and
  [#155](https://github.com/jryannel/sqlb/issues/155), with
  [#148](https://github.com/jryannel/sqlb/issues/148) as the case that named the
  pattern one issue earlier. The record exists because the fifth instance is
  where a shape stops being a coincidence, and because four of the five were
  filed as minor — which is the reason to write down what makes them not.
