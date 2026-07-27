# ADR-0021: A hook is handed an event, not a bare row

- **Status:** Exploring, except the transaction, which is Working
- **Confidence:** Low for the events; High for the transaction, which is built
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

[ADR-0008](0008-hooks-as-domain-seam.md) decided that hooks are where domain
logic lives, and gave `BeforeQuery` the thing that makes that work: the builder
itself. That half has held. `example/tasks` scopes six tables across
twenty-five endpoints in one file of about 180 lines, most of it comment, and
no handler in it mentions a tenant.

The write hooks did not get the same treatment, and building that example is
what made the difference visible. Three domain rules could not be written as
hooks, each for a different reason:

- **"A task's list must be in the caller's workspace."** Needs one query.
  `BeforeCreate(ctx, *T)` receives the row and nothing else — no executor, no
  handle — so it cannot ask the database anything. Pushed into a composite
  foreign key in a hand-written migration.
- **"`completed_at` follows `status`."** Needs to know what `status` is
  becoming. `BeforeUpdate` receives `*Update[T]` and can add assignments to it
  but cannot read the ones already there; there is no accessor. Pushed into a
  `BEFORE` trigger.
- **"Creating a comment bumps the task's `comment_count`."** Needs both writes
  in one transaction. `AfterCreate` has no executor either, and — the sharper
  half — nothing in `rest/` or `mutate.go` opens a transaction, so every
  generated create, update and delete runs under autocommit. `AfterCommit`,
  which this project documents as the place for work that must not happen if the
  write aborts, is therefore unreachable from any generated write. Pushed into a
  hand-written endpoint, and `comments` is not exposed for create at all.

The third is the one that matters most, because it is not a missing convenience
but a documented feature that no generated handler can reach.

Two constraints make the fix less obvious than it looks. sqlb does not own the
write path the way a framework does — there is no place it can unilaterally
decide a transaction exists. And `Update[T]` is a *statement* over however many
rows match, not a hydrated record, which is a real capability: a set-based
update that never reads a row.

