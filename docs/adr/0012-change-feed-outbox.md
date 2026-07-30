# ADR-0012: Change notification via a transactional outbox

- **Status:** Exploring — nothing is built, and **it is not in 1.0**. It is the
  largest unbuilt item in [the vision](../vision.md) and the one most likely to
  change shape on contact with real traffic, so freezing an outbox format on a
  guess is the mistake 1.0 exists to avoid ([the road to 1.0](../release-1.0.md))
- **Confidence:** Low
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

Dynamic data views need to know when their data changed. The original brief
called for "hooks to inform about changed", and the reference points — Convex in
particular — set the expectation that a view can be live rather than polled.

The naive implementation fires a notification from an `AfterCommit` hook in
process. That loses events whenever the process dies between commit and publish,
and delivers phantom events when a transaction commits the notification but
rolls back the data.

## Decision

Every mutation that goes through sqlb writes a row to an outbox table **in the
same transaction** as the change. A dispatcher tails that table — woken by
`LISTEN/NOTIFY` rather than polling — and fans out to in-process `AfterCommit`
hooks and to SSE or WebSocket subscribers.

Subscribers receive invalidation events (table plus row key), not recomputed
query results. Clients refetch.

### The event is the row; the notification is only a doorbell

This distinction decides most of the design, so it is worth stating outright
rather than leaving implied by the word "woken".

**The outbox row is the event.** It is written by sqlb's Go mutation code, which
is what keeps the payload a typed domain event rather than a decoded row — the
same property that defers logical replication below. The durable read path is
the dispatcher's tail query over that table.

**`NOTIFY` carries nothing.** It exists so the tail query runs promptly instead
of on a timer. Two consequences follow:

- **A lost notification degrades to latency, not lost data.** The row is already
  committed. So the dispatcher also polls on a slow fallback interval, and the
  feed stays correct if the `LISTEN` connection drops, or if someone points the
  dispatcher at a connection pooler that silently swallows it
  ([ADR-0019](0019-pgbouncer-in-the-path.md)).
- **The 8000-byte `NOTIFY` payload limit is not a constraint we have to design
  around**, because we are not putting anything in the payload.

### The doorbell is rung by a trigger on the outbox table

An `AFTER INSERT` trigger on the outbox table — and on no other table — calls
`pg_notify` with an empty payload.

The alternative is for sqlb to issue `NOTIFY` in the mutation transaction, which
is one fewer object in the migration surface. It loses because it is forgettable:
a new mutation path that writes the outbox but omits the notify produces a feed
that works in tests and lags in production. Putting it on the table means any
writer of the outbox rings the bell, including one that is not Go.

This is deliberately *not* a trigger on each domain table. That would capture row
changes rather than domain events — the same trade-off that defers logical
replication — and would fire during backfills and migrations, which are exactly
the moments a live view least wants a flood.

### `AfterCommit` exists now, and is not this

The hook this record assumed was built in
[ADR-0020](0020-transaction-scoped-handle.md), before the outbox. That makes the
relationship worth stating before someone concludes the feed is done.

`sqlb.AfterCommit` is an **in-process, at-most-once** callback: it runs after
`Commit` returns nil, and if the process dies in that window the callback never
runs and nothing records that it did not. That is exactly the failure mode this
record's Context names, and it is fine for the things people actually reach for
first — invalidating a local cache, logging, enqueuing to a broker that is
itself allowed to lose work.

It is not a change feed, and using it as one silently loses events. That warning
matters more now than when it was written: generated CRUD wraps its writes
([ADR-0021](0021-hooks-receive-an-event.md)), so `AfterCommit` is reachable from
every REST write rather than only from a hand-written `WithTx`. It is easier to
reach for and no more durable than it was.

What it changes here is the outbox's shape. The dispatcher fans out *to*
`AfterCommit` callbacks was the original phrasing; the accurate version is that
writing the outbox row happens inside the transaction like any other write, and
`AfterCommit` is how the *doorbell* gets rung in-process if we ever want that
without a trigger. The outbox becomes one registered callback among others
rather than the only way to observe a write, which is the property worth having:
a consumer that does not need durability should not have to run a dispatcher.

## Consequences

