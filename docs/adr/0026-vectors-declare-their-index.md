# ADR-0026: A vector column declares its index, and similarity search is its own operation

- **Status:** Exploring — nothing is built, and **it is not in 1.0**. The driver
  flip it was waiting on ([ADR-0040](0040-the-driver-is-a-dependency.md)) has
  landed, so it is now buildable as designed rather than as an approximation
- **Confidence:** Medium, raised from Low. The three physical claims this record
  reasons from have been measured against pgvector 0.8.6
  ([`pgtest/pgvector_test.go`](../../pgtest/pgvector_test.go)) and all three
  hold, so the indexed half is no longer argument. What keeps it off High is the
  regime rule: the collision this record listed as its most likely invalidator
  turns out to be deployed rather than hypothetical, and the third branch below
  is written but unbuilt and untested
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
candidates and the `WHERE` runs over them, so a filtered search comes back short
with no error. Convex can *enforce* its restrictions because it owns its storage
engine; sqlb sits on Postgres, which will cheerfully run the incoherent query.
Anything this project wants enforced, it must refuse itself.

### The three claims, measured

This record's own *What would change our mind* put the physical claims first and
said testing them was the work before the DSL. That is done, against pgvector
0.8.6, in [`pgtest/pgvector_test.go`](../../pgtest/pgvector_test.go). All three
hold. Four things about them were not known when this was written.

**No number in that file is a recall figure, and this record must not quote one
as if it were.** The first version of those tests measured uniform-random
vectors and found a filtered search returning 6 of 10, and 0 of 10 once the
filter was selective. Rewritten with a deterministic corpus so the gate would
not flake, the same query returned 10 of 10 — and an empty-table IVFFlat index,
which had returned nothing, answered in full. Neither run was wrong. Recall
depends on the corpus, on the planner's costing, and on HNSW's own build
randomness, since an index built twice over identical rows is not the same
graph. The tests therefore force the mechanism rather than sample it: the rows
the filter admits are placed opposite the probe, so the failure is a
demonstration and not a statistic. The draft above said "2 of 10"; it should
have said "fewer, and you will not be told".

**The planner may decline the ANN index, and did.** Under a selective filter it
costs the index above a sequential scan and chooses the scan, which returns the
exact answer. That makes the silent under-return conditional on a planning
decision — the same query complete on one database and quietly partial on
another, according to statistics nobody is watching. This is worse than an
unconditional failure, not better, and it is a fourth claim this record did not
have.

**pgvector's iterative scan is a real mitigation whose benefit is
data-dependent.** The draft above said it "converts under-recall into latency",
which was taken from the documentation. Measured, it recovers the full answer on
one corpus and moves six rows to seven on another, with `EXPLAIN` still
reporting a single index search. It may be offered. It may not be described as
the thing that makes a filtered search correct, because whether it is cannot be
seen from the schema.

**The IVFFlat case announces itself.** `CREATE INDEX ... USING ivfflat` on an
empty table emits a NOTICE — *"ivfflat index created with little data … Drop the
index until the table has more data"*. A migration runner that surfaced notices
would catch this at the moment it happens, which is cheaper than anything in the
DSL. Every runner this project knows of discards them. That does not change the
decision to default to HNSW; it adds a second, independent answer that costs
almost nothing.

### Two traps the harness found, which the feature will hit

Neither is about vectors. Both are about executing a search that needs session
state, which is what the operation below is.

- **Session settings must be `SET LOCAL` inside a transaction.** A plain `SET`
  rides the pooled connection back into whatever runs next. The first version of
  the tests measured an exact search, handed the connection back still carrying
  `enable_indexscan = off`, and reported a perfect result for the query the file
  exists to show failing.
- **pgx caches a prepared statement per connection, keyed on SQL text alone.** A
  plan built under one set of GUCs is reused under another. The measurement pool
  disables statement caching for that reason — and a `search` operation that sets
  `hnsw.*` per statement has exactly this problem, since the setting changes and
  the cached plan does not.

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

  **This takes a capability away, and the record should say so rather than let a
  port discover it.** `core/rag` reads its dimension from `RAG_EMBEDDING_DIM`, so
  one binary can deploy against a 768- or a 1,536-dimension embedder. Fixing the
  dimension at `generate` time ends that. It is stricter and it is the point —
  the mirror `schema.sql` disappears and the drift becomes a diff instead of a
  comment asking a human — but "config becomes a constant" is a real loss for one
  known consumer.
