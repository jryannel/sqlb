# example/outbox — each row is handed to exactly one worker

**Read this before the code.** [ADR-0012](../../docs/architecture.md#change-feed-outbox)
is explicit that freezing an outbox row format on a guess is the mistake
sqlb's pre-1.0 stance exists to avoid. The schema in
[`outboxschema/schema.go`](outboxschema/schema.go) — a status enum, an
attempts counter, an exponential backoff, a dead-letter threshold — is
**one worked answer to that shape, not the project's answer.** Nothing here
is a recommendation to copy the column names or the backoff formula
verbatim; it is a real table this example can measure real behaviour
against, on the record, once.

**This is not the `outbox` package at the repository root.** That package
(`outbox/doc.go`) is a change-feed: a table written in the same transaction as
a row change, tailed by one or more `Dispatcher`s that each fan the same
events out to their own connected REST subscribers — every dispatcher sees
every row, because broadcasting is the point. What this directory builds is
the other thing an "outbox" name covers: a work queue, where each row is
handled by exactly one of several competing workers and the ones that didn't
claim it never see it at all. The two share a table-plus-worker shape and
almost nothing else — different consumption model, different guarantee,
different failure mode. If you came here looking for the change-feed, that
package is what you want; this one is a job queue that happens to be built the
same way an outbox is.

## What this settles

**That `ForUpdate().SkipLocked()` inside a `db.WithTx` boundary hands each row
to exactly one worker, on a schema with a real retry and dead-letter
lifecycle around the claim, not just on a bare claim-only table.**
`pgtest/census_test.go`'s `TestClaimHandsEachRowToExactlyOneWorker` already
proved the mechanism itself — the census records `ForUpdate`/`SkipLocked` as
working and undocumented, and that test is the documentation. This example's
`TestConcurrentWorkersEachClaimExactlyOnce` repeats the proof here: four
workers race over twelve events through this package's own `Claim` and
`Complete`, not through hand-rolled query calls, and every event lands
exactly once. The lock has to be held by the transaction boundary rather than
by the bare statement — under autocommit the row unlocks the instant the
claiming `SELECT` returns, and a second worker's own `SELECT` beginning before
the first worker's `UPDATE` commits would still see the row as `pending`.
`Claim` in [`worker.go`](worker.go) wraps both in one `db.WithTx` for exactly
that reason.

**That the relative-time predicate `Claim` and `Fail` both depend on —
`available_at` compared against "now" — has to pick a clock, and this
package picks the same one at both call sites.**
`pgtest/census_test.go`'s `TestRelativeTimeWindowNeedsRawOrAGoComputedInstant`
lays out the choice: the builder has no interval literal, so a relative-time
window is either `sqlb.RawPred` evaluated by Postgres (the database's clock)
or an instant computed in Go and bound as an ordinary parameter (the
application's clock). `Claim`'s `available_at <= now` boundary and `Fail`'s
backoff computation both use `time.Now()`, so the two agree — a `RawPred`
claim boundary paired with a Go-computed backoff would let claim eligibility
and retry scheduling disagree by however far Postgres's clock and the
worker's host clock have drifted.

## What is one opinion here, and could reasonably be different

- **The backoff formula.** `2^attempts` seconds capped at 5 minutes
  (`maxBackoff` in `worker.go`) is the simplest thing that is still
  exponential. A real deployment might add jitter to avoid every failed event
  in a batch retrying in lockstep, pick a different cap, or use a different
  curve entirely.
- **The fixed `max_attempts` threshold.** Every event here gets the same
  default (5), set once at the table level. A real system might want it
  per-topic, or adjustable per-row at insert time.
- **No `LISTEN`/`NOTIFY`.** Workers here have to poll `Claim` on their own
  schedule; there is no doorbell telling them a new event just landed. `See
  ADR-0012` for the shape a notification-driven version would take — this
  example does not build it, because the question it answers is whether the
  claim holds under contention, not whether polling is the right delivery
  model.
- **No dead-letter replay path.** Once an event is `dead`, nothing here ever
  moves it back to `pending`. A real system would want an operator action (or
  an automated one, for transient outages that outlast every backoff) to
  requeue a dead-lettered batch.
- **A worker that claims a row and then dies mid-job leaves it stuck.**
  `Claim`'s transaction commits `status = 'processing'` before the caller does
  any work, so a worker that crashes (or is killed, or loses its connection)
  between the claim and its own `Complete`/`Fail` call leaves the row in
  `processing` with nobody coming back to it — `FOR UPDATE`'s lock releases
  with the connection, but nothing re-queues the row it was protecting. A
  heartbeat column or a sweep that reclaims `processing` rows past some
  staleness threshold would close this; neither is built here.

## Deliberately not

Exactly-once delivery. A scheduler or cron. A real message payload schema —
`payload` is `jsonb`/`json.RawMessage` and this package never looks inside
it.

## Running it

```bash
mise run pg-up
SQLB_TEST_POSTGRES='postgres://sqlb:sqlb@localhost:15432/sqlb?sslmode=disable' go test ./... -v -race
```

Standalone module — `go mod tidy` first if you haven't.
`outboxschema/sqlb.go`'s `go:generate` line is what to rerun after a schema
edit.
