# Porting subject-go to sqlb — first slice + issues report  (2026-07-30)

> **Status: all five findings are triaged**, in
> [release-1.0.md stream G](release-1.0.md#g-port-findings-triaged--two-land-four-are-written-down).
> One is done (#5, pgtype scanning — now covered by `pgtest/pgtype_test.go`), one
> is answered by [ADR-0007](adr/0007-generated-rest-handlers.md) (#1, the module
> graph), the bind-parameter cast (#2) lands before 1.0, and #3 and #4 are
> scheduled for 1.1 with the documentation they owe landing before the tag.
> Finding #1 is now **fixed rather than answered**, and along the lines it
> proposed: `rest` is a module of its own, so the root `go.mod` requires nothing
> — no huma, no chi, no `go.sum` — and its `go` directive is the engine's own
> measured floor of `1.24` rather than huma's `1.25`. This port's chi bump and
> its `go` bump both stop happening. The suggestion in that finding was exactly
> this split; it took measuring the directive to show why per-package dependency
> checking could not substitute for it.
> This report's own ranked list of missing features is triaged in the same place.

**The subject is anonymised.** It is called `subject-go` throughout — the same
subject as [the first evaluation](review-adoption-existing-app.md), here actually
*ported* rather than assessed (same `internal/platform/filterexpr` +
`internal/platform/listquery`, same Projects/Tasks/members, same React web SDK
and Flutter client). Its working branch is given without a SHA, and where the
subject has an internal architecture decision of its own it is described rather
than numbered, so it is not confused with one of sqlb's ADRs. Nothing technical
was removed: every count, every layer, every file path inside the application and
every finding is as written. What is gone is the identity, because none of it is
load-bearing — the shape of the codebase is what the findings are about.

Read it as a snapshot of one port at its working branch (with local modifications
to the subject's tree), not as a verdict. Where a finding names a file or a
behaviour, it was checked against the code or against a running Postgres 18; where
it is a judgement call about ergonomics, it says so.

**Branch:** `claude/subject-go-sqlb-port` (SHA elided)
**Approach (chosen):** structs-first coexistence — `sqlb.Describe[T]()` over the
existing sqlc models, sqlb owning the dynamic filter/sort/search list surface
while sqlc keeps the static queries, reports and dashboards (per sqlb's own
[`with-sqlc.md`](with-sqlc.md)).
**First slice:** Projects list + read path.
**DB wiring:** bridge the existing `*pgxpool.Pool` into a `*sql.DB` via
`stdlib.OpenDBFromPool` — one connection pool shared by sqlc/pgx and sqlb.

## What landed

`internal/platform/sqlbx/` — the seam package:

- `sqlbx.go` — `FromPool(pool) *sql.DB` bridge; `Describe()` (registers
  `db.Project` capabilities mirroring `filterexpr.ProjectFields` +
  `projectSortColumns` + the search columns); `OrgScopedRegistry(orgFrom)` — the
  `BeforeQuery` tenant hook.
- `list_projects.go` — `ListProjects(ctx, exec, params)`: the sqlb port of the
  `listquery.ListPage` + `ListProjectsByIDs` path. Builds one query (scope →
  filter tree → search → whitelisted sort → page window), returns rows directly
  (no id round-trip) plus the unpaged total.
- Tests (all green against Postgres 18): `sqlbx_test.go` (bridge + scan of the
  pgx-typed columns), `list_projects_test.go` (scope isolation, archived scope,
  filter tree, bad-filter→ParseError, search incl. EXISTS subqueries, sort +
  pagination + total), `hooks_test.go` (tenant hook scopes without an explicit
  predicate; missing org fails closed).

Run: `TEST_DATABASE_URL=postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable go test ./internal/platform/sqlbx/ -v`

## Verdict

The coexistence path works and is low-risk. The pgxpool→database/sql bridge is
transparent, the sqlc-generated `db.Project` (pgx-native `pgtype` columns
included) scans through sqlb unchanged, the filter/sort/search/paginate surface
ports faithfully, and the `BeforeQuery` tenant seam does what it advertises. No
blocking issues. The friction below is worth addressing before a wider port.

## Issues / friction found (for sqlb)

### 1. Engine's single go.mod leaks REST-adapter deps + bumps the toolchain
Adopting the **stdlib-only engine** still dragged sqlb's whole `go.mod` into
the subject's module graph:
- `go-chi/chi/v5` bumped **v5.2.5 → v5.3.1** (the subject already used chi, so MVS
  raised it).
- the `go` directive bumped **1.25.0 → 1.25.7** (sqlb requires `go 1.25.7`).
- `huma/v2` stayed out of the *build* list (unimported) but sits in the graph.

The README promises "no dependencies to inherit … the engine depends on the
standard library alone." That's true at *compile* time, but not at the module
level. **Suggestion:** split the REST adapter (`rest/`, huma+chi) into its own
module (`.../rest`) so engine consumers inherit neither, and keep the engine's
`go` directive as low as it actually needs.

### 2. Typed-column facade can't cast the bind parameter — the exact dynamic-filter case
The subject's filter values arrive as JSON strings that must compare against typed
columns, so its compiler binds every value with an explicit cast
(`$1::uuid`, `$1::date`, `$1::bigint`). sqlb's `F("start_date").Gte(v)` renders
`start_date >= ?` and binds `v` as-is — there is no `F(col).Gte(v).Cast("date")`
or per-bind cast. (A *column*-side `Field.Cast("type")` exists, rendering
`col::type` — but that casts the column, not the bind value, so it cannot produce
the `$1::date` this case needs.) So `date >= text` / `uuid = text` either error or rely on
driver-side type inference. The workaround is to drop to `RawPred("… ?::date",
v)`, which discards the typed facade for precisely the filterable-column case the
facade exists for. **Suggestion:** a bind-side cast, e.g. `F(c).Gte(Cast(v,
"date"))` or `F(c).GteCast(v, "date")`.

### 3. Typed operators don't cover the null-aware semantics a filter grammar needs
The subject deliberately compiles `neq` to `col IS DISTINCT FROM $1` (so NULL rows
count as "not equal") and `not_in` on a nullable column to
`col IS NULL OR NOT (col IN …)`. sqlb's `F.Neq` is a plain `<>` and `NotOneOf` a
plain `NOT IN`, both of which drop NULL rows. Not wrong — but a REST/filter layer
almost always wants the null-inclusive form. **Suggestion:** either document the
NULL semantics of each operator, or offer `IsDistinctFrom` / null-aware `NotOneOf`
variants. (In this port I sidestepped both by reusing the subject's validated
compiler — see below — so this only bites the *native* typed-translator path.)

### 4. `RawPred` injection of an existing compiler works — but has a sharp edge
The cleanest coexistence seam is: keep the subject's validated `filterexpr.Compile`
(property whitelist, operator/value checks, bind discipline), then inject its
whole fragment as **one** `RawPred`. This is drift-free (one grammar, two
builders) but the `$N`→`?` bridge is **not** a naive text swap, and getting it
wrong 500s in production:

- `RawPred` is strictly **positional** — every `?` consumes the next arg — while
  Postgres `$N` is **referential** and may repeat. The subject's "predicate" filter
  fields prove this: `memberId` compiles to
  `manager_id = $1::uuid OR team_member_ids @> to_jsonb($1::text)` — one bound
  arg, `$1` referenced twice. Replacing every `$N` with `?` yields two `?` for
  one arg → `RawPred` errors ("more placeholders than arguments"). The wiring
  had to walk each `$N` **occurrence** left to right and repeat that occurrence's
  argument (see `compileFilter` in `list_projects.go`). This was caught only by
  the list-contract conformance test, not by the happy-path unit tests.
- The safe direction relies on two behaviours that are true but unstated:
  `Compile` emits `$N` in left-to-right bind order, and `RawPred` renumbers
  `?`→`$N` in textual order consuming args in order. `??`→literal `?` is
  documented; this ordering guarantee is not.

**Suggestion:** either document the positional-vs-referential mismatch and the
`?`-ordering guarantee, or offer a first-class `sqlb.RawPredPositional($N, args)`
helper that accepts `$N` fragments (with reuse) directly — "wrap my existing SQL
builder" is a natural adoption path and this is the trap on it.

### 5. `pgtype` scanning works, but only because pgx implements `sql.Scanner`
The sqlc models use `pgtype.Date` / `pgtype.Timestamptz` (from
`sql_package: pgx/v5`). These scan through sqlb's `database/sql` path **only**
because pgx v5's pgtype implements `sql.Scanner`/`driver.Valuer`. This is load-
bearing for the whole structs-first-over-sqlc story and currently unverified in
sqlb's own tests (its adopt test uses `sql.NullTime`, not pgtype). **Suggestion:**
add a `pgtype`-typed struct to the with-sqlc adoption test so a future pgx change
that drops `sql.Scanner` is caught by sqlb rather than by a consumer.

## Not yet ported (next steps, in order)

1. ~~Wire `ListProjects` into `core.ListProjects`~~ **DONE.** `/api/projects`
   now serves from sqlb: `core.ListProjects` builds a shared `*sql.DB` from the
   pool once in the factory, parses the same params + mutual-exclusivity checks,
   and calls `sqlbx.ListProjects`; the id round-trip + order restoration are
   gone. Response decoration (progress/open-tasks/starred) and the paginated
   envelope are unchanged. Verified by the pre-existing `projects_filter_test.go`
   (HTTP behavioural suite) and `TestListProjects_ListContract` (conformance kit)
   — both green with no test changes, so the contract is preserved.
2. **Cursor pagination** — sqlb's `?cursor=` is a headline feature and a strict
   improvement over the offset paging the subject uses; adopt it for the list
   response (needs a client/SDK contract change, so it is its own slice).
3. ~~Second entity (Tasks) on the native typed-translator path~~ **DONE.**
   `/api/tasks` (`core.ListTasks`) now serves from sqlb. See below.
4. Decide whether to keep the subject's JSON filter-tree contract (done here) or adopt
   sqlb's `?status=eq.x` URL grammar — the latter changes the web SDK contract.

## Tasks slice — the native typed-translator path

`sqlbx.FilterPred` (`filter_translate.go`) translates a validated filter tree
into a predicate built **entirely from the typed column facade** — `F(col).Eq`,
`OneOf`, `Between`, `IsNull`, … — no raw-SQL splicing. `core.ListTasks` uses it:
sqlb builds the dynamic WHERE/ORDER/LIMIT and returns the ordered page of **ids**,
and the sqlc relations view (`ListTasksWithRelationsByIDs`, which sqlb does not
model) hydrates them — a clean split. Verified green by the pre-existing
`TestListTasks_ListContract` and the tasks filter suite, plus new translator
tests, with no test changes to the existing suites.

What the native path confirmed about the earlier findings:

- **Issue 3 (null-aware operators) is composable, not blocking.** `neq` ≡
  `Or(F(c).Neq(v), F(c).IsNull())` reproduces `IS DISTINCT FROM`, and nullable
  `not_in` ≡ `Or(F(c).NotOneOf(v…), F(c).IsNull())`. So the typed facade *can*
  express the subject's semantics — just not in one call, and only if you know to. A
  `DistinctFrom`/null-aware-`NotOneOf` helper would remove the footgun.
- **Issue 2 (no bind-param cast) is real and now proven at the API layer.**
  `cmp` wraps every value in `Param{Value: v}` even when the value is itself an
  `Expr`, so there is genuinely no way to render `$1::date`. Date filters bind a
  civil-midnight `time.Time` and rely on Postgres promoting the `date` column to
  `timestamptz`; the list contract passes because the test DB session is UTC.
  **This carries a latent production caveat:** under a non-UTC session timezone
  the promotion shifts the boundary, so a `plannedAt <= 2026-04-01` filter could
  include/exclude the wrong day. filterexpr's `$1::date` cast is TZ-independent;
  the native path is not. A bind-param cast would close this for good.
- **New finding — porting off `filterexpr` means re-implementing its value
  validation.** `filterexpr.validateValue`/`Coerce` are unexported, so the native
  translator had to restate the UUID/enum/date/int/bool coercion to keep the 400
  contract (`coerce` in `filter_translate.go`). This is drift-prone; the list
  contract test is what keeps the two in sync. If sqlb wants to be the
  destination for existing filter grammars, a small exported "coerce a JSON value
  to a column type" helper (or letting `filterexpr` export one) removes the
  duplication.
- **Template ("predicate") registry fields have no native form.** Projects'
  `memberId` (a `{v}`-template) can't be built from the typed facade; the native
  translator rejects such fields and they stay on the RawPred path. Fine as a
  split, worth stating: the two translators are not interchangeable per registry.

## Consumer assessment (after porting two real endpoints)

This section answers, as the first real consumer: how is sqlb to build on, what
docs/examples are missing, what features are missing, and whether to port the
whole REST/SQL layer.

### How it's working — the verdict

Good, and better than expected for a pre-1.0. The load-bearing promises held up
on a real, messy schema:

- **Query-as-value + conditional predicates** is the whole reason this port was
  worth doing. The subject's list endpoints were ~40 lines of scope/filter/sort/search
  assembly each; expressing them as a growing `*Builder` read cleanly and the
  branches disappeared.
- **Structs-first over sqlc worked verbatim** — the pgx-typed `db.Project` /
  `db.Task` (with `pgtype.Date`, `pgtype.Timestamptz`, `pgtype.UUID`) scanned with
  no adapters, because pgx implements `sql.Scanner`/`driver.Valuer`. Zero model
  edits.
- **The pgxpool bridge is a non-event** — `stdlib.OpenDBFromPool` and one shared
  `*sql.DB`; sqlc/pgx and sqlb run on the same connections.
- **Hooks are the real thing** — one `BeforeQuery` registration scopes every read
  of a model and fails closed with no tenant on the context.
- **Tests-as-oracle** — because the port kept the subject's contract, the *existing*
  conformance kit and HTTP suites validated it with zero test changes. That is
  the strongest signal available that a runtime query builder didn't regress a
  compile-time-checked one, and it's a direct consequence of sqlb being a library
  and not a framework.

The friction was real but always had an escape hatch (`RawPred`), and every sharp
edge below was caught by a test, not by production.

### Docs / examples — what would have saved the most time

Ranked by hours lost:

1. **"Adopting into a pgx / pgxpool app."** The single most valuable missing
   guide, and exactly the subject's situation. `with-sqlc.md` uses `sql.NullTime`, so
   it never shows the `stdlib.OpenDBFromPool` bridge or confirms `pgtype`
   scanning — the two things every pgx-based adopter needs to know *first*. Ten
   minutes of doubt over whether pgtype would scan at all; it's the make-or-break
   question and it's unanswered.
2. **A "wrap my existing filter grammar" recipe.** Both translation strategies I
   ended up writing (RawPred-injection of a validated compiler; native
   `Node→Pred`) are a general adoption category — anyone with an existing JSON/DSL
   filter API. Nothing documents either, and the RawPred one has a trap (below).
3. **`RawPred` placeholder semantics.** That it is positional (`?`, each consumes
   one arg) while Postgres `$N` is referential/repeatable is undocumented and cost
   a 500 (the memberId reuse). One paragraph would have prevented it.
4. **Hook resolution.** That a bare `*sql.DB` uses the global registry while
   `*sqlb.DB.WithHooks(r)` uses `r` (via `hooksFor`) is only discoverable by
   reading `db.go`. It matters the moment you want per-request or per-test hooks.
5. **"Return ids, hydrate elsewhere."** The Tasks split — sqlb owns
   WHERE/ORDER/LIMIT, an existing sqlc join owns the projection — is the realistic
   shape for any app with relation-enriched list rows, and isn't shown.

### Main features missing (verified against 0.1, ranked)

1. **A bind-parameter cast.** `F(c).Gte(v)` can only bind `$1`; there is no
   `$1::date` / `$1::uuid`. `cmp` wraps even an `Expr` value in `Param{}`, so the
   cast can't be smuggled in. For a filter layer whose values arrive as strings
   against typed columns this is *the* gap — it forced RawPred on Projects and a
   latent non-UTC-TZ date bug on Tasks. Highest-value single addition.
2. **Null-aware negation.** `DistinctFrom(v)` and a NULL-inclusive `NotOneOf`
   (or an `.IncludingNulls()` modifier). Composable today as `Or(Neq, IsNull)`,
   but that is a correctness footgun for exactly the REST-filter case sqlb targets.
3. **A module boundary between the engine and the REST adapter.** The single
   go.mod means adopting the stdlib-only engine still pulls huma+chi into the
   graph and bumped the subject's chi and its `go` directive. The "no dependencies to
   inherit" claim is a compile-time truth that isn't a module-graph truth.
4. **A `$N`-form raw predicate** (referential, reuse-safe) for splicing an
   existing SQL builder's output without the `?`-expansion dance.
5. **Registry-aware value coercion.** `filter.Coerce(s, reflect.Type)` exists but
   only does `string→type` via `TextUnmarshaler`/time parsing — no enum whitelist,
   no JSON number/bool, no per-field rules. So adopting a JSON filter grammar
   still means re-implementing validation (`coerce` in `filter_translate.go`). A
   coercion hook that takes a column's declared type + allowed values would close
   this.

Explicitly **not** missing (I assumed they might be, they aren't): NULLS
FIRST/LAST ordering (`Order.NullsFirst/NullsLast`), offset paging sugar
(`Builder.Page`), and cursor/keyset paging (`Stable().After(c)` + `CursorFor`).
Offset list still costs two queries (Count + page), but that is parity with the
`listquery` it replaced, and cursor paging avoids the count entirely.

### Would I port the whole REST / SQL layer to sqlb?

**The query/list/CRUD read surface: yes, incrementally — and that's most of the
value.** Endpoint-by-endpoint, behind the existing contract, keeping sqlc for
static queries, reports, dashboards, relation-view joins, the superadmin
cross-tenant plane, RAG/pgvector, and River. This is exactly the split sqlb's own
docs recommend, and the two slices prove it carries.

**The generated REST adapter + OpenAPI: no.** It is built on **huma**, which
the subject deliberately removed (an internal architecture decision: plain chi +
golden contract tests, and the whole swag/Swagger apparatus deleted). Adopting
sqlb's REST would re-introduce the exact dependency and generation model that was
ripped out, and would change the client contract — sqlb's URL grammar
(`?status=eq.done`) is not the subject's JSON filter tree, which the web SDK and
the Flutter app both depend on. That is a deliberate client migration, not a port,
and there is no current pain justifying it.

**Generated TS/Dart clients: not now.** They would collide with the hand-
maintained `web/src/sdk` and the Flutter client; a separate strategic decision,
not part of a data-layer port.

**Schema ownership / migrations (goose → sqlb `migrate`/`Diff`): defer.** Making
sqlb the schema source of truth is the largest commitment of all and buys the
least right now; the `introspect → schema.go` path exists if that ever changes.

Net: **selective adoption of the read/list/filter layer, no to REST codegen and
schema ownership.** The dividing line is precisely sqlb's own thesis — it is a
better *library* for the dynamic-query surface, and adopting it as a *framework*
would fight decisions the subject already made on purpose.

| Layer | Port to sqlb? | Why |
|---|---|---|
| Filter/sort/search list endpoints | **Yes, incremental** | Proven twice; sqlb's core strength |
| Simple CRUD reads/writes | **Yes, incremental** | Typed facade + hooks |
| Relation-enriched list rows | **Partly** | sqlb builds the id query; sqlc keeps the join |
| Static queries, reports, dashboards | **No** | sqlc's compile-time guarantee; sqlb sends these to `Raw` |
| Superadmin cross-tenant, RAG/pgvector, River | **No** | Out of sqlb's scope |
| Generated REST handlers + OpenAPI (huma) | **No** | Reverses the subject's chi/golden-test decision; changes client contract |
| Generated TS/Dart clients | **No (separate decision)** | Collides with existing SDK + Flutter |
| Schema ownership + migrations | **Defer** | Largest commitment, least current benefit |

## Revision: the point is drift detection, not the REST handlers

The "would I port the REST layer" section above judged sqlb's REST/codegen the
way huma was judged, and that was the wrong axis. Correcting it, because it
changes the conclusion.

**Huma was rejected because the OpenAPI document *was* the product and the subject
never consumed it** (the same internal decision). sqlb's OpenAPI emitter is very
likely dead weight for the subject too — but that does **not** carry over to sqlb,
because for sqlb the OpenAPI is one optional emitter, not the mechanism. sqlb even
argues the model expresses more than OpenAPI can (a `where` that admits only
filterable columns, a `select` that narrows the response type, a hidden column
with no spelling). The mechanism is: **one schema declaration → SQL, Go
models/queries, the HTTP surface, a typed TS client and a typed Dart client — and
`sqlb check` fails CI when any of them drifts from the others.** That is the actual
thesis, and it is the thing this port has *not* exercised.

### The subject's real drift surface

The same contract is hand-maintained in four places:

- **SQL** — goose migrations, sqlc-generated Go models.
- **Go HTTP** — hand-written handlers.
- **web TS SDK** — 51 files (`web/src/sdk`: client, types, services, query
  factories, queryKeys).
- **Flutter** — 10+ hand-written Dart API clients (`mobile/lib/**/data`).

Automated cross-surface protection today: **33 golden contract files**, which pin
the *Go response shape* for those 33 endpoints and nothing else. TS↔API and
Dart↔API drift is entirely ungated; request/filter shape is ungated; the other
directions of Go drift are ungated. And these gates exist *because things already
drifted* (the golden tests, the list/count-twin arch test). So the pain sqlb
targets is real here and recurring — a renamed column, a new filterable field, an
added enum value is 3–5 coordinated edits today with a gate on one of them.

### The git history says it out loud

The churn confirms the theory rather than assuming it. Over the history of
`main`:

- **`web/src/sdk/types.ts` — 1,614 lines, 172 hand-written interfaces/types —
  has been edited in 108 commits, and 100 of those 108 also edited Go
  (`internal/`) in the *same commit*.** That is the drift tax as a number: a
  hundred times, someone changed the backend and re-derived the TypeScript shape
  of it by hand, in one breath, because nothing does it for them. The next most-
  rewritten SDK files are the same story — `queryKeys.ts` (41), `client.ts` (37),
  `services/projects.ts` (23).
- The whole `web/src/sdk` surface: 170 commits, ~15.5k lines of churn. The
  Flutter data layer (`mobile/lib/**/data`) is the fourth copy — younger (13
  commits) and about to inherit the same tax as it fills in.
- The clearest single symptom is **enum triplication**. `TaskStatus =
  'backlog'|'todo'|'in_progress'|'blocked'|'done'|'cancelled'` in `types.ts` is a
  third hand-written copy of a vocabulary that also lives in Go
  (`models.TaskStatuses()`) and in SQL (the `tasks_status_check` CHECK
  constraint). During this very port the Tasks tests failed because `'open'` had
  been removed from the SQL/Go copies but the value lingered elsewhere — drift
  caught by a test failure, which is precisely the failure mode a generated
  single source removes.

`types.ts` is therefore the single highest-value thing to generate rather than
hand-write, and its 108-commit history is the payback estimate. The catch is
unchanged: generating it faithfully requires sqlb to own the contract those types
describe (the package-deal point above).

This is the strongest argument *for* sqlb, and my incremental library port
captures **none** of it. Better query ergonomics and hooks are worth having, but
they are not the reason to adopt sqlb; the drift gate is, and it is exactly what
the library-only path leaves on the table.

### The honest catch: the drift gate is a package deal

You cannot take the cross-surface gate à la carte. To generate the TS and Dart
clients, sqlb must know the HTTP surface; it derives that from the schema's
`Expose(REST{…})`. So generated-client drift protection is coupled to sqlb owning
schema → SQL → HTTP → clients end to end — which means adopting sqlb's request
contract (`?status=eq.done`) and regenerating both SDKs, i.e. the contract
migration the earlier section flinched at. The value and the cost are the same
commitment; you don't get the gate on the incremental path.

So the real decision is not "library or framework" in the abstract — it is:
**is cross-surface drift painful enough to make sqlb the source of truth and
migrate the HTTP contract + both SDKs onto its generated clients?** That is a
roadmap call, and the honest inputs are: four surfaces, one partial gate, drift
that has already bitten — against a bounded-but-real migration of the web SDK's
React-Query layer and the Flutter data layer, plus a client-visible contract
change.

### How to decide it cheaply (the same way the library path was proven)

A one-resource **schema-first spike**, end to end, before committing:

1. Declare one resource (Projects is the obvious pick — already `Describe`d here)
   in the sqlb schema DSL; render its DDL and reconcile with the goose migration.
2. `sqlb generate` its Go model, TS client and Dart client.
3. Wire `sqlb check` into CI and **prove the gate**: rename a column in the
   schema, confirm CI goes red across the Go model, the TS client and the Dart
   client in one run. That is the whole value proposition, demonstrated or not in
   an afternoon.
4. Wrap the generated TS client in one real screen's React-Query
   `queries`/`queryKeys` factory, and the generated Dart client in one Flutter
   screen, to measure what the contract change actually costs a consumer.

If step 3 lands and step 4 is cheap, the framework adoption is justified on
evidence; if step 4 is expensive, the library path stands and the drift gate is
consciously declined. Either way it's decided by a spike, not by this document.

### The feature that would change the calculus

The gate needs schema-first **only because codegen reads the schema DSL, not the
`Describe`d models.** If sqlb could emit the TS/Dart clients and the `check` gate
from `sqlb.Describe[T]()` over existing sqlc structs — the same registrations this
port already writes — plus a description of the HTTP surface it serves, then an
app could keep goose + sqlc as the schema source **and** get the cross-surface
drift gate. That is the single capability that would let the subject have the actual
point of sqlb on the incremental path instead of as an all-or-nothing migration.
It is the most valuable thing sqlb could build for an adopter that already has a
schema pipeline it is not going to give up.

## Spike results: the drift gate, demonstrated

The schema-first spike proposed above was run. It lives in a self-contained sqlb
module `spikes/sqlb-projects/` (its own `go.mod`, excluded from the subject's
build) declaring the Projects resource once. `generate` and `check` need no
database.

**One schema declaration emitted 7 files across all four surfaces** in one run:
`models_gen.go` + `columns_gen.go` + `rest_gen.go` (Go model, typed columns,
REST handlers), `web/api/client.gen.ts` + `queries.gen.ts` (TS client + query
factories), `mobile/api/client.gen.dart` (Dart client), and `sqlb.json` (the
manifest).

**The gate works, verbatim.** Renaming one column in the schema
(`due_date` → `deadline`) and running `sqlb check` *without* regenerating fails
with all six generated files listed out of date and exit 1 — i.e. CI red across
Go, TS, Dart and the manifest from a single edit. `due_date` occurs in ~52 places
across the generated surfaces (19 Go, 7 TS, 23 Dart, 3 manifest); regenerating
lands the rename in every one with zero leftovers and returns `check` to green.
**That is the 100-of-108 coupled `types.ts` edits from the git history collapsed
into one edit plus a gate** — the whole thesis, demonstrated rather than argued.

Three findings that sharpen the adoption picture:

1. **The generated TS is the subject's *own* architecture.** `queries.gen.ts` emits
   **TanStack Query `queryOptions` factories with a `projectKeys` hierarchy** —
   which is exactly the pattern the subject hand-builds in `web/src/sdk/queries/` +
   `queryKeys.ts` and *enforces* with `queryKeys.arch.test.ts`. sqlb generates
   precisely the layer the subject maintains by hand and polices with an arch test. So
   the generated client would slot into the React-Query architecture rather than
   fight it — the step-4 "cost to wrap in a real screen" looks low, because the
   generated shape is the shape the subject already standardised on.
2. **The generated Go model differs from the sqlc one, and that's the real Go-side
   cost.** It types ids as `string` and dates as `*time.Time` (with a typed
   `ProjectStatus` enum and `db:`/`json:`/`sqlb:` tags), where the subject's sqlc model
   uses `uuid.UUID` and `pgtype.Date`. A schema-first port would therefore change
   the Go struct the handlers and response transformers build against. It is
   tunable — the manifest carries a per-column `goType`, so `uuid.UUID` can be
   restored — but "adopt schema-first" means reconciling model types, not just
   regenerating clients.
3. **The schema validator enforces tenant-safety at generate time.** The first
   `generate` refused with *"Scoped column must be ReadOnly, or a create request
   gets to name the tenant it writes into"* until `org_id` was marked `ReadOnly`.
   A whole class of multi-tenant write bug is a generate-time error here, not a
   code-review question.

The spike models a subset of `projects` (no jsonb bags), but the load-bearing
question — "does one edit gate all four surfaces" — is answered yes, on the
subject's own resource.

### Live REST: the generated handlers, against real Postgres

The generated `rest_gen.go` was then stood up against a real Postgres and driven
over HTTP (`spikes/sqlb-projects/rest_live_test.go`, `go test` against a throwaway
`sqlb_spike_rest` database; run with the same `TEST_DATABASE_URL` as the subject's
`be:test:pg` task). One `humago.New` + `Register(api, handle)` mounts
create/read/list/update; tenant hooks supply and scope `org_id` from the request
(a header here, a JWT claim in production). All seven checks pass:

- **create** — `POST /projects` with no `org_id` in the body; the `BeforeCreate`
  hook stamps it.
- **tenant-scoped list** — org A sees its 3 rows, org B sees only its 1; the
  `BeforeQuery` hook confines every read.
- **filter / sort / search** — `?status=eq.todo` (2), `?sort=-name`
  (Gamma…Alpha), `?search=Alph` (Alpha) — the URL filter grammar executing real
  SQL.
- **read** — `GET /projects/{id}`; and a cross-tenant read returns **404**, not a
  leak, because the hook narrows the query so the row is simply not found.
- **update** — scoped `PATCH /projects/{id}`.

Three findings from standing it up:

1. **The server refuses to mount an unscoped write
   ([ADR-0030](adr/0030-declared-scope-is-required.md)).** With
   `BeforeQuery` + `BeforeCreate` registered but no `BeforeUpdate`, `Register`
   returned: *"/projects exposes …update…, and nothing confines Project — update:
   BeforeUpdate is not registered (org_id is Scoped)."* A PATCH that could cross
   tenants is a startup failure, not a latent bug. Adding the update hook fixed
   it. This is the same guarantee the subject enforces today by hand and by review;
   here it is a precondition of the process starting.
2. **`string`-typed uuids round-trip fine over pgx's `database/sql` driver** —
   insert, `org_id = $1` comparison, and scan all worked against a real `uuid`
   column with no cast, confirming the generated model's `string` uuid type is
   viable (it is what the tasks example ships, now reconfirmed on the subject's
   shaped data).
3. **Cross-tenant isolation falls out of the same hook** that scopes the list —
   read/update of another org's row is a 404, with no per-handler code. That is
   the property the subject spreads across every handler and every query today.

This closes the gap the codegen spike left open: the generated REST is not just
type-shaped, it serves correct, tenant-safe CRUD+filter over real Postgres. What
remains before a real adoption is breadth (the full table incl. jsonb; the other
resources) and the contract migration for the existing clients — not a question
of whether the generated server works.

### Reconciling the Go model to `uuid.UUID`

The one Go-side objection from finding #2 above — the generated model typed ids as
`string` where the subject's sqlc uses `uuid.UUID` — is a one-line fix, and it behaves
exactly as [ADR-0035](adr/0035-type-overrides.md) says: an override is a
**rendering** decision.

```go
Types: []codegen.TypeOverride{
    {Type: schema.TypeUUID, GoType: "uuid.UUID", Import: "github.com/google/uuid"},
},
```

After regenerating: `models_gen.go`, `columns_gen.go` and `rest_gen.go` all import
`google/uuid` and type `id`/`org_id`/`manager_id` as `uuid.UUID` — matching the
sqlc convention. The **TS client and the Dart client are byte-for-byte unchanged**
(`id: string` / `String orgId`): a uuid is a string on every wire, so the override
never reaches them. The manifest (`sqlb.json`) does record the change — three
columns flip `"goType": "string"` → `"goType": "uuid.UUID"` — but their SQL/wire
`"type": "uuid"` is untouched, which is exactly why the clients don't move: the
emitters map from `type`, not `goType`. So `sqlb check` tracks the Go-rendering
edit as the intended change it is, while proving the wire contract stayed put.
And the live REST test above passes unchanged with the new model — `uuid.UUID`
round-trips through insert, the
`org_id = $1` scope comparison, the scan, the `/{id}` path parameter, and huma's
request-body validation (a JSON uuid string decodes into `uuid.UUID` via its
`UnmarshalText`), with `filter.Coerce` parsing `?id=eq.<uuid>` the same way.

So the Go-model-type mismatch is not a real obstacle: it is one override entry,
invisible to the clients, and proven end to end against Postgres. `time.Time` vs
`pgtype.Date` is the same shape of decision, left as-is here because `time.Time`
is the more portable choice and the subject would likely prefer it over the pgtype
wrappers anyway.

## One architectural note for the wider port

The handler creates its `*sql.DB` in the factory (`sqlbx.FromPool(pool)`), so
each ported handler that does this opens its own `database/sql` pool over the
shared pgxpool. Fine for one endpoint; for a broad port, build **one** shared
`*sqlb.DB` at startup (carrying the org-scoping hook registry) and inject it
alongside the pool, rather than per-handler. Deferred until a second endpoint
needs it.
