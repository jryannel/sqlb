# ADR-0026: A vector column declares its index, and similarity search is its own operation

- **Status:** Exploring — nothing is built, and **it is not in 1.0**. The
  condition that qualifier carried has now been tested: a port did need it, and
  the answer is still not-in-1.0, because what blocked the port was the driver
  rather than this record's design. See the 2026-07-30 revision. This records
  the shape before the first line of it, which is the order
  [the README](README.md) asks for and the opposite of
  [ADR-0025](0025-expansion-is-one-statement.md)
- **Confidence:** Low — nothing here is built. The unindexed half is grounded in
  a working module (`core/rag` in `subject-mono`, an anonymised application
  repository) rather than reasoned about, which
  is why it is the starting point; the indexed half is entirely argument, and its
  sharpest claims are about how Postgres behaves under a filter — exactly the
  kind of claim [ADR-0025](0025-expansion-is-one-statement.md) learned not to
  trust until a real database has seen it
- **Decided:** 2026-07-28
- **Last reviewed:** 2026-07-30 (read against the two port reports and ADR-0040)

## Context

There are no vector references anywhere in the tree, and the feature fails in
three separate places if you try:

- `migrate.sqlType` ends in `unknown type %q`, so no declaration renders. Not
  even `OfType(schema.Type("vector(1536)"))`, the escape hatch external
  references use, survives the DDL layer.
- `migrate.createIndex` renders method, columns and `WHERE`. There is no operator
  class and no `WITH`, so `USING hnsw` renders and means nothing.
- `introspect` refuses a `vector` column with a diagnostic rather than guessing,
  which is right — but it means adopting an existing pgvector database produces a
  `schema.go` with the embedding column missing.

The query half, meanwhile, already works. `sqlb.OrderBy(sqlb.Raw{SQL: "embedding
<-> ?", Args: …})` compiles, binds and returns correct rows today. **That is the
problem, not the reassurance.** It returns correct rows by sequentially scanning
the table and sorting it, and Postgres reports no error for that. A vector
feature built on `Raw` looks finished and is a table scan.

### Why the window-function answer does not work here

The obvious move is the one this project already made once. Window functions are
a documented non-goal ([vision.md](../vision.md)): they go to `Raw`, or to sqlc,
and [with-sqlc.md](../with-sqlc.md) draws the line at *which queries go where*.

That line cannot be drawn here, because a vector is not a query shape. It is a
**column**, and the same document says the schema is sqlb's:

```
example/blogschema/schema.go     the one declaration you edit
  → go run ./example/withsqlc/gen     renders it to DDL
  → example/withsqlc/schema.sql       what sqlc reads
```

sqlc can type `ORDER BY embedding <-> $1` perfectly well. It types it against
`schema.sql`, which `migrate.Diff` renders from the declaration, which cannot
express the column. So the sqlc split does not rescue this the way it rescues
window functions — it breaks on it. `mise run generate-check` fails, or worse,
`schema.sql` is edited by hand and the two sources of truth quietly separate.

This is observed rather than predicted. `core/rag` in `subject-mono` runs pgvector
under sqlc, and its `schema.sql` is a hand-maintained mirror of the migration
because the migration cannot say what the migration means:

> sqlc parses `migrations/` to derive the schema for query type-checking, but
> the 00001 migration uses a `%%EMBEDDING_DIM%%` sentinel inside `vector(...)`
> that sqlc's parser can't tolerate. […] This file mirrors the post-migration
> shape with a concrete dim of 1536. Bump it here if you change the runtime
> default; the runtime value still controls what gets created in pg.

Two declarations, one of which is documented as needing to be bumped by hand.
The dimension wants to be a *value* — it changes when the embedding model
changes — and SQL text has nowhere to put one, so it gets a sentinel and a
rewrite step and a mirror file that can drift.

Either sqlb learns the type, or the arrangement it documents stops holding for
any project with an embedding column in it. That is the hinge, and it is why
this is a decision rather than a feature request.

### What one of these actually looks like

The same module disagrees with the obvious vector design in two ways, and both
are load bearing.

**It has no vector index.** `rag_chunks` carries a btree over `(scope_kind,
scope_id)`, a GIN over `metadata`, and nothing at all on `embedding`. Every
search is an exact sort over the rows a mandatory tenant scope already selected:

