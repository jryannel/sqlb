# ADR-0032: The command compiles a driver, and the project declares itself in Go

- **Status:** Working — `sqlb generate` and `sqlb check` produce byte-identical
  output to the hand-written generators they replaced in both examples, and
  `mise run generate-check` is now the command rather than three bespoke mains.
  `sqlb migrate` is built and exercised against a real Postgres in `pgtest`
- **Confidence:** Medium on the convention, High on the mechanism — the driver
  compile is exercised end to end against this repository's own blog example, so
  a green test is a real compile against a real module; the convention has one
  day of use and two projects behind it. Lower on `migrate`, which works, and
  whose first real run surfaced a round-trip defect that has since been fixed
  (#24)
- **Decided:** 2026-07-29
- **Last reviewed:** 2026-07-29

## Context

The promise is *one edit, one command*. What a project actually had to write
first was `cmd/gen/main.go`: a `-check` flag, a `-dir` flag whose default was
correct from the module root and wrong from the directory `go generate` runs in,
two error branches, and a `codegen.Options` literal. Only the literal said
anything about the project. Both examples in this repository carried a copy, the
copies had drifted, and `mise run generate-check` invoked all of them by hand
with different arguments.

The [adoption evaluation](../review-adoption-existing-app.md) ranks closing this
loop first of six — cheapest to do, and the one that makes an existing claim
literally true rather than adding a new one.

The obstacle is [ADR-0004](0004-schema-as-go-dsl.md). The schema is Go, and a
table is registered by the side effect of importing the package that declares
it. A prebuilt binary therefore cannot read a registry: there is nothing in one
until the schema package is linked in. `sqlc` and `atlas` ship a binary because
their schema is a file the binary can parse. Ours is a program, and the only
thing that can read a program is another program compiled with it.

So the question is not whether to compile — that is forced — but where a project
declares the part of itself that no amount of compiling reveals: which
directories the emitters write to.

## Decision

**`sqlb` writes a driver, compiles it inside your module, and deletes it.**

```
sqlb generate ./taskschema
sqlb check ./taskschema
```

The argument is the package that declares the schema, in the form `go build`
takes. The command resolves it with `go list`, writes a three-line `main` to a
temporary directory outside the repository, builds it with the working directory
set to the module root, runs it, and removes the directory. `go run` with an
absolute path resolves imports against the module in the working directory,
which is what makes a driver outside the tree possible at all — nothing is ever
written into the user's repository, so there is no artefact to gitignore and
none to leave behind on a failure.

**The driver is three lines, and everything it could contain lives in
`codegen.Main`.** Generated code cannot be tested; the package it calls can.
What the emitted file does is import the schema package under a name, and pass
the result of one function to one call.

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

A convention rather than a config file, because the alternative is a second
declaration language that mirrors `codegen.Options` field for field, drifts from
it whenever a field is added, and reports its mistakes at run time. In Go the
options are checked by the compiler, documented by `go doc`, and reachable by
every refactoring tool that understands a struct literal — and a project that
outgrows the convention can still write a `main` calling `codegen.Generate`
directly, which is what `example/withsqlc` does for an artefact `codegen` does
not emit.

**Paths resolve against the module root**, never against the schema package and
never against the working directory. That is the single rule that deletes
`-dir`: `sqlb generate ./taskschema` means the same thing from a shell, from a
`//go:generate` directive, and from CI, which are the three callers the old
default had to be correct for simultaneously and was not.

**`Project` wraps `Options` rather than being it.** `Options` is the emitters;
`Project` is the repository, and the repository has more in it — the migration
directory, the format, the minimum Postgres version, and the scratch database
`sqlb migrate` replays into. Widening a struct is invisible to every project;
changing what `SqlbProject` returns is not. Those five fields landed a day after
the type did, and no project's `SqlbProject` changed shape.

**`check` writes nothing and needs no database.** It is the drift gate, it runs
on every push, and keeping it free of Postgres is deliberate: the emitter half
of the loop is the half that fails often and the half every project has, so
gating it must not require a service container.

**`sqlb migrate` is the second half, and it is the one that needs a database.**
`check` asks whether the committed output matches what the emitters produce now,
which is a pure function of the schema. `migrate` asks whether the committed
migration history *builds* that schema, and the only trustworthy way to answer
it is to replay the history into an empty Postgres — reading a live database
reports what it looks like, not whether the migrations produce it
([ADR-0014](0014-migrations-and-import.md)). Two gates, two costs, kept apart
for that reason.

Its flags are the modes that write nothing (`-check`, `-dry-run`) and the two
that change what is written (`-unblock`, `-allow-destructive`). `-name` is
required for a write and not for the others, because it lands in a filename
`migrate.Write` refuses to overwrite afterwards.

**The scratch database is a function, not a DSN.** `Project.ShadowDB` opens it,
and it lives in the project for two reasons that point the same way. sqlb has no
Postgres driver — the engine is standard library only, and `deps-check` keeps it
that way — so the driver has to enter through an import in the consuming module.
And the database has to be *empty*, which `shadow.Build` will not make it:
creating and dropping databases needs credentials the rest of sqlb never asks
for, and dropping the wrong one is unrecoverable. The destructive half stays with
the caller who knows which database is scratch, and `ShadowDB` is exactly where
that caller lives — so the statement that wipes a database is written out, by
name, in a file in the repository that owns it.

**The first migration needs no database at all.** An empty history replays to an
empty schema, which is what an empty registry already is, so the baseline is
answered without connecting. Adopting sqlb therefore does not cost a scratch
Postgres until the second migration.

### Two halves, in two places

`cmd/sqlb` cannot see your schema and never tries: it resolves a package, checks
by AST that the convention function is there, compiles, and forwards an exit
code. `codegen.Main` and `codegen.Run` are the half that has the registry, and
they are ordinary tested code in the library. The seam is the generated file,
and it is deliberately too small to hold a bug.

The AST check earns its place by being unnecessary to correctness. Without it, a
missing `SqlbProject` is a compile error inside a temporary file the user cannot
open, naming a package they did not write. With it, it is a sentence naming the
file to create and the function to put in it.

## Consequences

**What this buys.** The workflow is a command. Both examples deleted their
generator — sixty lines that existed to be got right before the tool would run —
and `mise run generate-check` is now two invocations of the same binary instead
of three bespoke ones. A project adopting sqlb writes one function instead of
one program, and `sqlb check` in CI is a line, not a script.

The output is byte-identical to what the hand-written generators produced, in
both examples, across all six emitters. That is the strongest available evidence
that this changed the interface and nothing else.

**What this costs.** A compile. On a warm cache it is well under a second; on a
cold one it is the module's dependency graph, and in `example/tasks` that
includes cobra and pgx. `sqlc` and `atlas` pay nothing here because their input
is a file. This is the bill for ADR-0004, presented at the point of use instead
of hidden in a per-project `main`, and it is not going to get smaller.

The command shells out to `go`, so it needs a Go toolchain on `PATH` — fine for
a code generator, and worth stating because it means `sqlb` is not a binary you
can drop into a container that has no compiler.

**What building it changed.** Four things the design did not anticipate, each
caught by a test written to fail:

- `Project.Validate` refuses an absolute `Dir`, and the first version of the
  test helper handed it `t.TempDir()`. The guard fired on its own author, which
  is the ADR-0016 property arriving for free.
- Building and running as separate steps rather than one `go run`, because
  `go run` prints `exit status 1` of its own on a non-zero exit. On a
  `sqlb check` failure that lands underneath the list of stale files and reads
  like a second, unexplained error.
- **Versions cannot come from the clock.** `migrate.TimestampVersion` has
  one-second resolution, so two migrations generated in the same second collide
  — and the symptom is not a duplicate filename but `shadow` refusing to replay
  the history *at all*, several steps later, because the order the two applied
  in is recorded nowhere. Nothing about the second `sqlb migrate` looks like a
  failure at the time. The version is now taken from the directory: the
  timestamp is a starting point, and if it does not already sort after every
  version present, the highest one is incremented instead.
- **A hand-written `schema.Check` never round-tripped.** Running `migrate
  -check` against `example/tasks` proposed dropping and re-adding the same CHECK
  constraint on every run, because `migrate.Diff` compares constraint
  definitions as strings and Postgres hands back a normalised form —
  parenthesised, with an explicit `::text` cast. A pre-existing defect in the
  introspect/diff round trip rather than one this command introduced; nothing
  had previously compared a *declared* CHECK against an *introspected* one. It
  was [issue #24](https://github.com/jryannel/sqlb/issues/24), and it is fixed
  by `shadow.NormalizeChecks` — see below. Enums were never affected.

### The declared CHECK, and why the fix needed a database

Fixing #24 is where the shape of this command paid for itself a second time.

Postgres does not store a check expression as it was written; it stores a parse
tree, and renders it back canonically. There is no way to recover the author's
spelling, so the two sides of the comparison can only be made to agree by
putting the *declared* expression through the same normalisation — which means
asking a Postgres.

The obvious alternative was to canonicalise both strings in Go: strip redundant
parentheses, drop `::type` casts, collapse whitespace. It was rejected on
consequence asymmetry, the same test [ADR-0014](0014-migrations-and-import.md)
applies to inferring renames. Stripping parentheses loses information —
`(a OR b) AND c` and `a OR (b AND c)` reduce to the same string — so a heuristic
can report two genuinely different constraints as equal, and a diff that says
"unchanged" about a constraint that changed produces no migration at all. That
failure is silent and the schema edit never reaches the database. The failure it
would replace is churn: loud, visible, harmless. Trading a loud wrong answer for
a quiet one is the wrong direction.

So `shadow.NormalizeChecks` adds each declared expression to the replayed table,
reads back what Postgres stored, and rolls the whole thing back. Correct by
construction rather than by approximation, at one round trip per check. It lives
in `shadow` because that package already exists to answer questions by asking a
throwaway database, and it runs at the one moment the answer is available: the
shadow database is open, and the tables the checks refer to are in it.

Three details the implementation forced. Each probe takes a savepoint, because
Postgres aborts a transaction on any error and one unprobeable check would
otherwise take every check after it down — making the failure look like a
property of the next table. A check that cannot be probed is reported and left
as declared rather than failing the run, since the ordinary reason is that it
names a column this very migration adds, and such a check is new anyway. And a
table the replay did not produce is skipped silently, because every check on
every new table would otherwise arrive as a warning about a comparison nobody
was going to make.

`migrate.Diff` is untouched and still a pure function over two registries, which
is the property ADR-0014 rests on. The impurity is in a separate call the caller
makes first.

## What would change our mind

- If projects start writing a `main` again to do something `Project` cannot
  express, the type is too narrow — widen it before the pattern sets, because a
  hand-written generator that works is one nobody comes back from.
- If `SqlbProject` turns out to want arguments — a profile, a target, a
  variant — the convention is the wrong shape and the answer is a function
  taking a named configuration, not a second convention.
- If the compile cost is felt in the inner loop rather than in CI, the answer is
  caching the built driver keyed on the module's build ID, not abandoning the
  mechanism.
- If a schema ever becomes readable without compiling — a manifest complete
  enough to emit from, which `sqlb.json` is not today — then the binary can read
  that instead, and this record is about the era in which it could not.
- If more than one schema package in one module becomes normal, the single
  package argument is wrong and `Project` should name its own registries.

## Cost of change

Cheap in the mechanism, moderate in the convention.

**The driver, the temp directory and the two-step build are private.** Nothing
depends on them; they can be rewritten without a consumer noticing.

**`SqlbProject` is a name in other people's repositories.** Renaming it breaks
every project that adopted it, silently in the sense that the failure is a
missing function rather than a wrong result — which is the good kind of break,
but a break. The constant `codegen.ProjectFunc` exists so the name is written
once here and quoted everywhere else, including in the error that reports it
missing.

**The module-root rule is the expensive one.** Every path in every adopting
project is written against it. Changing what paths resolve against would
silently relocate generated files rather than fail, which is the failure mode
[ADR-0014](0014-migrations-and-import.md) calls unrecoverable by regenerating.

## Alternatives considered

**A JSON config file at the module root.** `sqlb.config.json` naming the schema
package and every output directory — stdlib to parse, readable by tools that are
not Go, one obvious place to look. Rejected because it buys nothing that matters:
the binary still has to compile the module to get a registry, so the file saves
no work, and what it adds is a second spelling of `codegen.Options` that can
disagree with the type it mirrors. It also collides: `sqlb.json` is already the
emitted manifest.

**Flags only.** `sqlb generate ./taskschema --ts web/src/api --dart …`. Nothing
new to define and trivially testable, and rejected because the invocation then
has to be repeated identically in `//go:generate`, in the Makefile and in CI —
and drift between those three is precisely the failure the command exists to
remove.

**Keeping `cmd/gen` and shipping a template for it.** Honest about the
constraint and requires no new concepts. Rejected because a template is a thing
you paste and then edit, and the evaluation's measurement is that per-project
ceremony is small per project and paid by every project. A template does not
reduce it; it just makes the first copy faster.

**Reading the schema with `go/types` instead of compiling.** Type-checking the
schema package and interpreting the DSL statically, with no build step.
Genuinely appealing, and rejected as a reimplementation of the Go runtime: a
table can be declared by any code that runs at init, including loops, helper
functions and mixins ([ADR-0023](0023-mixins-carry-behaviour.md)). A static
reader would handle the easy declarations and silently miss the interesting
ones, which is worse than a compile.

## Revisions

- 2026-07-29 — Written, after building `generate` and `check`.
- 2026-07-29 — `sqlb migrate` added, and with it `Project`'s migration fields
  and `ShadowDB`. The wrapper paid for itself exactly as predicted: five fields
  landed and no project's `SqlbProject` changed shape. Two things were learned
  that the record did not anticipate — that versions have to come from the
  directory rather than the clock, and that a declared CHECK never round-trips
  (#24). Both are in Consequences.
- 2026-07-29 — #24 fixed with `shadow.NormalizeChecks`, and the reasoning added
  under Consequences. Worth recording as a revision rather than an edit: the
  fix is only available because the command already had a database open at the
  right moment, which was not an argument this record made for the design and
  is now one of the better ones.
