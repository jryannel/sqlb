# ADR-0012: Change notification via a transactional outbox

- **Status:** Working — the `outbox` package is the table, the trigger and the
  dispatcher. `outbox.Outbox` implements `rest.TxPublisher` so an existing
  `rest.PublishChanges` call records into the writing transaction instead of
  after it, and `outbox.Dispatcher` is the `rest.Source`
  [ADR-0045](0045-the-stream-is-a-seam.md) left the seam for.
  `pgtest/outbox_test.go` runs both against a real server
- **Confidence:** High that event-and-data-commit-together is right and that the
  seam held — the swap turned out to be one constructor call, as ADR-0045
  predicted, and the existing Broker suite passes unchanged beside the new one.
  High on the ordering mechanism, because the test that covers it fails without
  it. **Low on what the advisory lock costs under real write volume**, which is
  the number nothing here has measured and the most likely reason this record
  gets revised. Medium on the retention default, which is a delivery guarantee
  chosen without a consumer
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-08-13

## Context

Dynamic data views need to know when their data changed, and the reference points
— Convex in particular — set the expectation that a view can be live rather than
polled. Firing a notification from an in-process `AfterCommit` hook loses events
when the process dies between commit and publish, and delivers phantom events
when a transaction commits the notification but rolls back the data.

## Decision

Every mutation that goes through sqlb writes a row to an outbox table **in the
same transaction** as the change. A dispatcher tails that table — woken by
`LISTEN/NOTIFY` rather than polling — and fans out to SSE or WebSocket
subscribers. Subscribers receive invalidation events (table plus row key), not
recomputed query results. Clients refetch.

**The fan-out is no longer part of this record.** The endpoint, the event shape
and the reconnection contract shipped separately
([ADR-0045](0045-the-stream-is-a-seam.md)) behind a `rest.Source` interface, so
what this record still owes is a dispatcher that implements it. That is
deliberate scoping rather than scope creep: everything downstream of the outbox
is needed whether or not the outbox exists, none of it is blocked on the outbox,
and building it first made the wire format a decision tested against a running
client instead of one made on paper.

**The outbox row is the event; `NOTIFY` is only a doorbell.** It carries no
payload and exists so the tail query runs promptly instead of on a timer. A lost
notification therefore degrades to latency, not lost data, so the dispatcher also
polls on a slow fallback interval — which keeps the feed correct behind a
connection pooler that swallows `LISTEN`
([ADR-0019](0019-pgbouncer-in-the-path.md)). The 8000-byte payload limit is not a
constraint, because nothing goes in the payload.

**An `AFTER INSERT` trigger on the outbox table rings the doorbell**, not sqlb's
mutation path. Issuing `NOTIFY` from Go is one fewer database object but is
forgettable: a new mutation path that writes the outbox and omits the notify
works in tests and lags in production. This is deliberately not a trigger on each
domain table — that captures row changes rather than domain events and floods
during backfills.

**A tail is only correct if ids come out in commit order, and a sequence does
not promise that.** This is the decision this record did not have and could not
have had before building: two transactions can take ids 5 and 6 and commit in
the other order, and a dispatcher reading `id > cursor ORDER BY id` would see 6,
advance past 5, and lose it. Silently — which is the failure the whole design
exists to prevent, arrived at from inside the mechanism meant to prevent it.

So `Outbox.Record` takes `pg_advisory_xact_lock` before it appends. The lock is
held until commit, so id order *is* commit order by construction and the
dispatcher needs no reasoning about visibility at all.

The alternative considered and rejected was gating the tail on
`pg_snapshot_xmin(pg_current_snapshot())` — hold back rows whose inserting
transaction is still in flight. It has no write-path cost, and it is wrong in a
way that took a while to see: the xid is assigned at the transaction's *first*
write and the sequence value at the outbox insert, so a transaction can hold an
earlier id and a later xid, and the watermark then admits the higher id first.
Repairing that means dispatching in `(xid, id)` order, which is no longer an
order a client's `Last-Event-ID` can name. The lock buys a position that is a
row id, and a row id is what makes replay across a restart possible at all.

**What it costs is stated rather than discovered.** Writes to published models
serialise from the outbox insert to the commit — approximately the commit, since
the append is the last thing the mutation does. That bounds write throughput on
published models at roughly one transaction per commit latency, which is the
same order Postgres's WAL flush already imposes and is nonetheless a real
ceiling. It is `pg_advisory_xact_lock` and not the session form because
[ADR-0019](0019-pgbouncer-in-the-path.md) forbids session-scoped state on a path
that may run through a pooler in transaction mode.

**Retention is a delivery guarantee, not a disk setting.** A subscriber resuming
from a position that has been pruned cannot be replayed and is sent a reset, so
the retention window is the longest disconnection a client survives cheaply. It
defaults to 24 hours. This is the half of the design most likely to be wrong for
a real consumer, because it was chosen without one.

**The dispatcher probes its own `LISTEN` at startup.** The *what would change
our mind* entry below asked for this and it is built: after `LISTEN` succeeds
the dispatcher rings the doorbell from a different connection and reports
through `OnError` if it does not hear it. A pooled `LISTEN` is accepted and
silently useless (ADR-0019 measured it), which leaves the feed correct, slow,
and looking fine to everyone — the exact shape of failure that earns a check
rather than a paragraph.

