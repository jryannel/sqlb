# ADR-0045: The stream is a seam, and its first source is honest about being in-process

- **Status:** Working — built. `rest.Events` mounts a documented SSE endpoint,
  `rest.Broker` is the in-process source behind it, and `rest.PublishChanges[T]`
  wires a model's writes to it. Additive: an application that mounts nothing new
  gets exactly what it got before
- **Confidence:** High that the split is right — the transport, the wire format
  and the client contract are the same whether events come from memory or from
  an outbox, and only one of the two is cheap to build today. High that
  invalidation rather than payload is correct, because the payload variant has a
  tenant-leak failure mode the invalidation variant does not have at all. Medium
  on the in-process source's default limits (256 events of history, disconnect
  on overflow), which are the numbers most likely to move on contact with real
  traffic. Low that `Source` is the final shape of the seam — it has one
  implementation, and a contract with one implementation has not been tested
- **Decided:** 2026-08-01
- **Last reviewed:** 2026-08-01

## Context

[ADR-0012](0012-change-feed-outbox.md) decided how change notification should
work and built none of it: an outbox table written in the same transaction as
the change, a dispatcher woken by `LISTEN/NOTIFY`, and a fan-out "to SSE or
WebSocket subscribers". It has been Status: Exploring, Confidence: Low since
2026-07-27, and it is the largest unbuilt item in [the vision](../vision.md).

Two things had happened since it was written. First, the
[adoption review](../review-adoption-existing-app.md) went looking for what sqlb
offered a real application's SSE endpoint and answered §11.4 with "does not touch
SSE today, does not help it, and does not need to" — the recommendation was to
build the outbox on infrastructure that already existed and not wait. Second,
`sqlb.AfterCommit` shipped ([ADR-0020](0020-transaction-scoped-handle.md)) and
generated CRUD started wrapping its writes ([ADR-0021](0021-hooks-receive-an-event.md)),
which means the *moment* to publish a change — after the commit, not inside it —
became reachable from every write sqlb issues.

So the missing piece was never the hard half. The hard half is durability across
a crash and correctness across replicas, and that is the outbox. The missing
piece was everything downstream of it: an endpoint, a wire format, a reconnection
story, and a decision about what a subscriber is actually sent. All of which the
outbox will need unchanged, and none of which is blocked on it.

## Decision

**The stream is a transport with a `Source` behind it.** `rest.Events` mounts the
endpoint; a `Source` supplies deliveries. The endpoint owns the HTTP concerns —
`Last-Event-ID`, heartbeats, the retry hint, the per-subscriber filter — and
knows nothing about where events come from. ADR-0012's dispatcher implements
`Source` and replaces what ships today without the endpoint, the wire format, or
any client changing.

**The first source is in-process, and says so in its own documentation.**
`rest.Broker` fans out to the subscribers connected to *this process*. It is
at-most-once, and it is single-replica. Both are stated at the top of its doc
comment rather than in a changelog, because the failure they produce — a client
that never learns a row changed and shows stale data indefinitely — is invisible
from the outside, and someone deploying two replicas behind a load balancer will
not discover it from a test.

**A subscriber receives an invalidation, not a row.** `{table, key, op}`, and the
client refetches through the ordinary GET endpoints. This is ADR-0012's decision
and it holds for a reason that got sharper while building: a payload would have
to be produced per subscriber under that subscriber's context, or the resource's
`BeforeQuery` scope would not run on it — and a change feed that skips the scope
hands one tenant's rows to another. Sending the address of the change keeps the
read path the only thing that ever reads, so every rule the read path enforces
still holds. The generated TypeScript client's `keysByTable` map
([ADR-0028](0028-typescript-client.md)) is the other half of it.

**The tenant comes from the schema, and stays off the wire.** `Event.Scope`
carries the value of the column the model declared `scope`
([ADR-0030](0030-declared-scope-is-required.md)), and it is not serialised. It
exists so `Filter` can answer the only question a multi-tenant deployment has
about this stream — is this event mine — without the endpoint knowing what a
tenant is. Putting it on the wire would tell a subscriber its own tenant id,
which it already knows, at the cost of enlarging the contract this record calls
the expensive half to change. This fell out of wiring `example/tasks`: with only
`{table, key, op}` the filter had nothing to compare, so the example could either
leak row ids across tenants or deliver nothing.

