# SQLB main-branch codebase review — 2026-08-02

## Executive summary

This review covers `main` at `eccca52`, which matches `origin/main`. The two
high-severity findings in the earlier branch review do **not** apply here:
main uses copy-on-write model descriptions and explicit per-application hook
registries, with concurrency tests for both request-time paths.

SQLB's central design is coherent and well executed. Programmatic predicates
and request filters converge on one AST; values are bound rather than
interpolated; identifiers resolve through model metadata; REST capabilities are
opt-in; generated writes are transactional by default; and the schema,
migration, runtime, wire-contract, and generated-client layers have independent
tests and drift gates. No SQL injection path or tenant-confinement bypass was
found in the reviewed mainline request path.

There is one high-severity issue outside that path: the new `sqlb survey`
adoption command trusts that its destination database is empty and directly
executes the diff against it. It never checks the assumption. If an operator
supplies a non-empty destination whose schema differs from the source, the diff
can contain destructive `DROP TABLE`/`DROP COLUMN` statements, and the command
executes them without confirmation. This should be fixed before publishing or
recommending the command.

With that guard added, main is a credible foundation for an MVP or controlled
pilot. Production risk remains dominated by maturity: pre-1.0, one maintainer,
rapid API movement, and little demonstrated time under independent production
traffic.

## Scope and method

The review covered the root query/compiler and reflection layers, filters and
cursor paging, transactions and hooks, REST CRUD/actions/events, schema and wire
generation, migrations/introspection/shadow replay, the command-line adoption
tools, examples, documentation, and CI.

Repository size at this revision:

- 98,376 lines of tracked Go.
- 45,606 lines of tests.
- 52,770 lines of non-test Go.

Existing ADRs and earlier reports were used as design context, then checked
against current source rather than accepted as implementation evidence.

## Verification

- `go test ./...`: passed for every package in the root module.
- `go test -race ./...`: passed for every package in the root module.
- `golangci-lint run`: zero issues, with one malformed-`nolint` warning described
  below.
- Root-module statement coverage: 76.7%.
- Important package coverage: `codegen` 85.9%, `filter` 85.9%, `migrate` 87.3%,
  `rest` 87.4%, `restcompat` 82.2%, and `schema` 78.2%.
- `cmd/sqlb` coverage fell to 42.7% after adding the database-backed adoption
  commands. The core database paths of `survey` and `introspect -out` are not
  exercised by the root suite.

The Docker/Postgres/PgBouncer gates and generated TypeScript/Dart/CLI gates were
not rerun in this sandbox. They are present in CI. Direct Go checks used a
temporary build cache because some `mise` tasks attempted to write to the
sandbox's read-only shared Go caches.

## Prioritized findings

### 1. High — `sqlb survey` can destructively rewrite a non-empty destination database

The command documents its second DSN as `<dst-empty-dsn>` and calls it a scratch
database, but this is only prose. `fixpoint` introspects whatever destination it
was given, diffs that registry against the source registry, then runs every
`Change.Up` directly.

`migrate.Diff` represents removal of a destination-only table as a change whose
`Up` is `DROP TABLE ...` and whose `Destructive` flag is true. The protection
that normally comments out destructive migration SQL exists in the migration
renderer, not in `Diff`. `survey` bypasses the renderer and does not inspect the
flag, so the drop runs live. The same applies to other destructive differences.

An operator who reverses two DSNs, reuses a scratch database that contains
unrelated tables, or points the destination at a real environment can therefore
lose data without a prompt or an opt-in flag. The source DSN is read-only by
behaviour; the destination has no equivalent safety rail.

Evidence:

- `cmd/sqlb/survey.go:89-106` states the empty-destination precondition.
- `cmd/sqlb/survey.go:339-350` introspects the destination, diffs it, and executes
  every raw `Up` statement without validating emptiness or `Destructive`.
- `migrate/diff.go:484-505` produces a live `DROP TABLE` statement with
  `Destructive: true`.
- `migrate/migrate.go:453-473` shows that destructive commenting happens only
  during rendering, which this path does not use.

