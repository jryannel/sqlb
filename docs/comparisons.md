# How sqlb compares

Every evaluator builds this table silently. Better to build it here, including
the places where sqlb loses.

Read the [status](compatibility.md) first, because it outranks everything below:
sqlb is pre-1.0, has one author and no observed consumers. Every tool on this
page is older, more used, and more likely to still exist in three years. Nothing
here argues otherwise.

## The short version

Most of this page is not either/or. Three of the five tools below sit happily
beside sqlb in one codebase, and one of those pairings is tested here rather
than asserted.

| Tool | Strongest at | How sqlb relates |
|---|---|---|
| **sqlb** | Filter/sort/search endpoints whose predicates vary per request, and one constraint applied to every read of a model | — |
| **sqlc** | Static queries typed against the real schema, and anything expressible in SQL | **Complementary**, and tested that way: sqlc owns the static queries, sqlb the dynamic list endpoint, both on one transaction |
| **Atlas** | Migrations as a product — multi-engine, declarative and versioned, linted in CI | **Complementary**: sqlb writes migration files, Atlas is a better tool for applying and linting them |
| **Bun** | A mature multi-dialect query builder and light ORM | **Overlapping** at the builder. sqlb adds the URL grammar and the capability model above it |
| **ent** | A mature graph model — traversal, M2M, nested eager loading — with an extension ecosystem | **Overlapping**, and the more complete answer today. sqlb layers over structs ent cannot touch |
| **PostgREST** | An API with no Go code at all | **An alternative**: the same job with the rules in RLS rather than in Go |

The sections below take each in turn, leading with what it does better.

### The rest of the table an evaluator builds

These are not compared at length below, because sqlb's relationship to each is
one line. They are here because leaving them out does not stop anyone reaching
for them — it only stops this page being useful when they do. Star counts and
importer counts checked August 2026.

