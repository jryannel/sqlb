# ADR-0020: The transaction handle is built now, not with Go 1.27

- **Status:** Working
- **Confidence:** Medium
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

`Executor` is `QueryContext` + `ExecContext`, which `*sql.Tx` satisfies, so a
caller could always run sqlb statements inside a transaction by threading the
`*sql.Tx` through every terminal call. Nothing sat on top of that:

- No `WithTx`, so every caller wrote its own begin/commit/rollback and its own
  panic handling. The rollback-on-panic case is the one people forget, and
  forgetting it leaks a connection with an open transaction.
- The hook registry was a package-level `sync.Map`
  ([ADR-0008](0008-hooks-as-domain-seam.md)), so hooks could not be scoped and
  — the sharper problem — a hook had no way to learn it was inside a unit of
  work. A `BeforeQuery` that needed to see rows written earlier in the same
  transaction could not reach them: whatever executor it found would be the
  pool, and the rows were not committed yet.

The README deferred all of this to Go 1.27, on the grounds that `db.Query[T]()`
needs methods to declare type parameters. That reasoning is right about the
*ergonomics* and wrong about the *scoping*. The object graph — a handle holding
an executor and a registry — needs no new language feature. Only the call syntax
does.

Waiting was therefore not a neutral choice. Every month of deferral adds call
sites written against the process-global registry, and those are what a later
migration has to find.

## Decision

Build `*sqlb.DB` now, and make it additive by having it satisfy `Executor`.

```go
db := sqlb.New(pool)                       // hooks: the process default
tenant := db.WithHooks(sqlb.NewRegistry()) // hooks: scoped to this handle

err := db.WithTx(ctx, func(ctx context.Context, tx *sqlb.DB) error {
    _, err := sqlb.InsertRows(&order).One(ctx, tx)
    return err
})
```

Four things follow from `*DB` being an `Executor`:

1. **No call site breaks.** Every terminal already takes an `Executor`, so a
   `*DB` goes wherever a `*sql.DB` went. `rest.Resource` needed no change at
   all — the report that prompted this expected it to grow a `*DB` parameter,
   and it did not, because it already accepts the interface.
2. **Hooks resolve from the executor.** One unexported `hooksFor[T]` asks
   whether the executor is a `*DB` and uses its registry if so, falling back to
   the process default. Terminal signatures are untouched.
3. **`sqlb.On[T]()` keeps working**, now as a wrapper over a default registry.
   Existing registrations compile and behave identically.
4. **Go 1.27 stays additive.** The methods that arrive then hang off this same
   handle; the package-level functions remain as wrappers, exactly as the
   README's table predicts.

`WithTx` commits on nil, rolls back on error, and rolls back and re-raises on
panic. It requires the underlying executor to implement `Beginner` — asserted
for, not required of `Executor`, so the two-method interface stays frozen and
every existing wrapper stays valid.

**Nesting joins rather than nests.** `WithTx` on a handle already in a
transaction runs the function on that transaction and leaves the commit to the
outermost call. A function that opens a transaction therefore stays callable
from inside one.

**`TxFrom(ctx)` is how a hook joins the unit of work.** `WithTx` passes its
function a context carrying the transaction handle, so a hook that needs to read
uncommitted rows reads through it.

## Consequences

**What this buys.** A multi-statement unit of work is one call with correct
rollback semantics, including on panic. Hooks can be scoped to a handle, which
makes test isolation a fresh `Registry` rather than a `Reset` in a teardown, and
makes two sets of domain rules in one process expressible. A hook can tell it is
inside a transaction and read what that transaction has written — the case that
was previously not expressible at all. `AfterCommit` now has an obvious home:
`WithTx` is the only code that knows a commit succeeded.

**What this costs.** `sqlb.QueryIn[T](tx)` is not offered, so the executor is
still threaded through terminal calls — `All(ctx, tx)` rather than
`tx.Query[T]().All(ctx)`. That is the ergonomic half that genuinely does need
1.27, and it is still ugly until then.

`*DB` being an `Executor` means `New(New(x))` type-checks. It is harmless — the
inner handle is just another executor — but it is a shape that admits a
meaningless call.

Hook resolution now depends on the *dynamic type* of the executor. Passing the
raw `*sql.DB` where a `*DB` was intended silently uses the default registry
rather than the scoped one. The compiler cannot catch it, because both satisfy
`Executor`; that is the price of not breaking call sites.

Nesting-joins means an inner failure rolls back the whole outer unit of work.
That is the right default and it can still surprise someone who expected the
inner block to be independent.

## What would change our mind

- **If someone needs partial rollback** — retrying one leg of a unit of work
  without discarding the rest — savepoints become necessary and joining is no
  longer sufficient. That is a real feature request, not a bug in this design,
  and the seam for it is `WithTxOptions`.
- **If the dynamic-type hook resolution bites** — someone passes a `*sql.DB`
  where they meant a scoped `*DB` and gets the wrong registry without noticing
  — the answer is to stop having terminals accept a bare `Executor` for models
  whose hooks are scoped, which is a breaking change worth making only once it
  has actually happened.
- **If `Beginner` proves too narrow** — a driver that wants its own transaction
  type rather than `*sql.Tx` cannot implement it. `pgx` through its stdlib
  adapter does, which is the case that matters today.
- **If nobody uses `WithHooks`** after a few months, the registry scoping is
  complexity without a customer, and `Registry` should collapse back into the
  global with only `TxFrom` retained.

## Cost of change

Low in the additive direction and moderate in reverse. Adding savepoints,
`AfterCommit`, or 1.27 methods are all additions to `DB` that break nothing.

Removing the handle would mean deciding what happens to `TxFrom` — the hook
capability it enables has no other home — so the reverse direction is not
symmetric with the forward one.

Removing `Registry` while keeping `DB` is cheap: `hooksFor` collapses to
`On[T]()` and `WithHooks` goes away.

## Alternatives considered

**Wait for Go 1.27.** The status quo, and genuinely close — the end-state API is
nicer and arrives without an intermediate step. It lost because the intermediate
step is not a step: the handle built now *is* the object 1.27's methods hang
off, so nothing is thrown away. Meanwhile three years of call sites would be
written against a global registry.

**`QueryIn[T](d *DB) *Builder[T]`**, as the review that prompted this suggested.
Rejected as unnecessary once `*DB` satisfies `Executor` — it would add a second
way to start every statement while the existing way already works, and the
1.27 method form makes both obsolete.

**Put the transaction in the context and read it implicitly**, so terminals pick
it up without being passed it. Rejected: it makes which connection a statement
runs on invisible at the call site, which is the same action-at-a-distance
ADR-0008 already lists as the cost of global hooks. `TxFrom` deliberately makes
the caller ask.

**Savepoints for nesting.** Rejected for now, not on principle. Partial rollback
changes what "the unit of work succeeded" means, and no caller needs it yet.

## Revisions

- 2026-07-27 — Written, closing finding 1 of
  [the adoption review](../review-adoption-readiness.md).
