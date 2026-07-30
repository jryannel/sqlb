# Compatibility

What `v0.1.0` promises, and what it deliberately does not.

sqlb is pre-1.0 and has one author, no tags before this one, and no observed
consumers. That is the honest starting position, and it is the reason this
document exists: an unreleased `main` reads as unknown risk, whereas a tag with
a stated blast radius is something a reader can decide against.

Semantic versioning applies from `v1.0.0`. Until then a minor bump may break a
surface listed under **Will move**, and each break is described in the release
notes with the mechanical edit that fixes it.

What has to be true before that version — and why the gating item is evidence
rather than features — is [the road to 1.0](release-1.0.md).

## Frozen

These are the surfaces worth freezing early, because they are the ones other
code and other *systems* couple to. Breaking them would invalidate stored data
or deployed clients, not just call sites.

- **`Executor`** — `QueryContext` and `ExecContext`, and nothing more. Every
  wrapper, tracer and pool adapter written against it stays valid. Widening this
  interface would break every implementation at once, so it grows by adding
  optional interfaces that are type-asserted for, never by adding methods.

  **This is the one entry here that is going to break before 1.0, deliberately.**
  [ADR-0040](adr/0040-the-driver-is-a-dependency.md) redefines `Executor` over
  pgx's types and removes the `database/sql` path entirely, because the driver
  is structurally blocking two things sqlb has already committed to. That is a
  pre-1.0-or-never change: after the tag the same work is a major version and a
  hand migration for every consumer. The signature above is what ships until it
  lands. What changes, and what it costs, is [below](#the-driver).
- **The filter grammar** — the URL syntax (`?status=eq.draft`, `?order=`,
  `?select=`, `?limit=`) and its operator names. This is a wire format: a
  deployed client or an agent driving the API off `sqlb.json` has requests built
  against it. New operators are additive; existing spellings do not change
  meaning. `has`, `hasany` and `hasall` were added for array columns
  ([ADR-0033](adr/0033-array-columns.md)) and are frozen from here on; `contains`
  was *not* extended to mean array containment, and will not be — one name
  meaning two things depending on the column it is applied to is the ambiguity
  the generated clients exist to remove.
- **The wire spelling of a column** — the column's own name, verbatim, in the
  JSON body, the OpenAPI document, the filter grammar's parameter names and both
  generated clients. There is one spelling and no way to configure a second, so
  `?created_at=gte.…` names the same thing the response does. A `Hidden` column
  has no spelling at all. Renaming a column is therefore an API change as well
  as a schema change; `RenamedFrom` makes the database half mechanical and the
  client half is a regeneration plus a compile error per call site
  ([ADR-0036](adr/0036-the-wire-is-the-column-name.md), which also says what a
  camelCase front end is expected to do about it).
- **The list envelope** — `{items, page, per_page, has_more, next_cursor?,
  total?}`, one shape for every resource. `next_cursor` is absent when there is
  no next page and `total` only when `?count=exact` asked for it. The key names
  and which of them may be absent are frozen; *adding* an optional key is
  additive and breaks no client that ignores unknown ones.
- **The generated DDL's shape** — `migrate.Diff` output for a given pair of
  schemas may improve, but a migration already written and applied is never
  reinterpreted.
- **The cursor payload** — `?cursor=` and the `next_cursor` field are wire
  format for the same reason the filter grammar is, and so is what a cursor
  decodes to: a client holds one across requests, so changing the payload's
  shape breaks a request already in flight rather than a call site. It is
  base64url of JSON and has room for a version field, but that field has to
  arrive before it is needed. Nothing today reads the payload, which is not a
  reason to treat it as private. See
  [ADR-0027](adr/0027-keyset-pagination.md).

## Will move

Named in advance, so the break is a documented plan rather than a surprise.

- ~~**Hook registration.**~~ Landed after `v0.1.0`, and it broke nothing:
  `sqlb.On[T]()` is now a wrapper over a process-default `Registry`, and
  `OnIn[T](r)` reaches a scoped one
  ([ADR-0020](adr/0020-transaction-scoped-handle.md)). Registrations written
  against `v0.1.0` compile and behave identically. The one behavioural subtlety
  worth knowing: which registry a statement uses is decided by the *dynamic
  type* of the executor passed to it, so passing a raw `*sql.DB` where a scoped
  `*sqlb.DB` was meant silently uses the default.
- **Terminal call signatures**, when Go 1.27 arrives. `sqlb.Collect[R](ctx, db,
  b)`, `filter.Apply(b, q)` and the `db` threaded through every terminal call
  all gain method forms, because a method on a concrete type cannot introduce a
  type parameter before then. These are additive — the functions stay.
- **Nested `?expand`.** One level resolves today. If nesting lands it arrives as
  a longer name — `?expand=list.workspace` — under a depth limit, so nothing a
  request can send today changes meaning.
- **Backwards cursors.** Paging goes forward only. If `?before=` lands it is a
  new parameter alongside `?cursor=`, so again nothing a request can send today
  changes meaning. [ADR-0027](adr/0027-keyset-pagination.md) says what would
  make it worth building.

One behavioural change landed with cursors and is worth stating plainly, because
it affects requests that do not use them: **every list is now ordered
deterministically**, since `filter.Apply` appends the primary key when the sort
does not already settle ties. Responses that were previously in an arbitrary
order within a tie group now have a defined one, and paging no longer repeats or
skips rows across pages. No request changes meaning; some get a different — and
correct — row order.

## Not covered

Anything under `introspect`, `migrate`, `codegen` or `pgtest` that is reached
only from a build step or a test. These are tools, not a runtime surface, and
they change with less ceremony.

## The driver

**`database/sql` is the contract today, and it is being replaced before 1.0.**
[ADR-0040](adr/0040-the-driver-is-a-dependency.md) decides that the engine
depends on pgx v5 and that `database/sql` stops being the contract — one driver,
not two. Nothing of it is built yet, so what follows describes what ships now,
and the paragraphs after it say what changes.

This document previously said the opposite, in as many words, and that reversal
is the point rather than an embarrassment: the answer it gave was correct for a
library that extends the standard library, and sqlb turned out to be aiming at
something else. Every evaluation of sqlb so far has asked the driver question
first, which is why it is answered here at length in both directions.

**pgx works today, through `database/sql`.** `github.com/jackc/pgx/v5/stdlib`
registers a driver, and `sql.Open("pgx", dsn)` — or `stdlib.OpenDBFromPool` over
a `pgxpool` you already have — produces a `*sql.DB` that satisfies `Executor`.
That is not a fallback nobody runs: it is how sqlb's own Postgres suite runs.
[`pgtest`](../pgtest/) opens every connection that way, including the
transaction-pooling tests [ADR-0019](adr/0019-pgbouncer-in-the-path.md) exists
for. The driver underneath every real-database claim this project makes is pgx.

**A pgx-native `Executor` is planned, and it replaces this one.** The two
objections this document used to raise are answered rather than waived. The
freeze is being broken on purpose and before the tag, which is the only window
in which it is a redefinition rather than a major version. And the work is no
longer a rewrite: the scanners read an internal `rowSource` interface rather than
`*sql.Rows`, so `exec.go` and `mutate.go` no longer name the concrete type and
the migration is an adapter behind that seam. What is *not* waived is the cost —
every consumer inherits pgx, "importing sqlb costs nothing" stops being true, and
anyone wanting this engine on another driver is out. ADR-0040 states that bill in
full.

**What going through `database/sql` costs**, stated plainly because it is not
nothing:

- **`CopyFrom`** — pgx's binary bulk load has no `database/sql` spelling. Bulk
  ingest keeps a pgx handle of its own, or becomes a multi-row `INSERT`. sqlb's
  multi-row `VALUES` runs within ~10% of the same statement hand-written over
  pgx, so this is reach rather than speed: the gap is `CopyFrom`'s absence, not
  bridge overhead.
- **Per-connection type codecs** — registering a binary codec on `AfterConnect`,
  as pgvector's Go support does, is a pgx API. Through `database/sql` those
  values move as text.
- **`pgx.Batch`** — the round-trip savings are unavailable.

The second of these *is* on sqlb's own path, which is what changed. This document
used to say none of them were, and that was true of the engine as it stood; it
stopped being true when [ADR-0026](adr/0026-vectors-declare-their-index.md)
designed a vector column and had to specify it over pgvector's text form for this
exact reason. Measured, a 50-row page of 1536-dimension embeddings costs 2.7× the
time and 21× the memory of the binary path, because the text literal is parsed
element by element in Go. The other two remain things the code *beside* sqlb may
want.

**Sharing a transaction is the one real constraint.** A library holding a
`pgx.Tx` and sqlb holding a `*sql.Tx` are in two transactions even against one
pool: `stdlib.OpenDBFromPool` shares connections, not transaction handles. For
sqlc this decides the whole adoption shape, and
[with-sqlc.md](with-sqlc.md#if-your-sqlc-is-generated-for-pgx) says what to do
about it. There is a raw-connection escape — `sql.Conn.Raw` reaches the
underlying pgx connection — and it is neither tested here nor recommended: it
puts both libraries on one session under two handles, neither of which knows
about the other's `BEGIN`.

Both adoption ports hit this, and they place the boundary precisely: the *bridge*
is cheap — one of them calls the pgxpool bridge "a non-event", and pgx's `pgtype`
values scan through it with no model edits — while the *flip*, moving a platform
onto `database/sql` so a unit of work can span a sqlb module and a pgx-native
one, is the expensive half. Leaf and disjoint modules are cheap either way. The
constraint only bites where a transaction crosses the two.

**What would change this.** The optional-interface path this document used to
name as the growth path is now the *rejected* alternative, not the plan:
type-asserting a narrow capability delivers the capability without the
positioning, and pgvector's binary codec only helps if it is on by default. What
would send it back to `database/sql` is in ADR-0040's *What would change our
mind* — the shortest version is that if the modules needing a shared transaction
stay a short list that can hold its own pgx handle, and pgvector stays outside
the registry, then the coexistence path was sufficient and this is an expensive
answer to a narrow problem. Those are things to check before the work, not after:
reversing afterwards costs a second `Executor` break on top of the first.