| Tool | What it is | How sqlb relates |
|---|---|---|
| [**GORM**](https://github.com/go-gorm/gorm) | The default Go ORM. ~40k stars, ~87,000 importers — the most-used database library in Go by a wide margin | **An alternative**, and the first comparison most evaluators make. GORM is a full ORM with associations, callbacks and auto-migration; sqlb is a builder plus an HTTP surface and declines to be an ORM. If your team wants an ORM, this is the one, and the [when not to use sqlb](#when-not-to-use-sqlb) list already says so |
| [**go-jet/jet**](https://github.com/go-jet/jet) | Type-safe SQL builder generated from the live database. ~3.8k stars | **Overlapping at the builder**, and stronger there: jet's column references are generated types, so a misspelling is a compile error rather than sqlb's runtime one. No HTTP layer, no capability model |
| [**stephenafamo/bob**](https://github.com/stephenafamo/bob) | Query builder and ORM generated from the schema, the modern successor to SQLBoiler. ~1.8k stars, ~69 importers | **Overlapping at the builder.** Its importer count is in sqlb's range rather than ent's, which is worth saying plainly after this page's opener claims everything here is more used |
| [**squirrel**](https://github.com/Masterminds/squirrel) | The classic fluent builder, usually paired with sqlc for the dynamic half. ~8k stars, no commit since April 2024 | **Overlapping**, and the pairing sqlb is proposing to replace: squirrel gives conditional predicates and nothing above them, and its maintenance has stopped |
| [**prest**](https://github.com/prest/prest) | "PostgREST in Go" — a standalone binary exposing tables over HTTP. ~4.6k stars | **An alternative to `rest`**, and an honest contrast: [GO-2025-3941](https://github.com/advisories/GHSA-p46v-f2x8-qp98) is a systemic SQL-injection advisory against it, found by an independent review. sqlb's answer to that class is structural — every value is a bind parameter and every identifier is validated against the model — which is a claim worth checking rather than believing |
| **The incumbent** | Huma or oapi-codegen for the handlers, hand-rolled query-parameter parsing for the filters, openapi-typescript or hey-api for the client | **This is the workflow sqlb replaces**, and the one most projects are actually on. It works. What it costs is that the allow-list, the rejection messages and the client are three hand-maintained things that drift from the schema independently — which is the whole of sqlb's argument, and why `rest` is built *on* Huma rather than against it |

## sqlc

**What it does better.** sqlc reads your actual SQL and generates types from it,
checked against the real schema at build time. That is a stronger guarantee than
sqlb offers and it is not close: `sqlb.F("titel")` is a runtime error, and the
[typed column facade](queries/typed-columns.md) narrows that without closing
it. Anything expressible in SQL — CTEs, window functions, `DISTINCT ON` — sqlc
handles by definition, where sqlb hands you `Raw`.

**Where it structurally cannot help.** sqlc requires complete, static SQL. A
`WHERE` clause that exists only when a search box is filled in is not a query it
can generate, and the [documented workarounds](https://dizzy.zone/2024/07/03/SQLC-dynamic-queries/)
are `sqlc.narg` with `coalesce`, chains of `(@x::text IS NULL OR col = @x)`, or
"use a query builder". The last one is the honest answer, and it is where sqlb
starts.

**These are not competitors.** [`docs/with-sqlc.md`](with-sqlc.md) is the
coexistence story, and `example/withsqlc` tests it against real sqlc output:
sqlc owns the static queries, sqlb owns the dynamic list endpoint, and
`DB.Tx()` lets both share one transaction. If you already use sqlc, that is the
cheapest way to try this.

## ent

The comparison that matters most, because ent overlaps the largest part of the
pitch and does it with Meta's name, years of production use and an extension
ecosystem.

**What ent does better.**

- **Relations are a graph, not a column.** `edge.To`/`edge.From` with `Ref` and
  `Unique` express O2O, O2M, M2O and M2M, and generate traversal predicates
  (`HasPetsWith(...)`) and chained traversal. sqlb has one-directional
  references and one level of forward `?expand`; there is no reverse expansion,
  no M2M vocabulary, and no relation-spanning predicate — see
  [ADR-0022](adr/0022-references-declare-their-inverse.md) for what is missing
  and why.
- **Maturity.** ~17.2k stars and ~4,000 importers on pkg.go.dev, production use
  at scale, and an extension for this exact job: [`entrest`](https://github.com/lrstanley/entrest)
  generates an OpenAPI spec *and* an HTTP handler implementation with filtering,
  pagination and eager-loaded edges, which covers a large fraction of what
  `rest` does here.

  Two corrections to what this page used to say, both of which cut against
  sqlb's case in one direction and for it in the other.
  [`entoas`](https://github.com/ent/contrib/tree/master/entoas) is spec-only —
  *"Generate a fully compliant, extendable OpenAPI Specification document"*, with
  the README pointing elsewhere for a server — so only entrest generates
  handlers. And entrest is one maintainer, 41 stars and created in June 2024,
  which is a thinner "ecosystem" than the word implies, and thinner than ent
  itself by a wide margin.
- **The privacy layer is the same idea as `BeforeQuery`**, arrived at first.
  Opt in per schema, and it then applies to every query and mutation of that
  type regardless of call site.

**Where sqlb differs, and one place it is genuinely better.**

`Describe[T]` has no ent equivalent. ent cannot be layered over structs it did
not generate, which makes it all-or-nothing per project. sqlb can be pointed at
structs another tool produced — including stock sqlc output — and adds
capabilities without editing them.

Capability opt-in also lives in the core schema here rather than in an
extension's annotations, so one vocabulary drives both the SQL layer and the
HTTP surface. In ent, `field.Sensitive()` is roughly `Hidden`, but the
filterable/sortable exposure that shapes a REST endpoint is entrest's
annotations — a separate extension with its own release cadence.

**One concrete difference worth knowing, because it cuts both ways.** ent's
eager loading issues additional queries: *"it is not possible to load all
associations in a single `JOIN` operation. Therefore, Ent executes additional
query to load each association."* sqlb expands in one statement, a `LEFT JOIN`
and a `json_build_object` per relation
([ADR-0025](adr/0025-expansion-is-one-statement.md)).

sqlb's version is consistent by construction — one snapshot, so a foreign key
and its expansion cannot contradict each other. **ent's version inherits its
privacy rules on the target for free**, because the second query is an ordinary
read of that type. sqlb's does not: a `BeforeQuery` hook on the target does not
apply to an expansion of it, which ADR-0025 concedes as the cost of the design.
If your boundary is enforced by a hook rather than by the schema, that is a real
difference and it is not in sqlb's favour.

## PostgREST

**What it does better.** No Go code, no application layer to write, and years of
production use. If the API you want is a faithful projection of your tables and
the rules are expressible as row-level security, PostgREST is less work than
anything on this page.

(This page used to say "no deployment beyond the database", which is not true:
PostgREST is its own Haskell server process to deploy, configure and operate.
The argument does not need the overstatement — what it saves you is the
*application*, not the process.)

**The trade.** Authorization is Postgres roles and RLS; there is no application
layer, so business rules become RLS policies, `SECURITY DEFINER` functions or
views. That is a legitimate architecture and it is the one sqlb declines: the
schema sits one policy mistake away from being public, and there is nowhere to
put Go domain logic.

sqlb's answer is the inverse. A column is unreachable unless it declares a
capability, and the failure is a 400 naming what would have been accepted rather
than a leak — and `BeforeQuery` is a place for the rules that RLS would
otherwise have to carry.

## Atlas

**What it does better.** Nearly everything about migrations. Atlas is a
language-independent schema tool with declarative *and* versioned workflows,
diffing, destructive-change detection, lock-aware linting, ORM integration and
CI/CD across engines. It is a company's entire product.

**What sqlb's `migrate` actually is.** A diff between two registries, rendered
as Postgres DDL, written as files for a runner you already have — with
[lock-aware sequencing](migrations/rollout.md) for the changes whose remedy is
mechanical. It does not apply migrations and does not track which have run.

If migrations are the problem you are solving, use Atlas. sqlb's migration layer
exists so that a schema declared in Go can reach the database at all, not to
compete with a dedicated tool. The two are compatible: Atlas can consume the SQL
files sqlb writes.

**One thing that changed, and that this recommendation should carry.** Since
v0.38 (October 2025), [`atlas migrate lint` is Atlas Pro only](https://atlasgo.io/versioned/lint)
— $9 per developer per month plus $59 per CI project per month. The linting is
the part of Atlas this page was pointing at hardest, so "use Atlas" now has a
price tag on it. Everything else above still holds, and sqlb's own
[lock-aware sequencing](migrations/rollout.md) covers a much narrower set of
changes than a linter does.

## Bun

**What it does better.** A mature, widely used query builder and light ORM,
across Postgres, MySQL, MSSQL and SQLite. It solves the conditional-predicate
problem the same way sqlb does — that is what a builder is for — and it has
years of use behind it.

**What it does not have.** No HTTP layer, no OpenAPI generation, and no
per-column capability model. Building a filter endpoint on Bun means writing the
parameter parsing, the allow-lists and the rejection messages yourself. That
work is the thing sqlb is trying to delete, and it is also the work where a
missing allow-list becomes a leak.

**The sharper difference, for a Postgres project.** Bun is built on
`database/sql` — you hand `bun.NewDB` a `*sql.DB` and a dialect. sqlb is
pgx-native since [ADR-0040](adr/0040-the-driver-is-a-dependency.md). So Bun cannot
join a `pgx.Tx`: an application already holding one for its pgx or sqlc code
cannot put a Bun query inside it without going through `database/sql` for
everything. That is the whole of the sqlc coexistence story above, and it is not
available on the other side.

## What is actually unique here

Stripped down, one thing on this page is not done elsewhere:

> A single predicate AST that a Go query builder and an HTTP query grammar both
> compile into, with per-column capability opt-in, so a `BeforeQuery` hook
> constrains reads from both — and an unlisted column is a 400, not a leak.

- PostgREST gives the grammar and no place for Go domain logic.
- sqlc cannot express a conditional predicate at all.
- Bun and the typed builders give the builder and nothing above it.
- ent plus entrest is the one real counter-example, and the honest version of
  this claim has to name it: entrest compiles URL filters into ent predicates
  that pass through ent's privacy layer, which is the same shape. What is left
  is narrower — not done elsewhere *in one tool, over structs you did not
  generate, with the capability vocabulary in the core schema rather than in an
  extension's annotations*.

Second, smaller: `Explain`-as-a-gate. It validates a query against the live
schema without running it, so it fails on a migration that was written and never
applied — which a compile-time column check cannot see. It costs a database in
CI, which sqlc's approach does not.

## When not to use sqlb

Stated plainly, because a comparison page that cannot answer this is marketing:

- **You need it to still exist in three years.** One author, no consumers, no
  production history. Use ent.
- **Your queries are mostly static.** The thing sqlb is for does not arise, and
  sqlc gives a stronger guarantee for that work.
- **You need a graph.** Many-to-many, reverse expansion, traversal predicates —
  ent has them and sqlb does not.
- **Migrations are the problem.** Atlas.
- **You are not on Postgres.** sqlb is Postgres-only and
  [will stay that way](adr/0001-postgres-only.md).
- **Your team wants an ORM.** This is not one, and does not intend to be.

---

*Claims about other projects were checked against their documentation, their
repositories and pkg.go.dev in August 2026, and carry links. They will go stale;
if you find one that has, please open an issue — an out-of-date comparison is
worse than none. [Issue #79](https://github.com/jryannel/sqlb/issues/79) is what
that looks like working, and four of the six things it corrected made sqlb's
case weaker rather than stronger.*
