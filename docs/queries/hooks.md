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
sqlb.On[Post](reg).BeforeQuery(func(ctx context.Context, q *sqlb.Builder[Post]) error {
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

The hook amends a clone, on the exec path, so `q.SQL()` on the builder you built
does not show what it added. `q.Resolved(ctx, db)` does — reach for it when the
predicate has to be read as *text*, for a raw statement that must count the same
rows or for a test asserting the scope is in force. See
[Inspecting](inspecting.md#resolved-which-renders-the-statement-that-runs).

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
| `AfterDeleteRows` | `[]T`, as they were | Anything needing the row's identity |

`AfterDelete` and `AfterDeleteRows` are two hooks rather than one because the
rows are not free. A `Delete` is write-only for predicates, so a `BeforeDelete`
cannot ask what a statement addresses and the rows have to come back from the
statement itself — which means `DELETE … RETURNING` and a scan of everything it
matched. sqlb adds that clause only when an `AfterDeleteRows` hook is registered
for the model, so a delete whose rows nobody reads still costs one command tag.

Register the rows form when a count is not enough, which in practice means
publishing anything: an event that says *how many* posts were deleted and not
*which* is worse than no event, because the subscriber invalidating a cache
keyed on the row has nothing to key on and the feed looks wired up.
[`rest.PublishChanges`](../rest/events.md) uses it for exactly that.

All of these run **inside** the caller's transaction. That is right for
validation — an error rolls the write back — and wrong for anything the outside
world can observe.

The write hooks are narrower than `BeforeQuery`: they receive the row or the
statement rather than a handle. They can still reach the whole database, but only
where a transaction is — `rest` wraps every generated write in one, so
`sqlb.TxFrom(ctx)` finds it and a hook can query and write through it. On a read,
or under `Options.DisableTransactions`, there is nothing to find.

That is the extent of a hook's reach and it is easy to miss, because nothing in
the signature mentions it. [Writing the
consequence](#writing-the-consequence-which-is-usually-another-table) is the
cross-model case, including which rules the statement runs under;
[Reading your own writes](#reading-your-own-writes) is the same-model one.

### What fires when, and inside which transaction

Four questions decide whether a domain invariant holds, and none of them is
answerable from a signature. Stated here so they need not be answered by
reading the source.

**Every write path fires the hooks, not just the generated ones.**
`sqlb.InsertRows(&a, &b).Exec(ctx, tx)` runs `BeforeCreate` on each row and
`AfterCreate` on each stored row, exactly as `POST /posts` does. A hand-written
HTML form handler and a generated REST handler enforce the same rules without
either knowing the other exists, and that is the property the whole arrangement
is for.

**`AfterCreate` receives a pointer into the returned rows, so it can change
the response.** Mutating `*T` there changes what the caller gets back and what
`rest` writes to the wire — which is how a generated `POST /orders` can answer
with the fill it just executed rather than with the order as submitted. This
makes `AfterCreate` a good deal more than the "validation" its row in the table
above suggests.

**A defaulted column holding its zero value is omitted from the insert.** So
the database supplies it rather than a zero overwriting it, and a `BeforeCreate`
that copies one column into another falls out correctly in the zero case
without a special case. This is why "has a default" and "is optional in the
create body" are the same question.

**Hooks reach the transaction.** `WithTx` hands `fn` a `*sqlb.DB` carrying the
same registry, so hooks fire on statements issued inside it, and `TxFrom(ctx)`
resolves *within* a hook — a `BeforeCreate` can read what earlier statements in
the same unit of work have written but not yet committed.

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

## Writing the consequence, which is usually another table

"Reserve, match, write the consequence" is the sentence above, and the
consequence is rarely on the model whose hook is running: placing an order
decrements stock, posting a comment bumps a count. The write hooks receive the
row or the statement rather than a handle, so the rest of the database is reached
through the context — `rest` wraps every generated write in a transaction, and
`sqlb.TxFrom(ctx)` is that transaction:

```go
sqlb.On[Order](reg).AfterCreate(func(ctx context.Context, o *Order) error {
    tx, ok := sqlb.TxFrom(ctx)
    if !ok {
        // Fail closed. Without a transaction the decrement cannot move
        // atomically with the order, and an order that quietly reserved
        // nothing is worse than one that is refused.
        return errors.New("orders must be placed inside a transaction")
    }
    _, err := sqlb.UpdateRows[Stock]().
        SetExpr("count", sqlb.Raw{SQL: `"count" - ?`, Args: []any{o.Qty}}).
        Where(sqlb.F("sku").Eq(o.SKU), sqlb.F("count").Gte(o.Qty)).
        One(ctx, tx)
    if errors.Is(err, sqlb.ErrNotFound) {
        return huma.Error409Conflict("not enough stock")
    }
    return err
})
```

Three things in there are load-bearing and none of them is visible in the hook's
signature. The `count >= o.Qty` predicate is what makes the decrement a decision
rather than a report — see [the four places a rule can
live](../concepts/domain-logic.md#the-four-places-a-rule-can-live). The handle it
runs on is `tx`, which the next section is about. And it is `One` rather than
`Exec`, which the one after that is about.

[`example/tasks/app/hooks.go`](../../example/tasks/app/hooks.go) is this shape
in a working application: a comment's `AfterCreate` bumps the task's
`comment_count` through `TxFrom`, and the file's own comment records that before
generated writes were transactional this had to be a hand-written endpoint.

### The handle carries the rules of the request that triggered the hook

`TxFrom` returns the handle the *request* resolved against, so a statement
issued through it runs that request's hooks — including the hooks of the model
you are reaching for. This is the single most surprising thing about a
cross-model write, and whether it is what you want depends on something the code
does not say out loud: **whether the two models are scoped on the same axis.**

| | the axis | the request's scope on the hook's write |
|---|---|---|
| A comment bumps its task's count | `workspace_id` on both | **Correct, and load-bearing.** A comment and its task are in one workspace, so the predicate that confines the request confines the consequence to the same place |
| An order decrements a shop's stock | `buyer_id` vs `shop_id` | **Wrong.** The buyer is not the shop, so the shop's inventory row is outside the buyer's scope |

The first row is `example/tasks`, and it is why this behaviour is the default
rather than a bug: the counter bump picks up `workspace_id = <the caller's>` from
`scopeWrites[tasks.Task]`, which is exactly the confinement the invariant wants.

The second row is the case that needs saying. `Stock`'s `BeforeUpdate` exists
because [the obligation check](#say-it-in-the-schema-so-the-missing-hook-is-the-one-that-is-caught)
requires it of any `Scoped` model with an exposed update, and it is written for
the request — a buyer may not `PATCH` a shop's stock. Run the decrement through
`tx` and that predicate lands on it too:

```sql
UPDATE "stocks" SET "count" = "count" - $1 WHERE ("sku" = $2) AND ("shop_id" = $3)
```

Zero rows, because `shop_id` is the buyer's. The domain logic that was supposed
to act *past* the request's scope has been confined by it.

The fix is to name the rules the consequence runs under, which is a second
registry and one handle built from it:

```go
// At wiring time, beside the request registry.
system := sqlb.NewRegistry()          // whatever the consequence needs, and no request scope
sqlb.On[Stock](system).BeforeUpdate(stampStockUpdatedAt)

sqlb.On[Order](reg).AfterCreate(func(ctx context.Context, o *Order) error {
    tx, ok := sqlb.TxFrom(ctx)
    if !ok {
        return errors.New("orders must be placed inside a transaction")
    }
    _, err := sqlb.UpdateRows[Stock]()….One(ctx, tx.WithHooks(system))
    …
})
```

`WithHooks` copies the handle and swaps the registry, so the statement stays on
the same transaction — same connection, same snapshot, rolled back by the same
failure — and only the rules change. Two things follow from that being a
*registry* rather than a flag. The consequence's own rules still run, so a
`system` registry is where a stock write's `updated_at` stamp or audit hook goes
rather than nowhere. And the set of code that can escalate is the set that was
handed the value, which is the whole question a `tx.Unscoped()` method could not
answer — see [Scoping and tests](#scoping-and-tests).

`tx.Tx()` and `sqlb.New(pgTx)` reach the same place with no registry at all,
because an `Executor` that is not a `*sqlb.DB` carries none. That is the blunter
instrument: it drops the consequence's rules along with the request's, which for
a model with a soft delete or a stamped column is usually not what was meant.

### When the write must have happened, say so

`Update.Exec` returns the rows it updated, and **zero rows is not an error**.
That is right for a statement whose predicate expresses a condition — an
idempotent flag, a conditional decrement that lost its race — and it is a trap
for a consequence that has to happen, because the hook returns nil and the
transaction commits with the consequence missing.

So use `One` when exactly one row must move. It returns `ErrNotFound` on zero,
which inside a hook rolls the whole unit of work back — the order does not exist
either, which is the correct outcome and the one a silent `Exec` does not give
you. `errors.Is(err, sqlb.ErrNotFound)` is then the place to say *why* in the
response, as the example above does with a 409.

This is also the check that catches the scope mistake in the section before,
which is the argument for reaching for `One` first and relaxing to `Exec`
deliberately. When the count is genuinely not one, `Exec` and a length check say
the same thing in two lines. To see what a hook actually added to the statement,
`Resolved` renders it:
[Inspecting](inspecting.md#resolved-which-renders-the-statement-that-runs).

## AfterCommit, for side effects

Publishing an event, enqueuing a job, invalidating a cache: none of these may
happen if the write does not. `AfterCreate` running inside the transaction means
the transaction can still abort after the hook has already told the world it
succeeded.

```go
sqlb.On[Order](reg).AfterCreate(func(ctx context.Context, o *Order) error {
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
sqlb.On[Post](reg).BeforeCreate(func(ctx context.Context, p *Post) error {
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

A hook is also where a lock is taken deliberately, and **it has to be taken in
`BeforeCreate`, not `AfterCreate`**. This one is invisible in the Go code.

Inserting a row takes a `FOR KEY SHARE` lock on every row its foreign keys
reference — Postgres checking the reference, not anything you wrote. Key-share
locks are shared, so two concurrent inserts both get them; if each then tries to
upgrade the same referenced row to `FOR UPDATE` inside `AfterCreate`, each waits
for the other's share lock. That is a guaranteed deadlock, it scales with
concurrency, and it surfaces as a 500 naming a statement you did not write:

```
ERROR: deadlock detected (SQLSTATE 40P01)
  on: SELECT ... FROM "stocks" WHERE "id" = $1 LIMIT 2 FOR UPDATE
```

Taking the exclusive lock in `BeforeCreate` — before the row exists, so before
the key-share lock is taken — fixes it, and costs one line of ordering.

The other rule is to **take locks in a consistent order**: two transactions
locking the same rows in opposite orders deadlock, and the test that would have
found it is the one nobody writes.

Both are easy to state and impossible to see, which is why they are worth being
a named function with the explanation attached rather than a line inside a
handler. `ForUpdate`, `ForShare` and `SkipLocked` are on
[Mutations and transactions](mutations.md#row-locking).

## One registration per model, and what that costs

A registry is keyed by type and `On[T]` is the only way in, so **there is no
receiver for every model**. Several listeners on one model is fine — hooks
append and run in registration order, so independent modules can each register
`AfterCreate` for the same type without knowing about each other. What has no
spelling is the other axis: one listener for the whole schema.

So a cross-cutting concern is one registration each, written out:

```go
for _, wire := range []func(*sqlb.Registry, rest.Publisher) error{
    rest.PublishChanges[tasks.Task],
    rest.PublishChanges[tasks.List],
    rest.PublishChanges[tasks.Comment],
} {
    if err := wire(reg, broker); err != nil {
        return err
    }
}
```

For the change feed that list is the design rather than a chore. Every model in
it is a fan-out every subscriber pays for, which is why the loop above is three
of that schema's six models and each of the three left out is left out for a
stated reason. A register-everything form would make the cheap choice the default
and the considered one an opt-out, which is backwards when each entry has a cost.

The concerns where it *is* a chore are the ones with no per-model decision in
them: an audit log, a write counter, a stamp. There the list is a place to forget
something, and forgetting is silent — a table added next month joins the schema,
gets a resource, gets migrated, and is simply absent. Nothing fails, because
nothing declared that the model was meant to be in it.

One thing narrows it already: registration is generic, so the list is Go the
compiler checks rather than strings resolved at startup, and a renamed or deleted
model breaks the build. What it cannot catch is an *added* one, because nothing
in the new model refers to the list.

**Nothing makes that omission fail, and it is worth knowing that rather than
assuming otherwise.** The shape of a mechanism that would is already in the
generated code: `Register` enumerates every exposed resource, and the generated
`Actions` struct turns a schema-declared action nothing wired into a refusal when
the resource mounts rather than a route that quietly 404s. Both work by making the
schema and the wiring meet somewhere that can fail. Hook registration has no such
meeting point, because a registration cannot be derived from a list of models at
runtime — `On[T]` needs the type at compile time, so an exhaustive list has to be
*generated* rather than iterated. That is not built.

Until it is, the mitigations are the ordinary ones: register the cross-cutting
concern next to the resource list so a reader adding a table sees both, and write
the assertion that the concern covers what it must in a test rather than trusting
the loop. Whether that is worth the trouble depends on what absence costs — a
missing entry in a change feed is a client that refetches late, and a missing
entry in an audit log is a compliance gap.

## Scoping and tests

`On[T](r)` registers into the registry you hand it, and `db.WithHooks(r)` is
how a handle acquires it. There is no process-wide registry to fall back on
([ADR-0047](../adr/0047-no-default-hook-registry.md)) — a handle built by
`sqlb.New` starts with an empty one of its own, so the rules in force are a
property of how the handle was assembled rather than of what ran first.

A test therefore gets isolation for free: build a registry, attach it, and
there is nothing to tear down. Two tenants' worth of differing domain rules
coexist in one process the same way.

One consequence worth knowing: an `Executor` that is not a `*sqlb.DB` — a raw
pool, a borrowed `pgx.Tx` — carries no registry, so a statement issued against
one runs unconfined. That is why models whose rows must not be read unscoped
declare `Scoped`, which refuses the mount rather than trusting the call site
([ADR-0030](../adr/0030-declared-scope-is-required.md)).

A second registry earns its keep beyond isolation, and there are two distinct
reasons to want one.

**A request that has no tenant yet.** `example/tasks` builds two handles over one
pool: one resolving against its hooks, and one against an empty registry, used by
exactly the two endpoints that have to read a user *before* there is a tenant to
scope the read to. `example/fxapp` provides the same thing as a named type,
`fxkit.Unscoped`.

**A consequence that has to act past the request's scope.** This is the checkout
case from [writing the
consequence](#writing-the-consequence-which-is-usually-another-table): the hook
is running under the buyer's rules and has to move the shop's stock, so it needs
a handle whose rules are the consequence's rather than the request's.

Both are the same shape, and the shape is the point. Two values, one of which
never leaves its own file, is harder to misuse than one handle and a "skip the
hooks" flag — a flag is something a caller can pass, and the set of callers
allowed to pass it is the whole question. A `tx.Unscoped()` method would be that
flag, reachable from every call site holding a handle; a registry built at wiring
time is reachable only by what was handed it.

## Next

- [Inspecting and tracing](inspecting.md) — seeing what a hook did
- [Mounting resources](../rest/README.md) — the handlers these hooks reach
- [Capabilities](../schema/capabilities.md) — `ReadOnly` plus a hook, and
  `Scoped`