**Every failure converts into a refetch, loudly.** A subscriber that falls behind
its buffer is *disconnected*, not skipped. A reconnection whose `Last-Event-ID`
is older than the retained history gets a `reset` event, not silence. A
`Last-Event-ID` that does not parse gets a `reset` rather than being read as a
fresh connection. The rule behind all three: a dropped event is a client that
stays wrong forever, and a dropped connection is a client that reconnects and
converges. When in doubt, drop the connection.

**Writes reach the feed through hooks, not through the handlers.**
`rest.PublishChanges[T]` registers `AfterCreate`, `AfterUpdate` and `AfterDelete`
on the model, each publishing through `sqlb.AfterCommit`. Wiring the REST write
path instead would have been simpler and would have produced a feed that goes
quiet for exactly the writes most likely to matter — the background job, the
migration, the admin script. It is the same argument that makes `BeforeQuery` the
place tenant scoping lives ([ADR-0008](0008-hooks-as-domain-seam.md)).

**The endpoint is in the OpenAPI document.** It registers through huma's `sse`
package rather than as a hand-rolled handler on the mux, so the document carries
one schema per event type. A consumer generating a client learns what a change
looks like instead of learning that the response is text.

## Consequences

**Buys.** A live view works today, on one replica, with no schema change, no
migration and no new infrastructure. The client contract — the endpoint, the two
event types, the `Last-Event-ID` behaviour — is settled and testable now, so the
outbox lands later as a source swap rather than as a client migration. And the
part of the problem that was genuinely undecided (what is on the wire, what
happens on reconnect) is decided against a running implementation instead of on
paper.

**Costs.** A feature that is correct on one replica and quietly wrong on two.
That is the real cost and no amount of documentation fully retires it; what
documentation can do is make the limit the first thing a reader meets, which is
where it is. Publication is also at-most-once in a second, subtler way: the
process can die between `COMMIT` and the fan-out, and nothing anywhere records
that the event was owed.

**Not addressed.** Ordering across tables. Authorization, beyond the `Filter`
hook that hands the decision to the application — the default is that every
subscriber sees every event, and a primary key is a fact about what exists.
WebSocket. Any delivery guarantee at all.

## What would change our mind

- ~~**A deployment runs two replicas and wants this.** That is the trigger for the
  outbox, not a reason to widen the Broker: a second in-memory broker that
  gossips is a distributed system with none of the durability the outbox gets for
  free by writing a row.~~ The outbox is built
  ([ADR-0012](0012-change-feed-outbox.md)), so the answer to two replicas is now
  a constructor call rather than a plan. The Broker stays as-is and stays
  documented as single-replica; nothing about it widened.
- ~~**A second `Source` appears** — River, NATS, the outbox dispatcher — and does
  not fit `Subscribe(ctx, since) (<-chan Delivery, error)`. One implementation
  has not tested the contract, and the position-based resume is the part most
  likely to be wrong for a source whose positions are not a dense sequence.~~
  Fired, and the contract held: `outbox.Dispatcher` implements it unchanged, and
  the endpoint, the wire format and both clients were untouched by the swap.

  The second half of the entry was right about the risk and wrong about where it
  would bite. A source whose positions are not dense *would* break resume — so
  the outbox makes them dense, with a transaction-scoped advisory lock that puts
  its ids in commit order ([ADR-0012](0012-change-feed-outbox.md)). The
  `Subscribe` signature did not have to change; something behind it had to pay
  for the guarantee the signature already assumed. That is a seam working, and it
  is also a warning: this interface makes a dense monotonic position look free,
  and for the second implementation it cost a throughput ceiling.

- **A third `Source` cannot make its positions dense.** NATS sequence numbers and
  a Kafka offset are per-partition; a source over either would have to either
  fake a position or reset every reconnection. That is the entry the one above
  turned into, and it is now the live risk on this interface rather than a
  hypothetical about the outbox.
