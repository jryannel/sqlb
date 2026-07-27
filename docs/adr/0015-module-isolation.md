# ADR-0015: Modules own their tables, and do not reference each other's

- **Status:** Working
- **Confidence:** Medium
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

The target codebase is arranged as independent fx modules — `auth`, `billing`,
`tenants`, `rag` and others — with a rule that no module imports another. Each
already owns its own goose migration directory (`<module>/migrations/`), and its
architecture document forbids cross-module foreign keys outright: *"owner_id
stores the platform/users user_id as a plain UUID column; no foreign key, per
architecture §8.1."*

Three things in sqlb collided with that.

The package-level `schema.Table` registers into one global registry, so two
modules could not both own a table called `events`. Table names had no
namespace, so ownership was invisible in the database and prefixing was left to
discipline — and in the reference codebase that discipline had already drifted:
`rag_chunks` and `llmcatalog_models` are prefixed, while `tenants`, `members`,
`users` and `secrets` are not. And `Ref` takes a `*TableDef`, which requires
precisely the Go-level import the architecture forbids.

## Decision

**A registry is the unit of module isolation.** `schema.NewModule("billing")`
returns a registry whose tables are prefixed with the module name. Declarations
use the local name — `Table("invoices")` — and the prefix is applied by the
registry, so it cannot be forgotten and a table moving between modules changes
one line.

**Prefixes, not Postgres schemas, and no abstraction over the two.**
`billing_invoices` rather than `billing.invoices`.

The tempting move is a pluggable namespacing strategy so the choice can be
deferred. We are not doing that, because Postgres schemas are not a rendering
strategy — they are a deployment model. Only one of the four things they
require is about how a name renders:

- the compiler must emit two identifiers rather than one;
- `search_path` must be managed in the pool, or every query fully qualified;
- `CREATE SCHEMA IF NOT EXISTS` must run before a module's first migration, and
  with per-module migration directories and deliberately no cross-module
  coordination, nothing currently owns that ordering;
- goose's version table has to be placed per schema.

A strategy interface covering only the rendering would create false confidence
that switching is a configuration change, while the parts that actually make it
hard stay unbuilt. An abstraction whose second implementation is hypothetical
also rots, and constrains the first for no benefit.

What we did instead is make the compiler render a qualified table name
*correctly* — `"billing"."invoices"` rather than the single mangled identifier
`"billing.invoices"` it produced before. That was a latent bug regardless of
this decision: a caller reaching it got a "relation does not exist" a long way
from its cause. Fixing it happens to mean the runtime would not need changing if
this record is ever revisited, but that is a side effect, not a strategy.

**The prefix is a storage concern and does not reach the URL.** A REST path
defaults to the local name, so moving a table between modules is not a breaking
API change.

**Cross-module relationships are declared, not enforced.**
`ExternalRef("tenant", "tenants.id")` produces the column and an index to join
on, and no `FOREIGN KEY`. The target is free text, because resolving it to a
real table would require the dependency this exists to avoid. Such a reference
cannot be `Expandable`: expanding it would join a table the module does not own.

The index is added to the table's own index list rather than applied at render
time, so it appears in `Indexes()`, the manifest and the generated DDL like any
other.

## Consequences

**What this buys.** Modules migrate and deploy independently, and either side of
a soft reference can be moved to its own database without dropping a constraint.
Ownership is visible in the table name. The relationship still appears in the
manifest, marked `enforced: false`, so tooling and readers can see it even
though the database cannot.

**What this costs.** Referential integrity is the application's job — nothing
stops a `tenant_id` pointing at a tenant that no longer exists, and no cascade
will clean it up. Prefixed names are longer, and prefixing is a convention the
registry enforces rather than a namespace the database understands: two modules
could still collide by both prefixing badly. `ExternalRef` targets are unchecked
strings that can rot silently when the other side renames a table.

## What would change our mind

- If orphaned rows become a real operational problem, the answer is probably a
  periodic reconciliation job per module rather than reintroducing foreign keys,
  since the FK would reintroduce the coupling deliberately removed.
- If a module ever needs to be moved to a separate database, prefixes stop
  helping and Postgres schemas become worth their operational cost. That is the
  clearest trigger to revisit the namespacing choice. The runtime is already
  able to render qualified names; what would still need building is
  `search_path` handling, schema creation ordered ahead of each module's first
  migration, and per-schema goose version tables.
- If `ExternalRef` targets rot often enough to matter, a lint pass could check
  them against a manifest published by each module — validation without a
  compile-time dependency.
- If `?expand` across modules turns out to be genuinely wanted, that is a signal
  the boundary is in the wrong place, not that the rule should be relaxed.

## Cost of change

Asymmetric, and the namespacing half is the expensive one. Adding a prefix to an
existing table is a rename in the database, which means a migration per table
and a coordinated deploy. Moving from prefixes to Postgres schemas later is the
same rename plus `search_path` work. Doing it before a module's tables exist is
free; doing it afterwards is a migration exercise.

The reference half is cheap in one direction only. Dropping a soft reference for
a real foreign key means backfilling or deleting whatever rows already violate
it, which on live data is the hard part. Going the other way — dropping an FK
for a soft reference — is one migration.

## Alternatives considered

**Postgres schemas.** Real isolation, per-schema grants, and a module could be
lifted into its own database untouched. Genuinely close, and the likely end
state if modules ever separate. Lost on operational surface: `search_path` in
the pool, schema creation ordering, and tooling that handles qualified names
poorly.

**Hard foreign keys named by string, no Go import.** Keeps database-enforced
integrity while staying decoupled in Go. Rejected because it reintroduces
cross-module migration ordering — the target table must exist first — which is
the coupling the module boundary exists to remove.

**Forbid cross-module references entirely**, leaving an opaque column. Purest,
and it was close. Lost because sqlb then cannot index the column for you or
surface the relationship in the manifest, and the relationship exists whether or
not it is written down.

## Revisions

- 2026-07-27 — Written, after reading the reference codebase and finding the
  no-cross-module-FK rule already documented in its migrations.
- 2026-07-27 — Considered and rejected supporting both namespacing strategies
  behind an abstraction. Recorded the reasoning: three of the four requirements
  for Postgres schemas are not about rendering, so an abstraction over rendering
  alone would mislead. Fixed the compiler to render qualified table names
  correctly, which was a latent bug on its own terms.
