# ADR-0001: Target Postgres only

- **Status:** Working
- **Confidence:** High
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

The features sqlb is built on are not evenly available across SQL dialects:
`LISTEN/NOTIFY` for the change feed, jsonb aggregation for relation expansion,
`RETURNING` on every mutation, `ON CONFLICT`, `SKIP LOCKED`, partial and GIN
indexes, `ILIKE`. Supporting a second dialect means either dropping to the
intersection or carrying per-dialect branches through the compiler, the schema
DSL and the migration generator.

## Decision

Target Postgres, and only Postgres. Keep a `Dialect` interface for placeholder
style and identifier quoting, but do not pretend to be portable.

## Consequences

**Buys.** Every feature can assume the best available primitive. Mutations
return their rows in one round trip. The compiler stays small — one set of rules.

**Costs.** Unusable for MySQL or SQLite projects. Tests wanting an in-memory
database need a real Postgres or the fake driver. `LISTEN` needs a session, so a
pooler in transaction mode takes part of the change feed back
([ADR-0019](0019-pgbouncer-in-the-path.md)).

## What would change our mind

- A concrete project we want to support is on MySQL and cannot move.
- The `Dialect` interface leaks — if compiling needs dialect knowledge outside
  placeholder rendering and quoting, the seam is in the wrong place.
- SQLite-in-tests becomes painful enough that the fake driver stops being enough.

## Cost of change

Asymmetric. Narrowing is free; widening is close to a restart — every compiler
assumption audited, replacements found for `LISTEN/NOTIFY` and jsonb expansion,
every generated migration re-validated.

## Revisions

- 2026-07-27 — Written.
- 2026-07-27 — Qualified the `LISTEN/NOTIFY` benefit: it does not hold through a
  transaction-pooling connection pooler.
- 2026-07-30 — Condensed.
