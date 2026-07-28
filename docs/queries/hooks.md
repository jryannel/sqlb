# Hooks

Hooks are where domain logic lives. Register once at startup, typically from
`init` or `main`; they run in registration order, and one returning an error
aborts the operation with the error reaching the caller unwrapped.

[Where domain logic goes](../concepts/domain-logic.md) argues why the seam is
here. This is how to use it.

## BeforeQuery is the load-bearing one

It receives the query itself, so **one registration constrains every read of the
model** — including the reads the generated REST handlers issue.

```go
sqlb.On[Post]().BeforeQuery(func(ctx context.Context, q *sqlb.Builder[Post]) error {
    org, ok := auth.OrgFrom(ctx)
    if !ok {
        return auth.ErrNoTenant
    }
    q.Where(sqlb.F("org_id").Eq(org), sqlb.F("deleted_at").IsNull())
    return nil
})
```

Multi-tenancy and soft deletes stop being something each call site has to
remember. This is also why REST registration is generic over the model rather
than reflective: hooks are keyed by type, and a reflective dispatcher could not
run them.

Returning an error is how "no tenant in this context" becomes impossible to
forget rather than merely documented — no statement runs at all.

## Say it in the schema, so the missing hook is the one that is caught

The hook above cannot be forgotten at a call site. It can be forgotten
entirely, and an unscoped model serves every tenant's rows with a `200` next to
them. So the table declares what it expects:

```go
schema.Ref("org", Org).Filterable().ReadOnly().Scoped()
```

`Scoped` writes no predicate — it is inert in exactly the way `SoftDelete`'s
column is. What it does is oblige the resource: `rest.Resource` refuses to
mount a model whose declarations no hook satisfies, and names every missing
registration at once. The obligation follows the operations, because a
`BeforeQuery` hook says nothing about what a request can overwrite by id — an
exposed update needs `BeforeUpdate`, a delete needs `BeforeDelete`, and a
create needs `BeforeCreate` to supply the tenant column that `ReadOnly` kept
out of the request body.

The check proves a hook exists, not that it is right. That is worth knowing
before relying on it, and it catches the case that actually happens: the table
somebody added last week. [ADR-0030](../adr/0030-declared-scope-is-required.md)
has the reasoning, including why the predicate is not generated for you.

[`example/tasks/app/hooks.go`](../../example/tasks/app/hooks.go) is this taken
as far as it goes: one file, a little over two hundred lines, confining six
models across twenty-five endpoints. Two details there are worth stealing. The
scoping is one generic function used four times rather than four near-copies,
which is only possible because every table in that schema names the column
`workspace_id` — a convention kept deliberately so the boundary can be written
once. And reads and writes are scoped by *separate* registrations, because a
`BeforeQuery` predicate constrains what a request can see and says nothing about
what it can overwrite by id.

## The rest

| Hook | Receives | Use for |
|---|---|---|
| `BeforeCreate` | `*T` | Normalising an email, deriving a slug, stamping an owner |
| `AfterCreate` | `*T`, with defaults populated | Validation |
| `BeforeUpdate` | `*Update[T]` | Forcing a column, narrowing affected rows |
| `AfterUpdate` | `[]T` | Validation |
| `BeforeDelete` | `*Delete[T]` | Narrowing, or refusing |
| `AfterDelete` | `int64` | Validation |

All of these run **inside** the caller's transaction. That is right for
validation — an error rolls the write back — and wrong for anything the outside
world can observe.

