# Where domain logic goes

Generated CRUD is only useful if the rules that are not CRUD have somewhere to
live. In sqlb that place is **hooks**: registrations keyed by model, run by
every path that touches it — your own code and the generated REST handlers
alike.

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

One registration, and every read of the model is constrained. Multi-tenancy and
soft deletes stop being something each call site has to remember.

## Why the seam is here and not in a handler

The alternative is a hand-written endpoint that applies the rule, and the
problem with it is not that it is more code. It is that **the generated door
stays open beside it**. If `POST /orders/place` reserves funds and
`POST /orders` still inserts a row, the next person to write against orders will
find the second one.

Putting the rule on the model closes both doors with one registration, because
there is only one path to the model. A generated create *is* the placement,
because the hook runs inside it.

Returning an error is how "no tenant in this context" becomes impossible to
forget rather than merely documented: no statement runs at all.

### The seam is the statement, not the API boundary

Worth stating explicitly, because frameworks that offer something called a hook
usually mean the other thing. PocketBase and Django apply their access rules at
the request boundary and hand a hook the application — so a hook queries whatever
it likes, unconfined, and the rule you wrote for the API does not apply to it.
sqlb applies hooks to the **statement**, wherever it was issued from.

That is the whole reason one registration covers the generated handler, the
hand-written HTML form, the background job and the admin script alike. It has one
consequence that surprises everybody once: a hook's *own* statements are
statements too, so a hook reaching for another table runs that table's rules as
well. Usually correct, occasionally not, and
[hooks](../queries/hooks.md#the-handle-carries-the-rules-of-the-request-that-triggered-the-hook)
has the case that decides which.

## The four places a rule can live

This is the judgement the examples spend most of their prose on, so it is worth
stating as a set. They are not alternatives — a serious invariant uses several.

**A database constraint** is a guarantee. `CHECK (available_copies >= 0)` cannot
be bypassed by code that has not been written yet. It is the floor under
everything else, and it is the only layer that survives a bug upstream.

**A single conditional statement** is how a contended resource is decided.
`UPDATE books SET available_copies = available_copies - 1 WHERE id = $1 AND
available_copies >= 1` takes a row lock for its duration, so twenty concurrent
requests for the last copy are serialised by the database: one matches, nineteen
match nothing and their transactions roll back. Reading the row first and
deciding in Go is wrong, and would pass every test that runs one request at a
time.

**A hook** is a convention, and it is where the rule is *stated*. It runs inside
the caller's transaction, so returning an error rolls the write back. It is the
right place for scoping, for stamping an owner, for normalising a value, and for
turning an unclassifiable Postgres error into a 409 that says what went wrong.

**A trigger** is for what only the database can see. A rule that depends on what
a column is *becoming* — the old row and the new one at once — cannot be written
as a `BeforeUpdate` hook, because that hook receives the statement rather than
the assignments.

The ordering is deliberate: a hook is a convention, a constraint is a guarantee,
and having both is what makes a lost race a rolled-back transaction rather than
a library that believes it owns −1 copies.

## Which hook, and when it runs

| Hook | Receives | Use for |
|---|---|---|
| `BeforeQuery` | `*Builder[T]` — the query itself | Scoping every read. The load-bearing one |
| `BeforeCreate` | `*T` | Normalising, deriving, stamping an owner |
| `AfterCreate` | `*T`, defaults populated | Validation, and turning an insert into a transition |
| `BeforeUpdate` | `*Update[T]` | Forcing a column, narrowing affected rows |
| `BeforeDelete` | `*Delete[T]` | Narrowing, or refusing |
| `AfterCommit` | — | Anything the outside world can observe |

The write hooks run **inside** the caller's transaction. That is right for
validation — an error rolls the write back — and wrong for anything observable
outside it, which is what `AfterCommit` is for. `AfterCreate` publishing an event
means the transaction can still abort after the hook has already told the world
it succeeded.

`rest.Resource` wraps every generated write in a transaction, so `AfterCommit` is
reachable from a generated handler and a hook can read its own writes through
`sqlb.TxFrom(ctx)`.

## What hooks do not cover

Four gaps, all deliberate and all documented where they bite:

- **`BeforeUpdate` cannot read the assignments it was handed**, so a rule
  depending on what a column is becoming belongs in a `BEFORE` trigger
  ([ADR-0021](../adr/0021-hooks-receive-an-event.md)).
- **A hook's own statements run under the caller's rules**, including the rules of
  the model it is reaching for. Right when both models are scoped on the same
  axis, wrong when they are not, and the wrong case is a write that matches
  nothing without erroring — so a consequence acting past the request's scope
  needs a handle built for it
  ([hooks](../queries/hooks.md#the-handle-carries-the-rules-of-the-request-that-triggered-the-hook)).
- **Hooks follow an `?expand` join, with one restriction.** The target's
  `BeforeQuery` predicates are requalified onto the join alias and added to the
  join condition. A predicate that cannot be requalified — `RawPred`, or a
  column from a table the expansion did not join — fails the query rather than
  being dropped silently ([ADR-0030](../adr/0030-declared-scope-is-required.md)).
- **`AfterCommit` is in-process and at-most-once.** A callback that never ran
  because the process died leaves no trace. That is what a transactional outbox
  is for, and it is not built ([ADR-0012](../adr/0012-change-feed-outbox.md)).

## Read next

- [Hooks](../queries/hooks.md) — registration, scoping, `AfterCommit`, testing
- [ADR-0008](../adr/0008-hooks-as-domain-seam.md) — the decision record
- [ADR-0030](../adr/0030-declared-scope-is-required.md) — why a schema can oblige
  a hook to exist
