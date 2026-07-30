# ADR-0038: A collection has one path, and the parent is a filter

- **Status:** Working as a decision — flat collection paths are what ships, and
  this records why rather than changing it
- **Confidence:** High that a flat path plus a filter is the right primary form.
  Medium that refusing the nested alias is right, which is the half a real
  adopter is most likely to push back on
- **Decided:** 2026-07-29
- **Last reviewed:** 2026-07-29

## Context

`schema.REST{Path: "/tasks"}` mounts one collection at one path. A task
belonging to a list is fetched as `GET /tasks?list_id=eq.<id>`, and there is no
way to declare `GET /lists/{id}/tasks`.

The first adoption evaluation names this in its wire-format table — the codebase
it examined uses nested collections, so every one of them changes shape — and it
lists the difference as cost rather than as a blocker. It is not in either
evaluation's blocker set and it is not in [the road to 1.0](../release-1.0.md)'s
either, because nothing is unreachable: the rows come back, from a different URL.

It is in the pre-freeze set for a different reason. **A route shape is wire
format.** If nested paths arrive after 1.0 they arrive as an addition, which is
fine — but if the *flat* form were ever going to move, it would have to move
before the freeze, and nobody had written down that it is not going to.

## Decision

**One collection, one path, and the parent relationship is expressed as a
filter.** `GET /tasks?list_id=eq.<id>` is the form, and
`GET /lists/{id}/tasks` is not offered.

### Why the filter is the better primary form

**It composes, and a nested path does not.** `?list_id=eq.<id>` is one predicate
in a grammar that already has sorting, projection, search, paging, expansion and
disjunction. Everything that works on `/tasks` works on that request unchanged.
A nested path is a second entry point that has to grow each of those separately,
or accept them and mean something subtly different — and "subtly different" here
means a caller cannot tell from the URL whether `?sort=` on a nested collection
sorts the children or is ignored.

**It is already the answer to a question the schema asks.** A capped expansion
returns twenty children and says `has_more`; the documented way to get the rest
is to follow the child's own endpoint filtered by the foreign key
([ADR-0022](0022-references-declare-their-inverse.md)). That is this exact
request. If the nested path existed, the expansion's overflow would have two
spellings and the docs would have to pick one anyway.

**It does not multiply.** A task belongs to a list *and* a workspace *and* an
assignee. Nested paths make the reader ask which of those is the canonical
parent, and a schema with two plausible answers gets two route trees. A filter
has no canonical parent, which is correct: there isn't one.

**Capability checking stays in one place.** `list_id` is reachable because it
declared `Filterable`. On a nested path the parent segment is not a filter
parameter, so whether the relationship is exposed becomes a property of the
route rather than of the column — and [ADR-0006](0006-capabilities-are-opt-in.md)
is that capabilities live on the column.

### What is given up, plainly

**The URL no longer reads as a hierarchy.** `GET /lists/{id}/tasks` says "the
tasks of this list" in a way `GET /tasks?list_id=eq.<id>` does not, and for a
human reading an access log or a route table that is a real loss.

**A 404 for a missing parent becomes an empty page.** Asking for the children of
a list that does not exist returns `{items: [], …}` rather than 404, because the
filter matched nothing and "nothing matched" is not an error. A nested path could
check the parent. This is the sharpest consequence and the one most likely to
surface as a bug report, because a client that renders "no tasks yet" for a
deleted list is showing a plausible screen about a row that is gone.

**Every nested URL in an adopting codebase changes.** Mechanical, and the
generated client makes it compile-checked, but not free.

### Why the nested form is refused rather than added as an alias

An alias — the same handler mounted twice, the path segment turned into a
predicate — is genuinely small to build. It is refused because a second spelling
of one request is the thing this project spends its refusals on: two names for
one operation is what [ADR-0033](0033-array-columns.md) refused for `contains`
and what [ADR-0036](0036-the-wire-is-the-column-name.md) refused for field
naming. The cost is not the code, it is that every generated client, every
`--help`, every OpenAPI operation and every cache key then has to choose, and
readers have to learn that the choice does not matter.

There is also a question an alias cannot answer without a policy: does the
nested route enforce that the parent exists? If yes it is not an alias, it is a
different endpoint with different failure modes. If no, it is an alias whose
extra path segment buys nothing but characters.

## Consequences

**`Options.Path` stays a single collection path**, and the OpenAPI document has
one operation per resource operation rather than one per relationship.

**The missing-parent 404 is a documented gap rather than a hidden one.** A caller
that needs to distinguish "no children" from "no such parent" fetches the parent,
which is one more request and is what a nested route would have done internally.

**A hand-written nested route remains available and is a fine answer.** `rest`
mounts generated resources onto an existing router; a project that wants
`GET /lists/{id}/tasks` for its own reasons can write it, call `filter.Parse` and
`filter.Apply` like any other handler, and add the parent check it wanted. That
is [ADR-0007](0007-generated-rest-handlers.md)'s seam working as intended — the
generated surface is not the whole surface.

## What would change our mind

- **If a port finds that the flat form breaks a client it cannot change**, which
  is a different claim from "the URLs changed". A mobile app in the field with
  hardcoded nested paths and a slow release cycle is the realistic version.
- **If the missing-parent 404 causes a real defect** rather than a theoretical
  one. That would argue for the nested form as a genuinely different endpoint
  with the parent check, not as an alias — and it should be designed as such.
- **If nested `?expand` lands** ([compatibility.md](../compatibility.md) names it
  under *Will move*), the relationship between a parent and its children gains a
  second representation anyway, and the ordering of these two features is worth
  reconsidering together rather than separately.
- **If someone wants it purely for URL aesthetics**, that is not evidence. It is
  a real preference and it does not survive the cost of two spellings.

## Cost of change

**Adding the nested form later is additive and cheap.** No request that works
today changes meaning, no client breaks, and the flat path stays canonical. That
asymmetry is the reason this record is a refusal rather than a design: waiting
costs nothing, and building it now would freeze a shape nobody has used.

**Removing the flat form later would be expensive and is not on the table.** It
is the form every generated client, cache key and CLI command is built on.

**So the freeze binds only one direction**, which is the useful one. This record
exists to say that the direction was chosen deliberately rather than by omission.

## Alternatives considered

**Mount the nested path as an alias to the same handler.** The cheap version,
rejected above: two spellings for one request, and it cannot answer the
parent-existence question without becoming a different endpoint.

**Derive nested routes from declared inverses.** `Inverse("tasks")` already names
the relationship, so `/lists/{id}/tasks` could be generated from it. Elegant, and
it makes route shape depend on a declaration whose purpose is `?expand` — so
adding `InverseExpandable` to a reference would silently add a route, which is
exactly the kind of coupling [ADR-0006](0006-capabilities-are-opt-in.md) exists
to prevent. Capabilities are opt-in one at a time.

**A `Nested` option on `schema.REST`.** Honest and explicit, and it is the shape
to reach for *if* the evidence above arrives. Not now, because it would ship a
second route form on nobody's actual need, and route forms are frozen at 1.0.

## Revisions

- 2026-07-29 — Written for [the road to 1.0](../release-1.0.md)'s Phase 1, as one
  of the four decisions that had no record. The behaviour is unchanged; what is
  new is that the refusal is deliberate and its one real cost — the missing-parent
  404 — is written down rather than discovered.