[PocketBase's event hooks](https://pocketbase.io/docs/go-event-hooks/) are the
closest prior art. Every handler receives an event carrying `e.App`, so a hook
can query; `e.Next()` lets one handler span before and after the write with the
after-phase inside the transaction; and `OnRecordAfterCreateSuccess` is
guaranteed to run post-commit — which PocketBase can promise precisely because it
always wraps. Its documentation also warns to reach the database through `e.App`
rather than a captured variable, to avoid transaction deadlock. That is the same
lesson [ADR-0020](0020-transaction-scoped-handle.md) already encodes with
`TxFrom(ctx)`; the difference is that PocketBase hands it over instead of making
the caller look it up and find nothing.

## Decision

**A hook receives an event value carrying an executor.**

```go
sqlb.OnIn[Task](reg).BeforeCreate(func(e *sqlb.CreateEvent[Task]) error {
    return validateList(e.Ctx, e.DB, e.Row.ListID)
})
```

The event carries the context, the row or statement, and `DB` — the same
executor the operation is running on, so a hook inside a transaction reads the
rows that transaction has written and a hook outside one does not silently open
a second connection.

**The update event exposes its pending assignments.** `e.Changes()` returns what
the statement will set, alongside the existing `Set` and `SetExpr`. Not the
resulting row — see the alternatives.

**`rest.Resource` wraps every generated write in a transaction.** This is what
makes `AfterCommit` mean something for the writes most applications actually
issue. **Built** — the rest of this record is not.

Two details settled in the building, both departures from what this record first
proposed:

- The option is `Options.DisableTransactions`, not `Options.Transactional`.
  Default-on was the requirement; a plain `bool` whose zero value is the safe
  one expresses that without a `*bool`, and `Options.DisableSearch` already set
  the precedent in the same struct.
- **An executor that cannot begin a transaction is refused at mount**, naming
  three ways out. Falling back to autocommit was the obvious alternative and is
  wrong: it restores this exact gap, silently, in the callback that was supposed
  to be the durable half. `sqlb.DB.CanBeginTx` exists so the refusal happens at
  startup rather than on the first POST.

A third detail was not anticipated at all: `ErrAfterCommit` must not become a
5xx. The row is durable, so reporting failure invites a retry that writes it
twice. `rest` logs it through `slog` and returns the success it actually
achieved.

Reads are not wrapped. A single `SELECT` is already atomic, and wrapping one
would hold a connection across a `BEGIN`/`COMMIT` round trip for a guarantee it
already had.

**The existing signatures stay, as wrappers.** `BeforeCreate(func(ctx, *T)
error)` keeps working, so this is additive and no call site breaks.

Explicitly not doing: PocketBase's `e.Next()` chain, hydrated records in update
hooks, string-keyed registration, or error hooks. Reasons in the alternatives.

## Consequences

**What this buys.** The three rules above become choices rather than
workarounds. `AfterCommit` becomes reachable from generated CRUD, which is the
difference between a documented feature and a decorative one. Validation against
the database — the single most common thing a `BeforeCreate` wants — stops
requiring a hand-written endpoint.

**What this costs.** An event struct per operation is public API that has to
stay stable, and it is the kind of type that accretes fields.

A hook that can query is a hook that can query badly. `BeforeQuery` in
particular now makes a per-request round trip easy to write and invisible at the
call site, which is the existing action-at-a-distance cost of ADR-0008 with a
database attached to it.

Wrapping generated writes costs a `BEGIN`/`COMMIT` round trip per write and
holds a connection for longer. Behind PgBouncer in transaction pooling mode that
is a change in how long a server-side connection is occupied, not merely a
latency figure ([ADR-0019](0019-pgbouncer-in-the-path.md)).

`Changes()` exposes a statement's internals, so a hook can come to depend on the
shape of an update some other package writes.

And there will be two places to write the same rule. `example/tasks` keeps its
`completed_at` trigger deliberately even though a hook could do it, because a
trigger also covers the backfill migration and the psql session at 3am. Making
the hook capable does not make it the right answer, and the documentation will
have to say so.

## What would change our mind

- **If a `BeforeQuery` hook issuing its own query turns up in a slow-query log
  running once per request**, the executor belongs on the write events only and
  should come off the query event. That is the half most likely to be a mistake.
- **If wrapping generated writes measurably raises connection-hold time** under
  PgBouncer transaction pooling — the thing to watch is `pgbouncer`'s
  `avg_xact_time` against `avg_query_time` — the escape hatch already exists per
  resource (`DisableTransactions`). Flipping the *default* is the change this
  record would not make lightly: it breaks anyone whose `AfterCommit` was
  working, and the Cost of change section says why that direction is the
  expensive one. Nothing has measured this yet; the number is unknown, not
  known-small.
- **If `Changes()` is not used by two independent applications**, drop it. It is
  the most speculative piece here, justified by exactly one rule in one example.
- **If people start writing rules in hooks that also need to hold for
  migrations and manual SQL**, that is a documentation failure rather than an
  API one, and the fix is guidance about triggers, not more hook surface.
- If a third application hits a rule that needs *before and after in one
  handler* — timing, `recover`, correlating a decision across the write — that
  is the evidence for `e.Next()` this record currently lacks.

## Cost of change

**Sharply asymmetric, and the asymmetry is the reason to decide it now.**

Adding the events is free today: the old function signatures stay as wrappers,
so nothing breaks and nobody has to migrate. Adding a *field* to an event later
is also free. Changing an event's shape once applications have written hooks
against it is not — that is every hook in every consumer, and hooks are exactly
the code that has no compiler-checked call site to grep for.

The transaction was the expensive one and shipped default-on, which was the
whole point: flipping it from off to on later is harmless, and flipping it from
on to off breaks anyone whose `AfterCommit` callback quietly stopped running —
silently, at runtime, in the callback that was supposed to be the durable half.
Per resource that risk is the caller's to take explicitly; as a default it would
be taken on their behalf.

Reverting to bare rows means rewriting every hook that was written against an
event. Cheap while `example/tasks` is the only consumer; not cheap after that.

## Alternatives considered

**PocketBase's `e.Next()` chain.** Genuinely close, and the most elegant thing
in the reference. One handler spans before and after, and the after-phase runs
inside the transaction, which makes correlating a decision across the write
trivial. Lost on two counts: it is the largest API change here and the smallest
marginal gain once the event carries an executor, and it presumes sqlb owns
validation and the write pipeline the way a framework does. Worth revisiting on
the trigger named above rather than dismissed.

**Hydrated records in update hooks**, as PocketBase does with `e.Record`. This is
what would let a hook see the resulting row rather than the assignments. Rejected
because `Update[T]` is set-based: hydrating means reading the matching rows
first, which gives up updating N rows without reading any of them. `Changes()`
answers the same question at a fraction of the cost.

**Pass the executor through the context instead.** Closest to today, and it needs
no new types — `TxFrom(ctx)` already does exactly this. Rejected because a lookup
that returns nothing most of the time *is* the present problem, and because the
prior art warns against the closure-captured equivalent for deadlock reasons a
context value shares.

**Leave it, and narrow the claim instead.** Document hooks as being for scoping
and stamping only, and say plainly that domain rules belong in triggers and
hand-written endpoints. Defensible — it is what `example/tasks` does today, and
the results are good. Lost because ADR-0008 claims hooks are *the* domain seam,
and a seam that cannot read the database is much narrower than that claim; the
honest version of this alternative is an edit to ADR-0008, not a smaller change.

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
