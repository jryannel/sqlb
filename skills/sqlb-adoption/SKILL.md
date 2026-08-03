---
name: sqlb-adoption
description: Use when evaluating whether an existing Go/Postgres codebase should adopt sqlb, or planning that adoption — "should we use sqlb", "how much of our API would sqlb replace", "migrate/port from sqlc to sqlb", "survey this database", "pick a pilot endpoint", or sizing the work before committing to it. A five-step census producing a ratio and a pilot, with the stop conditions that end the evaluation early.
---

# Surveying a codebase for sqlb adoption

This produces **a ratio and a pilot**, not a total. An adoption has two halves —
the database and the routes in front of it — and only the database half has a
command. The rest is shell over one repository's conventions, deliberately: a
program that parsed chi, huma, gin, echo and a hand-rolled mux would be a router
parser before it was an adoption tool, and the step that decides the answer
(is this handler a list surface or a domain verb wearing a `GET`?) is a judgement
no parse recovers.

**Counting is only meaningful once you know the per-endpoint price.** Read *The
per-endpoint price* at the bottom first, or the census produces numbers you
cannot value.

## Step 0 — establish the unit, before counting anything

**An app is the schema unit, not the repo.** A monorepo with four services on
four databases is four surveys with four verdicts, not one. Getting this wrong
produces a blended ratio that describes nothing that exists. For a modular
monolith on one database, the module is the unit — which is what `-modules`
is for below.

## Step 1 — the database, and it goes first

A route census concluding "twelve resources are ready to mount" is worthless if
nine of their tables carry a composite primary key the DSL cannot declare. **The
database sets the ceiling.**

```bash
go run ./cmd/sqlb survey -modules billing,catalog "$SRC_DSN" "$SCRATCH_DSN" > survey.md
```

Two DSNs, and their roles are not interchangeable:
`<src-migrated-dsn> <dst-empty-dsn>` — the first has the schema already applied,
the second must be **empty**, because the survey replays into it. This is the one
verb that compiles nothing: it builds its registry by introspecting a live
database rather than importing a declaration, so there is no package argument.

The report has four parts. Read them in this order:

- **Verdict** — the headline count.
- **Phase B, per-table isolation** — the triageable part, split into
  **Refused** / **Partial** / **Clean**. Every table is introspected *alone* as
  well as together, and that is the point: introspect reports per-construct but
  the drift gate is per-registry, so **one unmodelable table takes its whole
  module out of the gate.** A flat list of skips hides which tables are
  adoptable today.
- **Phase C, round-trip fixpoint** — whether what was read back re-renders to
  the same thing.
- **By module** — where `-modules` prefixes group the verdict.

Flags worth knowing:

- `-modules a,b,c` — table-name prefixes to group by.
- `-modules-file <file>` — JSON mapping module → exact table names, when
  prefixes cannot cover every table. **Wins over `-modules`.**
- `-exclude t1,%pat` — **extends** the built-in migration-runner list rather
  than replacing it (`goose_db_version`, `schema_migrations`,
  `atlas_schema_revisions`, `_sqlx_migrations`, `flyway_schema_history`). `%`
  matches any run of characters, so a goose-per-module monolith — one bookkeeping
  table per module, none matching the defaults — is covered by
  `-exclude '%_schema_migrations'`.

> **🛑 Stop condition.** If the survey blocks most tables, stop here. The route
> census cannot be acted on, and fixing the schema half is the actual work.
> This is a cheap answer, and reaching it early is the survey doing its job.

## Step 2 — the route table

The cheapest useful count. A route's *shape* lives in its last segment, so a
leaf census is sufficient for classification — `GET /{id}` is a read whether it
is mounted at `/api/tasks` or `/api/v2/admin/tasks`.

```bash
find . -name '*.go' -not -name '*_test.go' -print0 \
  | xargs -0 grep -hoE '\br\.(Get|Post|Put|Patch|Delete)\("[^"]*"' \
  | sed -E 's/^r\.//; s/\("/ /; s/"$//' \
  | sort | uniq -c | sort -rn | head -30
```

The first five lines are the CRUD vocabulary; everything below is the tail. Split
them:

```bash
find . -name '*.go' -not -name '*_test.go' -print0 \
  | xargs -0 grep -hoE '\br\.(Get|Post|Put|Patch|Delete)\("[^"]*"' \
  | sed -E 's/^r\.//; s/\("/ /; s/"$//' \
  | awk '{ if (($1=="Get"||$1=="Post"||$1=="Put") && $2=="/") print "crud";
           else if (($1=="Get"||$1=="Patch"||$1=="Put"||$1=="Delete") && $2 ~ /^\/\{[A-Za-z]+\}$/) print "crud";
           else print "other" }' \
  | sort | uniq -c
```

