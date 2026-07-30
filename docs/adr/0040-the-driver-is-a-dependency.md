# ADR-0040: The driver is a dependency, not a seam

- **Status:** Exploring — the positioning is decided and the enabling refactor
  has landed (the scanners read an interface rather than `*sql.Rows`), but no
  pgx code exists yet. This record fixes the shape before it is written, and
  before the 1.0 freeze makes it unavailable
- **Confidence:** High that `database/sql` structurally blocks two things sqlb
  has already committed to — pgvector's binary codec and joining a caller's
  `pgx.Tx` — and High that this is a pre-1.0-or-never decision, since it breaks
  `Executor`. High on the performance shape, which is now measured rather than
  reasoned (see below) and turned out narrower than this record first claimed:
  ordinary CRUD is ~30%, and the bulk-insert gap is an API gap rather than a
  driver one. Medium that a framework is the right product for sqlb at all,
  which is a positioning bet rather than a technical finding and is the part a
  reader should push on. Lower on deleting the `database/sql` path outright
  rather than keeping both, which is the cheapest thing here to get wrong
- **Decided:** 2026-07-30
- **Last reviewed:** 2026-07-30

## Context

This question has been asked and answered before, twice, and neither answer
stuck. [compatibility.md](../compatibility.md#the-driver) says `database/sql` is
the contract and "a pgx-native `Executor` is not planned". Then
[release-1.0.md](../release-1.0.md) opened it again as stream B and deliberately
*refused* to pre-decide, on the grounds that both target codebases are pgx-native
and the ports would answer it better than reasoning would. That was the right
call at the time. What has changed is not evidence from a port — it is the
question the project is answering.

**The unwritten invariant.** The engine depends on the standard library alone,
enforced by `mise run deps-check` with positive controls in both directions per
[ADR-0016](0016-guards-proven-both-ways.md). No ADR has ever recorded it.
[array.go](../../array.go) cites [ADR-0013](0013-no-internal-split.md) for it,
and ADR-0013 is about the public/internal package split — it says nothing about
dependencies. So the most consequential constraint on the engine's design has
been enforced by a shell script and justified nowhere. This record is where it
gets written down, which is also where it gets inverted.

The invariant's actual argument is good: sqlb is a library, every dependency it
takes is one its consumers inherit, and importing it costing nothing is a real
property. That argument holds for a library that extends the standard library.
It is the wrong argument for what sqlb is turning into.

**What sqlb is turning into.** The target is not "a query builder someone adds
to their stack". It is the thing you build a Postgres application *with*, for
applications that already run huma and pgx. Two thirds of that is already true
and was decided without anyone framing it this way: `rest` depends on huma and
`deps-check` exempts it by name, and chi was removed from the library graph in
[#32](https://github.com/jryannel/sqlb/pull/32), surviving only in
`example/tasks` where an application brings its own router. The remaining third
is the driver.

**The driver is not merely inconvenient — it is bending designs.**
[compatibility.md](../compatibility.md#the-driver) lists what going through
`database/sql` costs (`CopyFrom`, per-connection codecs, `pgx.Batch`) and then
correctly observes that none of them are on sqlb's own path. That list is
incomplete, because two items *are* on sqlb's own path:

- **[ADR-0026](0026-vectors-declare-their-index.md) is already compromised by
  it.** That record specifies `sqlb.Vector` as a `[]float32` carrying `Scan` and
  `Value` over pgvector's *text* form — and says why, in as many words: "since
  `[]float32` implements neither and `Executor` is `database/sql`". Registering
  pgvector's binary codec on `AfterConnect` is a pgx API and has no
  `database/sql` spelling. The vector support this project has designed is a
  text-encoded shadow of the one the ecosystem uses, and the driver is the
  entire reason.
- **Joining a caller's transaction is impossible.** A library holding a `pgx.Tx`
  and sqlb holding a `*sql.Tx` are in two transactions against one pool;
  `stdlib.OpenDBFromPool` shares connections, not transaction handles.
  [ADR-0020](0020-transaction-scoped-handle.md) already notes that a `Beginner`
  returning anything but `*sql.Tx` cannot implement its interface. For a
  pgx-native application this decides the adoption shape rather than annoying
  it — the adoption review counts 25 sites wrapping writes in `pool.Begin`, each
  of which must end up entirely on one side or the other.

A third, smaller: [array.go](../../array.go) is 449 lines of Postgres array
literal codec written in both directions because `database/sql` has no array
case and `pq.Array` was unavailable to a stdlib-only engine
([ADR-0033](0033-array-columns.md)). pgx decodes arrays natively.

### What was measured

The paragraph above argues from what the protocols do. That is the position
[ADR-0019](0019-pgbouncer-in-the-path.md) was in before it tested a real
PgBouncer and found two of its three claims came back different, so the same
correction is applied here first: `pgtest/bench_test.go`, run with
`mise run bench-pg`.

Apple M1 Max, Postgres 18 in a local container, pgx 5.10, one goroutine, warm.
The pgx column is a hand-written statement and a manual `rows.Scan` — the
*floor*, not what an application would write — so every ratio is biased against
sqlb on purpose.

| Shape | sqlb over the bridge | hand-written pgx | Ratio |
|---|---|---|---|
| List 200 scalar rows | 542 µs · 84 KB · 1,461 allocs | 413 µs · 55 KB · 807 allocs | **1.3×** |
| List 200 rows, two small arrays | 914 µs · 286 KB · 8,247 allocs | 545 µs · 96 KB · 2,811 allocs | **1.7×** |
| List 50 rows × 1536 `float32` | 9.1 ms · 6.8 MB · 78,314 allocs | 3.3 ms · 317 KB · 210 allocs | **2.7× time, 21× memory** |
| Insert 500 rows, VALUES + RETURNING | 3.8 ms · 1.1 MB | 3.5 ms · 758 KB | **1.1×** |
| Insert 500 rows, `CopyFrom` | *unavailable* | 1.8 ms · 260 KB | **2.1× vs sqlb** |

Three of these change what this record should claim.

**Ordinary CRUD is ~30% slower, and that is not an argument for breaking
`Executor`.** It is the honest number for the case almost every request is, and
on its own it would not justify any of this. A record claiming pgx is
categorically faster would be overstating the common path.

**The wide-float row is where the cost stops being incremental.** 2.7× the time,
21× the memory and 373× the allocations, because the text literal is parsed
element by element in Go. This is the pgvector case
[ADR-0026](0026-vectors-declare-their-index.md) already conceded on design
grounds, now with a number attached, and it is the strongest evidence in this
record.

**The bulk-insert gap is an API gap, not a driver-overhead gap** — and this is
the finding that most sharpens the argument. sqlb's multi-row `VALUES` runs
within ~10% of the same statement hand-written over pgx, so the bridge costs
almost nothing there. The entire 2.1× belongs to `CopyFrom`, which has no
`database/sql` spelling at all. "Going pgx-native makes inserts faster" would
therefore be wrong; "going pgx-native makes `CopyFrom` reachable" is right, and
they are different claims. (`CopyFrom` also returns no rows, where sqlb's insert
always appends `RETURNING` to write generated ids back — so part of that 2.1× is
work it does not do.)

**What makes this hard**, and why it is not obvious from the above:
`compatibility.md` marks `Executor` **Frozen**, and a public interface that
appears in every terminal call's signature is the worst possible thing to break.
Doing it costs the "importing sqlb is free" property permanently and locks out
anyone who wanted this engine on a driver other than pgx. That is a real
audience, deliberately abandoned.

### What the ports found

[release-1.0.md](../release-1.0.md)'s stream B said the ports would answer this
better than reasoning would, and it was right. Both are now in-tree
([port](../review-adoption-port.md),
[multi-app](../review-adoption-port-multi-app.md)) and they cut both ways.

**They confirm the two structural blockers, observed rather than predicted.**
The multi-app port classifies the driver split as *architectural* — its first
finding, not a footnote — and records the consequences precisely: two pools per
process, no shared transaction between a sqlb module and a pgx/sqlc one, and
pgvector's `AfterConnect` codec "**not** on the sqlb `sql.DB`", so the `rag` and
`memory` modules "can't port this way". That is this record's strongest argument
and its second-strongest, hit by the first real port to go looking.

**They also narrow it, in a way the benchmark did not reach.** The single-app
port calls the pgxpool bridge "a **non-event**" and reports that sqlc's
pgx-typed models — `pgtype.Date`, `pgtype.Timestamptz`, `pgtype.UUID` — scanned
through sqlb with *zero* model edits, because pgx's pgtype implements
`sql.Scanner`/`driver.Valuer`. The benchmark above measured plain Go types and
so says nothing about this path; the read story over a pgx-native codebase is
better than an argument from protocols would guess.

**And they correct this record's own revisit trigger.** "A port measures the
flip as cheap" conflated two things the ports keep apart: the *bridge* is cheap,
and the *flip* — moving a platform onto `database/sql` so that transactions can
be shared — is the large one. The multi-app port states the boundary exactly: a
module sharing a transaction with pgx-native code "needs the whole platform on
`database/sql` first". Leaf and disjoint modules are cheap either way. The
decision is only forced where a unit of work crosses the two.

One thing the ports surface that this record owns regardless of its outcome:
pgtype scanning is load-bearing for the whole structs-first-over-sqlc story, and
was unverified in sqlb's own tests — the `with-sqlc` adoption test uses
`sql.NullTime`, not pgtype. It is now covered by `pgtest/pgtype_test.go`, in
both directions and including NULLs, plus compile-time assertions that fail the
build if a pgx release ever drops `sql.Scanner`/`driver.Valuer` from pgtype.

It went into `pgtest` rather than `example/withsqlc`, where the port report
asked for it and where the rest of the sqlc story lives, because it cannot go
there: `example/withsqlc` is in the root module, whose direct requirements
`deps-check` pins to huma by name so that a test file cannot quietly grow a
driver. That is the guard working as designed, and it is worth noting here
because it is a small, concrete instance of the tension this whole record is
about — the stdlib invariant deciding where a pgx-shaped fact is allowed to be
tested. Going pgx-native would make the question disappear along with the
constraint.

## Decision

**The engine depends on pgx v5, and `database/sql` stops being the contract.**

- `Executor` is redefined over pgx's types. This is a breaking change to the
  central public interface, which is why it lands **before 1.0 or not at all**.
- **One driver, not two.** The `database/sql` path is removed rather than kept
  alongside a pgx one. Supporting both means two scanners' worth of type-mapping
  tests forever, and that cost compounds in a way a one-time break does not.
- The scanners keep reading the internal `rowSource` interface introduced ahead
  of this record. It is no longer load-bearing for driver choice — it is a test
  seam, and it makes the migration an adapter rather than a rewrite of `scan`,
  `mutate` and their type tables.
- **`deps-check` is rewritten, not deleted.** The invariant it enforces becomes:
  the engine depends on pgx and nothing else; `rest` additionally on huma;
  anything else is a regression. The guard keeps its positive controls
  ([ADR-0016](0016-guards-proven-both-ways.md)) — a dependency check that cannot
  fail is one this repository has already shipped three times.
- **No chi.** huma is router-agnostic and the application supplies its router.
  Re-adding chi to the library graph would forfeit that and buy nothing.

## Consequences

**What this buys.**

- [ADR-0026](0026-vectors-declare-their-index.md) becomes buildable as designed
  rather than as a text-form approximation, and can move off Confidence: Low.
- sqlb writes can join a transaction the application already opened, which is
  the single largest mechanical cost of adoption for both target codebases.
- `CopyFrom` and `pgx.Batch` become available to the engine rather than
  requiring a second handle beside it. `CopyFrom` is worth 2.1× on a 500-row
  load; the multi-row `VALUES` path sqlb has today is already within ~10% of
  hand-written pgx, so this is reach rather than speed.
- [array.go](../../array.go)'s codec becomes deletable, along with its test
  matrix.
- [ADR-0019](0019-pgbouncer-in-the-path.md)'s open question gets a real answer:
  `QueryExecMode` becomes something sqlb sets in code and can default safely,
  rather than something a deployment has to remember to put in a DSN.
- One driver means one type-mapping matrix, and the ability to assume Postgres
  types end to end — which is what [ADR-0001](0001-postgres-only.md) committed
  to and the driver abstraction was quietly taking back.

**What this costs.**

- **`Executor` breaks.** Every caller changes. There is no deprecation path that
  preserves both, because the point is to have one.
- Every consumer inherits pgx and its dependency tree. "Importing sqlb costs
  nothing" stops being true and must be removed from the pitch, not softened.
- sqlb becomes unusable on any other driver. Anyone wanting this engine over
  lib/pq, or over a non-Postgres database through a shim, is out — and given
  [ADR-0001](0001-postgres-only.md) that population was always small, but it is
  not zero.
- [with-sqlc.md](../with-sqlc.md) inverts. Today it advises sqlc users generated
  for pgx that they cannot share a transaction; afterwards the same advice
  applies to users generated for `database/sql`, and that document needs
  rewriting rather than amending.
- Work is discarded: `array.go`'s codec, and the parts of the scan path written
  around `database/sql`'s conversion rules.
- The `pgtest` module's reason for existing weakens — it has a `go.mod` of its
  own specifically so a driver stays out of the root module's requirements.
  That arrangement needs revisiting, though the testcontainers dependency is
  reason enough to keep the split.

## What would change our mind

- **The modules that need a shared transaction turn out to be few.** This
  replaces the trigger the first draft carried ("a port measures the flip as
  cheap"), which the ports showed to be two questions wearing one name. The
  bridge is already known to be cheap; that is settled and is not a reason to
  revisit. What is still open is how much code sits on the boundary. If the
  modules whose writes must be atomic with pgx-native code stay a short list
  that can hold its own `pgx` handle, the coexistence path is sufficient and
  this record is an expensive answer to a narrow problem.
- **pgvector support stops mattering.** If the RAG tables stay permanently
  outside the registry, as [ADR-0026](0026-vectors-declare-their-index.md)
  allows, the strongest technical argument here evaporates and only transaction
  sharing is left.
- **A second consumer arrives that is not pgx-native.** The positioning bet is
  that every real consumer already runs pgx. One that does not, with a reason,
  is a signal the framing is wrong.
- **`database/sql` grows the hooks.** Unlikely on any relevant timescale, but a
  standard-library path to per-connection codecs would remove the pgvector
  argument entirely.

## Cost of change

**Sharply asymmetric, and the asymmetry is the whole point of deciding now.**

Before 1.0: the change is a redefinition of one interface, an adapter behind the
`rowSource` seam, a rewritten `deps-check`, and edits to `compatibility.md` and
`with-sqlc.md`. Days, not weeks, and no external client is affected — the REST
contract, the filter grammar and the generated clients do not touch `Executor`.

After 1.0: the same work plus a major version and a migration every consumer
must perform by hand. That is what makes this pre-1.0-or-never rather than
merely urgent.

Reversing it *later* — going back to `database/sql` — costs a second `Executor`
break on top of the first, and would arrive after `array.go`'s codec had been
deleted and would need writing again. Reversal is therefore the expensive
direction in both senses, which argues for treating the "what would change our
mind" triggers above as things to check *before* the work rather than after.

## Alternatives considered

**An optional interface sqlb type-asserts for.** This is what
[compatibility.md](../compatibility.md#the-driver) proposes as the growth path,
and it was genuinely close — it is additive, it preserves the freeze, and it can
land after 1.0. It loses because it delivers the capability without the
positioning: pgvector's binary codec only helps if it is on by default, and two
supported drivers means the type-mapping matrix doubles permanently. It buys
compatibility with an audience this record has decided not to serve.

**Keep `database/sql`, bridge with `stdlib.OpenDBFromPool`.** The status quo, and
it works — it is how sqlb's own Postgres suite runs, so it is not a fallback
nobody exercises. It loses on the two path items above, and on the fact that it
makes every application stack two connection poolers with independent limits.

**Support both behind `rowSource`.** Now technically available, since the
scanners no longer name `*sql.Rows`. Rejected for the maintenance matrix rather
than the mechanism: two drivers is two sets of type-mapping tests, two sets of
NULL and array semantics, and a permanent question on every bug report about
which path it was on.

**Generic over a driver abstraction.** [ADR-0001](0001-postgres-only.md) already
refused dialect portability on the grounds that pretending to be portable costs
more than it returns. Abstracting the driver while targeting one database is the
same mistake one layer down.

## Revisions

- 2026-07-30 — Written. The scan-path refactor onto an internal `rowSource`
  interface landed first, as the step that is correct under either answer.
- 2026-07-30 — Benchmarked, and the performance claim narrowed as a result. The
  first draft implied pgx is broadly faster; `pgtest/bench_test.go` says
  ordinary CRUD is ~30%, wide float arrays are 2.7× time and 21× memory, and
  bulk insert is not a driver gap at all — sqlb's `VALUES` matches hand-written
  pgx, and the whole difference is `CopyFrom`'s absence. The vector row is now
  the strongest evidence here; it was previously listed alongside weaker ones.
- 2026-07-30 — Read against the two port reports, which landed on `main` the
  same day. They confirm both structural blockers from a real port — the
  multi-app one classifies the driver split as architectural and finds
  pgvector's codec missing from the sqlb handle — and narrow the read-path
  argument, since pgx's pgtype scans through the bridge with zero model edits,
  a path the benchmark did not cover. The revisit trigger was rewritten as a
  result: "a port measures the flip as cheap" turned out to be two questions,
  and the ports answered the cheap one already.
