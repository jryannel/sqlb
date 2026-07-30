# ADR-0038: A collection has one path, and the parent is a filter

- **Status:** Working as a decision — flat collection paths are what ships, and
  this records why rather than changing it
- **Confidence:** High that a flat path plus a filter is the right primary form;
  Medium that refusing the nested alias is right, which is the half a real
  adopter is most likely to push back on
- **Decided:** 2026-07-29
- **Last reviewed:** 2026-07-29

## Context

`schema.REST{Path: "/tasks"}` mounts one collection at one path. A task belonging
to a list is fetched as `GET /tasks?list_id=eq.<id>`, and there is no way to
declare `GET /lists/{id}/tasks`.

The first adoption evaluation names this — the codebase it examined uses nested
collections, so every one changes shape — and lists it as cost rather than
blocker, because nothing is unreachable: the rows come back, from a different
URL. It is in the pre-freeze set for a different reason. **A route shape is wire
format.** Nested paths arriving after 1.0 would be an addition, which is fine;
but if the *flat* form were ever going to move, it would have to move before the
freeze, and nobody had written down that it is not going to.

## Decision

**One collection, one path, and the parent relationship is a filter.**
`GET /tasks?list_id=eq.<id>` is the form; `GET /lists/{id}/tasks` is not offered.

- **It composes, and a nested path does not.** `?list_id=eq.<id>` is one
  predicate in a grammar that already has sorting, projection, search, paging,
  expansion and disjunction. A nested path is a second entry point that grows
  each of those separately, or accepts them and means something subtly different
  — and a caller cannot tell from the URL whether `?sort=` sorts the children.
- **It is already the answer to a question the schema asks.** A capped expansion
  says `has_more`, and the documented way to get the rest is the child's own
  endpoint filtered by the foreign key
  ([ADR-0022](0022-references-declare-their-inverse.md)) — this exact request.
- **It does not multiply.** A task belongs to a list *and* a workspace *and* an
  assignee. Nested paths make the reader ask which is the canonical parent; a
  filter has no canonical parent, which is correct, because there isn't one.
- **Capability checking stays in one place.** `list_id` is reachable because it
  declared `Filterable`. On a nested path, exposure becomes a property of the
  route rather than the column, and
  [ADR-0006](0006-capabilities-are-opt-in.md) is that capabilities live on the
  column.

**What is given up, plainly.** The URL no longer reads as a hierarchy, which is a
real loss for a human reading an access log. **A 404 for a missing parent becomes
an empty page** — the sharpest consequence and the one most likely to surface as
a bug report, because a client rendering "no tasks yet" for a deleted list is
showing a plausible screen about a row that is gone. And every nested URL in an
adopting codebase changes: mechanical, compile-checked by the generated client,
not free.

**The nested form is refused rather than added as an alias.** An alias is small
to build and is a second spelling of one request — the thing this project spends
its refusals on, as with `contains` in
[ADR-0033](0033-array-columns.md) and field naming in
[ADR-0036](0036-the-wire-is-the-column-name.md). The cost is not the code; it is
that every generated client, `--help`, OpenAPI operation and cache key then has
to choose, and readers have to learn that the choice does not matter. There is
also a question an alias cannot answer: does the nested route enforce that the
parent exists? If yes it is not an alias; if no, the extra segment buys nothing.

## Consequences

**`Options.Path` stays a single collection path**, and the OpenAPI document has
one operation per resource operation rather than one per relationship.

**The missing-parent 404 is a documented gap rather than a hidden one.** A caller
that must distinguish "no children" from "no such parent" fetches the parent —
one more request, and what a nested route would have done internally.

**A hand-written nested route remains available and is a fine answer.** A project
that wants `GET /lists/{id}/tasks` can write it, call `filter.Parse` and
`filter.Apply` like any other handler, and add the parent check it wanted. That
is [ADR-0007](0007-generated-rest-handlers.md)'s seam working as intended.

## What would change our mind

- A port finds the flat form breaks a client it cannot change — a different claim
  from "the URLs changed". A mobile app in the field with hardcoded nested paths
  and a slow release cycle is the realistic version.
- The missing-parent 404 causes a real defect. That argues for the nested form as
  a genuinely different endpoint *with* the parent check, and it should be
  designed as such.
- Nested `?expand` lands — the parent/child relationship gains a second
  representation anyway, and the ordering of the two features is worth
  reconsidering together.
- Someone wants it purely for URL aesthetics. That is a real preference and not
  evidence; it does not survive the cost of two spellings.

One alternative is worth naming for what it would cost: deriving nested routes
from declared inverses. Elegant, and it would make adding `InverseExpandable`
silently add a route — exactly the coupling ADR-0006 exists to prevent.

## Cost of change

**Adding the nested form later is additive and cheap** — no request that works
today changes meaning, and the flat path stays canonical. That asymmetry is why
this is a refusal rather than a design: waiting costs nothing, and building it
now would freeze a shape nobody has used. **Removing the flat form later would be
expensive and is not on the table**, since every generated client, cache key and
CLI command is built on it. So the freeze binds only one direction.

## Revisions

- 2026-07-29 — Written for [the road to 1.0](../release-1.0.md)'s Phase 1, as one
  of four decisions with no record. The behaviour is unchanged; what is new is
  that the refusal is deliberate and its one real cost — the missing-parent 404 —
  is written down rather than discovered.
- 2026-07-30 — Condensed.