- **A dimension is not a vector space, and `Diff` noticing a change in one gives
  false comfort.** The space is provider *and* model *and* dimension. Swap
  `text-embedding-3-small` for a different model of the same width and the column
  diffs clean while every stored vector is meaningless — an outcome with no
  symptom except worse answers. `core/rag` carries an `embedding_fingerprint` per
  chunk precisely because re-embedding has to be resumable, which is that
  problem solved by hand downstream of a schema that could not express it. So a
  vector column should be able to declare a **space identity** and not only a
  width. The first version need not act on it beyond storing it and refusing to
  search across a mismatch; what it must not do is let the dimension stand in for
  the space.
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
- **Which filters may accompany a search depends on the index and nothing else**,
  and there are **three** regimes rather than two. The first draft had two, and
  the case it could not express is deployed — see *the collision, which is not
  hypothetical* below.
  - *No index.* Any filter the model allows. Exact scan; correct and slow, which
    is the right answer at `core/rag`'s size.
  - *ANN index, filters known at declaration time.* A constant predicate becomes
    the index's `WHERE`; a variable one must be declared. An undeclared filter is
    refused at build time, as before.
  - *ANN index, filters variable per request.* The caller opts in explicitly, and
    the search operation sets `hnsw.iterative_scan` and `hnsw.max_scan_tuples`
    for the statement. This is the branch the measurement constrains: iterative
    scan helps by an amount that depends on the data, so what the caller is
    opting into is **"try harder, and it may still come back short"** — not a
    fix. Naming it as one would reintroduce the silent failure with a
    configuration flag in front of it.

  All three are explicit and none is silent. Refusing the undeclared fourth case
  at build time stays as written. The regimes are a property of the *search
  operation*, which is what makes this expressible at all: the operation owns
  execution, so it can own the session GUCs — subject to the two traps above,
  which is why they are recorded here rather than in a test file nobody reads.
- **The score is similarity, not distance** — larger is closer, whichever metric
  produced it — and it is a **projection as well as an ordering**. `1 - (embedding
  <=> $1)` appears in the select list, in the threshold comparison and in the
  `ORDER BY` of the same statement, so `Near(vec)` returns a handle yielding all
  three rather than three call sites that must agree. This is the argument
  `GroupBySelection` already makes for `date_trunc`.
- **A shortfall cannot be detected by counting rows**, which is the trap in the
  branch above. A `min_score` threshold makes "fewer than *k*" the normal case,
  so `len(rows) < k` — the obvious alarm — is unusable exactly where it is most
  needed, and a corpus applying both a threshold and a filter has no signal at
  all. Any shortfall signal sqlb offers must distinguish rows lost to the
  threshold from rows the index never found, which means counting before the
  threshold cut. Whether to offer one is open; that it cannot be the naive count
  is settled.
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
removed that constraint — it cites this record as its strongest evidence — and
it is built. `sqlb.Vector` is therefore specified as pgvector's binary codec
registered on the pool, which is now an ordinary thing to write rather than a
thing the driver had no spelling for. The measurement that made the case: 2.7×
the time and 21× the memory for a 50-row page of 1536-dimension embeddings,
through the text form. The rest of this record is unaffected either way.

**What this record is still waiting on** is therefore nothing external. The
blocker named in the status above is gone; what remains is the three places in
`migrate` and `introspect` that do not know the type, and the index half, which
this record deliberately leaves as a second decision.

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
- *Approximate results are hard to test the way this project tests*, and harder
  than this record first thought. The guard cannot be a recall number, because
  recall varies with the corpus, the planner and the index build's own
  randomness; it has to be "the plan is the dangerous shape" or "the index is
  used", which means reading a query plan rather than a result set.
  `pgtest/pgvector_test.go` is the worked example and the warning.
- *The index build is the slowest migration this project will generate.* A vector
  index wants its own migration file, and nothing enforces that yet.
- *`CREATE EXTENSION` usually needs privileges the migration role lacks* — failing
  at the first statement of the first migration, which is at least a good place.
- *The metadata half is a debt this record does not pay.* `filter.operators` has
  no containment operator and `Coerce` refuses `json.RawMessage`, so a JSONB
  column is not filterable through sqlb at all, and the first regime is available
  to Go callers via `RawPred` only.

## What would change our mind

- ~~**Any of the three physical claims failing against a real database.**~~ Done,
  and none of them failed —
  [`pgtest/pgvector_test.go`](../../pgtest/pgvector_test.go) against pgvector
  0.8.6. What the measurement changed is recorded in the Context: a fourth claim
  (the planner may decline the index), a weaker statement about iterative scan,
  and the rule that no recall figure here is transferable. The trigger that
  replaces it: **a claim measured on one corpus being quoted as a property of
  pgvector.** This record has already made that mistake once.
