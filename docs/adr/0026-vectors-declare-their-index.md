# ADR-0026: A vector column declares its index, and similarity search is its own operation

- **Status:** Exploring — nothing is built, and **it is not in 1.0**. Buildable as
  designed only after the driver flip ([ADR-0040](0040-the-driver-is-a-dependency.md))
- **Confidence:** Low — the unindexed half is grounded in a working module
  (`core/rag` in `subject-mono`); the indexed half is argument, and its sharpest
  claims are about how Postgres behaves under a filter
- **Decided:** 2026-07-28
- **Last reviewed:** 2026-07-30 (read against the two port reports and ADR-0040)

## Context

There are no vector references in the tree, and the feature fails in three
places: `migrate.sqlType` ends in `unknown type %q`, so no declaration renders —
not even the `OfType` escape hatch; `migrate.createIndex` has no operator class
and no `WITH`, so `USING hnsw` renders and means nothing; and `introspect`
refuses a `vector` column, so adopting an existing pgvector database drops the
embedding.

The query half already "works": `sqlb.OrderBy(sqlb.Raw{SQL: "embedding <-> ?"})`
compiles and returns correct rows — by sequentially scanning the table. **That is
the problem, not the reassurance.** A vector feature built on `Raw` looks
finished and is a table scan.

**The sqlc split does not rescue this.** Window functions go to `Raw` or to sqlc
because they are a *query shape*; a vector is a **column**, and the schema is
sqlb's. sqlc types its queries against `schema.sql`, which `migrate.Diff` renders
from a declaration that cannot express the column. Observed, not predicted:
`core/rag` hand-maintains a mirror `schema.sql` because its migration uses a
`%%EMBEDDING_DIM%%` sentinel sqlc's parser cannot tolerate. The dimension wants
to be a *value*, and SQL text has nowhere to put one. Either sqlb learns the
type, or the arrangement this project documents stops holding for any project
with an embedding column.

**What a real one looks like.** `core/rag` has **no vector index** — every search
is an exact sort over the rows a mandatory tenant scope already selected, which is
the right shape at that size — and its filters are **open-ended** JSONB
containment, because a caller attaching whatever it likes is the feature. Set
against Convex's `filterFields`, which insists every filterable key be declared,
these look like opposite philosophies. They are the two ends of one axis, and the
axis is **whether the index is approximate**.

**An ANN index is not an index in the sense the rest of the schema means.** A
btree is an optimisation the planner may ignore. HNSW changes the answer (recall
is a tuning parameter), serves only `ORDER BY <op> LIMIT k`, and is built for one
metric — an index created `vector_l2_ops` does not serve a `<=>` query, and
Postgres does not complain, it sequentially scans. A metric typo is a correct
answer a thousand times slower, which no test asserting on rows can see.

**Filters and approximate search fight, silently.** An HNSW scan finds *k*
candidates and the `WHERE` runs over them, so a filtered search returns 2 of 10
with no error. pgvector 0.8's iterative scans convert under-recall into latency
but do not make the filter free. Convex can *enforce* its restrictions because it
owns its storage engine; sqlb sits on Postgres, which will cheerfully run the
incoherent query. Anything this project wants enforced, it must refuse itself.

## Decision

**A vector column declares its dimension; an index is optional and carries the
metric when there is one; and similarity search is a distinct operation.**

The unindexed declaration is complete on its own — the `core/rag` shape:

```go
schema.Vector("embedding", ragcfg.Dim).Searchable()
```

Adding an index is a second decision, taken when exact search stops being fast
enough:

```go
schema.Vector("embedding", ragcfg.Dim).
    Searchable().
    Index(schema.HNSW, schema.Cosine).
    Where("deleted_at IS NULL")
```

- **The dimension is part of the type and is a Go expression**, which is the whole
  answer to the `%%EMBEDDING_DIM%%` sentinel — no rewrite step, no mirror file,
  and `Diff` *notices* a dimension change and proposes the rewrite. Over 2,000 is
  accepted for a column and refused for an index.
- **An unindexed column is a supported configuration, not a missing index.**
  `Lint` may observe it; it does not warn, because at rag's size the index would
  be the mistake.
- **The metric lives with the index, and a query cannot pick a different one.**
  Asking for a metric no declared index serves is refused at build time naming
  the ones that are ([ADR-0011](0011-actionable-errors.md)). This is the most
  important line here: the failure it prevents is invisible in results and shows
  up as a latency graph.
- **HNSW is the default, because the migration model chooses it.** An IVFFlat
  index built on an empty table clusters nothing, and a `Diff`-generated
  `CREATE INDEX` runs at exactly that moment.
- **Which filters may accompany a search depends on the index and nothing else.**
  With none, any filter the model allows. With an ANN index, a constant predicate
  becomes the index's `WHERE`, a variable one must be declared, and an undeclared
  one is refused. sqlb carries both regimes because, unlike Convex, it can offer
  the first.
