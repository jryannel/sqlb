# ADR-0021: A hook gets a transaction, not an event

- **Status:** Working
- **Confidence:** High
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

[ADR-0008](0008-hooks-as-domain-seam.md) decided that hooks are where domain
logic lives, and gave `BeforeQuery` the thing that makes that work: the builder
itself. That half has held. `example/tasks` scopes six tables across
twenty-five endpoints in one file, and no handler in it mentions a tenant.

The write hooks did not get the same treatment, and building that example is
what made the difference visible. Three domain rules could not be written as
hooks, each for a different reason:

- **"A task's list must be in the caller's workspace."** Needs one query.
  `BeforeCreate(ctx, *T)` receives the row and nothing else — no executor, no
  handle — so it could not ask the database anything. Pushed into a composite
  foreign key in a hand-written migration.
- **"`completed_at` follows `status`."** Needs to know what `status` is
  becoming. `BeforeUpdate` receives `*Update[T]` and can add assignments to it
  but cannot read the ones already there; there is no accessor. Pushed into a
  `BEFORE` trigger.
- **"Creating a comment bumps the task's `comment_count`."** Needs both writes
  in one transaction. `AfterCreate` had no executor either, and — the sharper
  half — nothing in `rest/` or `mutate.go` opened a transaction, so every
  generated create, update and delete ran under autocommit. `AfterCommit`, which
  this project documents as the place for work that must not happen if the write
  aborts, was therefore unreachable from any generated write. Pushed into a
  hand-written endpoint, and `comments` was not exposed for create at all.

This record originally proposed two things: a transaction around generated
writes, and an event value handed to every hook carrying an executor and the
pending assignments. The transaction was built first — and turned out to answer
most of the complaint on its own.

## Decision

**`rest.Resource` wraps every generated write in a transaction.** Built, and the
whole of what this record decides.

The consequence was larger than the proposal predicted, and is why the rest of
it was dropped. Wrapping a generated write means the hook's context carries that
transaction, so `sqlb.TxFrom` finds one — which answers the *executor* half of
the complaint wherever a generated write is running, with no new types at all.

Three details settled in the building, two of them departures from what was
first proposed:

- The option is `Options.DisableTransactions`, not `Options.Transactional`.
  Default-on was the requirement; a plain `bool` whose zero value is the safe
  one expresses that without a `*bool`, and `Options.DisableSearch` already set
  the precedent in the same struct.
- **An executor that cannot begin a transaction is refused at mount**, naming
  three ways out. Falling back to autocommit was the obvious alternative and is
  wrong: it restores the gap silently, in the callback that was supposed to be
  the durable half. `sqlb.DB.CanBeginTx` exists so the refusal happens at
  startup rather than on the first POST.
- `ErrAfterCommit` must not become a 5xx, which nobody anticipated. The row is
  durable, so reporting failure invites a retry that writes it twice. `rest`
  logs it through `slog` and returns the success it actually achieved.

Reads are not wrapped. A single `SELECT` is already atomic, and wrapping one
would hold a connection across a `BEGIN`/`COMMIT` round trip for a guarantee it
already had.

### What is not being built

**The event types are closed.** No `CreateEvent[T]`, no `e.DB`, no
`e.Changes()`. Hook signatures stay as they are: `func(context.Context, *T)` and
`func(context.Context, *Update[T])`.

Of the three rules that motivated them:

- The **first** is expressible as a hook now. It is still a composite foreign
  key, and that was always the better answer for a reason unrelated to this
  record: a constraint also binds the migration, the repair script and the psql
  session, none of which run an application's hooks.
- The **third** is a hook. `BeforeCreate` reads the task on the transaction,
  `AfterCreate` moves the counter and registers the `AfterCommit`.
  `example/tasks/app/comments.go` was deleted and `comments` is exposed for
  create — the invariant moved from a route to the model, which is a stronger
  guarantee than the endpoint gave rather than a tidier one.
- The **second** is the only one left, and one motivating rule in one example is
  not enough to add public API that every consumer's hooks would be written
  against. This record's own revisit trigger said to drop `Changes()` unless two
  independent applications wanted it. One does. That is the trigger firing, and
  the honest response is to act on it rather than wait for a second case that
  may never come.

