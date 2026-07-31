# ADR-0014: Migrations by diff, adoption by import

- **Status:** Working — `sqlb migrate` writes the diff, `introspect` reads a
  database back, `shadow` replays the history, and CI enforces the fixpoint in
  both directions
- **Confidence:** High that the loop closes; Medium on what it cannot carry —
  generated columns, triggers and data backfills stay hand-written
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

[ADR-0004](0004-schema-as-go-dsl.md) makes the Go DSL the source of truth, so
something must turn a schema edit into DDL, and something must turn an existing
database into a schema. A wrong answer is destructive: a diff that mistakes a
rename for a drop-and-add loses a column of production data, and it cannot tell
the two apart from the schema alone.

## Decision

**Migrations are generated, not applied.** sqlb emits files and stops. It does
not own a runner, track applied versions, or connect to the database to migrate.

**Goose is the default format**; golang-migrate and plain SQL are selectable, and
`Plain` is the escape hatch for runners we do not ship. The format is not
cosmetic: `-- +goose NO TRANSACTION` is *file-level*, so a migration containing
`CREATE INDEX CONCURRENTLY` would strip the rollback guarantee from every
unrelated change in the file. **Index changes therefore get their own migration
file**, versioned to sort immediately after the one they depend on.

**The diff is between two registries, not between a registry and a database.**
Introspection produces the same `*schema.Registry` the DSL does, so
`Diff(current, target) []Change` is a pure function — testable without a
database, and the same machinery pointed in either direction. Current state comes
from replaying the checked-in migrations into a scratch database and
introspecting that, which validates the history and catches drift.

**Destructive changes are opt-in.** Dropping a column or table, narrowing a type,
or adding NOT NULL without a default is emitted commented out with its reason.
**A change that depends on a commented-out one is commented out too**, carrying
`DependsOn` rather than `Destructive` — it is premature, not dangerous. Without
this, a commented `ADD COLUMN` followed by a live `ADD CONSTRAINT` dies partway
and leaves the schema in neither state. The dependency is tracked structurally in
the differ, not sniffed out of rendered SQL, and matched over the finished change
list so it reads the flag that was set rather than recomputing the rule
([ADR-0016](0016-guards-proven-both-ways.md)). Table-level `CHECK`s and partial
index predicates have no column list, so they wait on any pending column of their
table — over-blocking costs one uncomment; parsing the expression would cost
correctness.

**Lock hazards are stated, not gated.** A statement that rewrites or scans a
table is emitted live, with the lock named and the expand/contract sequence
spelled out. Destructive is commented because it is irreversible; this is only
occasionally slow, and how slow depends on a row count the schema does not
contain. `Migration.Blocking` is the hook for a project that knows its big tables.

**The lock-brief sequences are generated, and called rather than applied.**
`migrate.Unblock` rewrites a scanning `ADD CONSTRAINT` into `NOT VALID` plus
`VALIDATE`, a `SET NOT NULL` into the same pair, and a `UNIQUE`/`PRIMARY KEY`
into `CREATE UNIQUE INDEX CONCURRENTLY` plus `ADD CONSTRAINT … USING INDEX`. The
caller decides because the sequences buy nothing on a small table and are not
equivalent *under failure*: they can leave a binding unvalidated constraint or an
invalid index behind, where a plain statement leaves nothing.

**Renames are declared, never inferred.** `.RenamedFrom("old")` for one release;
without it, a rename is a drop and an add — lossy, never silently wrong. A hint
whose old name is gone is ignored rather than rejected, or every rename would
come with a scheduled build break.

**Adoption is `sqlb import`.** It reads `pg_catalog` and emits a `schema.go` with
no capabilities, so the result describes the database exactly and exposes nothing
over REST; widening is a deliberate edit. Generated names are overridable
(`Named`, `ConstraintNamed`, `PrimaryKeyNamed`) and only names that *differ* from
the convention are pinned. **What import cannot represent, it reports** — an
empty `Report` is the claim that the registry describes the database completely.

**Reading the catalog is a separate package from writing DDL.** `introspect`
connects; `migrate` does not, which is what keeps the diff a pure function.

**Formats are rendered in code, never translated by an agent.** The variation
between runners is syntax, roughly fourteen lines each; what they share is
semantics — the file split, Down reversing Up, destructive statements commented,
multi-statement delimiting — and a translation step would re-derive all four and
get each right *most* of the time. A wrong migration is applied once, often
irreversibly, and nothing type-checks it. Agents are better spent on backfills,
sequencing a rollout, reviewing a destructive migration, and supplying rename
hints.

**No `USING` clause is ever generated** for a type change. Postgres refusing an
implicit cast is the correct outcome; a generated cast nobody reviewed truncates
silently.

## Consequences

**Buys.** sqlb composes with the migration tooling a project already has. Output
is a text file a human reads, edits and commits, so a generator mistake is
recoverable. Import gives a correct, closed schema in one step.

