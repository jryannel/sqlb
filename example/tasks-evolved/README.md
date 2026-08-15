# example/tasks-evolved — the second year

`example/tasks` has two migrations past its first, and both are additive:
labels, then an index. Nothing has ever asked what `migrate.Diff` does with a
change that is not additive — a rename, a widened enum, a `NOT NULL` backfill,
a column split into a table, a drop something else still names, an index added
against data that already breaks it. This module is that question, asked
against a real, running Postgres, six times in a row on the same database so
the answer includes what data does, not just what SQL gets generated.

It is [docs/special-cases.md](../../docs/special-cases.md)'s `tasks-evolved`
entry, the highest-value item on that census's list and — before this — the
only one nothing had measured. **It needed no new sqlb feature.** Every
mechanism below (`RenamedFrom`, `Destructive`, `AllowDestructive`, `Change.Up`
vs `Change.Down`) already existed. What nobody had done is run the loop a
second time, non-additively, and look at what came back.

`schema/schema.go` declares seven registries — `V0` through `V6`, plus the
`V1Bad` trap and the `V3Direct` shortcut — each a fresh `schema.NewRegistry()`
with its own `reg.Table(...)` calls. No registry is shared or mutated between
steps; that would make "before" and "next" the same Go value, which no real
migration ever diffs. `evolve_test.go` is one `TestTasksEvolved`, six ordered
`t.Run` subtests sharing one Postgres connection, because the entire point is
that data carries from one non-additive change to the next.

## What each step found

**1. Rename `status` → `state`.** The trap is the obvious first attempt:
delete the field named `status`, declare one named `state`. `migrate.Diff` has
no memory across two independent registries — it sees a column that vanished
and a column that appeared, and proposes exactly that: `DROP COLUMN status`,
`ADD COLUMN state`. Every value would be gone. The fix is one method,
`schema.Enum("state", ...).RenamedFrom("status")`, and it changes the proposal
to a single `ALTER TABLE tasks RENAME COLUMN "status" TO "state"` — which the
test applies and then confirms every seeded row's value survived under its new
name. `RenamedFrom` is needed for exactly one release; `V2` and everything
after it declare the column as plain `state`, because a stale hint would claim
a rename that already happened.

**2. Widen the enum.** `state` gains `"blocked"`. This is a real DDL rewrite —
Postgres has no `ALTER CONSTRAINT` for what a `CHECK` permits, so `Diff` drops
the old one and adds a new one — but it is not `Destructive`: nothing already
stored stops being valid. The test proves the negative first: inserting
`state = 'blocked'` against the three-value enum genuinely fails, then applies
the diff, then proves the same insert now succeeds and every existing row's
value is untouched. The comment in `schema.go`'s `V2` points at
`migrate/diff.go`'s own doc comment for why the comparison behind this reads
Postgres's *rendered* spelling of the constraint rather than the declared Go
string — a `CHECK` comes back from Postgres as a parenthesised, type-cast
parse tree, not the text it was declared with, and comparing anything else
proposes rebuilding every check on every diff, forever.

**3. `assignee_id NOT NULL`, with a backfill.** First the shape that looks
like the obvious way to add a required reference — UUID, `Ref(users)`, `NOT
NULL`, no default, one step (`V3Direct`, never applied). `Diff` marks the `ADD
COLUMN` `Destructive`, with a `Reason` naming exactly what's about to happen.
The test does not stop at reading that — it runs the one statement against a
`tasks` table that already has rows, and Postgres itself refuses:
`column "assignee_id" of relation "tasks" contains null values`. Nothing is
half-added; the statement is atomic. The two-step path is the actual answer:
add the column nullable (not `Destructive`), then a hand-written
`UPDATE tasks SET assignee_id = $1 WHERE assignee_id IS NULL` — DML, which
`migrate` does not and will not render, because it renders schema, not data —
then a second `Diff` between the nullable and the required shape, proposing
`SET NOT NULL`. That change is *still* marked `Destructive`, even though the
backfill already ran: `Diff` is a pure function over two `schema.Registry`
values, never a database, so it has no way to know a backfill happened. It
marks the risk it can see and lets the human hold the fact it cannot.

**4. Split `labels` into `task_labels`.** The ADR-0033 array-column shape,
reversed: that record chose an array specifically to avoid the join table this
step reintroduces. A task is seeded with two labels first. `Diff` creates
`task_labels(id, task_id, label)` alongside the still-present `labels` column
— an ordinary, non-destructive `CREATE TABLE`. Then hand-written DML —
`INSERT INTO task_labels (task_id, label) SELECT t.id, label FROM tasks t,
unnest(t.labels) AS label WHERE cardinality(t.labels) > 0` — copies every
array element into its own row, because `migrate` has no spelling for "unnest
this into a different table" and never will; that is exactly the DDL/DML line
step 3 already drew. Only once the copy is confirmed does a second `Diff`
propose dropping `labels` — `Destructive`, with a `Reason`, for the same
reason as step 3: the tool cannot see that the data already has a home
elsewhere. The test confirms both that the array column is gone and that
`task_labels` holds precisely what the array held.