```sql
WHERE c.scope_kind = @scope_kind AND c.scope_id = @scope_id
  AND c.metadata @> @filter::jsonb
ORDER BY c.embedding <=> @query::vector
LIMIT @top_k
```

That is not an oversight for this record to correct. It is the right shape at
that size — an exact sort over a few thousand scoped rows beats an approximate
index that the same scope filter would fight — and it is the configuration a
vector feature has to support *first*, before it supports any index at all.

**Its filters are open-ended, and that is the whole point of them.** Additional
information rides in a `metadata JSONB` column and is narrowed by containment
against the GIN index. `@filter` defaults to `'{}'`, which every JSONB contains,
so one query text serves both the filtered and the unfiltered search. Nothing
declares which keys may be filtered, because a caller attaching whatever it
likes and narrowing by it later is the feature.

Set that against Convex's `filterFields`, which insists every filterable key be
declared up front, and the two look like opposite philosophies. They are not.
They are the two ends of one axis, and **the axis is whether the index is
approximate**. Convex declares filter fields because Convex always uses an ANN
index and an undeclared filter would silently cost recall. The rag module owes
no such declaration because it has no ANN index to lose recall against.

### An ANN index is not an index in the sense the rest of the schema means

A btree index is an optimisation. The query is correct with or without it and
the planner decides. Every assumption in `migrate` and `schema.Index` is built
on that.

An HNSW index is none of those things:

- **It changes the answer.** Results are approximate. Recall is a tuning
  parameter (`hnsw.ef_search`), not a property of the data.
- **It is used only for `ORDER BY <operator> LIMIT k`.** Any other query shape
  ignores it.
- **It is built for one distance metric.** An index created with
  `vector_l2_ops` does not serve a `<=>` cosine query. Postgres does not
  complain; it sequentially scans. A metric typo is a correct answer that is
  a thousand times slower, which no test asserting on rows can see — the same
  quiet-failure shape as the `row_to_json` leak in ADR-0025.

### Filters and approximate search fight, and Postgres does not say so

An HNSW scan finds *k* candidates and the `WHERE` clause then runs over them.
Ask for 10 nearest with `WHERE tenant_id = $1` and get 2 back, or 0, with no
error and no indication that anything was dropped. pgvector 0.8 added iterative
index scans (`hnsw.iterative_scan`) to re-scan until enough rows survive, which
converts under-recall into latency but does not make the filter free.

So "which filters may accompany a vector search" is a question the schema has to
answer in advance — *once there is an ANN index*, and only then. Without one the
question does not arise, because an exact sort over whatever the `WHERE` left is
exact by construction and composes with any filter sqlb already allows. The
declaration is not a property of vector search. It is the price of the index.

### What Convex does, and which part of it transfers

