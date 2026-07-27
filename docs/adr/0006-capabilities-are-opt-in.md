# ADR-0006: Column capabilities are opt-in

- **Status:** Working
- **Confidence:** High
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

Exposing a table over a dynamic filter API means deciding what a client may ask
about. PostgREST's model is that everything in the exposed schema is fair game,
with row-level security as the guard. That puts the entire schema one policy
mistake away from being public, and makes "which columns can a client filter on"
a question you answer by reading policies rather than by reading the table.

## Decision

Every capability is opt-in per column: `Filterable`, `Sortable`, `Searchable`,
`Expandable`, `Hidden`. A column that does not declare a capability cannot be
reached through it, and the request is rejected with a 400 rather than silently
ignored.

A `Hidden` column is reported as *unknown* rather than as *not filterable*, so
its existence cannot be probed by reading the rejection. `Hidden` combined with
`Filterable` is a schema validation error, because a filterable secret can be
recovered a character at a time.

`filter.Apply` owns the projection and defaults to non-hidden columns, so a
handler that forgets to project cannot leak one.

## Consequences

**What this buys.** The blast radius of exposing a table is legible from the
schema file alone. Adding a column does not silently widen the API. An index
can be guaranteed for every filterable column, because the set is finite and
declared.

**What this costs.** Every new filter needs a schema edit and a regeneration —
this is friction by design, but it is friction. A column that is genuinely
useful to filter on and cheap to filter on still requires the ceremony.

## What would change our mind

- If the declare-and-regenerate loop becomes the main complaint from people
  building views, consider a per-resource "permissive" mode that opts a whole
  table in, still excluding `Hidden` columns.
- If we find a capability leak through a path that does not consult the model —
  ordering by an expression, a computed column, an expansion — that path needs
  the same gate, and the record should say so explicitly.

## Cost of change

Strongly asymmetric, and worth understanding before changing either way.

Loosening is nearly free — a default flip. But it is close to irreversible in
practice: once clients depend on filters that opt-in would not have granted,
tightening again breaks them, and you will not know which ones until it does.

Tightening from where we are is cheap, because nothing is exposed that was not
declared. Widen only with that ratchet in mind.

## Alternatives considered

**Everything filterable, rely on row-level security.** Rejected: RLS controls
which rows are visible, not which columns can be probed, and does nothing about
the cost of an unindexed filter.

**Deny-list instead of allow-list.** Rejected: a new column would default to
exposed, so the failure mode of forgetting is a leak rather than a 400.

## Revisions

- 2026-07-27 — Written.
- 2026-07-27 — Added the projection default after finding `filter.Apply` would
  project hidden columns when a handler did not specify a projection.