**Costs.** The shadow step needs a real Postgres at generation time, which
complicates CI. Rename hints are manual and easy to forget, and forgetting one is
data loss unless the destructive guard catches it.

**Proven, not assumed.** `pgtest` runs the round trip in CI against Postgres 18:
render, apply, read back, diff. The **fixpoint** — import, re-render, apply,
re-import, diff — is asserted unconditionally and is empty, which is what makes
adoption work. One difference appears, and only one: Postgres normalises
`CHECK ("views" >= 0)` to `CHECK (views >= 0)`. It is forgiven narrowly, only
when the same constraint name is re-added as a `CHECK` in the same diff. Lock
modes in the generated comments were measured against `pg_locks` on three million
rows, not recalled — which caught one wrong claim: the `NOT VALID` add still
takes the strong lock briefly, so the remedy is lock-*brief*, not lock-free.

**Deliberately undetected:** `ADD COLUMN` with a volatile default rewrites the
table, and volatility lives in the database. Recognising only the generators we
ship would fire sometimes and read as coverage, which is worse than not looking.

Three structural constraints the implementation forced, each now in code:
foreign keys are never inlined into `CREATE TABLE`; phase ordering is a
correctness property, and the Down falls out of reversing it; and the concurrent
index split is *not* order-preserving, so an index over a column that is going
away drops non-concurrently. Renames add one more: a rename must carry the
constraints and indexes named after what it renamed, or the derived names diverge
permanently.

## What would change our mind

- The shadow database proves too heavy for the inner loop — replay into an
  in-memory model instead, losing validation against a real parser.
- People uncomment destructive changes without reading — the guard is not
  working, and it should become a separate reviewed file rather than a comment.
- Import silently drops a construct we care about — it needs a raw-DDL
  passthrough. This is the failure mode to watch for hardest.
- Rename hints are forgotten in practice — the hint is in the wrong place, and
  the alternative is a review step listing every drop-and-add.
- Matching a constraint or index by definition ever pairs the wrong two objects —
  that rule has to go.
- Shipped formats stop being ~15 lines each — the shared semantics have leaked
  into the `Format` interface.
- Lock hazards get scrolled past and cause an outage — then stating is not
  enough and they need to become a refusal with an opt-out.

## Cost of change

Rising sharply once the first generated migration is applied anywhere real.
Before that the diff engine is a pure function and can be rewritten freely.
After, the migration history is permanent: changing the file format, the
numbering, or the semantics of the destructive guard means reconciling against
files already in production. The format is the most expensive thing here to
change later. Choosing to own a runner afterwards is comparatively cheap.

## Revisions

- 2026-07-27 — Written, then built in increments: the diff engine and Postgres
  DDL, rename hints, lock-hazard detection, the generated `NOT VALID` and
  concurrent-unique sequences, `introspect`, `codegen.RenderSchema`, and
  `shadow.Build`.
- 2026-07-27 — Revised to goose's single-file format on learning the project uses
  goose; added the concurrent-index file split once `NO TRANSACTION` turned out
  to be per file.
- 2026-07-27 — Recorded the decision to render formats in code rather than have an
  agent translate one canonical output.
- 2026-07-27 — Fixed the defect the shadow database found before it had a caller:
  a change depending on a commented-out destructive one was emitted live, so the
  file failed partway instead of being the intended no-op.
- 2026-07-27 — `migrate.MinPostgres(major)` closes the UUIDv7 gap: at 18 the DDL
  layer emits built-in `uuidv7()` rather than the `pg_uuidv7` extension spelling.
  It threads to `renderDefault` so rendering and comparison resolve against the
  same version; `introspect` maps both spellings to one generator.
- 2026-07-27 — Automated the round trip in the `pgtest` module against Postgres
  18. A separate module was forced, not chosen: `deps-check` runs `go list -deps`,
  which cannot see test-only imports, so a driver in this module's tests would
  have left the gate reporting success while covering nothing.
- 2026-07-30 — Condensed.
- 2026-07-31 — The round trip is asserted as a *fixpoint*, and three
  disagreements fell out of asserting it (issue #53). Each lived between two
  packages that were individually well tested: `introspect` read a `vector`
  column that `RenderSchema` refused to write — which blocked the bootstrap this
  record calls the point of importing at all — an index lost its operator class
  and storage parameters, so the DDL for a pgvector index was rejected outright
  rather than being merely poorer, and an enum's CHECK lost its name, so a
  database whose constraint is called `chk_org_plan` was rebuilt with
  `orgs_plan_check` and every later diff proposed dropping and re-adding it.

  The gate that catches them is not another round-trip test of one package. It
  applies a deliberately awkward schema, reads it, writes it back out as source
  that must *compile*, rebuilds a second database from what was read, and
  compares the two databases through `pg_catalog` — not the two registries.
  That distinction is the finding: two registries agree about everything they
  both dropped, which is exactly how a lost constraint name stayed invisible.
