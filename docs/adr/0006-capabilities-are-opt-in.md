# ADR-0006: Column capabilities are opt-in

- **Status:** Working
- **Confidence:** High
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

Exposing a table over a dynamic filter API means deciding what a client may ask
about. PostgREST's model — everything in the exposed schema is fair game, with
row-level security as the guard — puts the whole schema one policy mistake away
from public, and makes "which columns can a client filter on" a question you
answer by reading policies rather than the table.

## Decision

Every capability is opt-in per column: `Filterable`, `Sortable`, `Searchable`,
`Expandable`, `Hidden`. A column that does not declare a capability cannot be
reached through it, and the request is rejected with a 400 rather than silently
ignored.

A `Hidden` column is reported as *unknown* rather than *not filterable*, so its
existence cannot be probed from the rejection. `Hidden` plus `Filterable` is a
schema validation error — a filterable secret can be recovered a character at a
time. `filter.Apply` owns the projection and defaults to non-hidden columns, so a
handler that forgets to project cannot leak one.

## Consequences

**Buys.** The blast radius of exposing a table is legible from the schema file
alone. Adding a column does not silently widen the API. An index can be
guaranteed for every filterable column, because the set is finite and declared.

**Costs.** Every new filter needs a schema edit and a regeneration. Friction by
design, but still friction.

## What would change our mind

- The declare-and-regenerate loop becomes the main complaint from people building
  views — then consider a per-resource permissive mode, still excluding `Hidden`.
- A capability leak through a path that does not consult the model (ordering by
  an expression, a computed column, an expansion) — that path needs the same gate.

## Cost of change

Strongly asymmetric. Loosening is a default flip and nearly irreversible: once
clients depend on filters opt-in would not have granted, tightening breaks them
and you will not know which until it does. Tightening from here is cheap, because
nothing is exposed that was not declared.

## Revisions

- 2026-07-27 — Written.
- 2026-07-27 — Added the projection default after finding `filter.Apply` would
  project hidden columns when a handler did not specify a projection.
- 2026-07-30 — Condensed.