**What this buys.** Delivery is at-least-once and survives a process restart,
because the event and the data commit or roll back together. The dispatcher is
the only component that needs to be highly available, and it can be restarted
without losing events. Because the notification is a doorbell rather than a
transport, the design tolerates a connection pooler in the path: only the
dispatcher's `LISTEN` needs a direct connection, while `NOTIFY` works fine from a
pooled one.

**What this costs.** A write amplification on every mutation, and a table that
needs pruning. Ordering is per-table at best. At-least-once means consumers must
be idempotent. The fallback poll that makes a lost notification harmless is also
capable of hiding a permanently broken `LISTEN`, so slow delivery is a symptom
that has to be looked for rather than one that announces itself. None of this is
built, hence Low confidence.

## What would change our mind

- If outbox write volume is a measurable drag on mutation latency, evaluate
  logical replication (`pgoutput`) instead — it has no write cost and captures
  changes made outside sqlb, at the price of a replication slot and considerably
  more operational complexity.
- If invalidate-and-refetch produces too much refetch traffic for realistic
  views, consider shipping row deltas — but only after measuring, since deltas
  bring cache-coherence problems that invalidation does not have.
- If consumers cannot practically be made idempotent, we need deduplication in
  the dispatcher, which changes its storage requirements.
- If the fallback poll ever turns out to be doing the real work — that is, if
  delivery latency sits at the poll interval rather than spiking to it — the
  `LISTEN` connection is broken or pooled, and the dispatcher needs a startup
  assertion rather than a quieter symptom.
- If the outbox trigger shows up as measurable overhead on write-heavy tables,
  the answer is to move the `pg_notify` back into sqlb's mutation path and accept
  the forgettability, not to drop the doorbell.

## Cost of change

Free now, since nothing is built.

Once it ships, the outbox trigger is part of the migration surface, so moving the
`pg_notify` into sqlb's mutation path later means a migration rather than a code
change. Cheap, but not free — and it has to land before the code stops sending
it, or events go undelivered in the gap.

After it ships, moving to logical replication means draining or migrating
in-flight outbox rows, and a different operational posture — a replication slot
that will fill the disk if a consumer stalls. Worth deciding deliberately before
the first subscriber depends on delivery semantics, because the guarantees are
what consumers build against.

## Alternatives considered

**In-process `AfterCommit` publish.** Simplest, and adequate for a single
process that may drop events. Rejected as the *change feed* because losing a
change event silently desynchronises every live view — but built anyway, as a
primitive in its own right, since plenty of side effects are allowed to be
at-most-once and were previously forced to run inside the transaction. See the
section above for why having it does not make this record redundant.

**Logical replication from the start.** Captures changes sqlb did not make,
which is a real advantage. Deferred: it requires a replication slot, careful
operational handling, and it decodes rows rather than domain events, so hooks
would lose their typed payload.

**Polling.** Rejected as the *primary* mechanism: the latency-versus-load curve
is bad at both ends. It survives as the fallback, run slowly enough that its load
does not matter and its latency only shows when the doorbell has failed.

**sqlb issues `NOTIFY` in the mutation transaction**, instead of a trigger on the
outbox table. One fewer database object, and no migration surface. Lost on
forgettability — see the Decision section.

**A trigger on every domain table.** Would capture changes sqlb did not make,
which is a genuine advantage and the same one that makes logical replication
tempting. Lost for the same reason: it decodes rows rather than carrying domain
events, so hooks lose their typed payload. It also fires during backfills and
migrations.

## Revisions

- 2026-07-27 — Written, before any implementation exists.
- 2026-07-27 — Recorded that the outbox row is the event and `NOTIFY` is only a
  doorbell, which the original text left implied. Added the fallback poll, named
  an `AFTER INSERT` trigger on the outbox table as what rings it, and noted the
  consequence for a pooled connection path ([ADR-0019](0019-pgbouncer-in-the-path.md)).
  Prompted by PgBouncer turning out to be in the target deployment: the question
  "is this at the DB level or the Go API level?" had no clear answer in the
  record, and the two halves of `LISTEN/NOTIFY` behave differently behind a
  pooler.
- 2026-07-27 — `sqlb.AfterCommit` shipped ahead of this
  ([ADR-0020](0020-transaction-scoped-handle.md)). Said plainly that it is
  at-most-once and in-process, so it is not a change feed, and corrected the
  Decision's implication that the dispatcher fans out *to* it.
