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

**Lock hazards are stated, not gated.** A statement that rewrites a table, scans
it, or builds an index over it holds its lock for a time proportional to the row
count, and is emitted with the lock named and the expand/contract sequence
spelled out — but emitted live. Destructive is commented out because applying it
is irreversible; this is reversible, it is just occasionally very slow, and
whether it is slow depends on how many rows the table holds. That number is not
in the schema and never will be. A generator that made every `SET NOT NULL`
require an edit would be useless for the ordinary case and would teach people to
edit without reading, which is the same failure mode that would break the
destructive guard. `Migration.Blocking` is the hook for a project that does know
which of its tables are big.

**The lock-brief sequences are generated, and chosen rather than assumed.**
`migrate.Unblock` rewrites the changes whose remedy is a fixed sequence: an
`ADD CONSTRAINT` that would scan becomes an `ADD … NOT VALID` and a `VALIDATE
CONSTRAINT` in a later migration, and a `SET NOT NULL` becomes the same pair
with the requirement set between them. Calling it is the caller's decision for
two reasons. The sequence is longer, splits the migration across files, and buys
nothing on a table small enough that the scan is instant. And it is not
equivalent *under failure*: a plain `ADD CONSTRAINT` that meets a bad row leaves
nothing behind, while this one leaves the constraint in place, unvalidated and
binding from that moment. On success the end states are identical, which is what
makes the substitution safe at all — the temporary check a `SET NOT NULL` needs
is dropped by the same sequence that created it.

**Renames are declared, never inferred.** A column or a table carries
`.RenamedFrom("old")` for one release; the diff reads it and emits
`ALTER TABLE … RENAME` instead of a drop and an add. Without the hint, a rename
is a drop and an add, which is correct if lossy and never silently wrong.