- **The score is similarity, not distance** — larger is closer, whichever metric
  produced it.
- **Search is its own operation.** `POST /documents/search`, not `?near=`: 1,536
  float32s is ~20KB and does not fit in a URL; `?sort=…&near=…` is incoherent;
  and `OFFSET 100` into an approximate neighbour set is not page six of anything.
- **The embedding does not go over the wire.** A vector column is `Hidden`, not
  optionally. Go callers through the query engine still get it.

Two supporting pieces: `CREATE EXTENSION IF NOT EXISTS vector;` ordered ahead of
every table (the collision argument that kept `CREATE TYPE` out does not apply —
one global name, owned by nobody, idempotent), and `sqlb.Vector`.

**Superseded:** the original `sqlb.Vector` was a text-form `[]float32` because
`Executor` was `database/sql`. [ADR-0040](0040-the-driver-is-a-dependency.md)
removes that constraint and cites this record as its strongest evidence. When
built, `sqlb.Vector` is pgvector's binary codec on the pool — measured at 2.7×
the time and 21× the memory for a 50-row page of 1536-dimension embeddings. The
rest of this record is unaffected either way.

## Consequences

**Buys.** The schema stays the single source of truth for a project that uses
embeddings, which is what the whole sqlc arrangement rests on and what `Raw`
cannot restore. Adoption round-trips. The mistake with no symptom — a metric that
does not match its index — is refused at build time. And staging the index as a
second decision means the risky half can be deferred, or dropped entirely if
nothing ever outgrows an exact scan.

**Costs.**

- *This is the largest surface added since expansion, and the first that is not
  table stakes* — a type, an index kind, a Go value type, a REST operation, an
  extension in the diff engine, an introspect mapping, a lint rule. The honest
  reason it is worth it is the schema argument, not that vector search is popular.
- *Approximate results are hard to test the way this project tests.* The guard is
  "the index is used", and failing it on purpose means reading a query plan, not
  a result set.
- *The index build is the slowest migration this project will generate.* A vector
  index wants its own migration file, and nothing enforces that yet.
- *`CREATE EXTENSION` usually needs privileges the migration role lacks* — failing
  at the first statement of the first migration, which is at least a good place.
- *The metadata half is a debt this record does not pay.* `filter.operators` has
  no containment operator and `Coerce` refuses `json.RawMessage`, so a JSONB
  column is not filterable through sqlb at all, and the first regime is available
  to Go callers via `RawPred` only.

## What would change our mind

- **Any of the three physical claims failing against a real database** — silent
  under-return under a filter, sequential-scan fallback on a mismatched opclass,
  an empty-table IVFFlat being useless. Testing them is the first work, before
  the DSL.
- A second metric on one column makes `Near(vec)` ambiguous. The fix is additive,
  but it means the schema-only story was too simple.
- Someone needs the raw vector over the wire — client-side re-ranking is the
  plausible case, and one instance turns `Hidden` into an overridable default.
- **An ANN index whose declared filters cannot express what a caller narrows by.**
  If a corpus outgrows exact search *and* needs open-ended metadata narrowing,
  the two regimes collide and this record has no answer. That is the measurement
  most likely to be needed first.
- A second extension wants the same machinery — PostGIS is the obvious one.
- The surface cost arrives as bugs elsewhere — the fallback is not to fix it but
  to cut back to an opaque passthrough type, a tenth of the code, which unblocks
  the thing that actually forced the decision.

## Cost of change

Declining is free until the first line is written. After that: the DDL
rendering, opclasses, build parameters and HNSW defaults are cheap — and because
an unindexed column is a complete configuration, the index machinery can stay
unbuilt without leaving the feature half-finished. `TypeVector` in Stable-tier
`schema` and the `POST /resource/search` wire format are expensive. A shipped
HNSW index whose metric changes is an hours-long rebuild on every deployment
holding data.

Asymmetric in the useful direction on the decision most likely to be wrong:
schema-first metric with a query argument added later is additive; shipping a
query argument and later insisting on declaration breaks every caller — the same
shape as [ADR-0017](0017-enums-as-text-and-check.md)'s reason for starting from
text.

## Revisions

- 2026-07-28 — Written before any implementation, because the metric-in-the-schema
  decision is the expensive one to reverse and is settled by argument. The
  physical claims are listed first among what could invalidate it, since ADR-0025
  found a green test suite asserting what somebody expected rather than what
  Postgres accepts.
- 2026-07-28 — Revised against `core/rag`, which corrected the record within
  hours: the first draft made the ANN index mandatory and every filter declared,
  which refuses the shape that module actually runs. The index is now the second
  decision, and declared filters are the price of an approximate index.
- 2026-07-30 — The "unless a port needs it" qualifier fired, and the answer did
  not change. What blocked the multi-app port was the driver, not a missing vector
  DSL — a text-form `sqlb.Vector` cannot host a binary codec however well the
  schema declares its index. Still not in 1.0, now with the dependency stated.
- 2026-07-30 — Condensed.
