# ADR-0001: Target Postgres only

- **Status:** Working
- **Confidence:** High
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

sqlb could target several SQL dialects, as sqlc and most query builders do.
The features we most want are not evenly available, though: `LISTEN/NOTIFY`
for the change feed, jsonb aggregation for relation expansion, `RETURNING` on
every mutation, `ON CONFLICT` upserts, `SKIP LOCKED` for queue consumers,
partial and GIN indexes, and `ILIKE`.

Supporting a second dialect means either dropping to the intersection of what
they all offer, or carrying per-dialect branches through the compiler, the
schema DSL and the migration generator.

## Decision

Target Postgres, and only Postgres. Keep a `Dialect` interface for placeholder
style and identifier quoting so the AST does not have to change if that ever
stops being true, but do not pretend to be portable.

## Consequences

**What this buys.** Every feature can assume the best available primitive. The
change feed can use `LISTEN/NOTIFY` rather than polling. Mutations return their
rows in one round trip. The compiler stays small because there is one set of
rules.

**What this costs.** sqlb is unusable for MySQL or SQLite projects, which
removes a large part of the potential audience. Tests that want an in-memory
database cannot use SQLite and need either a real Postgres or the fake driver
we use today.

## What would change our mind

- A concrete project we want to support is on MySQL and cannot move.
- The `Dialect` interface turns out to leak — if compiling a query requires
  knowing the dialect anywhere outside placeholder rendering and quoting, the
  seam is in the wrong place and should be fixed regardless.
- SQLite-in-tests becomes painful enough that the fake driver stops being
  adequate.

## Cost of change

Asymmetric. Narrowing further is free; widening is not. Adding a second dialect
means auditing every compiler assumption, finding replacements for
`LISTEN/NOTIFY` and jsonb expansion, and re-validating every generated
migration. The features that would have to go are the ones that justify the
project, so this is close to a restart rather than a refactor.

## Alternatives considered

**Multi-dialect from the start.** Rejected: it would cost the change feed and
the expansion strategy, which are two of the three things that make this
project worth building.

**Postgres-first, dialect-pluggable later.** This is effectively what we have —
the `Dialect` interface exists — but we are explicitly not maintaining the
constraint that other dialects stay implementable.

## Revisions

- 2026-07-27 — Written.
