# ADR-0014: Migrations by diff, adoption by import

- **Status:** Exploring
- **Confidence:** Medium
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

[ADR-0004](0004-schema-as-go-dsl.md) makes the Go DSL the source of truth, which
means something has to turn a schema change into DDL. Two questions follow
immediately, and they are the ones that decide whether this is adoptable in an
existing project at all: how does a schema edit become a migration, and how does
an existing database become a schema?

The hard constraint is that a wrong answer is destructive. A diff that mistakes
a rename for a drop-and-add loses a column of production data, and it cannot
tell the two apart from the schema alone — the before and after states are
identical either way.

## Decision

**Migrations are generated, not applied.** `sqlb` emits migration files and
stops. Applying them is the job of whatever runner the project already has. sqlb
does not own a runner, does not track applied versions, and does not connect to
the database to migrate.

**Goose is the default output format**, because it is what this project's
authors already use. `golang-migrate` and plain SQL are also supported, selected
by configuration. The format matters more than it first appears:

- goose is a *single* file per migration with `-- +goose Up` and
  `-- +goose Down` annotations. golang-migrate uses separate `.up.sql` and
  `.down.sql` files. These are not interchangeable.
- `-- +goose NO TRANSACTION` is a **file-level** directive. A migration
  containing `CREATE INDEX CONCURRENTLY` must therefore disable transactions for
  everything in the file, which would silently strip the rollback guarantee from
  every unrelated change generated at the same time.

So index changes are **split into their own migration file**, versioned to sort
immediately after the migration they depend on. Building an index without
`CONCURRENTLY` locks the table against writes for the duration, so on a live
table concurrency is not optional — and once it is required, the split follows.

**The diff is between two registries, not between a registry and a database.**
Introspection produces the same `*schema.Registry` the DSL produces, so the
diff is `Diff(current, target) []Change` over one data structure. That symmetry
is the whole design: it makes the diff testable without a database, and it makes
import and migration the same machinery pointed in different directions.

Current state is obtained by replaying the existing migrations into a scratch
database and introspecting that, rather than by introspecting production. This
validates that the migration history actually produces the schema it claims to,
and catches drift.

**Destructive changes are opt-in.** Dropping a column or table, narrowing a
type, or adding a NOT NULL without a default is emitted commented out, with the
reason, and requires an explicit flag to emit live.

**Renames are declared, never inferred.** A column carries `.RenamedFrom("old")`
for one release; the diff reads it and emits `ALTER TABLE … RENAME COLUMN`
instead of a drop and an add. Without the hint, a rename is a drop and an add,
which is correct if lossy and never silently wrong.

**Adoption is `sqlb import`.** It reads `pg_catalog` and emits a `schema.go`.
Capabilities cannot be inferred from DDL, so everything imports with none: the
result is a schema that describes the database exactly and exposes nothing over
REST. Widening is then a deliberate, reviewable edit.

For this to round-trip, generated names must be overridable, which is why
`Named`, `ConstraintNamed` and `PrimaryKeyNamed` exist. An imported schema whose
constraint names do not match the database would produce a diff that drops and
recreates every constraint on the first run.

**Formats are rendered in code, not translated by an agent.** The tempting
alternative is to emit one canonical output and let an AI coding agent convert
it to whatever runner a project uses. We are not doing that, for three reasons.

The variation between runners is *syntax* — roughly fourteen lines per format.
What they share is *semantics*, and that is where the danger lives: the
concurrent-index file split, Down reversing Up's order, destructive statements
rendering commented out, explicit delimiting of multi-statement SQL. A
translation step would have to re-derive all four every time, and would get each
right most of the time. Most of the time is a catastrophic hit rate for DDL
applied to production.

Migrations are also the one artefact here where a mistake is not recoverable by
regenerating. A wrong model struct fails `go build`; a wrong migration is
applied once, often irreversibly, and nothing type-checks it. That inverts the
usual calculus in favour of boring deterministic code.

Third, a rendered format is golden-testable. A translation would test the agent
rather than the system.

The `Plain` format is the canonical-output escape hatch for runners we do not
ship, and the `Format` interface keeps adding one small and local.

**Agents are better spent on the parts a generator cannot do**: authoring data
backfills, sequencing an expand/contract rollout when a change would take a
long lock, reviewing a destructive migration before it is uncommented, and
supplying the rename hints that cannot be inferred.

## Consequences

**What this buys.** sqlb composes with the migration tooling a project already
has rather than replacing it, which is most of what makes it adoptable
incrementally. Output is a text file a human can read, edit and commit, which
means the generator being wrong is recoverable rather than fatal. The diff engine is a pure function over two registries and can be
tested exhaustively without a database. Import gives a correct, closed schema in
one step.

**What this costs.** The shadow-database step needs a real Postgres available at
generation time, which complicates CI. Rename hints are a manual step that is
easy to forget, and forgetting one is data loss unless the destructive guard
catches it.

**What is built.** `migrate.Diff(current, target *schema.Registry) ([]Change,
error)` and the Postgres DDL under it. The symmetry claim held: the diff is a
pure function, tested exhaustively without a database. Still unbuilt: the
shadow database that produces `current`, `sqlb import`, and rename hints — so
in practice `Diff` currently has no way to learn the current state except from
another hand-written registry.

