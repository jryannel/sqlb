# ADR-0004: Schema is a Go DSL, and codegen flows from it

- **Status:** Working — the DSL is what every other artefact is generated from:
  models, the typed facade, REST bodies, three clients, the manifest and the
  migration history
- **Confidence:** High on the decision, which two outside evaluations exercised
  against real schemas without reaching for the alternative. Medium on the parts
  the DSL still cannot express, which are enumerated in
  [the road to 1.0](../release-1.0.md) rather than here
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

Something has to be the source of truth for table structure, and every artefact
we want — migrations, model structs, REST handlers, OpenAPI, a TypeScript
client — has to agree with it. The candidates were a Go DSL that generates
migrations (ent, Convex), or introspection of an existing database that
generates everything else (sqlc, PostgREST).

Introspection has a specific weakness for this project: DDL has nowhere to
record *capabilities*. There is no way to say "this column may be filtered on"
in `CREATE TABLE`, so an introspecting tool needs a side-car config file, and
now there are two sources of truth.

## Decision

The schema is a Go DSL in its own package. `sqlb generate` reads it and emits
migrations, models, typed column sets, REST handlers and OpenAPI. One file is
edited; everything else is derived.

## Consequences

**What this buys.** Capabilities, REST exposure, comments and relations live
next to the column they describe. There is one file to edit and one command to
run — which is a large part of why this is pleasant to drive with an agent.
Validation can catch authoring mistakes before any SQL is generated.

**What this costs.** Adopting sqlb in an existing project means importing the
current schema and handing DDL control over, which is a real migration. The
generator is the single biggest unbuilt piece, and until it exists the models
are hand-written — so this record is Exploring rather than Working.

## What would change our mind

- If migration diffing against a live database turns out to be substantially
  harder than expected, invert to introspection-first and keep the DSL only for
  capabilities.
- If adopting this in a large existing codebase stalls, build `sqlb import` to
  generate the DSL from an existing database, making adoption incremental.
- If generated migrations ever produce a destructive diff we did not intend,
  that is a stop-the-line signal for the whole approach.

## Cost of change

Cheapest now, and it gets steadily more expensive. Nothing is generated yet, so
today changing direction costs only the schema package.

Once migrations are generated and a production database has been migrated by
them, reversing means reconciling a generated DDL history against a hand-managed
one, which is delicate and hard to test. If this decision is going to change, it
should change before the first generated migration is applied anywhere real.

## Alternatives considered

**Introspect an existing database.** Genuinely close, and better for
incremental adoption — see [ADR-0010](0010-codegen-is-optional.md), which
recovers most of that benefit by a different route. Lost because capabilities
need a second file, and two sources of truth drift.

**Both directions, DSL primary with an import path.** Still the likely end
state; deferred as extra scope until the DSL direction proves out.

## Revisions

- 2026-07-27 — Written.