Recommendation: before computing or applying the diff, query the destination
for all user tables and abort if any exist. Do this independently of the
survey's source exclusion patterns, because an excluded migration table is
still evidence that the destination may not be disposable. For additional
defence, refuse source and destination identities that cannot be distinguished,
print the resolved destination identity before writing, and require an explicit
flag for any future mode that permits a non-empty destination. Add a real
Postgres regression test proving that a sentinel table and row survive a refused
run.

### 2. Medium — the new database-backed CLI paths have no end-to-end test

The repository's strongest principle is that database behaviour must be proven
against Postgres rather than inferred from SQL strings, yet the command that
applies an entire schema to a destination has no database-backed test. Coverage
confirms the gap:

- `runSurvey`: 0%.
- `fixpoint`: 0%.
- `introspectCmd`: 26.5%.
- `writeSchema`: 0%.
- Overall `cmd/sqlb`: 42.7%, down from the mid-70s before these commands landed.

The unit tests cover argument routing, module grouping, pattern matching, and
report formatting, but not connection setup, destination safety, DDL
application, re-introspection, fixpoint reporting, or writing an imported schema
from a real database. This is how the destructive-destination issue passed the
otherwise strong suite.

Recommendation: add a `pgtest` integration that invokes the command logic with
two temporary databases. Prove a clean fixpoint, an unsupported construct, an
apply failure, extension reporting, and—most importantly—refusal of a non-empty
destination. Keep formatting helpers in the fast suite.

### 3. Medium — the promised minimum Go version is not exercised in CI

`go.mod` and the README promise Go 1.25, while `mise.toml` pins Go 1.26.4 and all
CI jobs install that toolchain. The `go 1.25.0` directive constrains language
semantics, but it does not prevent a contributor from using a standard-library
API introduced in Go 1.26. Such code can pass every current gate and fail for a
consumer on the documented minimum.

Evidence: `go.mod:3`, `README.md:96`, `mise.toml:10-14`, and
`.github/workflows/ci.yml:18-34`.

Recommendation: retain 1.26.4 for development, but add a small Go 1.25
compatibility job that runs `go test ./...` across the public modules. It does
not need Docker or the generated-client toolchains.

### 4. Low — the primary adoption guide invokes a command that no longer exists

`sqlb-survey` was folded into `sqlb survey`, and ADR-0032 records the change.
The dedicated adoption guide still links to the deleted
`cmd/sqlb-survey/main.go` and tells readers to run `go run ./cmd/sqlb-survey`.
That command fails before the user reaches the feature the page is explaining.

Evidence: `docs/surveying-a-codebase.md:3-5` and `:36-42`; the old command path
does not exist, while `cmd/sqlb/survey.go` implements the new verb.

Recommendation: link to `cmd/sqlb/survey.go`, use the installed form
`sqlb survey ...`, and search the generated site for residual `sqlb-survey`
references as part of the rename.

### 5. Low — architecture and lint metadata have visible drift

The architecture package table calls the root package “stdlib only,” while the
same page later correctly says it depends on pgx. Its known-gaps section says
there is no change feed even though main ships an SSE transport, in-process
broker, publication hooks, tests, and a dedicated guide. The correct limitation
is that no durable multi-replica source ships.

Separately, `rest/events_test.go:53` writes an explanatory sentence on the same
line as `//nolint:bodyclose` using an em dash. Golangci-lint parses the sentence
as part of the linter name and warns that the directive names an unknown linter.
The actual call has a second, valid directive, so the gate remains green; the
warning nevertheless trains CI readers to ignore analyzer output.

Recommendation: refresh `docs/architecture.md`, move the nolint explanation to
an ordinary preceding comment, and leave only `//nolint:bodyclose` on the call.

## What is working well

### Security and correctness

- Values converge on one bind path. No request-controlled value interpolation
  into SQL text was found.
- Identifier-bearing operations resolve against model metadata and quote names.
  `Raw`, `RawPred`, and `RawSel` are explicit escape hatches.
- Hidden and capability-restricted columns are enforced across filtering,
  sorting, selection, request bodies, actions, and expansion JSON.
- URL groups and JSON filter trees share parser budgets. Limits cover condition
  count, nesting depth, list length, operand length, sort terms, page size, and
  offset depth; JSON number decoding preserves integer precision.
