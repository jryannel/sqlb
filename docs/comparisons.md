# How sqlb compares

Every evaluator builds this table silently. Better to build it here, including
the rows where sqlb loses.

Read the [status](compatibility.md) first, because it outranks everything below:
sqlb is pre-1.0, has one author and no observed consumers. Every tool on this
page is older, more used, and more likely to still exist in three years. Nothing
here argues otherwise.

## The short version

| If you need | Use | Why |
|---|---|---|
| Static queries, checked against a real schema at build time | **sqlc** | Its whole guarantee. sqlb's is weaker by design |
| Reporting, recursive CTEs, window functions | **sqlc** | sqlb sends these to `Raw`, which is a declared non-goal |
| A mature graph model — traversal, M2M, nested eager loading | **ent** | Years of production use and an extension ecosystem |
| Migrations as the main event, across engines, with linting and CI | **Atlas** | A funded company doing exactly this |
| An API with no Go code at all | **PostgREST** | Nothing to write or deploy but the database |
| A filter/sort/search endpoint whose predicates vary per request | **sqlb** | The reason it exists |
| The same constraint applied to every read, hand-written and HTTP alike | **sqlb** | `BeforeQuery` has no equivalent in the others |

## sqlc

**What it does better.** sqlc reads your actual SQL and generates types from it,
checked against the real schema at build time. That is a stronger guarantee than
sqlb offers and it is not close: `sqlb.F("titel")` is a runtime error, and the
[typed column facade](guide/queries-and-hooks.md) narrows that without closing
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
- **Maturity.** Thousands of stars, production use at scale, and an ecosystem —
  `entrest` and `entoas` generate an OpenAPI spec plus handlers with filtering,
  pagination and eager-loaded edges, which covers a large fraction of what
  `rest` does here.
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

**What it does better.** No Go code, no deployment beyond the database, and
years of production use. If the API you want is a faithful projection of your
tables and the rules are expressible as row-level security, PostgREST is less
work than anything on this page.

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
[lock-aware sequencing](guide/migrations.md) for the changes whose remedy is
mechanical. It does not apply migrations and does not track which have run.

If migrations are the problem you are solving, use Atlas. sqlb's migration layer
exists so that a schema declared in Go can reach the database at all, not to
compete with a dedicated tool. The two are compatible: Atlas can consume the
SQL files sqlb writes.

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

## What is actually unique here

Stripped down, one thing on this page is not done elsewhere:

> A single predicate AST that a Go query builder and an HTTP query grammar both
> compile into, with per-column capability opt-in, so a `BeforeQuery` hook
> constrains reads from both — and an unlisted column is a 400, not a leak.

- PostgREST gives the grammar and no place for Go domain logic.
- ent gives filtering, but the filter surface and the query builder are
  different code paths.
- sqlc cannot express a conditional predicate at all.
- Bun gives the builder and nothing above it.

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

*Claims about other projects were checked against their documentation in July
2026 and carry links. They will go stale; if you find one that has, please open
an issue — an out-of-date comparison is worse than none.*