[Convex's vector search](https://docs.convex.dev/search/vector-search) declares
the index in the schema:

```js
.vectorIndex("by_embedding", {
  vectorField: "embedding",
  dimensions: 1536,
  filterFields: ["cuisine"],
})
```

and issues the search as a **separate API**, in an action rather than a query,
returning only `_id` and `_score` — the documents are fetched afterwards:

```js
ctx.vectorSearch("foods", "by_embedding", {
  vector, limit: 16, filter: (q) => q.eq("cuisine", "French"),
})
```

Filters may only reference declared `filterFields`, and only as equality or
`OR`. There is no pagination, no combining with another index, and `limit` is
capped at 256.

Read as API design that is a lot of restrictions. Read against the section
above, every one of them is the physics: filter fields are declared because an
approximate index cannot serve an undeclared filter without losing recall; the
result is ids and scores because a ranked candidate set is not a page of a
collection; there is no pagination because an offset into an approximate result
is not a position in anything stable.

The disanalogy is worth stating too. Convex owns its storage engine, so it can
*enforce* that a vector search is unfiltered and unpaginated. sqlb sits on
Postgres, which will cheerfully run the incoherent query and return a plausible
answer. Anything this project wants enforced, it has to refuse itself.

## Decision

**A vector column declares its dimension; an index is optional and carries the
metric when there is one; and similarity search is a distinct operation rather
than a parameter on the list endpoint.**

The unindexed declaration is the starting point and it is complete on its own —
this is the `core/rag` shape, and it needs no index, no metric and no declared
filters:

```go
schema.Vector("embedding", ragcfg.Dim).Searchable()
```

Adding an index is a second, separate decision, taken when exact search stops
being fast enough:

```go
schema.Vector("embedding", ragcfg.Dim).
    Searchable().
    Index(schema.HNSW, schema.Cosine).
    Where("deleted_at IS NULL")
```

which renders

```sql
"embedding" vector(1536) NOT NULL
CREATE INDEX "docs_embedding_hnsw" ON "docs" USING hnsw ("embedding" vector_cosine_ops)
    WITH (m = 16, ef_construction = 64) WHERE deleted_at IS NULL;
```

Eight things follow, and they are the decision as much as the syntax is.

**The dimension is part of the type, and it is a Go expression rather than a
literal.** `TypeVector` carries it the way `TypeVarchar` carries `Size`, and
`ragcfg.Dim` above is the entire answer to the `%%EMBEDDING_DIM%%` sentinel: a
Go DSL has somewhere to put a value, so the dimension needs no rewrite step and
no mirror file. It also means `Diff` *notices* when the dimension changes and
proposes the rewrite and reindex, which the sentinel silently performs by
producing a different database from the same migration text. A dimension over
2,000 is accepted for the column and refused for an index, naming the limit —
a `vector` holds up to 16,000 dimensions and an index cannot cover them.

**An unindexed vector column is a supported configuration, not a missing
index.** `Near` over one is an exact sort, it composes with every filter the
model already allows, and it is the correct shape whenever a mandatory scope
already cuts the candidate set small. `Lint` may observe that a vector column
has no index; it does not warn, because at rag's size the index would be the
mistake.

**The metric lives with the index, and once there is an index a query cannot
pick a different one.** `Near(vec)` compiles to the operator the declared index
was built for; with no index it compiles to the operator the caller named,
because there is nothing for it to disagree with. Asking for a metric no
declared index serves is refused at build time naming the ones that are
([ADR-0011](0011-actionable-errors.md)), rather than answered by a sequential
scan. This is the most important line in the record: the failure it prevents is
invisible in results and shows up as a production latency graph.

**HNSW is the default index, because the migration model chooses it.** An
IVFFlat index built on an empty table clusters nothing and must be rebuilt once
data arrives — and a `CREATE INDEX` generated by `Diff` for a new table runs at
exactly that moment. HNSW builds incrementally and is correct built empty.
IVFFlat stays declarable for the case where someone is loading a corpus and
knows their row count.

**Which filters may accompany a search depends on the index, and on nothing
else.** With no index, any filter the model already allows — including an
open-ended JSONB containment over a `metadata` column. With an ANN index, a
constant predicate becomes the index's `WHERE` (the partial-index case, which
`schema.Index` already supports and which covers soft-delete), a variable
predicate must be declared on the index and is served by iterative scans that
trade latency for recall, and an undeclared filter is refused with the list of
the declared ones. Convex's `filterFields` is the second regime; sqlb has to
carry both, because unlike Convex it can offer the first.

**The score is similarity, not distance.** `1 - (embedding <=> query)` for
cosine, and the operator's own inversion for the others, so that larger is
closer and the number means the same thing whichever metric produced it. This is
what `core/rag` returns and what Convex's `_score` is, and it costs one
subtraction to not make every caller remember which way round their metric
runs.

**Search is its own operation.** `schema.OpSearch` extends the mask (`Op` is a
`uint8` with three bits free) and mounts `POST /documents/search`, not a `?near=`
on the list endpoint. Three independent reasons, and the first alone settles it:

- 1,536 float32 values is roughly 20KB of text. It does not fit in a URL, so it
  was never going to be a query parameter.
- `?sort=created_at&near=…` is incoherent. Either the sort destroys the ranking
  or the ranking ignores the sort.
- Pagination here is `page`/`offset` (`filter.parsePagination`). `OFFSET 100` into
  an approximate neighbour set is not page six of anything — it is a worse
  hundred-candidate scan whose tail is the part the index is least sure about.
  The search operation takes `limit` and no offset.

