# ADR-0012: Change notification via a transactional outbox

- **Status:** Exploring
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

## Consequences

**What this buys.** Delivery is at-least-once and survives a process restart,
because the event and the data commit or roll back together. The dispatcher is
the only component that needs to be highly available, and it can be restarted
without losing events.

**What this costs.** A write amplification on every mutation, and a table that
needs pruning. Ordering is per-table at best. At-least-once means consumers must
be idempotent. None of this is built, hence Low confidence.

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

## Cost of change

Free now, since nothing is built.

After it ships, moving to logical replication means draining or migrating
in-flight outbox rows, and a different operational posture — a replication slot
that will fill the disk if a consumer stalls. Worth deciding deliberately before
the first subscriber depends on delivery semantics, because the guarantees are
what consumers build against.

## Alternatives considered

**In-process `AfterCommit` publish.** Simplest, and adequate for a single
process that may drop events. Rejected because losing a change event silently
desynchronises every live view.

**Logical replication from the start.** Captures changes sqlb did not make,
which is a real advantage. Deferred: it requires a replication slot, careful
operational handling, and it decodes rows rather than domain events, so hooks
would lose their typed payload.

**Polling.** Rejected: the latency-versus-load curve is bad at both ends.

## Revisions

- 2026-07-27 — Written, before any implementation exists.
