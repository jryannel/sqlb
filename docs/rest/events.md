# Change events

A view that shows a row should find out when the row changes. `rest.Events`
mounts a Server-Sent Events stream that says so — and says nothing else. A
subscriber receives the *address* of a change, never the row:

```
event: change
id: 41
data: {"table":"posts","key":"p1","op":"update"}
```

The client refetches through the ordinary `GET` endpoints. That is the whole
protocol, and the reason for it is in
[ADR-0045](../adr/0045-the-stream-is-a-seam.md): a payload would have to be built
per subscriber under that subscriber's context, or the `BeforeQuery` hook that
scopes every other read of that table would not run on it — and a change feed
that skips the scope hands one tenant's rows to another. Sending the address
keeps the read path the only thing that ever reads.

## Read this first

The source that ships today, `rest.Broker`, holds events **in memory**. Two
consequences, both of which matter more than anything else on this page:

- **At-most-once.** The event is published after the transaction commits, from
  the same process. A crash in between loses it, and no client learns the row
  changed.
- **One replica.** A `Broker` serves the subscribers connected to *its* process.
  Behind two replicas, a write served by one is invisible to everyone connected
  to the other.

So this is a real feature for a single-replica deployment and a trap for a
horizontally scaled one. The durable, multi-replica version is the transactional
outbox in [ADR-0012](../adr/0012-change-feed-outbox.md), which is unbuilt. It
plugs in as a `rest.Source` when it exists, and nothing on the wire changes.

## Wiring it

Three calls: a source, the models whose writes feed it, and the endpoint.

```go
srv := rest.NewServer(rest.Config{Title: "Blog", Version: "1.0.0"})
rest.Must(blog.Register(srv.API, db))          // generated, unchanged

broker := rest.NewBroker(rest.BrokerOptions{})
defer broker.Close()

rest.Must(rest.PublishChanges[blog.Post](broker))
rest.Must(rest.PublishChanges[blog.Comment](broker))
rest.Must(rest.Events(srv.API, rest.EventsOptions{Source: broker}))

http.ListenAndServe(":8080", srv.Handler)
```

`PublishChanges[T]` registers hooks, not handler wrappers. One registration
therefore covers the generated CRUD handlers, the generated actions, and your own
`sqlb` writes alike — including the background job and the admin script, which is
exactly where a handler-level feed would go quiet. It is the same reasoning that
makes `BeforeQuery` the place tenant scoping lives.

Publication happens through `sqlb.AfterCommit`, so a write that rolls back
announces nothing. A resource that set `DisableTransactions` still publishes:
under autocommit the statement is already durable when the hook runs.

The stream is in the OpenAPI document like every other operation, with a schema
per event type, because it registers through huma rather than as a hand-rolled
handler on the mux.

## What a client sees

Two event types.

**`change`** carries `{table, key, op}`, where `op` is `create`, `update` or
`delete`. `key` is the row's primary key rendered the way the URL renders it, so
it concatenates onto the resource path.

A delete carries its `key` like any other change, one event per removed row.
That is `AfterDeleteRows` doing the work: `PublishChanges` registers it, so the
delete runs `DELETE … RETURNING` and the publisher sees what went. The cost is a
scan of everything the statement matched, paid on every delete of a published
model — the alternative was an event naming no row, which a subscriber keyed on
one cannot use and a tenant filter cannot attribute.

`key` may still be **empty**, and a client has to handle it: a publisher written
by hand against `AfterDelete` gets a count and can only name the table. Read a
keyless event as "invalidate this collection", which is what a delete asks of a
client anyway — the row is gone and the list it was in changed.

**`reset`** carries `{reason}` and means the stream could not be resumed —
refetch everything you display. It arrives when a reconnection's `Last-Event-ID`
is older than the retained history, when that header cannot be read, or when it
is *ahead* of the stream, which is what a client from before a restart looks
like.

The generated TypeScript client's `keysByTable` map is the other half of this: it
maps a table name to the query keys that read it, which is what turns
`{table: "posts", key: "p1"}` into the set of cached queries to invalidate.

```ts
const events = new EventSource("/events");
events.addEventListener("change", (e) => {
  const { table, key } = JSON.parse(e.data);
  invalidate(keysByTable[table], key);       // your cache, your call
});
events.addEventListener("reset", () => invalidateEverything());
```

`EventSource` reconnects on its own and resends `Last-Event-ID`, so a brief
disconnection replays rather than losing events. Nothing client-side has to
remember to send it.

## Failure is a reconnection, on purpose

Every failure mode here converts into a refetch rather than into silence:

| What happened | What the subscriber gets |
|---|---|
| It stopped reading and filled its buffer | **Disconnected.** It reconnects with its `Last-Event-ID` and is replayed or reset |
| It was away longer than the retained history | A `reset` |
| Its `Last-Event-ID` is unreadable | A `reset` |
| The stream sat idle | A comment line every `Heartbeat`, so an intermediary does not reclaim the connection |

The rule underneath all of it: a dropped *event* is a client that shows stale
data forever and never finds out, while a dropped *connection* is a client that
reconnects and converges. When in doubt, drop the connection.

`BrokerOptions` tunes the two numbers that decide this — `History`, how many
events are kept for replay (256), and `Buffer`, how many may queue for one
subscriber before it is dropped (256, raised to `History+1` if set lower).

## Who sees what

By default, **every subscriber receives every event**. The events carry no row
data, but a primary key is still a fact about what exists, and nothing else on
this path is scoped: an `Event` is published by a write rather than read through
a query, so the `BeforeQuery` hook that confines every other read of that table
does not run here.

`Filter` is where that decision goes. It runs per event per subscriber with the
request's context, so it can reach whatever your authentication middleware put
there — and the event tells it which tenant the change belonged to, because the
schema already declared which column confines the rows
([ADR-0030](../adr/0030-declared-scope-is-required.md)):

```go
rest.Must(rest.Events(srv.API, rest.EventsOptions{
    Source: broker,
    Filter: func(ctx context.Context, e rest.Event) bool {
        org, err := orgOf(ctx)          // fails closed: no claims, no events
        return err == nil && e.Scope != "" && e.Scope == org
    },
}))
```

`Event.Scope` is the value of the `.Scoped()` column on the row that changed, and
it is **not on the wire**. It exists for this decision. A subscriber gains
nothing from being told its own tenant id, and the wire is the half
[ADR-0045](../adr/0045-the-stream-is-a-seam.md) records as expensive to change.

It is empty when the model declares no scope, and empty on a hard delete, which
has no row to read it from. Decide what an empty scope means for you — the
example refuses it, on the grounds that an event it cannot attribute is one it
should not deliver.

Soft deletes do not have this problem: a soft delete is an `UPDATE`, so it
carries the key and the scope like any other change.

A filtered event's id is never written, so a subscriber filtered out of
everything keeps an old `Last-Event-ID` and is eventually told to reset when it
reconnects. That costs it a refetch, which is the safe direction: the alternative
is advancing its position past events it was never shown, and then a genuine gap
would be indistinguishable from a filtered one.

## Bringing your own source

`Source` is one method:

```go
type Source interface {
    Subscribe(ctx context.Context, since uint64) (<-chan Delivery, error)
}
```

`since` is the client's `Last-Event-ID`, or zero for a fresh connection. A source
that can replay from that position should; one that cannot must open the stream
with a `Delivery` carrying a `Reset`, so the gap is announced rather than
skipped. Close the channel to disconnect a subscriber — the client reconnects on
its own.

That is the seam an outbox dispatcher, River, or NATS goes behind. The endpoint,
the wire format and every client stay as they are.