In one real corpus this was **92 crud / 220 other — 29%**. That is the fraction
of the route table sqlb is even arguing about. The other 220 (`/{id}/complete`,
`/bulk-assign`, `/upload`, `/billing/webhook`) stay hand-written on the same
router, by design. **A survey reporting "sqlb replaces the API" has miscounted.**

For which *resource* a route belongs to, nested routers hide the prefix, so
flatten the tree — `docs/surveying-a-codebase.md` carries `routes.awk`, which
tracks brace depth to compose full paths. Note its trailing-slash
normalisation: `r.Route("/tasks")` plus `r.Get("/")` composes to `/api/tasks/`,
which sorts apart from `/api/tasks` and splits one resource into two
half-populated ones.

The shape to look for is a **five-operation CRUD core with domain verbs hanging
off it**. `rest.Resource` takes the five; the verbs are hand-written handlers or
declared actions, and they are usually the majority.

> **🛑 Stop condition.** CRUD-shaped routes under ~15% of the table means the API
> is mostly domain verbs, and sqlb has little to say about it.

## Step 3 — the queries, and the bucket that matters

By kind first, but do not read anything into it:

```bash
grep -rhoE '^-- name: [A-Za-z0-9_]+ :[a-z]+' internal/db/queries \
  | awk '{print $NF}' | sort | uniq -c
```

`:one` dominating at ~56% is typical and is **not** an adoption signal — most of
those are a single-row read inside a domain verb, which sqlb does not touch.

**By shape is the classification that decides the answer:**

```bash
awk '
  /^-- name: / { if (n) classify(); n=$3; body=""; next }
  { body = body " " toupper($0) }
  END { if (n) classify() }
  function classify() {
    if (body ~ /WITH [A-Z_]+ AS|OVER *\(|GROUP BY|UNION|DISTINCT ON|GENERATE_SERIES/) print "reporting"
    else if (body ~ /SQLC\.NARG|COALESCE\(\$|IS NULL OR/) print "dynamic-workaround"
    else if (body ~ / JOIN /) print "join"
    else print "single-table"
  }
' internal/db/queries/*.sql | sort | uniq -c | sort -rn
```

| Bucket | Where it goes |
|---|---|
| `dynamic-workaround` | **sqlb.** The structural win, and the bucket to read first |
| `single-table` | sqlb where it backs a mounted resource; otherwise leave it alone |
| `join` | Candidates for `?expand` — needs a declared reference, so check against Step 1 |
| `reporting` | **sqlc, permanently.** `Raw` is an escape hatch, not a feature |

**The `dynamic-workaround` count is what the adoption argument rests on.** Each
one contains `sqlc.narg`, `COALESCE($1, column)` or
`($1::text IS NULL OR column = $1)` — a hand-written simulation of a `WHERE`
clause that depends on which parameters the request carried. sqlc cannot express
that, not for want of a feature but because the query does not exist yet when
sqlc runs. That is the one thing sqlb is structurally better at.

**Deflate the bucket before believing it.** A `COALESCE($1, x)` in an `UPDATE`'s
`SET` list is a partial-update idiom, not a dynamic filter:

```bash
awk '
  /^-- name: / { if (n) classify(); n=$3; body=""; next }
  { body = body " " toupper($0) }
  END { if (n) classify() }
  function classify() {
    if (body ~ /WITH [A-Z_]+ AS|OVER *\(|GROUP BY|UNION|DISTINCT ON|GENERATE_SERIES/) return
    if (body ~ /SQLC\.NARG|COALESCE\(\$|IS NULL OR/) print (body ~ /UPDATE /) ? "update-idiom" : "real-filter"
  }
' internal/db/queries/*.sql | sort | uniq -c
```

In the reference corpus 99 became **75 `real-filter` + 24 `update-idiom`** — a
quarter evaporated on one line of shell. Run the deflation: a census that
survives it is worth showing to someone who has to approve the work.

## Step 4 — the scope predicate

```bash
awk '/^-- name: /{ if(n) print (body ~ /ORG_ID|TENANT_ID|ORGANIZATION_ID/) ? "scoped" : "unscoped";
                   n=$3; body=""; next }
     { body = body " " toupper($0) }
     END { if(n) print (body ~ /ORG_ID|TENANT_ID|ORGANIZATION_ID/) ? "scoped" : "unscoped" }' \
  internal/db/queries/*.sql | sort | uniq -c
```