What survives of the original complaint, unaddressed and accepted:

- an executor is available **only where a transaction is**. A `BeforeQuery` hook
  on an ordinary read has none, and reads are deliberately not wrapped.
- `BeforeUpdate` cannot read its own assignments. The workaround is a `BEFORE`
  trigger, which is a better answer than a hook for most rules of that shape
  anyway — see Consequences.

One constraint from the original framing turned out to be wrong, and is worth
recording because it nearly stopped the transaction being built. The claim that
sqlb "does not own the write path, so there is no place it can unilaterally
decide a transaction exists" was false — `rest` is exactly such a place, because
it owns the handler. The mistake was reading a property of the *engine* as a
property of the whole project.

## Consequences

**What this buys.** `AfterCommit` is reachable from generated CRUD, which is the
difference between a documented feature and a decorative one. Validation against
the database — the single most common thing a `BeforeCreate` wants — no longer
needs a hand-written endpoint. Both without new API: `TxFrom(ctx)` and the
existing hook signatures carry it.

**What this costs.** Wrapping generated writes costs a `BEGIN`/`COMMIT` round
trip per write and holds a connection for longer. Behind PgBouncer in
transaction pooling mode that is a change in how long a server-side connection
is occupied, not merely a latency figure
([ADR-0019](0019-pgbouncer-in-the-path.md)). Nothing has measured it; the number
is unknown, not known-small.

`TxFrom(ctx)` is a worse interface than a field would be. It returns two values,
it returns nothing on a read, and a hook cannot tell "no transaction" from "not
wrapped" without checking. Every hook that wants the database pays two extra
lines for that. This is the price of closing the events half, and it is being
paid deliberately rather than overlooked.

A hook that can query is a hook that can query badly — a per-request round trip
is now easy to write and invisible at the call site, which is ADR-0008's
action-at-a-distance cost with a database attached to it.

And there are now two places to write the same rule. `example/tasks` keeps the
composite foreign key binding a task's list to its workspace even though a hook
could express it, because a constraint also binds writers that are not the
application. Making a hook capable does not make it the right answer, and the
documentation has to say so or the capability becomes a trap.

## What would change our mind

The transaction and the closure have separate triggers.

**Reopening the events:**

- **A second application hitting the `BeforeUpdate` gap.** One rule in one
  example was not enough; two independent ones would be, and `Changes()` is the
  piece to add first — not the whole event, which is the larger change with the
  weaker case.
- **A hook needing before and after in one handler** — timing, `recover`,
  correlating a decision across the write. That is the evidence for PocketBase's
  `e.Next()` chain, and nothing has produced it.
- **`TxFrom`'s two-value shape causing a real bug** rather than costing extra
  lines: a hook that checked `ok` wrongly and silently skipped its rule. That
  would mean the interface is not merely worse but unsafe, which is a different
  argument from the one weighed here.

**On the transaction:**

- **If wrapping measurably raises connection-hold time** under PgBouncer
  transaction pooling — watch `avg_xact_time` against `avg_query_time` — the
  per-resource escape hatch already exists (`DisableTransactions`). Flipping the
  *default* is the change this record would not make lightly; see Cost of change.
- **If people write rules in hooks that also need to hold for migrations and
  manual SQL**, that is a documentation failure rather than an API one, and the
  fix is guidance about triggers, not more hook surface.

## Cost of change

**Closing cost nothing**, which is the main argument for closing now rather than
leaving it open. Nothing was built, no signature changed, and no consumer wrote
a hook against an event. Reopening is the same work it always was — the design
is recorded below, and the two lessons the transaction taught (a `Disable`-shaped
option; refusing rather than falling back) would apply to it too.

Had the events shipped first this would read very differently. Changing an
event's shape once applications have written hooks against it is every hook in
every consumer, and hooks are exactly the code with no compiler-checked call
site to grep for. Not building it is the cheap direction, and it stays cheap.

**The transaction is the expensive one**, and shipped default-on deliberately.
Flipping it from off to on is harmless; from on to off it breaks anyone whose
`AfterCommit` callback quietly stopped running — silently, at runtime, in the
callback that was supposed to be the durable half. Per resource that risk is the
caller's to take explicitly; as a default it would be taken on their behalf.