**The embedding does not go over the wire.** A vector column is `Hidden`, and
not optionally. Nothing a REST client can do with 1,536 raw floats is better
served than by the search operation, and putting a 20KB column in a list
response is a mistake worth making structurally impossible rather than
warning about. `SearchChunks` selects content, metadata and a score and never
the embedding, which is the same conclusion reached by hand. Go code going
through the query engine gets the column, exactly as it bypasses `ReadOnly` and
`Immutable` today.

Two supporting pieces come with it: `CREATE EXTENSION IF NOT EXISTS vector;`
ordered ahead of every table in `Diff` — the first schema-level object migrate
emits, and the collision argument that kept `CREATE TYPE` out in
[ADR-0017](0017-enums-as-text-and-check.md) does not apply, because an extension
has one global name owned by nobody and `IF NOT EXISTS` makes two modules
declaring it idempotent — and `sqlb.Vector`, a `[]float32` with `Scan` and
`Value` over pgvector's `[1,2,3]` text form, since `[]float32` implements
neither and `Executor` is `database/sql`.

**That last clause is superseded.** [ADR-0040](0040-the-driver-is-a-dependency.md)
decides that the engine depends on pgx and `database/sql` stops being the
contract, which removes the constraint that forced the text form — and it cites
this record as its strongest evidence, since the compromise here was the clearest
case of the driver bending a design rather than merely inconveniencing one. When
this is built, `sqlb.Vector` is pgvector's binary codec registered on the pool,
not a text literal parsed element by element in Go. The measured difference on a
50-row page of 1536-dimension embeddings is 2.7× the time and 21× the memory.
The rest of this record — metric in the schema, the index as the second
declaration, search as its own operation — is unaffected either way.

## Consequences

**What this buys.** The schema stays the single source of truth for a project
that uses embeddings, which is the property the whole sqlc arrangement rests on
and the one thing `Raw` cannot restore. Adoption round-trips: `introspect` maps
`vector(1536)` back and the column survives into the generated `schema.go`
instead of vanishing. And the mistake that has no symptom — a metric that does
not match its index — is refused at build time rather than served slowly.

Staging the index as a second decision is most of what it buys in practice. The
useful configuration is available with one declaration and no new index
machinery, which means the risky half of this record — opclasses, build
parameters, iterative scans, recall — can be deferred until something actually
outgrows an exact scan, and can be dropped entirely if it never does.

**What this costs.** Five things, and the first is about the project rather than
the feature.

*This is the largest single surface added since expansion, and it is the first
that is not table stakes.* A type, an index kind with build parameters, a Go
value type, a REST operation, an extension in the diff engine, an introspect
mapping and a lint rule. [ADR-0024](0024-no-annotation-slot.md) quotes the
outside review's strategic point back at itself: maintaining a DSL, codegen, a
compiler, a migration engine, an introspector, a REST layer and a filter grammar
alone is the thing most likely to kill this project. This adds an eighth thing,
and the honest reason it is worth it is the schema argument above, not that
vector search is popular.

*Approximate results are hard to test the way this project tests.* `pgtest`
judges by asking Postgres rather than by comparing a golden string, and an ANN
query's answer is legitimately non-deterministic across index builds. Either
fixtures stay small enough that `ef_search` makes the scan exact — which tests
the SQL and not the index — or recall is asserted statistically.
[ADR-0016](0016-guards-proven-both-ways.md) asks that a guard be made to fail on
purpose; the guard here is "the index is used", and the way to fail it on
purpose is to read a query plan, not a result set.

*The index build is the slowest migration this project will generate.* An HNSW
build over a large table takes hours. `CREATE INDEX CONCURRENTLY` cannot run
inside a transaction, and [ADR-0014](0014-migrations-and-import.md) already
records what file-scoped transaction control costs: the change drags every
unrelated change in the file out of its transaction. A vector index wants its
own migration file, and nothing enforces that yet.

*`CREATE EXTENSION` usually needs privileges the migration role does not have.*
Managed Postgres allow-lists it; a locked-down role does not get it. The failure
is at the first statement of the first migration, which is at least a good place
to fail.