- **`Filter` turns out to be where authorization actually lives** rather than a
  hook a few applications set. Then it should not be a func on an options struct
  — it should be the same kind of declared obligation
  [ADR-0030](0030-declared-scope-is-required.md) makes of scoped reads, checked
  at mount time rather than left to whoever wired the endpoint.
- **Disconnect-on-overflow shows up as reconnect storms** under a write burst.
  The fix is a larger buffer, and if that is not enough, collapsing queued events
  per table before the buffer fills — which is sound precisely because the events
  are invalidations and two of them for the same table are one.
- ~~**Keyless deletes prove too coarse.**~~ Answered on 2026-08-03, and not by
  the measurement this entry asked for — see the revision below. What settled it
  was that a *scope* cannot be recovered from a count either, so the filter this
  record added had to let every delete through to every tenant.

## Cost of change

**Low for the source, high for the wire.** Swapping `Broker` for the outbox
dispatcher is one constructor call in one application — that is the whole point
of the seam, and it is cheap by construction.

The wire is the expensive half, and it is expensive in the ordinary way: `event:
change` with `{table, key, op}`, `event: reset`, and `Last-Event-ID` as a decimal
position are what every subscriber will be written against, including
hand-written frontend code that no generator will update. Changing the shape
means a version of the endpoint, not an edit. That asymmetry is why the wire is
deliberately the smallest thing that works — three fields, two event types — and
why the payload question was settled before shipping rather than after.

**Free to withdraw entirely** while nothing depends on it: `Events`, `Broker` and
`PublishChanges` are additive, and an application that mounts none of them is
unaffected by their removal.

## Revisions

- 2026-08-13 — The outbox landed behind this seam
  ([ADR-0012](0012-change-feed-outbox.md)) and two *what would change our mind*
  entries fired. Nothing in this record's decision changed, which is the result
  it was written hoping for: `Source`, `Delivery`, the two event types and the
  `Last-Event-ID` contract are what they were, and `rest.Broker` is untouched.
  One thing was added to `rest` rather than changed — `TxPublisher`, the optional
  interface `PublishChanges` asserts for, which is what lets a durable publisher
  record inside the writing transaction while a Broker keeps announcing after it.
- 2026-08-01 — Written, with the transport built and the outbox still unbuilt.
  Splits ADR-0012's decision in two: this record owns the fan-out and the wire,
  0012 keeps the durability.
- 2026-08-01 — Added `Event.Scope`, off the wire, after wiring `example/tasks`.
  The multi-tenant case could not be filtered without it: an invalidation names a
  row, and a filter needs to know which tenant owned the row. Reading it from the
  model's declared scope column rather than from a new option means the same
  declaration that obliges the read hooks also makes the feed filterable.
- 2026-08-03 — **A delete names its row.** `PublishChanges` registers
  `sqlb.Hooks.AfterDeleteRows` — a second delete hook added for
  [#144](https://github.com/jryannel/sqlb/issues/144) — and publishes one event
  per removed row. The open question above asked for a measured refetch cost
  before paying for this, and the thing that actually decided it was the
  revision immediately above rather than any measurement: `Event.Scope` is read
  off the changed row, so a keyless delete was also a *scopeless* one, and
  `EventsOptions.Filter` had nothing to compare. A feed with a tenant filter that
  cannot attribute its deletes is not a filtered feed for the operation a client
  most needs to hear about.

  The cost this entry was hedging against is real and now paid: a delete of a
  published model runs `DELETE … RETURNING` and scans what it matched. It is
  confined to published models, because the clause is added only where a
  row-taking hook is registered.

  Worth keeping as a lesson about how these entries are written. "Worth it only
  if a measured refetch cost says so" named the wrong axis — the cost that
  mattered was not what subscribers refetch but what the filter can decide, and
  that was already knowable from the record's own previous revision. A *what
  would change our mind* item that names one metric can hide the argument that
  settles the question.