**5. Drop `priority`.** The full doc entry imagines this as "a client
generated one commit ago" still selecting the dropped column; this lean module
has no client-codegen harness to check that against, so the step narrows to
what `Diff` and `Render` actually do, which is the mechanism that would have
protected that imagined client. The drop is `Destructive`, with a `Reason`.
Rendered through `migrate.Render` with default `Options`, the statement comes
back **commented out** — `-- ALTER TABLE "tasks" DROP COLUMN "priority";` —
behind a `DESTRUCTIVE:` note, not live. Rendered with
`Options{AllowDestructive: true}`, the identical statement comes back live,
uncommented. Both renders are asserted on directly. The test then applies the
change to the one running database anyway, by executing `Change.Up` — a choice
this test makes deliberately to keep one database moving into step 6, not
something `Diff` or `Render` did on its own.

**6. A partial unique index against data that already violates it.** "One task
in progress per assignee" — `AddIndex(schema.Index{Unique: true, Columns:
[]string{"assignee_id"}, Where: "state = 'doing'"})`. Two tasks are seeded
first, same assignee, both `state = 'doing'`: the violation. `Diff` renders
`CREATE UNIQUE INDEX ... WHERE state = 'doing'` and never touches the
database — it cannot know the data disagrees. Applying it does: Postgres
refuses with a duplicate-key error naming the index. That much matches the
doc's framing exactly. What the test found underneath it does not: an index
added to a table that already has rows is *always* rendered `CONCURRENTLY`
(`migrate/diff.go`'s `indexCreated`, unconditionally — building it locked
would block every writer for the duration). A plain `CREATE INDEX` that fails
rolls back atomically and leaves nothing behind. `CREATE INDEX CONCURRENTLY`
cannot, because Postgres builds it across more than one internal transaction —
so the failed build leaves an **invalid index occupying the name**. Reissuing
the identical statement Diff proposed does not reproduce the original
duplicate-key error; it fails with `relation "..." already exists`, which is a
worse error to hand an operator mid-incident because it no longer names the
real problem. The fix is in the same `Change`: `Down` for a concurrent index
build is `DROP INDEX CONCURRENTLY`, which is exactly the cleanup an invalid
index needs. The test drops it, fixes the data, and only then does the
identical `CREATE UNIQUE INDEX` succeed.

## What this settles, and what it narrows on purpose

Every mechanism above already shipped: `RenamedFrom`, the `Destructive` /
`Reason` pair, `AllowDestructive`, `Change.Up` and `Change.Down`. What this
module adds is evidence that running them **against each other, in sequence,
on live data** behaves the way their doc comments claim — including the one
place (step 6) where the doc comments say less than the database does.

Two simplifications versus the full `docs/special-cases.md` entry, both
because this is meant to stay lean:

- **No generated TypeScript client.** The doc entry imagines step 5 breaking a
  client generated one commit before the drop. `tasks-evolved` has no
  client-codegen harness — building one to exercise a single drop would be
  most of `example/tasks`'s own weight, for one assertion. What is checked
  instead is the mechanism that would have protected that client: the
  render-time gate, on both sides of `AllowDestructive`.
- **No `example/tasks` drift-gate replay.** `example/tasks/migrations` has its
  own history and its own gate (`migrations/drift_test.go`), and this module
  does not touch it, generate into it, or run alongside it — the task that
  built this was explicit that `example/tasks` stays untouched. `Diff` is
  exercised directly against in-memory registries instead of through a
  generated migration file replayed by goose, which is the same machinery
  `sqlb migrate` uses, minus the file I/O.

## Running it

```bash
mise run pg-up   # or point SQLB_TEST_POSTGRES at any Postgres 18
cd example/tasks-evolved
go mod tidy
SQLB_TEST_POSTGRES='postgres://sqlb:sqlb@localhost:15432/sqlb?sslmode=disable' \
  go test ./... -v -race
```

Postgres 18 is required, not merely preferred: `V0` bootstraps with
`migrate.MinPostgres(18)`, which emits the built-in `uuidv7()` rather than the
`pg_uuidv7` extension's spelling, so the first `CREATE TABLE` fails outright
against an older server — a true statement about this module, the same one
`example/fxapp`'s and `example/tasks`'s own tests make.
