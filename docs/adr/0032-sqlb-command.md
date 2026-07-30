# ADR-0032: The command compiles a driver, and the project declares itself in Go

- **Status:** Working — `sqlb generate` and `sqlb check` produce byte-identical
  output to the hand-written generators they replaced, and `mise run
  generate-check` is now the command rather than three bespoke mains. `sqlb
  migrate` is built and exercised against a real Postgres in `pgtest`
- **Confidence:** High on the mechanism — the driver compile runs end to end
  against this repository's own blog example. Medium on the convention, which has
  two projects behind it. Lower on `migrate`, whose first real run surfaced a
  round-trip defect since fixed (#24)
- **Decided:** 2026-07-29
- **Last reviewed:** 2026-07-29

## Context

The promise is *one edit, one command*. What a project actually wrote first was
`cmd/gen/main.go`: a `-check` flag, a `-dir` flag whose default was correct from
the module root and wrong from the directory `go generate` runs in, two error
branches, and a `codegen.Options` literal. Only the literal said anything about
the project. Both examples carried a copy, the copies had drifted, and
`mise run generate-check` invoked all of them by hand with different arguments.

The obstacle is [ADR-0004](0004-schema-as-go-dsl.md). The schema is Go, and a
table is registered by importing the package that declares it, so a prebuilt
binary cannot read a registry — there is nothing in one until the schema package
is linked in. `sqlc` and `atlas` ship a binary because their schema is a file
their binary parses. Ours is a program, and only another program can read it.

So the question is not whether to compile — that is forced — but where a project
declares what no amount of compiling reveals: which directories the emitters
write to.

## Decision

**`sqlb` writes a driver, compiles it inside your module, and deletes it.**

```
sqlb generate ./taskschema
sqlb check ./taskschema
```

The argument is the schema package in the form `go build` takes. The command
resolves it with `go list`, writes a three-line `main` to a temporary directory
*outside* the repository, builds it with the working directory at the module
root, runs it, and removes the directory — so there is no artefact to gitignore
and none left behind on a failure.

**The driver is three lines, and everything it could contain lives in
`codegen.Main`.** Generated code cannot be tested; the package it calls can.

**A project declares itself by exporting `SqlbProject() codegen.Project`.**

```go
// taskschema/sqlb.go
func SqlbProject() codegen.Project {
	return codegen.Project{
		Options: codegen.Options{
			Package: "tasks",
			TSDir:   "web/src/api",
			DartDir: "mobile/lib/api",
			CLIDir:  "cli",
			CLIName: "taskctl",
		},
	}
}
```

A convention rather than a config file, because a config file is a second
declaration language mirroring `codegen.Options` field for field, drifting
whenever a field is added, reporting its mistakes at run time. In Go the options
are compiler-checked and `go doc`-documented — and a project that outgrows the
convention can still write a `main` calling `codegen.Generate` directly.

**Paths resolve against the module root**, never the schema package and never the
working directory. That single rule deletes `-dir`: the command means the same
thing from a shell, a `//go:generate` directive and CI — the three callers the
old default had to be right for simultaneously, and was not.

**`Project` wraps `Options` rather than being it.** `Options` is the emitters;
`Project` is the repository, which also has the migration directory, the format,
the minimum Postgres version and the scratch database. Widening a struct is
invisible to every project; changing what `SqlbProject` returns is not. Five
fields landed a day later and no project's `SqlbProject` changed shape.

**`check` writes nothing and needs no database.** It is the drift gate and runs
on every push; the emitter half fails often and every project has it, so gating
it must not require a service container.

**`sqlb migrate` is the half that needs a database.** `check` asks whether the
committed output matches what the emitters produce, which is a pure function of
the schema. `migrate` asks whether the committed history *builds* that schema,
and the only trustworthy answer is replaying it into an empty Postgres
([ADR-0014](0014-migrations-and-import.md)). Two gates, two costs, kept apart.

**The scratch database is a function, not a DSN.** `Project.ShadowDB` opens it,
in the project because the driver has to enter through the consuming module and
because the database must be *empty* — creating and dropping databases needs
credentials the rest of sqlb never asks for, and dropping the wrong one is
unrecoverable. The statement that wipes a database is written out, by name, in
the repository that owns it. **The first migration needs no database at all**: an
empty history replays to an empty schema, which is what an empty registry is.

**Two halves, in two places.** `cmd/sqlb` never sees your schema: it resolves a
package, checks by AST that the convention function is there, compiles, and
forwards an exit code. `codegen.Main` and `codegen.Run` have the registry and are
ordinary tested code. The AST check earns its place by being unnecessary to
correctness — without it, a missing `SqlbProject` is a compile error inside a
temporary file the user cannot open.

## Consequences

**Buys.** The workflow is a command. Both examples deleted sixty lines of
generator that existed to be got right before the tool would run. Output is
byte-identical across all six emitters, which is the strongest available evidence
that this changed the interface and nothing else.

**Costs.** A compile — sub-second warm, the module's dependency graph cold, which
in `example/tasks` includes cobra and pgx. This is the bill for ADR-0004,
presented at the point of use instead of hidden in a per-project `main`, and it
will not get smaller. The command shells out to `go`, so `sqlb` is not a binary
you can drop into a container with no compiler.

**What building it changed**, each caught by a test written to fail:

- `Project.Validate` refuses an absolute `Dir`, and the first test helper handed
  it `t.TempDir()` — the guard fired on its own author.
- Build and run as separate steps, not one `go run`, which prints its own
  `exit status 1` underneath the list of stale files.
- **Versions cannot come from the clock.** `TimestampVersion` has one-second
  resolution, and the symptom of a collision is not a duplicate filename but
  `shadow` refusing to replay the history at all, several steps later. The
  version now comes from the directory: the timestamp is a starting point, and
  the highest present version is incremented if it does not already sort after.
- **A hand-written `schema.Check` never round-tripped** — `migrate.Diff` compares
  definitions as strings and Postgres returns a normalised form. A pre-existing
  defect ([#24](https://github.com/jryannel/sqlb/issues/24)), since fixed.

**The declared CHECK, and why the fix needed a database.** Postgres stores a
parse tree and renders it canonically, so the author's spelling is unrecoverable
and the two sides can only agree by putting the declared expression through the
same normalisation. Canonicalising both strings in Go was rejected on consequence
asymmetry: stripping parentheses loses information — `(a OR b) AND c` and
`a OR (b AND c)` reduce alike — so a heuristic can call two different constraints
equal, and a diff that says "unchanged" produces no migration at all. That
failure is silent; the one it replaces is loud, visible churn.

So `shadow.NormalizeChecks` adds each declared expression to the replayed table,
reads back what Postgres stored, and rolls back — correct by construction, at one
round trip per check. Each probe takes a savepoint, because Postgres aborts a
transaction on any error and one unprobeable check would take down every check
after it. A check that cannot be probed is reported and left as declared, since
the ordinary reason is that it names a column this migration adds. `migrate.Diff`
is untouched and still a pure function; the impurity is in a separate call the
caller makes first.

With that fixed the check became a gate: `example/tasks/migrations/drift_test.go`
is the only thing in the build that can catch a schema edited without a
migration. Writing it found one more thing — an earlier version applied
migrations with goose, whose `goose_db_version` table came back from
introspection as a table the declaration does not have, so the gate proposed
dropping it. That shadow writes no version table is a line in its package doc;
this is what the line is for.

## What would change our mind

- Projects start writing a `main` again to do something `Project` cannot express
  — widen the type before the pattern sets, because a hand-written generator that
  works is one nobody comes back from.
- `SqlbProject` wants arguments — a profile, a target, a variant. The convention
  is the wrong shape, and the answer is a function taking a named configuration.
- The compile cost is felt in the inner loop rather than CI — cache the built
  driver keyed on the module's build ID, do not abandon the mechanism.
- A schema becomes readable without compiling — a manifest complete enough to
  emit from, which `sqlb.json` is not today.
- More than one schema package per module becomes normal — `Project` should name
  its own registries.

## Cost of change

**The driver, the temp directory and the two-step build are private** and can be
rewritten without a consumer noticing. **`SqlbProject` is a name in other
people's repositories**, so renaming it breaks every adopter — loudly, which is
the good kind; `codegen.ProjectFunc` exists so the name is written once here.
**The module-root rule is the expensive one**: every path in every adopting
project is written against it, and changing it would silently relocate generated
files rather than fail.

## Revisions

- 2026-07-29 — Written, after building `generate` and `check`.
- 2026-07-29 — `sqlb migrate` added, with `Project`'s migration fields and
  `ShadowDB`. The wrapper paid for itself as predicted. Two things learned:
  versions must come from the directory, and a declared CHECK never round-tripped.
- 2026-07-29 — #24 fixed with `shadow.NormalizeChecks`. Worth a revision rather
  than an edit: the fix is only available because the command already had a
  database open at the right moment, which was not an argument this record made
  for the design and is now one of the better ones.
- 2026-07-30 — Condensed.
