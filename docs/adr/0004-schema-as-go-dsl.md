# ADR-0004: Schema is a Go DSL, and codegen flows from it

- **Status:** Working — every other artefact is generated from the DSL: models,
  the typed facade, REST bodies, three clients, the manifest, migrations
- **Confidence:** High on the decision; Medium on what the DSL still cannot
  express, which [the road to 1.0](../release-1.0.md) enumerates
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

Something has to be the source of truth for table structure, and every artefact —
migrations, models, REST handlers, OpenAPI, clients — has to agree with it. The
candidates were a Go DSL that generates migrations (ent, Convex) or introspection
of an existing database (sqlc, PostgREST).

DDL has nowhere to record *capabilities*: there is no way to say "this column may
be filtered on" in `CREATE TABLE`. An introspecting tool therefore needs a
side-car config file, and then there are two sources of truth that drift.

## Decision

The schema is a Go DSL in its own package. `sqlb generate` reads it and emits
migrations, models, typed column sets, REST handlers and OpenAPI. One file is
edited; everything else is derived.

## Consequences

**Buys.** Capabilities, REST exposure, comments and relations live next to the
column they describe. One file to edit, one command to run — a large part of why
this is pleasant to drive with an agent. Authoring mistakes are caught before any
SQL is generated.

**Costs.** Adopting sqlb in an existing project means importing the current
schema and handing DDL control over, which is a real migration.

## What would change our mind

- Migration diffing against a live database proves substantially harder than
  expected — then invert to introspection-first, keeping the DSL for capabilities.
- Adoption in a large existing codebase stalls — lean harder on `sqlb import`.
- A generated migration produces a destructive diff we did not intend. That is a
  stop-the-line signal for the whole approach.

## Cost of change

Rises steadily. Once a production database has been migrated by generated DDL,
reversing means reconciling a generated history against a hand-managed one. If
this changes, it should change before the first generated migration is applied
anywhere real.

## Revisions

- 2026-07-27 — Written.
- 2026-07-30 — Condensed.
