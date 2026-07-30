# ADR-0021: A hook gets a transaction, not an event

- **Status:** Working
- **Confidence:** High
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

[ADR-0008](0008-hooks-as-domain-seam.md) gave `BeforeQuery` the builder itself,
and that half held — `example/tasks` scopes six tables across twenty-five
endpoints in one file, and no handler mentions a tenant. The write hooks did not
get the same treatment, and building that example found three domain rules that
could not be written as hooks:

- **"A task's list must be in the caller's workspace."** `BeforeCreate(ctx, *T)`
  gets the row and no executor, so it could not ask the database anything.
- **"`completed_at` follows `status`."** `BeforeUpdate` can add assignments to
  `*Update[T]` but cannot read the ones already there.
- **"Creating a comment bumps `comment_count`."** Needs both writes in one
  transaction — and nothing in `rest/` or `mutate.go` opened one, so every
  generated write ran under autocommit and `AfterCommit` was unreachable from
  generated CRUD.

This record originally proposed two things: a transaction around generated
writes, and an event value carrying an executor and the pending assignments. The
transaction was built first, and answered most of the complaint on its own.

## Decision

**`rest.Resource` wraps every generated write in a transaction** — and that is
the whole of what this record decides. Wrapping means the hook's context carries
the transaction, so `sqlb.TxFrom` finds one, which answers the *executor* half
wherever a generated write is running, with no new types.

Three details settled in the building:

- The option is `Options.DisableTransactions`, not `Options.Transactional`:
  default-on was the requirement, and a `bool` whose zero value is the safe one
  expresses that without a `*bool`.
- **An executor that cannot begin a transaction is refused at mount**, naming
  three ways out. Falling back to autocommit restores the gap silently, in the
  callback that was supposed to be the durable half. `sqlb.DB.CanBeginTx` makes
  the refusal happen at startup rather than on the first POST.
- `ErrAfterCommit` must not become a 5xx. The row is durable, so reporting
  failure invites a retry that writes it twice; `rest` logs it and returns the
  success it achieved.

Reads are not wrapped — a single `SELECT` is already atomic.

**The event types are closed.** No `CreateEvent[T]`, no `e.DB`, no `e.Changes()`.
Of the three motivating rules, the first is now expressible as a hook (though it
stays a composite foreign key, which also binds migrations and psql sessions);
the third is a hook, and `example/tasks/app/comments.go` was deleted. The second
is all that remains, and this record's own trigger said to drop `Changes()`
unless two independent applications wanted it. One does.

Accepted and unaddressed: an executor is available only where a transaction is,
and `BeforeUpdate` still cannot read its own assignments.

## Consequences

**Buys.** `AfterCommit` is reachable from generated CRUD, which is the difference
between a documented feature and a decorative one. Validation against the
database no longer needs a hand-written endpoint. Both without new API.

**Costs.** A `BEGIN`/`COMMIT` round trip per write, holding a connection longer.
Behind PgBouncer in transaction pooling that is a change in server-connection
occupancy, not just latency ([ADR-0019](0019-pgbouncer-in-the-path.md)), and it
is unmeasured — unknown, not known-small.

`TxFrom(ctx)` is a worse interface than a field: two return values, nothing on a
read, and a hook cannot tell "no transaction" from "not wrapped" without
checking. That is the deliberate price of closing the events half.

A hook that can query is a hook that can query badly — a per-request round trip
is now easy to write and invisible at the call site. And there are two places to
write the same rule: a constraint also binds writers that are not the
application, so the documentation has to say when a hook is the wrong answer.

## What would change our mind

**Reopening the events:**

- A second application hits the `BeforeUpdate` gap. `Changes()` is the piece to
  add first, not the whole event.
- A hook needs before and after in one handler — timing, `recover`, correlating a
  decision across the write. That is the case for PocketBase's `e.Next()` chain,
  and nothing has produced it.
- `TxFrom`'s two-value shape causes a real bug rather than costing lines — a hook
  that checked `ok` wrongly and silently skipped its rule.

**On the transaction:**

- Wrapping measurably raises connection-hold time under transaction pooling —
  watch `avg_xact_time` against `avg_query_time`. `DisableTransactions` is the
  per-resource escape hatch; flipping the *default* is the change not to make
  lightly.
- People write rules in hooks that also need to hold for migrations and manual
  SQL — that is a documentation failure, fixed with guidance about triggers.

## Cost of change

**Closing cost nothing**, which is the argument for closing now: nothing was
built, no signature changed, no consumer wrote a hook against an event. Changing
an event's shape after applications depend on it would be every hook in every
consumer — code with no compiler-checked call site to grep for.

**The transaction is the expensive one**, and shipped default-on deliberately.
Off→on is harmless; on→off silently breaks anyone whose `AfterCommit` callback
stops running. Per resource that risk is the caller's to take explicitly; as a
default it would be taken on their behalf.

## Revisions

- 2026-07-27 — Written, after `example/tasks` found three domain rules that had to
  leave the hook layer.
- 2026-07-27 — The transaction shipped. Renamed the option to
  `DisableTransactions`, made a non-transactional executor a mount-time refusal,
  and recorded that `ErrAfterCommit` must not become a 5xx.
- 2026-07-27 — Corrected: the transaction *does* give a hook database access,
  because the hook's context carries it. This record spent a day arguing for an
  executor it had already been handed.
- 2026-07-27 — The events half is closed and the record retitled. Its own trigger
  — drop `Changes()` unless two independent applications want it — fired with
  one. The event design is a road not taken; the conditions to revive it are
  above.
- 2026-07-30 — Condensed.