- Model descriptions are now copy-on-write. Published metadata is immutable,
  relations and indexes are rebuilt onto the copy, and concurrent tests cover
  cold model resolution and request traffic.
- Hook registries are explicit. Registration and execution name the same
  registry, removing the ambient-state tenant-boundary hazard from the earlier
  branch.
- Generated writes are transactional by default, rollback is panic-safe, nested
  transaction constraints are explicit, and after-commit failures do not invite
  duplicate write retries.
- Cursor paging uses total ordering, handles nullable sort terms, and fetches
  unprojected ordering values for cursor construction without exposing them in
  the response.

### Architecture and maintainability

- Runtime SQL construction remains independent of the schema DSL. Design-time
  packages consume schemas without entering the request path.
- The one-AST/two-producer architecture prevents programmatic and HTTP filters
  from acquiring different escaping or authorization rules.
- The wire-name layer is applied consistently to generated Go models,
  TypeScript, Dart, CLI bodies, filters, OpenAPI parameters, and compatibility
  snapshots. Negative compile tests guard generated-client narrowing.
- ADRs record rejected alternatives and revisit triggers rather than presenting
  decisions as timeless facts.
- Error handling generally fails loudly and distinguishes client-safe details
  from internal database failures.
- The eject path is genuine: committed plain-pgx output and parity checks make
  dependency exit testable rather than aspirational.

### Testing and delivery

- Test density and coverage are high in the runtime, filter, migration, REST,
  and code-generation packages.
- CI separates fast unit/race/lint checks from Postgres and PgBouncer checks and
  gates dependency boundaries, generated drift, REST contract impact, eject
  parity, and generated clients in their native toolchains.
- Real-Postgres round trips cover errors canned rows cannot, while the in-memory
  executor keeps the normal loop fast.
- The per-commit bisect gate preserves buildable history, an uncommon and useful
  maintenance property.

## Residual product and operational risks

These are acknowledged limitations rather than newly discovered defects:

- Maturity remains the dominant risk: pre-1.0, one maintainer, rapid change,
  and limited independent production soak time.
- Relation support has an intentional ceiling: one-level expansion, no nested
  traversal grammar, and no first-class many-to-many vocabulary.
- The shipped event broker is process-local and lossy. Durable replay across
  restarts and replicas requires an application-provided `Source`/`Publisher`.
- `numeric` defaults to `float64`; `bigint` reaches JavaScript as a JSON number
  unless the application chooses an override and wire policy. Money and values
  beyond 2^53 require explicit treatment.
- SQL three-valued logic remains a usability trap: `NotOneOf` excludes NULL
  rows unless the caller adds an `IS NULL` arm. `RawPred` placeholders are
  positional and remain an expert-only escape hatch.
- Shadow replay cannot reproduce destructive migrations that were generated
  commented out and manually enabled. Down histories do not receive the same
  fixpoint proof as forward histories.
- Rate limiting, slow-query telemetry, index advice for filter/search
  capabilities, and multi-replica migration coordination remain application
  responsibilities.

## Recommended sequence

1. Make `sqlb survey` reject every non-empty destination before executing any
   DDL, and prove the guard against a real Postgres database.
2. Add end-to-end Postgres coverage for `survey` and `introspect -out`.
3. Add the Go 1.25 compatibility job.
4. Repair the renamed survey command in the adoption guide, refresh the
   architecture page, and remove the malformed nolint directive.
5. Run the complete `mise run ci` gate after those changes, including Postgres,
   PgBouncer, generated clients, contract impact, and eject parity.
6. Before a production-readiness claim, run a controlled external pilot with
   query latency/cardinality telemetry and a durable event source where events
   matter. Treat elapsed operational time as a release criterion.

## Verdict

Main is materially stronger than the previously reviewed feature branch. The
runtime request path is thoughtfully designed, heavily tested, and showed no
new high-severity defect in this pass. The unsafe survey destination is a real
data-loss risk in a newly added design-time command and should block that
command's release, but it does not undermine the query/REST engine itself.

After closing that issue and adding database-backed command tests, SQLB is a
credible choice for an MVP or carefully observed pilot. Production adoption
should remain conservative until the library accumulates external traffic,
maintainership redundancy, and an operational history that repository rigor
alone cannot provide.