The generated DDL has been applied to a real Postgres 17 once, by hand: an
initial migration for the blog schema and an incremental one exercising every
kind of change the diff emits, each applied forward against seeded rows and
then reversed, ending in the exact structure it started from. That is what
raised confidence to Medium. It is not High because the check was manual and is
not in CI — the test suite deliberately needs no database, so the round-trip
that proves the DDL is *valid* rather than merely *expected* has to be
re-run by hand whenever the DDL layer changes. Automating it behind a build tag
is the obvious next increment.

Building it surfaced three constraints the record did not anticipate, each now
enforced in code:

- **Foreign keys are never inlined into `CREATE TABLE`.** Adding them as
  separate `ALTER TABLE` changes means table creation needs no dependency sort
  and no cycle special case, and one code path adds a reference whether the
  table is new or not.
- **Ordering is a correctness property, not presentation.** Changes are emitted
  in phases so nothing is dropped out from under something that still refers to
  it, and rendering reverses that list for the Down — so reversibility falls
  out of the ordering rather than being arranged separately.
- **The concurrent-index file split can reorder against itself.** An index drop
  is normally `CONCURRENTLY`, which `Split` moves into a file that runs *after*
  the one holding the column drops — by which time Postgres has already dropped
  that index along with its column, and the statement fails. An index over a
  column that is going away therefore drops non-concurrently, which costs
  nothing: `DROP COLUMN` takes an `ACCESS EXCLUSIVE` lock on the same table
  moments later regardless. The general lesson is that the split is not
  order-preserving, and any future concurrent change has to be checked against
  the ordinary changes it depends on.

**No `USING` clause is ever generated** for a type change. Postgres refusing a
cast it cannot make implicitly is the correct outcome; a generated `USING`
would pick a cast nobody reviewed, and casting to a narrower type truncates
silently rather than failing.

## What would change our mind

- If the shadow database proves too heavy for the inner loop, compute current
  state by replaying migrations into an in-memory model instead. That is faster
  but no longer validates the history against a real parser.
- If destructive-by-default-commented turns out to be ignored — people
  uncommenting without reading — the guard is not working and should become a
  separate reviewed file rather than a comment.
- If import cannot faithfully represent a construct we care about (exclusion
  constraints, partial indexes, generated columns), it needs a passthrough for
  raw DDL rather than silently dropping it. Silently dropping is the failure
  mode to watch for hardest.
- If the shipped formats stop being ~15 lines each, the `Format` interface is
  carrying the wrong responsibilities and the shared semantics have leaked into
  it. That is a design signal, not a reason to add more formats.
- If the DDL layer changes and the manual Postgres round-trip is not re-run,
  that is the signal it needs to become a build-tagged test rather than a
  habit. A habit that has to be remembered is a guard that will eventually not
  be ([ADR-0016](0016-guards-proven-both-ways.md)).
- If the diff starts emitting changes that need a lock long enough to matter
  (`SET NOT NULL` on a large table, a type narrowing), rendering them as a
  single migration is actively harmful. They need to be flagged as requiring an
  expand/contract sequence that a person or an agent authors.

## Cost of change

Rising sharply once the first generated migration is applied anywhere real.
Before that — which is still true today — the diff engine is a pure function
and can be rewritten freely.

After that, the migration history is a permanent artefact: changing the file
format, the numbering, or the semantics of the destructive guard means
reconciling against files already applied in production. Decide the format
deliberately and early — it is the most expensive thing in this record to
change later.

Choosing to own a runner after choosing not to is comparatively cheap and
additive.

## Alternatives considered

**Own the migration runner.** Rejected: it forces a project to abandon working
tooling to adopt sqlb, which is a much larger ask than adopting a code
generator, and there is nothing sqlb would do better.

**Introspect production directly for current state.** Rejected: it makes
generation depend on production access, and it silently accepts drift instead of
reporting it.

**Infer renames heuristically** (same type, similar name, position). Genuinely
tempting and rejected on consequence asymmetry: a wrong inference destroys data,
and a missed inference costs a manual hint.

**No migrations — introspection only.** This is what [ADR-0010](0010-codegen-is-optional.md)
already supports for adoption, and it stays valid for projects that never want
sqlb to own DDL.

## Revisions

- 2026-07-27 — Written, before implementation.
- 2026-07-27 — Recorded the decision to render formats in code rather than
  emit one canonical output for an agent to translate. Prompted by the question
  of whether translation would be simpler; it would, and it would put
  nondeterminism in the one place the project cannot afford it. Verified the
  `Format` interface stays cheap by implementing a fourth format outside the
  package in fourteen lines.
- 2026-07-27 — Built the diff engine and the Postgres DDL under it. Confidence
  Low → Medium: the pure-function claim held and is now covered by tests, and
  the generated DDL was applied to and reversed against a real Postgres 17 by
  hand — but that check is manual, not in CI. Recorded three constraints
  implementation surfaced — foreign keys are never inlined, phase ordering is a
  correctness property, and the concurrent-index split is not order-preserving
  — plus the decision never to generate a `USING` clause. Enum representation
  became large enough to need its own record, [ADR-0017](0017-enums-as-text-and-check.md).
- 2026-07-27 — Revised on learning the project uses goose. The original record
  specified separate `.up.sql`/`.down.sql` files, which is golang-migrate's
  convention, not goose's. Corrected to goose's single-file format, made the
  format pluggable, and added the concurrent-index file split after finding that
  `NO TRANSACTION` applies per file rather than per statement. The
  generate-don't-apply decision was unaffected and is now better supported: the
  project already has a runner it is happy with.