## Alternatives considered

**An event value carrying an executor — this record's own original proposal.**
`func(e *CreateEvent[T]) error` with `e.Ctx`, `e.Row`, `e.DB`, and `e.Changes()`
on the update event. Genuinely the better interface, and it lost to timing
rather than to a counter-argument: the transaction shipped first and took two of
its three motivating cases with it, leaving one rule in one example to justify
public API every consumer's hooks would be written against. Kept here rather
than deleted, because the design is sound and the trigger to revive it is named
above.

[PocketBase's event hooks](https://pocketbase.io/docs/go-event-hooks/) are the
prior art it came from. Every handler receives an event carrying `e.App`, so a
hook can query; `e.Next()` lets one handler span before and after the write with
the after-phase inside the transaction; and `OnRecordAfterCreateSuccess` is
guaranteed to run post-commit — which PocketBase can promise precisely because
it always wraps, which is the part this record ended up copying. Its
documentation also warns to reach the database through `e.App` rather than a
captured variable, to avoid transaction deadlock; that is the same lesson
[ADR-0020](0020-transaction-scoped-handle.md) already encodes with `TxFrom`.

**PocketBase's `e.Next()` chain.** One handler spans before and after, with the
after-phase inside the transaction. The most elegant thing in the reference, and
the largest API change here. Lost on presuming sqlb owns validation and the
write pipeline the way a framework does — it owns the handler, which is enough
for a transaction and not enough for a pipeline.

**Hydrated records in update hooks**, as PocketBase does with `e.Record`. What
would let a hook see the resulting row rather than the assignments. Rejected
because `Update[T]` is set-based: hydrating means reading the matching rows
first, which gives up updating N rows without reading any of them.

**Pass the executor through the context.** Closest to today, needing no new
types. Originally rejected on the grounds that a lookup returning nothing most
of the time *is* the problem. **That rejection was overtaken, and this is what
happens now** — the alternative won on the ground rather than on the argument.
What the argument got right is narrower and still true, and is recorded as a
cost above rather than pretended away.

**Leave hooks alone and narrow ADR-0008's claim instead.** Document hooks as
being for scoping and stamping only. Defensible, and closer to the outcome than
it looked: what actually happened is that hooks became *more* capable without
gaining API, so ADR-0008's claim needed narrowing less than expected. Its
consequences section carries the narrowing that remains.

## Revisions

- 2026-07-27 — Written, after building `example/tasks` and finding three domain
  rules that had to leave the hook layer. Evidence is for the problem; the
  solution is unbuilt, hence Exploring / Low.
- 2026-07-27 — The transaction shipped, closing the third rule's sharper half:
  `AfterCommit` is now reachable from generated CRUD. Renamed the option to
  `DisableTransactions`, made a non-transactional executor a mount-time refusal
  rather than a fallback, and recorded that `ErrAfterCommit` must not become a
  5xx. The events and `Changes()` remain unbuilt and Exploring — nothing here
  gives a hook a way to read the database, which is what the first two rules
  needed.
- 2026-07-27 — **Corrected the entry above, and the Context with it.** The
  transaction *does* give a hook a way to read the database: it wraps the write,
  so the hook's context carries the transaction and `TxFrom` finds one. That was
  a consequence nobody noticed while building it, and this record spent a day
  arguing for an executor it had already been handed on the path that matters.
  The first rule is therefore solvable as a hook, and the second never needed
  database access at all — it needs to read a *statement*, which is a different
  gap the previous entry conflated with this one.
- 2026-07-27 — **The events half is closed, and the record retitled to match.**
  Its own revisit trigger — drop `Changes()` unless two independent applications
  want it — fired with one, and acting on a trigger when it fires is the point
  of writing them down. The event design is kept in Alternatives rather than
  deleted, with the conditions that would revive it. Status Working, confidence
  High: what remains is built and in use, and the closure rests on evidence
  rather than taste. The cost of closing is recorded honestly — `TxFrom`'s
  two-value shape is worse than a field, and every hook that wants the database
  pays for it. The filename keeps its old slug so inbound links do not break.