*The metadata half is a debt this record incurs and does not pay.* The rag
pattern's filter is `metadata @> '{"lang":"de"}'::jsonb`, and the REST filter
grammar cannot express it: `filter.operators` has no containment operator, and
`filter.Coerce` refuses a `json.RawMessage` outright — a JSONB column is not
filterable through sqlb today at all. So the first regime above is only fully
available to Go callers via `RawPred` until the grammar grows containment. That
work has nothing to do with vectors and would be worth doing without them; it is
listed here because a vector search whose companion filter has to be `Raw` is
not the feature this record claims to deliver.

## What would change our mind

- **Any of the three physical claims failing against a real database.** That
  filtered HNSW silently under-returns, that a mismatched opclass falls back to
  a sequential scan, that an empty-table IVFFlat is useless — each is load
  bearing and each is currently a claim about Postgres that this repository has
  not tested. If one is wrong, the decision it supports is unsupported. Testing
  them is the first work, before the DSL.
- **A second metric on one column.** A table indexed for both cosine and L2 is
  declarable as two indexes, and `Near(vec)` immediately becomes ambiguous. The
  fix is an optional metric argument that selects among the declared indexes,
  which is additive — but it means the schema-only story was too simple, and
  that is worth knowing.
- **Someone needing the raw vector over the wire.** Client-side re-ranking is the
  plausible case. One concrete instance and the unconditional `Hidden` becomes a
  default with an override.
- **An ANN index whose declared filters cannot express what a caller narrows
  by.** The rag pattern's `metadata @> $1` is arbitrary by design, and there is
  no declaration that covers "any key the caller might send". If a corpus
  outgrows exact search *and* needs open-ended metadata narrowing, the two
  regimes collide and this record has no answer — the honest options at that
  point are a partial index per known filter value, accepting whatever recall
  iterative scans give, or moving that corpus out of Postgres. Finding out which
  is a measurement, and it is the measurement most likely to be needed first.
- **A second extension wanting the same machinery.** PostGIS is the obvious one.
  If it turns up, `CREATE EXTENSION` generalises and the vector-specific parts
  of the diff engine should be re-read as a special case of something.
- **The surface cost arriving as bugs elsewhere.** If maintaining this visibly
  slows the parts of sqlb that are table stakes, the fallback is not to fix it
  but to cut back to the opaque-type alternative below, which is a tenth of the
  code and unblocks the thing that actually forced the decision.

## Cost of change

**Declining today is free, and it stays free until the first line is written.**
Nothing exists, no schema declares a vector, and this record costs nothing to
reverse to "not yet, use a dedicated store".

After that the bill splits three ways.

*Cheap.* The DDL rendering, the opclass and `WITH` spellings, the build
parameters, `sqlb.Vector`'s internals, the choice of HNSW defaults. All of it is
generated per migration or per statement and none of it is anything a caller
wrote. The whole index half is cheap in one particular way worth naming: because
an unindexed column is a complete configuration, the index machinery can be left
unbuilt without leaving the feature half-finished, and can be removed later
without touching a schema that never declared one.

*Expensive.* `TypeVector` and `schema.Vector` are in `schema`, which is
Stable-tier ([ADR-0013](0013-no-internal-split.md)) where changes are breaking
changes. `POST /resource/search` and its response shape are a wire format from
the first response, and [ADR-0025](0025-expansion-is-one-statement.md) already
paid to learn that the wire format is the expensive half. A shipped HNSW index
whose metric changes is an hours-long rebuild on every deployment holding data.

*Asymmetric in the useful direction, on the one decision most likely to be
wrong.* Putting the metric in the schema and later allowing it in the query is
additive: an optional argument that defaults to the declared index. Going the
other way — shipping a metric argument and later insisting it be declared —
breaks every caller. Schema-first is the cheaper direction to be wrong in, which
is the same shape as [ADR-0017](0017-enums-as-text-and-check.md)'s reason for
starting from text.

## Alternatives considered

**Decline it. Send vectors where window functions went.** The strongest
alternative, and the one this project's own non-goals argue for. It loses on the
schema half and only on the schema half: `Raw` and sqlc between them handle the
*query* completely, and neither can make `migrate.Diff` render a column it has
no type for. If the schema were not declared to be sqlb's, this would be the
answer.