The write hooks are narrower than `BeforeQuery`: they receive the row or the
statement rather than a handle. They can still reach the database, but only
where a transaction is — `rest` wraps every generated write in one, so
`sqlb.TxFrom(ctx)` finds it and a hook can query, as
[Reading your own writes](#reading-your-own-writes) below shows. On a read, or
under `Options.DisableTransactions`, there is nothing to find.

The gap that remains is narrower and deliberate: `BeforeUpdate` cannot read the
assignments it was handed, so a rule that depends on what a column is *becoming*
belongs in a `BEFORE` trigger.
[ADR-0021](../adr/0021-hooks-receive-an-event.md) records why the event types
that would have closed it are not being built.

What has landed from that record is the transaction: `rest.Resource` wraps every
generated create, update and delete in one, so `AfterCommit` is reachable from a
generated write. Set `Options.DisableTransactions` to opt out, and read the next
section before you do.

## An insert can mean something

`AfterCreate` running inside the write's transaction is what lets a generated
`POST` be a domain operation rather than an insert. The handler decodes a body,
validates it and inserts a row, and knows nothing about the rule; the hook turns
that insert into a placement — reserve, match, write the consequence — in the
same transaction, so a refusal rolls the row back with it.

That is why a schema modelled this way has no "rejected" status: an operation
that could not be performed is not a row, it is a 422.

The alternative is a hand-written `/orders/place`, which would work and would be
a **second door**: the generated create would still exist, and the next person
to write against the model would insert rows that reserved nothing. Closing both
doors with one registration is the argument for hooks in a sentence.

## AfterCommit, for side effects

Publishing an event, enqueuing a job, invalidating a cache: none of these may
happen if the write does not. `AfterCreate` running inside the transaction means
the transaction can still abort after the hook has already told the world it
succeeded.

```go
sqlb.On[Order]().AfterCreate(func(ctx context.Context, o *Order) error {
    id := o.ID
    return sqlb.AfterCommit(ctx, func(ctx context.Context) error {
        return events.Publish(ctx, OrderPlaced{ID: id})
    })
})
```

Callbacks run in registration order once `Commit` returns nil, and not at all if
it rolls back. The context they receive carries no transaction — there is
nothing left to join, and handing back a committed one would be a trap.

A failing callback does not stop the others; the failures are joined under
`ErrAfterCommit`. That sentinel matters, because the two cases need opposite
responses:

```go
if err := db.WithTx(ctx, placeOrder); err != nil {
    if errors.Is(err, sqlb.ErrAfterCommit) {
        // The order exists. Something downstream of it did not fire.
        log.Error("order placed, notification failed", "err", err)
    } else {
        return err // The order does not exist.
    }
}
```

Outside a transaction, `AfterCommit` is an error rather than an immediate call:
under autocommit sqlb cannot say when the commit happened, so the callback would
fire before the insert or after it depending on which hook called it.

**From a generated handler there is always a transaction**, because
`rest.Resource` opens one per write. The two ways to end up without one are a
write you issue yourself outside `WithTx`, and a resource that set
`Options.DisableTransactions`. The second is worth stating plainly: turning it on
does not disable `AfterCommit`, it makes every registration fail at request time.
That is loud rather than silent, which is the point — but it means the option is
a decision about the resource's hooks, not only about its latency.

This is in-process and at-most-once. A callback that never ran because the
process died leaves no trace — that is what a transactional outbox is for, and
it is not built ([ADR-0012](../adr/0012-change-feed-outbox.md)).

## Reading your own writes

A hook that needs to see rows written earlier in the same transaction must read
through the transaction handle. Reading through the pool would miss them,
because they are not committed yet:

```go
sqlb.On[Post]().BeforeCreate(func(ctx context.Context, p *Post) error {
    tx, ok := sqlb.TxFrom(ctx)
    if !ok {
        return errors.New("posts must be created inside a transaction")
    }
    n, err := sqlb.Query[Post]().Where(sqlb.F("slug").Eq(p.Slug)).Count(ctx, tx)
    …
})
```

A check-then-act like that is only sound when something else has the last word.
Where the guarantee is a unique index, the read exists to turn an unclassifiable
Postgres error into a 409 that names the problem — which is a good reason. Where
there is no constraint underneath, two concurrent requests will both pass the
check; see [where domain logic goes](../concepts/domain-logic.md).

## Locking order

A hook is also where a lock is taken deliberately. Inserting a row takes a
`FOR KEY SHARE` lock on every row its foreign keys reference — Postgres checking
the constraints, invisible in the Go code — so two transactions that later try
to upgrade the same row to `FOR UPDATE` deadlock. Taking the exclusive lock in
`BeforeCreate`, before the row exists, collapses that into a queue.

The rule is easy to state and impossible to see, which is why it is worth being
a named function with the explanation attached rather than a line inside a
handler.

## Scoping and tests

`On[T]()` reaches a process-wide registry, which is what you want at startup. A
test needs isolation, and has two options: `Reset()` in a defer, or
`sqlb.NewRegistry()` plus `db.WithHooks(r)`, which needs no teardown. The second
is also how two tenants' worth of differing domain rules coexist in one process.

A scoped registry earns its keep outside tests too. `example/tasks` builds two
handles over one pool: one resolving against its hooks, and one against an empty
registry, used by exactly the two endpoints that have to read a user *before*
there is a tenant to scope the read to. Two values, one of which never leaves
its own file, is harder to misuse than one handle and a "skip the hooks"
flag — a flag is something a caller can pass, and the set of callers allowed
to pass it is the whole question.

## Next

- [Inspecting and tracing](inspecting.md) — seeing what a hook did
- [Mounting resources](../rest/README.md) — the handlers these hooks reach
- [Capabilities](../schema/capabilities.md) — `ReadOnly` plus a hook, and
  `Scoped`
