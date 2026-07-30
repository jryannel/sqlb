# ADR-0015: Modules own their tables, and do not reference each other's

- **Status:** Working
- **Confidence:** Medium
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

The target codebase is arranged as independent fx modules — `auth`, `billing`,
`tenants`, `rag` — with a rule that no module imports another, each owning its
own migration directory, and cross-module foreign keys forbidden outright.

Three things in sqlb collided with that: `schema.Table` registered into one
global registry, so two modules could not both own `events`; table names had no
namespace, leaving prefixing to a discipline that had already drifted in the
reference codebase; and `Ref` takes a `*TableDef`, requiring exactly the Go
import the architecture forbids.

## Decision

**A registry is the unit of module isolation.** `schema.NewModule("billing")`
returns a registry whose tables are prefixed with the module name. Declarations
use the local name — `Table("invoices")` — so the prefix cannot be forgotten and
a table moving between modules changes one line.

**Prefixes, not Postgres schemas, and no abstraction over the two.**
`billing_invoices`, not `billing.invoices`. Postgres schemas are a deployment
model, not a rendering strategy: only one of their four requirements is about how
a name renders — the others are `search_path` management, `CREATE SCHEMA`
ordered ahead of each module's first migration, and per-schema goose version
tables. A strategy interface covering rendering alone would suggest switching is
configuration while the hard parts stay unbuilt.

**The prefix is a storage concern and does not reach the URL.** A REST path
defaults to the local name, so moving a table between modules is not a breaking
API change.

**Cross-module relationships are declared, not enforced.**
`ExternalRef("tenant", "tenants.id")` produces the column and a join index, and
no `FOREIGN KEY`. The target is free text, because resolving it would require the
dependency this avoids. Such a reference cannot be `Expandable`.

## Consequences

**Buys.** Modules migrate and deploy independently, and either side of a soft
reference can move to its own database without dropping a constraint. Ownership
is visible in the table name. The relationship still appears in the manifest as
`enforced: false`, so tooling and readers can see what the database cannot.

**Costs.** Referential integrity is the application's job — nothing stops a
`tenant_id` pointing at a deleted tenant, and no cascade cleans it up. Prefixed
names are longer, and the prefix is a registry convention rather than a namespace
the database understands. `ExternalRef` targets are unchecked strings that rot
silently when the other side renames a table.

## What would change our mind

- Orphaned rows become a real operational problem — the answer is a periodic
  reconciliation job per module, not foreign keys, which reintroduce the coupling.
- A module has to move to a separate database — prefixes stop helping and
  Postgres schemas become worth their operational cost. The compiler already
  renders qualified names; `search_path`, schema-creation ordering and per-schema
  version tables would still need building.
- `ExternalRef` targets rot often enough to matter — a lint pass could check them
  against a per-module manifest, validating without a compile-time dependency.
- `?expand` across modules turns out to be genuinely wanted — that is a signal the
  boundary is in the wrong place, not that the rule should relax.

## Cost of change

Asymmetric, and namespacing is the expensive half: adding a prefix to an existing
table is a rename, so a migration per table and a coordinated deploy. Free before
a module's tables exist. The reference half is cheap in one direction only —
replacing a soft reference with a real foreign key means backfilling or deleting
whatever rows already violate it.

## Revisions

- 2026-07-27 — Written, after finding the no-cross-module-FK rule already
  documented in the reference codebase's migrations.
- 2026-07-27 — Rejected a pluggable namespacing abstraction; fixed the compiler to
  render `"billing"."invoices"` rather than one mangled identifier, which was a
  latent bug on its own terms.
- 2026-07-30 — Condensed.
