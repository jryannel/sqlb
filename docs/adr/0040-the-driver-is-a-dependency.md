# ADR-0040: The driver is a dependency, not a seam

- **Status:** Exploring — the positioning is decided and the enabling refactor has
  landed (the scanners read an interface rather than `*sql.Rows`), but no pgx code
  exists yet. This fixes the shape before the 1.0 freeze makes it unavailable
- **Confidence:** High that `database/sql` structurally blocks two things sqlb has
  committed to — pgvector's binary codec and joining a caller's `pgx.Tx` — and
  High that this is pre-1.0-or-never, since it breaks `Executor`. High on the
  performance shape, now measured and narrower than first claimed. High, as of
  2026-07-30, that a framework is the right product for sqlb at all. Lower on
  deleting the `database/sql` path outright, which is the cheapest thing here to
  get wrong
- **Decided:** 2026-07-30
- **Last reviewed:** 2026-07-30

## Context

**The unwritten invariant.** The engine depends on the standard library alone,
enforced by `deps-check` and justified in no ADR. This record is where it gets
written down, which is also where it gets inverted. Its argument is good — sqlb
is a library, every dependency it takes is one its consumers inherit — and it is
the right argument for a library that extends the standard library.

**What sqlb is turning into** is not a query builder someone adds to a stack. It
is the thing you build a Postgres application *with*, for applications already
running huma and pgx. Two thirds of that is already true and was decided without
anyone framing it this way: `rest` depends on huma and `deps-check` exempts it by
name. The remaining third is the driver.

**The driver is bending designs, not merely inconveniencing them.** Two items are
on sqlb's own path:

- **[ADR-0026](0026-vectors-declare-their-index.md) is already compromised.** It
  specifies `sqlb.Vector` over pgvector's *text* form, and says why in as many
  words: `Executor` is `database/sql`. Registering the binary codec on
  `AfterConnect` is a pgx API with no `database/sql` spelling.
- **Joining a caller's transaction is impossible.** A library holding a `pgx.Tx`
  and sqlb holding a `*sql.Tx` are in two transactions against one pool;
  `stdlib.OpenDBFromPool` shares connections, not transaction handles. The
  adoption review counts 25 sites wrapping writes in `pool.Begin`.

A third, smaller: `array.go` is 449 lines of array-literal codec written because
`database/sql` has no array case. pgx decodes arrays natively.

**What was measured** — `pgtest/bench_test.go` on Postgres 18, pgx 5.10, against
hand-written pgx (the *floor*, so every ratio is biased against sqlb):

| Shape | sqlb over the bridge | hand-written pgx | Ratio |
|---|---|---|---|
| List 200 scalar rows | 542 µs · 84 KB | 413 µs · 55 KB | **1.3×** |
| List 200 rows, two small arrays | 914 µs · 286 KB | 545 µs · 96 KB | **1.7×** |
| List 50 rows × 1536 `float32` | 9.1 ms · 6.8 MB | 3.3 ms · 317 KB | **2.7× time, 21× memory** |
| Insert 500 rows, VALUES + RETURNING | 3.8 ms · 1.1 MB | 3.5 ms · 758 KB | **1.1×** |
| Insert 500 rows, `CopyFrom` | *unavailable* | 1.8 ms · 260 KB | **2.1×** |

Three of these change what this record should claim. **Ordinary CRUD is ~30%
slower, and that is not an argument for breaking `Executor`** — it is the honest
number for the case almost every request is. **The wide-float row is where the
cost stops being incremental**, because the text literal is parsed element by
element in Go; that is the pgvector case with a number attached, and the
strongest evidence here. **The bulk-insert gap is an API gap, not a driver
one** — sqlb's multi-row `VALUES` runs within ~10% of hand-written pgx, and the
entire 2.1× belongs to `CopyFrom`. "Going pgx-native makes inserts faster" is
wrong; "makes `CopyFrom` reachable" is right.

**What makes this hard:** `Executor` is Frozen, and a public interface in every
terminal signature is the worst possible thing to break.

**What the ports found.** Both confirm the two structural blockers from a real
port — the multi-app one classifies the driver split as *architectural*, records
two pools per process and no shared transaction, and finds pgvector's codec "not
on the sqlb `sql.DB`", so its `rag` and `memory` modules "can't port this way."
They also narrow it: the single-app port calls the pgxpool bridge "a
**non-event**", and sqlc's pgx-typed models scanned through sqlb with *zero*
model edits, because pgtype implements `sql.Scanner`/`driver.Valuer`. And they
correct this record's own trigger — the *bridge* is cheap, and the *flip* is the
large one; the decision is only forced where a unit of work crosses the two.

One thing the ports surfaced that this record owns regardless of outcome: pgtype
scanning is load-bearing for the structs-first-over-sqlc story and was unverified
in sqlb's own tests. It is now covered by `pgtest/pgtype_test.go`, with
compile-time assertions that fail the build if pgx ever drops those interfaces.

