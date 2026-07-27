# Handoff — migrate-diff-engine  (2026-07-27 16:26)

Supersedes [`20260727-1327-sqlb-foundation.md`](20260727-1327-sqlb-foundation.md),
which is still the better read for the project as a whole. This one covers the
migration diff engine, which is what the session after it built.

## Where
- Repo / worktree: `/Users/jryannel/dev/tmp/sqlb`
- Remote: https://github.com/jryannel/sqlb — **private**
- Branch: `main` @ `82e42bc` · 0 unshipped commits · 0 uncommitted files · CI green
- 25 commits, 47 Go files (~14.1k lines), 7 packages, 18 ADRs

Everything is committed and pushed. No checkpoint was needed.

## Goal

A schema-first data layer for Go and Postgres (see the foundation handoff for
the full statement). This session finished the **migration diff engine**: the
part that turns a schema edit into migration files a runner can apply, without
sqlb ever connecting to a database.

The through-line of the whole session: *a generated migration is the one
artefact where being wrong is not recoverable by regenerating.* So the engine
prefers stating a fact over guessing, and every claim it makes about Postgres
was measured against a real Postgres rather than recalled.

## Done so far

Five commits, each independently green (`mise run bisect-check`):

- **`3f91a2a`** — `migrate.Diff(current, target *schema.Registry)` and the
  Postgres DDL under it. Phase ordering as a correctness property; foreign keys
  never inlined; the concurrent-index split is not order-preserving.
- **`26dcdd2`** — **renames**. `schema.RenamedFrom` on a column or a table;
  without it a rename is still a drop and an add. A renamed column carries its
  index and constraints with it via catalog-only `RENAME CONSTRAINT` /
  `ALTER INDEX … RENAME`, because Postgres does not rename them for you and the
  derived names would otherwise diverge permanently.
- **`b6d3652`** — **lock hazards**. `Change.Lock` / `Change.Hazard` name the
  lock a change holds for a time proportional to the row count, plus the
  sequence to use instead. `Migration.Blocking()` lists them. Stated, not
  gated — see the decision note below.
- **`b20194d`** — **`migrate.Unblock`**, generating the `NOT VALID` +
  `VALIDATE CONSTRAINT` sequences for `CHECK`, `FOREIGN KEY` and `SET NOT NULL`.
  Introduced `Change.Stage` (replacing `Concurrent bool`) and multi-file splits.
- **`82e42bc`** — the unique-constraint sequence: `CREATE UNIQUE INDEX
  CONCURRENTLY` plus `ADD CONSTRAINT … USING INDEX`. Every remedy the hazard
  notes name is now one the tool can write.

Also `215b0bd` (from a spun-off background session): `mise run fmt`/`fmt-check`
took their file list from the filesystem and so reached into `.claude/worktrees/`.
`fmt` would have *rewritten* another session's checkout. Both now use
`git ls-files`.

### The measurements

Every lock claim in the generated comments was checked against a real Postgres 18
by reading `pg_locks` inside the open transaction. `SET NOT NULL`,
`ALTER COLUMN … TYPE`, `ADD CHECK` and `ADD UNIQUE` take `ACCESS EXCLUSIVE`;
`ADD FOREIGN KEY` takes `SHARE ROW EXCLUSIVE`; `VALIDATE CONSTRAINT` takes
`SHARE UPDATE EXCLUSIVE`. Rewrite exemptions checked by watching `relfilenode`.

| | plain | after `Unblock` |
|---|---|---|
| `SET NOT NULL` + two validating constraints | `ACCESS EXCLUSIVE` ~1.2 s | ~2 ms |
| `ADD CONSTRAINT … UNIQUE` | `ACCESS EXCLUSIVE` ~2.3 s | ~1.2 ms (build ~2.6 s, writes open) |

Both end byte-identical to the plain form and reverse to where they started.

**These checks are manual and not in CI.** They were run from throwaway
databases against the local `services-db-1` container (pgvector/pgvector:pg18,
localhost:5432, postgres/postgres), each dropped afterwards. The scripts are
gone with the scratchpad — rewriting them is an hour, and
[ADR-0014](../docs/adr/0014-migrations-and-import.md) says the habit of
remembering to re-run them is itself a guard that will eventually not be.

## In progress / not finished

Nothing is half-done or broken. Unbuilt, in priority order:

1. **Where `current` comes from.** This is the one that matters: `Diff` compares
   two registries and *nothing produces one from a database*. Both sides are
   hand-written today, which makes the engine unusable end to end however good
   the diff is. Needs either `sqlb import` reading `pg_catalog`, or the shadow
   database that replays the existing migration history (ADR-0014 prefers the
   latter; it validates the history and catches drift).
