# Handoff — sqlb-foundation  (2026-07-27 13:27)

## Where
- Repo / worktree: `/Users/jryannel/dev/tmp/sqlb`
- Remote: https://github.com/jryannel/sqlb — **private**
- Branch: `main` @ `8a2ff58` · 0 unshipped commits · 0 uncommitted files · CI green
- 18 commits, 41 Go files (~9.8k lines), 7 packages, 16 ADRs

Everything is committed and pushed. No checkpoint was needed.

## Goal

A schema-first data layer for Go and Postgres, replacing the boilerplate between
HTTP handlers and typed SQL for **dynamic** data views (filter, sort, search,
group, paginate) — the case sqlc structurally cannot express, because a static
query text cannot say "this WHERE clause exists only when the user typed
something".

The design rests on two things: a query is a **value** so predicates compose
conditionally, and there is **one predicate AST with two producers** (hand-written
Go, and the URL filter grammar) so escaping, authorisation and hooks each happen
once. Declare a schema once; derive migrations, models, a typed query facade, a
REST surface and an OpenAPI document from it.

Intended consumer is `github.com/mind-vm/studio-apps/core` — independent fx
modules, per-module goose migrations, no cross-module foreign keys.

## Done so far

Working, tested, and documented (each is one commit; see `git log`):

- **`schema/`** — declarative DSL, validation (16 authoring mistakes reported at
  once), operational linting, JSON manifest, module isolation with table
  prefixes and `ExternalRef`. `18ba9e0`, `93be0a9`, `fd1a0ae`
- **`.` (`sqlb`)** — expression AST, Postgres compiler, generic builder, model
  reflection, mutations, hooks, `Describe`, `Explain` with plan diagnostics,
  typed column facade (`Col`/`TextCol`). `c66222f`, `f07f041`, `56d3e0b`
- **`filter/`** — URL grammar → predicates, capability-enforced, actionable
  errors. `24724cf`
- **`migrate/`** — renders goose / golang-migrate / plain SQL files. Does **not**
  apply them. `6bc7151`
- **`codegen/`** — generates models, typed facade, manifest; `Check` is the
  dry-run/CI mode. `12d82c6`, `21ff34f`
- **`example/blog/`** — now *generated* from `blogschema`; all behaviour tests in
  it pass against generated output, which is the generator's real verification.
- **Tooling** — `mise.toml` (go 1.26.4, golangci-lint 2.12.2), `.golangci.yml`,
  `.github/workflows/ci.yml`. Both CI jobs verified actually running. `5e7cc43`
- **`docs/`** — architecture, vision, 16 ADRs. `04bdef8`, `8a2ff58`

## In progress / not finished

Nothing is half-done or broken. These are simply unbuilt, in priority order:

1. **Migration diff engine** — `migrate/` renders files; nothing computes *what
   changed*. Needs registry-vs-registry diff (see ADR-0014) plus a shadow
   database to derive current state.
2. **REST handlers + OpenAPI** — codegen emits models and the facade only.
   ADR-0007 is Exploring/Low and names the hard part: an OpenAPI schema for a
   *compositional* filter grammar.
3. **`?expand`** — `filter` validates relation names but performs no join.
4. **Change feed** — transactional outbox, designed in ADR-0012, nothing built.
5. **`sqlb import`** — bootstrap a schema from an existing database (ADR-0014).

## Next steps

1. **Start the migration diff engine.** `Diff(current, target *schema.Registry)
   []migrate.Change` as a pure function — testable exhaustively without a
   database, and it makes import and migration the same machinery reversed.
   Emit the destructive-change and `Concurrent` flags `migrate.Change` already
   carries.
2. Decide the current-state source: shadow database (ADR-0014's choice) vs.
   introspecting a dev database. The former validates the migration history;
   the latter is cheaper in the inner loop.
3. **Before any generated migration is applied anywhere real**, settle the file
   format — ADR-0014's Cost of change says this is the most expensive thing in
   the record to alter later.
4. Add lock-hazard detection: `SET NOT NULL` on a large table takes an
   `ACCESS EXCLUSIVE` lock and needs an expand/contract sequence a generator
   cannot author. Flag it rather than emitting something that causes an outage.

## How to verify

```bash
mise run ci             # full gate: fmt-check vet lint tidy-check deps-check generate-check test-race
mise run test           # inner loop; no Docker or Postgres needed
mise run bisect-check   # every commit builds/vets/tests in isolation
mise tasks              # everything else
```

No database is required: engine tests run against an in-memory `database/sql`
driver defined in `sqlb_test.go`.

## Open questions / decisions pending

- **Repo visibility** — still private. `gh repo edit jryannel/sqlb --visibility
  public` when wanted.
- **`go.mod` is 1.25.7 while mise pins 1.26.4** — deliberate (a library should
  not force consumers onto a newer toolchain) but worth a conscious confirm,
  since studio-apps is on 1.26.4.
- **Go 1.27 generic methods** (~Aug 2026) make `db.Query[Post]()` and
  `b.Collect[R](ctx, db)` possible — purely additive, see ADR-0005 and the README.
- **Postgres schemas vs. table prefixes** — prefixes chosen (ADR-0015). Revisit
  trigger recorded: *if a module moves to its own database.* The compiler already
  renders qualified names correctly; `search_path`, schema-creation ordering and
  per-schema goose version tables would still need building.

## Key files & pointers

- `builder.go:40` — `Query[T]()`; builders mutate in place, terminal methods
  clone before hooks so running twice does not accumulate predicates.
- `exec.go:147` — `Collect[R, T]`; **strict** scan, errors on an unfilled field
  (a mistyped alias would otherwise scan as a real-looking zero).
- `explain.go:38` — `Explain`; validates a query against the live schema *and*
  reports plan regressions without executing. Usable as a test assertion.
- `filter/filter.go:149` — `Parse`; `filter/filter.go:119` — `Apply` owns the
  projection and excludes hidden columns.
- `schema/registry.go:43` — `NewModule`; registry applies the table prefix so it
  cannot be forgotten at a call site.
- `schema/lint.go:77` — `Lint`; operational rules (unindexed filter, search
  without trigram, list without stable sort).
- `migrate/migrate.go:91` — `Split`; concurrent index changes get their own file
  because `NO TRANSACTION` is file-scoped in goose.
- `codegen/codegen.go:112` — `Check`; the dry-run mode wired into CI.
- `example/blog/post_ext.go` — the generated/hand-written seam: `AddViewCount` is
  a correctness decision a generator cannot infer.
- `docs/adr/README.md` — the ADR process. Records are **living documents**; each
  pairs *What would change our mind* with *Cost of change*.
- `docs/adr/0016-guards-proven-both-ways.md` — read this before adding any CI
  guard. Three guards in the founding session reported success while checking
  nothing.