**Both halves are findings.** The scoped count is what becomes one `BeforeQuery`
hook per model — a predicate repeated by convention becomes one the builder
cannot be asked to skip, which is why a schema declaring its rows confined
refuses to mount without one. That is a *safety* argument rather than a
line-count one, and often the stronger case.

The unscoped ones **need reading, not counting.** Some are legitimately global
(lookup tables, the auth path before a tenant is known), some inherit scope from
a parent via a join, and some are the bug the hook would have prevented. The
count's value is that it bounds how long the reading takes.

## Step 5 — the client

The generated TypeScript client is usually the largest single win, so size what
it would replace: hand-written SDK code (non-test), and hand-maintained cache
keys. See `docs/surveying-a-codebase.md` §4 for the greps.

## Reading the result

| If | Then |
|---|---|
| `real-filter` is a large share | The strongest case. The row sqlb exists for |
| ≥ 5 collections have a full CRUD set | A pilot has an obvious first subject |
| `scoped` high, `unscoped` unexplained | Adopt for the safety argument before the line-count one |
| `reporting` is a large share | A smaller adoption than it looked; sqlc keeps that half |
| The survey blocks most tables | **Stop.** Fix the schema half first |
| CRUD routes under ~15% | **Stop.** The API is mostly domain verbs |

## Choosing the pilot

Intersect three lists: collections with a **full CRUD set**, whose tables the
survey reports **adoptable**, and whose queries fall in the deflated
**`real-filter`** bucket.

**The smallest surviving member is the pilot, not the most important one.**
Read-only, one endpoint, behind a flag, with the old handler still live.

**If the intersection is empty, the survey has done its job** — that is a
cheaper answer than a pilot would have been.

## The per-endpoint price

What moving one endpoint costs. Four stages, each a place to stop:

| | Owns the query | Owns the capabilities | Deps |
|---|---|---|---|
| **1** | `query.sql`, static | the handler | pgx, sqlc |
| **2** | the builder, at runtime | `Describe` + a map in the handler | ＋ sqlb |
| **3** | the builder, at runtime | the schema declaration | the same |
| **4** | the generated resource | the schema declaration | ＋ huma |

Lines of Go per stage: **36 → 59 → 15 → 26.** Stage 2 is *bigger* than stage 1,
and pretending otherwise misrepresents the trade — what it buys is the sort and
the end of the always-sent predicates, not brevity. **The drop happens at stage
3**, when the capabilities stop being restated. Stage 4's 26 lines mount every
resource in the schema, not just this one.

Where to stop: **2** if the win you wanted was filter and sort (one dependency,
no schema work, reversible in an afternoon). **3** if you have or will generate
a schema declaration — the largest single improvement. **4** if you want the
REST surface generated and accept huma. Going backwards is cheap at every step.

**Two things change on the wire at stage 3,** and both are release notes rather
than internal details. The query string becomes the documented filter grammar
(`?status=eq.published&view_count=gte.100&per_page=20`) instead of whatever
spelling the handler invented. And `?search` widens from whatever the SQL said
to every column the schema declared `Searchable`. If neither is acceptable yet,
`restcompat` and stopping at stage 2 are both real answers.

## What this cannot tell you

Stated because a census that reads as complete is worse than one that reads as
partial.

- **Whether a `GET` collection is a list surface.** Returning every row of a
  small table is not the same endpoint as filter-sort-paginate over a large one,
  and only one is worth generating. Grep for query-parameter reads
  (`URL.Query().Get("sort"|"limit"|"search")`) to shortlist, then read them.
- **What the handlers do besides query.** Status transitions, activity emission,
  billing limits and geocoding live in the same functions and **relocate into
  hooks rather than disappearing.** A LOC count of the handler layer overstates
  the saving by that fraction — in the reviewed corpora, about two thirds of the
  single-row surface.
- **Whether the wire format can change.** Field casing, error envelopes and
  pagination shape are a client contract; whether it can break is a product
  question with no signal in the source.
- **Whether the migration history is honest.** That is `shadow.Build` against
  `introspect`, and a disagreement there invalidates the whole schema half.
- **Which non-CRUD routes are secretly resources.** `POST /{id}/duplicate` is a
  verb; `GET /{id}/messages` is very often a nested collection that would mount
  cleanly. The census flags them; only reading resolves them.

## Troubleshooting

- **The scratch DSN needs an empty database**, and `survey` replays into it.
- **A locale mismatch on the scratch database** can fail the replay on newer
  Postgres majors; creating it with `LC_ALL=C` is the workaround to try before
  assuming the survey is at fault.
- **`sqlb survey` with no arguments** prints what its two DSNs must be.