**`sqlb.AfterCommit` is not this.** It shipped first
([ADR-0020](0020-transaction-scoped-handle.md)) and is in-process and
at-most-once: fine for invalidating a local cache or enqueuing to a broker that
may lose work, silently lossy as a change feed. Generated CRUD wraps its writes
([ADR-0021](0021-hooks-receive-an-event.md)), so it is now reachable from every
REST write and no more durable than before.

## Consequences

**Buys.** At-least-once delivery that survives a process restart, because event
and data commit together. Only the dispatcher needs to be highly available, and
it can restart without losing events. Only its `LISTEN` needs a direct
connection; `NOTIFY` works from a pooled one.

Two things turned out to be worth more than this record expected, both
downstream of the position being a row id rather than a per-process counter.
**Replay survives a deployment**: a client reconnecting with `Last-Event-ID`
after a rolling restart is caught up out of the table by the process that
replaced the one it was talking to, where a `Broker` would have had to reset it
and make it refetch everything. And **two dispatchers over one table both
deliver**, which is the horizontal-scaling claim stated as a test rather than as
a consequence.

**Costs.** Write amplification on every mutation, and a table that needs pruning.
Ordering is per-table at best. Consumers must be idempotent. The fallback poll
that makes a lost notification harmless can also hide a permanently broken
`LISTEN` — mitigated by the startup probe above, which narrows it to a
`LISTEN` that breaks *after* startup.

And the one added by building it: **writes to published models now serialise
against each other**, per the lock above. An application that publishes a
write-heavy table pays for a feature its clients may not be subscribed to,
and the remedy — do not publish that model — is a coarse one.

## What would change our mind

- **The advisory lock is the binding constraint on write throughput.** The
  likeliest revision, and the one this record is least confident about. The
  replacement is the `(xid, id)` dispatch order rejected above, which costs the
  row-id position and therefore costs replay-across-a-restart — so the trade is
  concrete: contention against catch-up. Measure before choosing; neither half
  has a number yet.
- Outbox write volume is a measurable drag on mutation latency — evaluate logical
  replication (`pgoutput`), which has no write cost and captures non-sqlb
  changes, at the price of a replication slot and decoded rows instead of typed
  domain events.
- Invalidate-and-refetch produces too much refetch traffic — consider row deltas,
  but only after measuring: deltas bring cache-coherence problems invalidation
  does not have.
- ~~Delivery latency sits at the poll interval rather than spiking to it — the
  `LISTEN` connection is broken or pooled, and the dispatcher needs a startup
  assertion.~~ Built; the dispatcher probes its own channel and reports.
- The outbox trigger shows measurable overhead on write-heavy tables — move
  `pg_notify` back into sqlb's mutation path and accept the forgettability.
- **Retention turns out to be the wrong knob.** If real clients disconnect for
  longer than anyone wants to retain, the answer is not a bigger window but a
  cheaper reset — a client that could refetch only what it displays rather than
  everything would make the window nearly irrelevant.

## Cost of change

**No longer free, and the shape of the bill is now visible.** The trigger is part
of the migration surface, so moving `pg_notify` into Go later is a migration that
must land before the code stops sending it. Moving to logical replication means
draining in-flight outbox rows and a different operational posture.

What is still cheap is *not adopting it*: the `outbox` package is additive, an
application on `rest.Broker` is untouched, and the swap in either direction is a
constructor call. What is expensive is changing the **delivery semantics** after
a client depends on them — at-least-once and a replayable position are what
consumers build against, and a client written to skip its own refetch because
the stream promised catch-up cannot be told later that it does not.

Deleting the package outright is cheap today and stops being cheap the moment
someone's clients are resuming across deploys.

## Revisions

- 2026-07-27 — Written, before any implementation exists.
- 2026-07-27 — Recorded that the outbox row is the event and `NOTIFY` only a
  doorbell; added the fallback poll and the outbox trigger. Prompted by PgBouncer
  turning out to be in the target deployment.
- 2026-07-27 — `sqlb.AfterCommit` shipped ahead of this; said plainly that it is
  at-most-once and in-process, so it is not a change feed.
- 2026-07-30 — Condensed.
- 2026-08-13 — **Built**, and Exploring becomes Working. Three things this record
  did not have before there was an implementation. The **ordering problem** —
  that a bigserial does not give commit order, that a tail is silently lossy
  without it, and that the `pg_snapshot_xmin` fix costs the row-id position and
  therefore replay — is the whole of the new argument, and it is the kind that
  only appears once something has to be correct rather than described. The
  **cost** is named as a throughput ceiling rather than as "write amplification",
  which is what it actually is. And the **startup probe** this record asked for
  under *what would change our mind* is built, which is the second time a check
  has been cheaper than the paragraph warning about the thing it checks.

  Also worth recording: the seam held exactly as
  [ADR-0045](0045-the-stream-is-a-seam.md) said it would. That record predicted
  "one constructor call in one application" and predicted that `Subscribe(ctx,
  since)` was the part most likely to be wrong for a source whose positions are
  not a dense sequence. The first was right. The second was right *about the
  risk* and the mitigation was to make the positions dense — which is the lock,
  and is why the lock is load-bearing for more than ordering.
- 2026-08-01 — The fan-out half shipped as
  [ADR-0045](0045-the-stream-is-a-seam.md), behind a `rest.Source` seam this
  record's dispatcher is expected to implement. What is unbuilt here narrowed
  from "all of it" to the outbox table, the trigger and the dispatcher; the
  invalidation-not-payload decision is now load-bearing in shipped code rather
  than only recorded.