## Decision

**The engine depends on pgx v5, and `database/sql` stops being the contract.**

- `Executor` is redefined over pgx's types — a breaking change to the central
  public interface, which is why it lands **before 1.0 or not at all**.
- **One driver, not two.** Supporting both means two scanners' worth of
  type-mapping tests forever, and that cost compounds where a one-time break does
  not.
- The scanners keep reading the internal `rowSource` interface, which is now a
  test seam rather than a driver seam, and makes the migration an adapter rather
  than a rewrite of `scan` and `mutate`.
- **`deps-check` is rewritten, not deleted**: the engine depends on pgx and
  nothing else, `rest` additionally on huma, anything else is a regression. It
  keeps its positive controls ([ADR-0016](0016-guards-proven-both-ways.md)).
- **No chi.** huma is router-agnostic and the application supplies its router.

## Consequences

**Buys.** ADR-0026 becomes buildable as designed rather than as a text-form
approximation. sqlb writes can join a transaction the application already opened
— the single largest mechanical cost of adoption for both target codebases.
`CopyFrom` and `pgx.Batch` become reachable (reach, not speed: the `VALUES` path
is already within ~10%). `array.go`'s codec becomes deletable.
[ADR-0019](0019-pgbouncer-in-the-path.md)'s exec-mode question becomes a library
default rather than a DSN every deployment must get right. And one driver means
one type-mapping matrix and Postgres types end to end, which is what
[ADR-0001](0001-postgres-only.md) committed to and the driver abstraction was
quietly taking back.

**Costs.** **`Executor` breaks** — every caller changes, with no deprecation path
that preserves both, because the point is to have one. Every consumer inherits
pgx: "importing sqlb costs nothing" stops being true and must be removed from the
pitch, not softened. sqlb becomes unusable on any other driver — a population
[ADR-0001](0001-postgres-only.md) already made small, but not zero.
[with-sqlc.md](../with-sqlc.md) inverts and needs rewriting rather than amending.
Work is discarded: `array.go`'s codec, and the scan path written around
`database/sql`'s conversion rules. And `pgtest`'s reason for a separate `go.mod`
weakens, though testcontainers is reason enough to keep the split.

## What would change our mind

- **The modules that need a shared transaction turn out to be few.** The bridge
  is already known to be cheap and that is settled; what is open is how much code
  sits on the boundary. If the modules whose writes must be atomic with
  pgx-native code stay a short list that can hold its own `pgx` handle, this is
  an expensive answer to a narrow problem.
- **pgvector support stops mattering** — if the RAG tables stay permanently
  outside the registry, the strongest technical argument evaporates and only
  transaction sharing is left.
- **A second consumer arrives that is not pgx-native**, with a reason. The
  positioning bet is that every real consumer already runs pgx.
- **`database/sql` grows the hooks.** Unlikely on any relevant timescale, but a
  standard-library path to per-connection codecs removes the pgvector argument.

## Cost of change

Sharply asymmetric, which is the whole point of deciding now. **Before 1.0**: one
interface redefined, an adapter behind the `rowSource` seam, a rewritten
`deps-check`, and edits to two documents. Days, and no external client is
affected — the REST contract, the filter grammar and the generated clients do not
touch `Executor`. **After 1.0**: the same work plus a major version and a
migration every consumer performs by hand.

Reversing it later costs a second `Executor` break on top of the first, after
`array.go`'s codec has been deleted and would need writing again — which argues
for checking the triggers above *before* the work rather than after.

The closest alternative was **an optional interface sqlb type-asserts for**:
additive, preserves the freeze, can land after 1.0. It loses because it delivers
the capability without the positioning — pgvector's binary codec only helps if it
is on by default, and two supported drivers doubles the type-mapping matrix
permanently.

## Revisions

- 2026-07-30 — Written. The scan-path refactor onto an internal `rowSource`
  interface landed first, as the step that is correct under either answer.
- 2026-07-30 — Benchmarked, and the performance claim narrowed: ordinary CRUD is
  ~30%, wide float arrays are 2.7× time and 21× memory, and bulk insert is not a
  driver gap at all. The vector row is now the strongest evidence here.
- 2026-07-30 — Read against the two port reports. They confirm both structural
  blockers and narrow the read-path argument, since pgtype scans through the
  bridge with zero model edits. The revisit trigger was rewritten: "a port
  measures the flip as cheap" turned out to be two questions.
- 2026-07-30 — Propagated. This record was decided and cited by nothing, which
  left five records and two planning documents asserting the premise it
  overturns. `compatibility.md`, `release-1.0.md`, ADR-0026, ADR-0033, ADR-0020
  and ADR-0019 now all point at it. Nothing here changed; what changed is that
  disagreeing with it now requires disagreeing with it.
- 2026-07-30 — Condensed.