- A second metric on one column makes `Near(vec)` ambiguous. The fix is additive,
  but it means the schema-only story was too simple.
- Someone needs the raw vector over the wire — client-side re-ranking is the
  plausible case, and one instance turns `Hidden` into an overridable default.
- **An ANN index whose declared filters cannot express what a caller narrows by.**
  ~~That is the measurement most likely to be needed first.~~ **This has fired**,
  and the arrangement is deployed rather than hypothetical — see below. The
  record now has a third regime for it, which is written and unbuilt; what would
  change our mind again is that branch failing to hold in practice, which cannot
  be known until something runs it.
- A second extension wants the same machinery — PostGIS is the obvious one.
- The surface cost arrives as bugs elsewhere — the fallback is not to fix it but
  to cut back to an opaque passthrough type, a tenth of the code, which unblocks
  the thing that actually forced the decision.

### The collision, which is not hypothetical

The trigger above was written as a thing to watch for. A census of `subject-go`
reports it already in production: an `hnsw (embedding vector_cosine_ops)` index,
and a search narrowing by `org_id`, `conversation_id IS NULL`, `backoffice_only`,
`project_id`, `work_package_id` and an `EXISTS` over archived projects — every
one variable per request, none expressible as the index's `WHERE`. No
`hnsw.ef_search` or `hnsw.iterative_scan` is set anywhere in that repository.

On the measurement above, that arrangement returns fewer rows than it asks for
and says nothing about it. Whether it is doing so today depends on the planner,
the corpus and the index build — which is to say nobody there knows, and the
failure has no symptom that would tell them. It is reported rather than
observed: this project has not run that query. But it is the shape, and it is the
reason the third regime exists rather than a fourth refusal.

Two details from the same census sharpen the record elsewhere. That codebase
applies a `min_score` threshold *and* a filter, which is the case where counting
rows cannot detect a shortfall — the trap now stated in the Decision. And its
migration names `vector_cosine_ops` in a comment for a future index while its
query uses `<=>`: those agree because an author was careful once, in prose, with
nothing checking it. That is this record's metric argument, standing in someone
else's repository.

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
- 2026-07-30 — **The three physical claims measured**, which this record said was
  the first work and had not been done. All three hold, against pgvector 0.8.6,
  in [`pgtest/pgvector_test.go`](../../pgtest/pgvector_test.go). Four changes
  followed, and only one of them was expected. The claims are now stated as
  mechanisms rather than as recall figures, because the first version of those
  tests produced 6-of-10 on one corpus and 10-of-10 on another and *both were
  correct measurements* — recall varies with the corpus, the planner's costing
  and HNSW's own build randomness. The draft's "returns 2 of 10" was a number
  this record was never entitled to. A fourth claim was added: the planner may
  decline the ANN index and return the exact answer, which makes the silent
  failure conditional on statistics nobody watches. The iterative-scan sentence
  was weakened from the documentation's framing to what was observed — a real
  mitigation whose benefit is data-dependent and invisible from the schema. And
  the IVFFlat case turns out to announce itself as a build-time NOTICE, which is
  a second answer costing almost nothing. Confidence: Low → Medium.
- 2026-07-30 — **The regime rule gains a third branch, because its most likely
  invalidator is deployed.** A census of `subject-go` reports an HNSW index with
  six per-request filters and no scan tuning — the exact collision this record
  listed as hypothetical and had no answer for. The two-regime rule became
  three: no index, declared filters, or an explicit opt-in to iterative scan
  whose honest promise is "try harder, and it may still come back short". Also
  from that census, two things the record was missing: a `min_score` threshold
  makes `len(rows) < k` unusable as a shortfall alarm exactly where it is most
  needed, and a vector space is provider *and* model *and* dimension — so `Diff`
  noticing a width change gives false comfort, and a column should be able to
  declare a space identity. Reported rather than observed; this project has not
  run that codebase's queries.
- 2026-07-30 — Two smaller corrections while folding the above in. The score is a
  projection as well as an ordering, so `Near(vec)` yields select, predicate and
  order together rather than three call sites that must agree. And making the
  dimension a Go expression removes a capability one known consumer uses — a
  single binary deploying against a 768- or 1,536-dimension embedder — which is
  the intended trade but was not written down.
