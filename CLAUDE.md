# Working in this repository

sqlb is a composable SQL builder and schema DSL for Postgres. A schema is
ordinary Go, and everything else — migrations, models, REST handlers, the
OpenAPI document, four clients — is derived from it.

This file is the map and the traps. It does not restate the docs; it says where
they are and what is not inferable from reading code.

## Orientation

Read in this order, and stop as soon as you have what you need:

1. **The package's own `doc.go`.** Every library package has one and it is the
   real introduction — what the package is for, the argument behind its shape,
   and a worked example. `schema/doc.go` and `rest/doc.go` are the two that
   most repay reading before touching anything. A command documents itself the
   Go way instead, as `// Command sqlb …` at the head of `cmd/sqlb/main.go`.
2. **[docs/architecture.md](docs/architecture.md)** for how the pieces fit and
   why the seams are where they are.
3. **[docs/adr/](docs/adr/)** — 52 records, and they are *load-bearing rather
   than historical*. A decision here is usually the answer to "why is this not
   simpler", and reversing one without reading it is the most common way to
   spend an afternoon rediscovering a rejected alternative. Each carries a
   revisit trigger and the cost of change.

`docs/` mirrors to the published site. Everything under it is prose for humans;
the API reference is the Go doc comments.

## Layout

Four Go modules. `go test ./...` at the root covers **only the first**:

| | |
|---|---|
| `.` | the engine — builder, compiler, hooks, model cache. 19 files at the root, which is the package |
| `pgtest/` | round-trip tests against real Postgres in containers. Its own module so the engine's suite stays database-free |
| `example/tasks/`, `example/fxapp/` | worked applications, each with its own gate |

Packages: `schema` (the DSL), `codegen` (emitters), `rest` (Huma mount),
`filter` (URL → predicate), `migrate` (diff → DDL), `introspect` (database →
declaration), `shadow` (replay a history), `restcompat` (contract diffing),
`sqlbtest`, `cmd/sqlb`.

## The gate

```bash
mise run heal   # everything the tooling can fix on its own
mise run ci     # the gate: never rewrites, only fails
```

`mise run preflight` is the push path: heal, build, database-free tests, about
fifteen seconds. `mise run ci` mirrors `.github/workflows/ci.yml` in full and is
for reproducing a CI failure rather than for routine use — CI is the gate. The
database-backed suites read a DSN and start nothing; `mise run pg-up` provides
it from `compose.yaml`, and the tasks that need it depend on that. Individual
steps — `vet`, `lint`, `generate-check`, `impact-check`, `eject-check`,
`test-race`, `test-pg`, `test-ts`, `test-dart`, `test-cli` — run on their own
and `mise tasks` describes all 28.

**`mise run site-check` needs no npm install.** It is the fast way to find out
whether a docs edit can be published, and the check that catches a link whose
target moved.

## Traps

Four things that are not visible from where you would look for them.

**The gate is two workflows, and a PR shows one.** `ci` and `pages` are
separate on purpose — folding Astro into the gate would make Node a build
dependency of a Go library. But `pages` triggers only on pushes to `main`, so
it never appears on a pull request. `gh pr checks` is therefore half the
answer; `gh run list --commit <sha>` is the whole of it. This has bitten twice:
v0.7.0 was tagged with `pages` red, and v0.8.0 nearly was.

**A docs link can break across two green pull requests.** One adds a page
linking to a file, another moves the file; each is green on its own base and
the pair is red. `sync-docs` refuses to publish rather than emitting a dead
link, which is what it is for — but the failure lands on `main`, not on either
PR. Run `mise run site-check` when you move or rename anything `docs/` points
at.

**Docs mirror source by hand.** `docs/typescript/README.md` and
`docs/dart/README.md` restate what `codegen.Options` says about the files each
emitter writes. Change an emitter's output and three places need the edit;
nothing links them yet.

**A fresh worktree has no `site/node_modules`.** `npm run build` fails with
`astro: command not found` until `npm ci` in `site/`. Prefer `site-check`,
which does not need it.

## Conventions

**Commit messages argue.** The subject is a claim in the repo's voice —
`fix(model): a description publishes a copy, so a model handed out is never
written` — and the body says what was wrong, what was decided, and what was
deliberately not done. `git log` is the design record that ADRs summarise, so a
body that only restates the diff loses the reasoning nothing else captures.

**A guard is not trusted until it has failed on purpose**
([ADR-0016](docs/adr/0016-guards-proven-both-ways.md)). When you add a check,
break the thing it checks and record that it caught it. The v0.8.0 exclusion
work is the model: with the import silently dropping exclusions, one test
failed and named the constraint while the fixpoint test *passed*, because both
registries had dropped the same thing.

**Tooling operates on tracked files only**
([ADR-0018](docs/adr/0018-tooling-scoped-to-tracked-files.md)).

**Prefer a failing check to a written-down rule.** `generate-check`,
`eject-check`, `impact-check`, `deps-check` and `bisect-check` all exist
because a convention that is only documented is a convention that drifts. If
you are about to add a paragraph telling someone to remember something,
consider whether it can fail in CI instead.

**Releases.** A release is an annotated tag whose message *is* the notes, plus
a GitHub release carrying the same text, on a commit where **both** workflows
are green. [docs/compatibility.md](docs/compatibility.md) says what is frozen
and what is expected to move; a pre-1.0 minor may break a surface listed under
*Will move*, and the notes carry the mechanical edit that fixes it.

## Things that are deliberate

Worth knowing before you propose removing them:

- **Postgres only** ([ADR-0001](docs/adr/0001-postgres-only.md)), and **pgx is
  the driver, not `database/sql`**
  ([ADR-0040](docs/adr/0040-the-driver-is-a-dependency.md)). `Executor` is
  `Query` and `Exec` over pgx types and grows by optional interfaces that are
  type-asserted for, never by adding methods.
- **The schema DSL is optional.** The engine reflects over struct tags, so
  every feature must be reachable without codegen
  ([ADR-0010](docs/adr/0010-codegen-is-optional.md)).
- **Nothing on the read path locks.** Describing a model is copy-on-write: a
  published `*Model` is never written again, so the cost sits with the writer.
- **A column has one wire spelling**, derived from its name by the schema's
  `WireCase` ([ADR-0036](docs/adr/0036-the-wire-is-the-column-name.md)). There
  is no mapping layer and no per-field override, in either direction.
- **Capabilities are opt-in.** Nothing is filterable, sortable or selectable
  unless the column declares it, and a rejection names what would have been
  accepted ([ADR-0011](docs/adr/0011-actionable-errors.md)).