**An opaque type escape hatch** — `schema.Opaque("embedding", "vector(1536)")`,
a column whose SQL type sqlb passes through without understanding. Genuinely
close, and it is the minimum that unblocks the sqlc arrangement: the DDL
renders, `generate-check` passes, the query goes through `Raw`. It loses because
it buys the DDL and stops: no opclass, so the index is still wrong; no Go type,
so the column cannot be scanned; no capability model, so the embedding is
serialised into every list response. It is [ADR-0024](0024-no-annotation-slot.md)'s
argument arriving again — the slot is the small half of the feature. It is also
the right retreat if the surface-area worry above wins, and it is the reason
that retreat is available rather than theoretical.

**Declared filter fields unconditionally, as Convex has them.** The first draft
of this record did exactly that, and `core/rag` is why it does not. Convex can
require declaration because it always has an ANN index behind the search; sqlb
cannot, because the configuration a real module runs today — no vector index,
open-ended JSONB metadata, an always-present tenant scope — is refused outright
by that rule, and it is the configuration most projects should start from. What
survives from Convex is the reason for the rule, which is why it reappears
verbatim in the second regime.

**A metric argument on the query instead of in the schema.** Close, and more
flexible. Rejected because the failure mode is silent: `Near(vec,
sqlb.Cosine)` against an L2 index is a correct answer and a table scan, and
nothing in the type system, the tests or the response distinguishes it from the
fast one. The declaration cannot be wrong in that way, because the index is
generated from it.

**`?near=` on the list endpoint**, ranking as just another sort. Rejected on the
URL length before any of the design arguments get a turn, and then again on
pagination and sort coherence.

**Keep embeddings out of Postgres entirely.** A dedicated vector store is the
right answer above some corpus size, and nothing here disputes it.
[ADR-0001](0001-postgres-only.md) is unaffected — sqlb simply would not be
involved in that column. The case this record serves is the much more common
one: a few hundred thousand rows, one embedding column, and no appetite for a
second datastore and its consistency problem.

**`halfvec`, `sparsevec`, binary quantization.** Same machinery, more type
constructors, and each is a real answer to a real cost at scale. Declined for
now under the same rule as everywhere else here: added when something asks,
because the shape of the ask determines whether they are types or an option on
`Vector`.

## Revisions

- 2026-07-28 — Written before any implementation, on the reasoning that the
  metric-in-the-schema decision is the one that is expensive to reverse and it
  is settled by argument rather than by building. The physical claims about
  Postgres are deliberately listed as the first thing that could invalidate it,
  because ADR-0025 was written after a green test suite turned out to have been
  asserting what somebody expected rather than what Postgres accepts.
- 2026-07-28 — Revised against `core/rag` in `subject-mono`, a working pgvector
  module, which corrected the record within hours of it being written. The first
  draft made the ANN index mandatory and required every filter to be declared,
  which would have refused the shape that module actually runs — no vector index
  at all, exact search inside a mandatory tenant scope, and arbitrary JSONB
  metadata narrowed by containment. The index is now the second decision rather
  than the first, and declared filters are the price of an approximate index
  rather than a property of vector search. Its `%%EMBEDDING_DIM%%` sentinel also
  moved the two-declarations argument from predicted to observed, and made the
  dimension's being a Go expression a stated part of the decision rather than an
  incidental consequence of the DSL.
- 2026-07-30 — **The scope qualifier's condition fired, and the answer did not
  change.** This record's status said "not in 1.0 unless one of the ports needs
  it to complete." One did: the multi-app port finds pgvector's `AfterConnect`
  codec is not on the sqlb `sql.DB` and concludes its `rag` and `memory` modules
  "can't port this way." The other port reaches the opposite verdict from the
  other direction, listing RAG/pgvector as out of sqlb's scope.

  They are not in conflict, and reading them together is what closes this. What
  the port hit is not a missing vector DSL — it is the driver, which
  [ADR-0040](0040-the-driver-is-a-dependency.md) now owns. Building this record
  would not have unblocked that module; a text-form `sqlb.Vector` cannot host a
  binary codec no matter how well the schema declares its index. So the
  qualifier resolves to **still not in 1.0**, and the dependency is now stated:
  this is buildable as designed only after the driver flip, which is also what
  would let it move off Confidence: Low.

  What this changes in the record itself: the `sqlb.Vector` text-form clause is
  marked superseded above rather than deleted, since the reasoning for it was
  sound under the constraint it was written against and the constraint is what
  moved.