2. **The Postgres round-trip in CI.** Blocked on a real decision, see below.
3. **REST handlers + OpenAPI** — ADR-0007, still Exploring/Low.
4. **`?expand`** — `filter` validates relation names, performs no join.
5. **Change feed** — ADR-0012, nothing built.

## Next steps

1. **Decide the current-state source and build it.** Everything else in the
   migrate package is speculative until a schema can be read out of a database.
   Start with `sqlb import`: it is the smaller piece, it is what makes adoption
   possible at all, and the shadow database can reuse its introspection.
2. When writing it, remember the round-trip constraint from ADR-0014: generated
   names must match what Postgres actually stores, or the first diff after an
   import drops and recreates every constraint. `Named`, `ConstraintNamed` and
   `PrimaryKeyNamed` exist for this.
3. Postgres 18 names `NOT NULL` constraints `<table>_<column>_not_null` and does
   not rename them with the table. An importer reading `pg_catalog` has to
   ignore them, or every renamed table will diff against itself forever.
4. **Settle the file format before any generated migration is applied anywhere
   real.** ADR-0014's Cost of change still says this is the most expensive thing
   in the record to alter later, and this session changed the file naming twice
   (`_validate`, `_indexes`, `_constraints`).

## How to verify

```bash
mise run ci             # full gate: fmt-check vet lint tidy-check deps-check generate-check test-race
mise run test           # inner loop; no Docker or Postgres needed
mise run bisect-check   # every commit builds/vets/tests in isolation
```

No database is required: engine tests run against an in-memory `database/sql`
driver defined in `sqlb_test.go`. The migrate package's ~2.1k lines of tests are
all pure functions over two registries.

## Open questions / decisions pending

- **Automating the Postgres round-trip costs more than ADR-0014 assumed.** Every
  Postgres driver is a third-party module and `deps-check` asserts the standard
  library alone. The choice is: give up that gate, or move the round-trip into a
  module of its own. Nobody has decided.
- **No minimum Postgres version is declared anywhere.** It now matters: the
  `SET NOT NULL` sequence is only *fast* on 12+, though it is correct on older
  ones. Worth stating in the README.
- **`Unblock` is all-or-nothing.** It rewrites every eligible change in the
  list. On a migration touching one huge table and one tiny one, the tiny one
  gets a pointless file split. Fine so far; if it grates, the answer is probably
  filtering by table rather than an option on `Unblock`.
- **Lock hazards are stated, not gated** — deliberate, with the revisit trigger
  recorded in ADR-0014: *an outage caused by a statement that said in the file
  above it exactly what it was about to do.*
- **Repo visibility** — still private.
- **`go.mod` is 1.25.7 while mise pins 1.26.4** — still deliberate, still worth
  a conscious confirm.
- CI warns that `actions/checkout@v4` and `jdx/mise-action@v2` target the
  deprecated Node 20. Not breaking anything; worth a bump.

## Key files & pointers

- `migrate/diff.go:91` — `Diff`. The package doc above it is the map: what
  Destructive means, what Lock means, what is not inferred, and the phase order.
- `migrate/diff.go:213` — `currentFor`; a rename hint loses to a name match,
  which is what makes a stale hint a no-op rather than a second rename.
- `migrate/diff.go:650,671,704` — the three lock-brief sequences.
- `migrate/migrate.go:36` — `Stage`, and the `stages` table under it. **Read
  this before touching ordering.** Order and file are separate fields because
  they answer different questions, and a lock is held until the transaction
  commits rather than until the statement ends — which is why `StageFinish` and
  `StageAdopt` must follow every scan sharing their file.
- `migrate/migrate.go:194` — `Unblock`; `:414` — `Blocking`.
- `migrate/ddl.go:145` — `rewrites`; the only type-change rewrite exemption, and
  why it is the text family only.
- `migrate/ddl.go:425` — `rewriteIdents`. Safe *because its result is only ever
  compared, never rendered* — a misread costs a drop and an add, not a wrong
  statement. That property is load-bearing; do not start rendering from it.
- `schema/field.go:261`, `schema/table.go:219` — `RenamedFrom`.
- `docs/adr/0014-migrations-and-import.md` — the whole design and every
  measurement. Long, and the single most useful thing to read first.
- `docs/adr/0016-guards-proven-both-ways.md` — read before adding any CI guard.

### Two mistakes made twice, worth not making a third time

Both were **a change that is correct alone and wrong next to its neighbours**:

- A concurrent index drop is moved by `Split` into a file that runs *after* the
  column drop it depends on — so an index over a disappearing column drops
  non-concurrently.
- Three `VALIDATE CONSTRAINT`s scheduled after a `SET NOT NULL` in the same
  transaction did their scans underneath its `ACCESS EXCLUSIVE` — the exact lock
  the sequence exists to escape.

Any new stage or any new concurrent change has to be checked against what else
lands in its file, not just against its own correctness. This was found by
reading generated output, not by a failing test.
