# ADR-0017: An enum is text with a CHECK constraint, not a Postgres ENUM

- **Status:** Working
- **Confidence:** Medium
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

`schema.Enum("status", "draft", "live")` declares a column constrained to a fixed
set. Postgres has a native type for this, and it is the obvious choice right up
to the point where the list has to change — which is one of the most ordinary
schema edits there is:

- **A value cannot be removed.** There is no `ALTER TYPE … DROP VALUE`. The route
  is a replacement type, a rewrite of every column using the old one, and a drop.
- **`ALTER TYPE … ADD VALUE`** cannot be used in the same transaction that reads
  the new value, and a change needing `NO TRANSACTION` drags every unrelated
  change in the file out of its transaction too
  ([ADR-0014](0014-migrations-and-import.md)).
- **The type is schema-level, not table-level.** Under the prefixing of
  [ADR-0015](0015-module-isolation.md), two modules declaring a `status` enum
  collide in a namespace neither owns.

Against that, the native type buys storage compactness, a defined sort order, and
type-level rejection at every call site.

## Decision

An enum column is `text` with a named `CHECK` constraint:

```sql
"status" text NOT NULL,
CONSTRAINT "posts_status_check" CHECK ("status" IN ('draft', 'live'))
```

Changing the list is a `DROP CONSTRAINT` plus an `ADD CONSTRAINT`, which the diff
engine already does for every other constraint — no special case, no new object
type, no transaction exception.

Removing a value is not marked destructive: it cannot lose data, because Postgres
rejects the whole `ADD CONSTRAINT` if any row violates it. It carries a comment
naming the values no longer permitted, since the fix is in the rows.

## Consequences

**Buys.** Editing a value list is an ordinary, transactional, reversible
migration from machinery that already existed. Module isolation holds, because a
`CHECK` is named after its table and inherits the prefix. The column reads as
`text` to every client and introspection tool without a type lookup.

**Costs.** More storage than a native enum's four bytes. No implicit ordering —
`ORDER BY status` sorts alphabetically, and declaration order needs an explicit
`CASE`. A bad value is rejected at insert time by the constraint rather than at
parse time by the type system, so the error arrives later.

## What would change our mind

- Declaration-order sorting is needed in more than one or two places — but expect
  the answer to be an explicit ordinal column, not a native enum.
- A consumer's tooling reads `pg_enum` to discover permitted values and cannot be
  taught to read `pg_constraint`.
- Profiling shows the text column's width mattering — expect the answer to be a
  lookup table, which is also the right escalation once a value list acquires
  attributes of its own.

## Cost of change

Asymmetric in the useful direction. Nothing outside `migrate/ddl.go` knows the
representation: `GoType()` already returns `string`, and the engine and filter
grammar treat it as text. Moving to a native enum is confined and mechanical.
Moving *away* from one later would mean rewriting every table that used it, so
text is the cheaper direction to be wrong in.

## Revisions

- 2026-07-27 — Written, when the diff engine forced the question of what an
  enum's DDL actually is.
- 2026-07-30 — Condensed.
