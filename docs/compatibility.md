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
  optional interfaces that are type-asserted for, never by adding methods. What
  that means for a codebase built on pgx is [below](#the-driver).
- **The filter grammar** — the URL syntax (`?status=eq.draft`, `?order=`,
  `?select=`, `?limit=`) and its operator names. This is a wire format: a
  deployed client or an agent driving the API off `sqlb.json` has requests built
  against it. New operators are additive; existing spellings do not change
  meaning. `has`, `hasany` and `hasall` were added for array columns
  ([ADR-0033](adr/0033-array-columns.md)) and are frozen from here on; `contains`
  was *not* extended to mean array containment, and will not be — one name
  meaning two things depending on the column it is applied to is the ambiguity
  the generated clients exist to remove.
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

**`database/sql` is the contract.** It is not a placeholder for a pgx path that
has not been written yet. Every evaluation of sqlb so far has asked this first
and had to infer the answer from a comment on `Executor`, so it is written down
here instead.

**pgx works today, through `database/sql`.** `github.com/jackc/pgx/v5/stdlib`
registers a driver, and `sql.Open("pgx", dsn)` — or `stdlib.OpenDBFromPool` over
a `pgxpool` you already have — produces a `*sql.DB` that satisfies `Executor`.
That is not a fallback nobody runs: it is how sqlb's own Postgres suite runs.
[`pgtest`](../pgtest/) opens every connection that way, including the
transaction-pooling tests [ADR-0019](adr/0019-pgbouncer-in-the-path.md) exists
for. The driver underneath every real-database claim this project makes is pgx.

**A pgx-native `Executor` is not planned**, for two reasons that point the same
way. The interface is frozen above and grows only by optional interfaces, so a
second set of methods is not available to it. And the work is not an adapter:
`*sql.Rows` is a concrete struct rather than an interface, so `pgx.Rows` cannot
be made to satisfy the signature, and scanning is written against `*sql.Rows`
throughout `exec.go` and `mutate.go`. A pgx path is a second scanner, a second
set of type-mapping tests, and a dependency in an engine that today has none —
`mise run deps-check` enforces the standard library alone, and that invariant is
worth more than the alternative spelling.

**What going through `database/sql` costs**, stated plainly because it is not
nothing:

- **`CopyFrom`** — pgx's binary bulk load has no `database/sql` spelling. Bulk
  ingest keeps a pgx handle of its own, or becomes a multi-row `INSERT`.
- **Per-connection type codecs** — registering a binary codec on `AfterConnect`,
  as pgvector's Go support does, is a pgx API. Through `database/sql` those
  values move as text.
- **`pgx.Batch`** — the round-trip savings are unavailable.

None of these are on sqlb's own path, which issues single statements with `$N`
parameters over types the standard library maps. They are things the code
*beside* sqlb may want, which is why the answer is a second handle rather than a
different interface.

**Sharing a transaction is the one real constraint.** A library holding a
`pgx.Tx` and sqlb holding a `*sql.Tx` are in two transactions even against one
pool: `stdlib.OpenDBFromPool` shares connections, not transaction handles. For
sqlc this decides the whole adoption shape, and
[with-sqlc.md](with-sqlc.md#if-your-sqlc-is-generated-for-pgx) says what to do
about it. There is a raw-connection escape — `sql.Conn.Raw` reaches the
underlying pgx connection — and it is neither tested here nor recommended: it
puts both libraries on one session under two handles, neither of which knows
about the other's `BEGIN`.

**What would change this.** Not a pgx `Executor` — an optional interface. If
bulk ingest, or a vector column carrying a binary codec
([ADR-0026](adr/0026-vectors-declare-their-index.md)), turns out to need the
wire protocol, the shape is a narrow capability sqlb type-asserts for on the
executor it was handed and an adapter the caller writes. That keeps one scanner
and one interface, which is what the freeze above is protecting.