A hint whose old name is no longer there is ignored rather than rejected. It has
to be: the migration it produced was generated once, and after that every
database it was applied to knows only the new name. Making a leftover hint an
error would mean every rename came with a scheduled build break.

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
error)`, the Postgres DDL under it, rename hints, and lock-hazard detection.
The symmetry claim held:
the diff is a pure function, tested exhaustively without a database. Still
unbuilt: the shadow database that produces `current`, and `sqlb import` — so in
practice `Diff` has no way to learn the current state except from another
hand-written registry.

The generated DDL has been applied to a real Postgres and reversed twice, by
hand: once on 17 for the blog schema and an incremental migration exercising
every kind of change the diff emits, and once on 18 for a rename of a table and
a column carrying an index, a unique constraint and a foreign key, alongside an
added column, a dropped one and a changed enum. Each was applied forward
against seeded rows and then reversed, ending in the exact structure it started
from. That is what raised confidence to Medium.

It is not High because the check is manual and not in CI — the test suite
deliberately needs no database, so the round-trip that proves the DDL is *valid*
rather than merely *expected* has to be re-run by hand whenever the DDL layer
changes. Automating it behind a build tag is the obvious next increment, and it
now has a cost attached that the record did not anticipate: every Postgres
driver is a third-party module, and this project currently depends on the
standard library alone and has a CI gate asserting it. Automating the
round-trip means either giving that up or moving the test into a module of its
own.

One lock hazard is deliberately not detected: `ADD COLUMN` with a *volatile*
default rewrites the table, and a non-volatile one does not. Volatility is a
property of the function, which lives in the database — `now()` is stable and
`gen_random_uuid()` is not, and `schema.Expr` takes arbitrary SQL. The package
could recognise the two generators it ships and miss every hand-written
equivalent, which is worse than not looking: a guard that fires sometimes reads
as coverage ([ADR-0016](0016-guards-proven-both-ways.md)). The complete answer
is to ask the database, which is the shadow-database dependency that does not
exist yet, so this waits for it.

One thing the second round-trip surfaced, worth recording because it will look
alarming to whoever meets it first: Postgres 18 names `NOT NULL` constraints
after the table and column they came from, and does not rename them when either
is renamed — so a renamed table keeps `orgs_id_not_null` indefinitely. That is
invisible here only because sqlb neither generates nor compares those names. If
a future `sqlb import` reads them from `pg_catalog`, it will have to keep
ignoring them, or every renamed table will diff against itself forever.

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

Renames surfaced three more:

- **Where the renames sit in the order is the design.** Everything emitted
  before them is written in the old names and everything after in the new ones,
  so that reversing the list reverses each half while the names it was written
  against are the ones in effect. Putting renames first — the obvious choice —
  leaves every constraint drop's `Down` re-adding a constraint against a column
  that no longer answers to that name. The `Down` is only free if the `Up` is
  ordered for it.
- **A rename is not one statement.** Postgres does not rename a constraint or
  an index when the table or column it is named after is renamed, and this
  package derives those names from the table and column. So the rename has to
  carry its dependent objects with it explicitly. Otherwise the derived names
  diverge permanently: the very next diff sees `orgs_pkey` where it expects
  `organisations_pkey`, and proposes dropping and rebuilding it — every time,
  forever.
- **The rewritten schema is compared, never rendered.** To recognise an index
  that merely follows a renamed column, the current schema has to be read as it
  will be *after* the rename. That rewrite has to touch hand-written SQL — a
  `CHECK` expression, a partial index predicate — which is exactly the kind of
  guessing this record rejects elsewhere. It is safe here for a structural
  reason worth stating: the rewritten form only ever feeds a comparison. A drop
  emits the original definition and an add emits the target's, so the worst a
  misread can do is fail to match, which costs a drop and an add rather than
  producing a wrong statement. A heuristic is acceptable when it cannot reach
  the output.

Lock hazards surfaced two more:

- **The lock modes were checked against a database, not recalled.** Every claim
  the generated comments make was measured on a real Postgres 18 by reading
  `pg_locks` inside the open transaction: `SET NOT NULL`, `ALTER COLUMN … TYPE`,
  `ADD CHECK` and `ADD UNIQUE` take `ACCESS EXCLUSIVE`, `ADD FOREIGN KEY` takes
  `SHARE ROW EXCLUSIVE`, and `VALIDATE CONSTRAINT` takes `SHARE UPDATE
  EXCLUSIVE`. The rewrite exemptions were checked by watching `relfilenode`, and
  the claim that a validated `IS NOT NULL` check lets `SET NOT NULL` skip its
  scan was checked by timing it on three million rows: 1.7 ms against 108 ms.
  One claim was wrong and is now corrected — the `NOT VALID` add still takes the
  strong lock, briefly, so the remedy is not lock-free, only lock-*brief*.
  Advice in a generated file is read as authoritative, which is a reason to
  measure it rather than a reason to hedge it.
- **The remedies are mechanical enough to generate.** `NOT VALID` plus
  `VALIDATE CONSTRAINT` is now generated by `migrate.Unblock`; `CREATE UNIQUE
  INDEX CONCURRENTLY` plus `ADD CONSTRAINT … USING INDEX` is not yet. What
  looked like the obstacle — that each wants its second half in a *later*
  migration — turned out to be the file split this package already had.

Generating those sequences surfaced two more:

- **A lock is held until the transaction commits, not until the statement
  ends** — so ordering inside a file is as load-bearing as ordering between
  files. The first cut put every generated statement that follows a validation
  in the same file, which is correct in isolation and wrong in company: a
  `SET NOT NULL` takes `ACCESS EXCLUSIVE` and keeps it, so the *other*
  constraints' validations, scheduled after it, did their scans underneath the
  exact lock the sequence exists to escape. The fix is a fourth stage that
  shares the validate file and always follows it, so every scan happens before
  any strong lock is taken. The general shape of this mistake — a change that is
  safe alone and unsafe next to its neighbours — is the same one the concurrent
  index split produced, which is twice now.
- **The rollback of a validation is not symmetric, and that is fine.** There is
  no statement that un-proves a constraint, so rolling back only the validate
  file leaves the constraints validated rather than `NOT VALID`. Measured and
  accepted: the state is stricter than it was, never looser, and rolling back
  the file that added them removes them outright. The generated `Down` says so
  rather than rendering as an unexplained gap.

The result was measured on three million rows, applying the generated files to a
real Postgres 18 and reporting `pg_locks` after each statement. The plain
migration holds `ACCESS EXCLUSIVE` for about 1.2 seconds — its first
`SET NOT NULL` takes the lock and the remaining half-second of scans runs
underneath it. The generated sequence does every scan under `SHARE UPDATE
EXCLUSIVE` and holds `ACCESS EXCLUSIVE` for about 2 milliseconds. Both databases
end in a byte-identical structure, and the sequence reverses to the state it
started from.

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
- If rename hints turn out to be forgotten in practice — a schema edit that
  should have carried one and did not, caught only by the destructive guard —
  the hint is in the wrong place. The alternative is a review step that lists
  every drop-and-add and asks whether it was meant, which is a worse developer
  experience and a better safety property.
- If matching a constraint or an index by definition when its name changed ever
  pairs the wrong two objects, that rule has to go. It is safe today because
  two constraints with identical definitions are interchangeable, and that
  stops being true the moment a definition stops fully describing the object.
- If the shipped formats stop being ~15 lines each, the `Format` interface is
  carrying the wrong responsibilities and the shared semantics have leaked into
  it. That is a design signal, not a reason to add more formats.
- If the DDL layer changes and the manual Postgres round-trip is not re-run,
  that is the signal it needs to become a build-tagged test rather than a
  habit. A habit that has to be remembered is a guard that will eventually not
  be ([ADR-0016](0016-guards-proven-both-ways.md)).
- If lock hazards turn out to be scrolled past — an outage caused by a statement
  that said in the file above it exactly what it was about to do — then stating
  them is not enough and they need to become a refusal with an opt-out, the way
  destructive changes are. The reason not to start there is that the warning
  fires on ordinary migrations against small tables, where it is noise; if that
  turns out not to be true in practice, the balance changes.

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

- 2026-07-27 — Generated the `NOT VALID` plus `VALIDATE CONSTRAINT` sequences,
  which the entry below said were mechanical enough to generate and were not.
  The obstacle named there — that the second half wants a later migration —
  turned out to be the file split this package already had. `migrate.Unblock`
  performs the substitution and is called rather than applied, because the
  sequence is not equivalent under failure and buys nothing on a small table.
  Recorded two constraints implementation surfaced: a lock is held until the
  transaction commits, so ordering *inside* a file matters as much as ordering
  between them — the first cut had three validations scanning underneath a
  `SET NOT NULL` — and a validation's rollback is not symmetric, which is
  acceptable because the asymmetry is always in the strict direction. Measured
  on three million rows: `ACCESS EXCLUSIVE` held for ~1.2 s becomes ~2 ms, with
  a byte-identical end state. `CREATE UNIQUE INDEX CONCURRENTLY` plus
  `ADD CONSTRAINT … USING INDEX` is the one mechanical remedy still unwritten.
- 2026-07-27 — Built lock-hazard detection, the last thing this record named as
  needed and did not have. A change that rewrites a table, scans it or builds an
  index over it now carries the lock it takes and the expand/contract sequence
  to use instead. Decided it states rather than gates, because whether a scan
  matters depends on a row count the schema does not contain, and a warning that
  forces an edit on every ordinary migration is a warning that stops being read.
  Every lock mode in the generated text was measured against a real Postgres
  rather than recalled, which caught one wrong claim. Recorded that the remedies
  are mechanical enough to generate and are not, because each needs its second
  half in a later migration — a release-sequencing decision. Also recorded the
  one hazard deliberately left undetected, `ADD COLUMN` with a volatile default,
  and why it waits for the shadow database.
- 2026-07-27 — Built rename hints, the half of the diff this record specified
  and the previous increment left out. Confidence stays Medium: the pure
  function claim held again, and the DDL was round-tripped against a real
  Postgres 18 — but by hand, which is the same gap as before. Recorded three
  constraints implementation surfaced: the renames' position in the phase order
  is what makes the `Down` free, a rename has to carry the constraints and
  indexes named after what it renamed, and a heuristic that only ever feeds a
  comparison is safe in a way the same heuristic in the output would not be.
  Also noted that automating the round-trip costs more than the record assumed,
  since every Postgres driver is a third-party module and the project gates on
  having none.
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
