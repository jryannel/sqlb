# ADR-0012: Change notification via a transactional outbox

- **Status:** Exploring — the **outbox and its dispatcher are unbuilt**, and
  **not in 1.0**. It remains the largest unbuilt item in
  [the vision](../vision.md) and the one most likely to change shape on contact
  with real traffic. What *is* built is the half downstream of it: the SSE
  endpoint, the wire format and an in-process source, split out into
  [ADR-0045](0045-the-stream-is-a-seam.md). The dispatcher this record describes
  is the `rest.Source` implementation that will replace that in-process one
- **Confidence:** Low on the outbox itself, unchanged. Higher than before on the
  consumer end, because the wire format below — invalidation events carrying
  table plus row key — has now been built against and the payload alternative
  has a concrete reason to stay rejected (ADR-0045 records it)
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-08-01

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

**Costs.** Write amplification on every mutation, and a table that needs pruning.
Ordering is per-table at best. Consumers must be idempotent. The fallback poll
that makes a lost notification harmless can also hide a permanently broken
`LISTEN`. None of it is built.

## What would change our mind

- Outbox write volume is a measurable drag on mutation latency — evaluate logical
  replication (`pgoutput`), which has no write cost and captures non-sqlb
  changes, at the price of a replication slot and decoded rows instead of typed
  domain events.
- Invalidate-and-refetch produces too much refetch traffic — consider row deltas,
  but only after measuring: deltas bring cache-coherence problems invalidation
  does not have.
- Delivery latency sits at the poll interval rather than spiking to it — the
  `LISTEN` connection is broken or pooled, and the dispatcher needs a startup
  assertion.
- The outbox trigger shows measurable overhead on write-heavy tables — move
  `pg_notify` back into sqlb's mutation path and accept the forgettability.

## Cost of change

Free now. Once shipped, the trigger is part of the migration surface, so moving
`pg_notify` into Go later is a migration that must land before the code stops
sending it. Moving to logical replication after ship means draining in-flight
outbox rows and a different operational posture. Decide before the first
subscriber depends on the delivery semantics — the guarantees are what consumers
build against.

## Revisions

- 2026-07-27 — Written, before any implementation exists.
- 2026-07-27 — Recorded that the outbox row is the event and `NOTIFY` only a
  doorbell; added the fallback poll and the outbox trigger. Prompted by PgBouncer
  turning out to be in the target deployment.
- 2026-07-27 — `sqlb.AfterCommit` shipped ahead of this; said plainly that it is
  at-most-once and in-process, so it is not a change feed.
- 2026-07-30 — Condensed.
- 2026-08-01 — The fan-out half shipped as
  [ADR-0045](0045-the-stream-is-a-seam.md), behind a `rest.Source` seam this
  record's dispatcher is expected to implement. What is unbuilt here narrowed
  from "all of it" to the outbox table, the trigger and the dispatcher; the
  invalidation-not-payload decision is now load-bearing in shipped code rather
  than only recorded.
