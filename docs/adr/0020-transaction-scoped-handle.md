# ADR-0020: The transaction handle is built now, not with Go 1.27

- **Status:** Working
- **Confidence:** Medium
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-30 (the `Beginner` trigger fired; see Revisions)

## Context

`Executor` is the two-method subset a transaction satisfies — `QueryContext` and
`ExecContext` when this was written, `Query` and `Exec` over pgx since
[ADR-0040](0040-the-driver-is-a-dependency.md) — so a caller could always thread
a transaction through terminal calls. Nothing sat on top
of that: no `WithTx`, so every caller wrote its own begin/commit/rollback and its
own panic handling — and forgetting the rollback-on-panic leaks a connection with
an open transaction. The hook registry was a package-level `sync.Map`, so hooks
could not be scoped and, worse, a hook had no way to learn it was inside a unit
of work: a `BeforeQuery` needing rows written earlier in the same transaction
would find the pool.

The README deferred this to Go 1.27, because `db.Query[T]()` needs methods with
type parameters. That is right about the *ergonomics* and wrong about the
*scoping* — a handle holding an executor and a registry needs no new language
feature. Waiting was not neutral: every month adds call sites written against the
process-global registry.

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

Four things follow from `*DB` being an `Executor`: no call site breaks, since
every terminal already takes the interface (`rest.Resource` needed no change at
all); hooks resolve from the executor through one unexported `hooksFor[T]`, so
terminal signatures are untouched; `sqlb.On[T]()` keeps working as a wrapper over
a default registry; and Go 1.27's methods stay additive, hanging off this same
handle.

`WithTx` commits on nil, rolls back on error, and rolls back and re-raises on
panic. It asserts the executor implements `Beginner` rather than requiring it of
`Executor`, so the two-method interface stays frozen.

**Nesting joins rather than nests.** `WithTx` on a handle already in a
transaction runs the function on that transaction and leaves the commit to the
outermost call, so a function that opens a transaction stays callable from inside
one. **`TxFrom(ctx)` is how a hook joins the unit of work** — deliberately an
explicit ask, not an implicit context read, which would make the connection a
statement runs on invisible at the call site.

## Consequences

**Buys.** A multi-statement unit of work is one call with correct rollback
semantics, including on panic. Hooks can be scoped to a handle, making test
isolation a fresh `Registry` rather than a teardown `Reset`. A hook can tell it is
inside a transaction and read what that transaction wrote — previously not
expressible. `AfterCommit` got an obvious home: `WithTx` is the only code that
knows a commit succeeded.

**Costs.** `sqlb.QueryIn[T](tx)` is not offered, so the executor is still threaded
through terminals — `All(ctx, tx)` rather than `tx.Query[T]().All(ctx)`. That is
the ergonomic half that genuinely needs 1.27.

Hook resolution depends on the executor's *dynamic type*: passing a raw pool
where a `*DB` was intended silently uses the default registry. The compiler
cannot catch it, because both satisfy `Executor` — the price of not breaking call
sites. Nesting-joins also means an inner failure rolls back the whole outer unit
of work.

## What would change our mind

- Someone needs partial rollback — savepoints become necessary and joining is no
  longer sufficient. The seam for it is `WithTxOptions`.
- The dynamic-type hook resolution bites — then terminals should stop accepting a
  bare `Executor` for models whose hooks are scoped, a breaking change worth
  making only once it has actually happened.
- **`Beginner` proves too narrow — this has fired.** Not a hypothetical driver: a
  caller holding an already-open `pgx.Tx`, which the adoption reviews count 25
  sites of. [ADR-0040](0040-the-driver-is-a-dependency.md) redefines `Beginner`
  alongside `Executor` rather than widening it, because the narrowness is the
  driver's. Nothing else here moves.
- Nobody uses `WithHooks` after a few months — registry scoping is complexity
  without a customer, and `Registry` should collapse back into the global with
  only `TxFrom` retained.

## Cost of change

Low additively — savepoints, `AfterCommit` and 1.27 methods all break nothing.
Moderate in reverse: removing the handle means deciding what happens to `TxFrom`,
which has no other home. Removing `Registry` while keeping `DB` is cheap.

## Revisions

- 2026-07-27 — Written, closing finding 1 of
  [the adoption review](../review-adoption-readiness.md).
- 2026-07-27 — `AfterCommit` built on the handle, closing finding 2. Registering
  outside a transaction is refused, since under autocommit sqlb does not own the
  commit. `ErrAfterCommit` distinguishes "committed, side effect failed" from
  "the unit of work failed".
- 2026-07-30 — The `Beginner` trigger fired; ADR-0040 is the answer. The
  transaction-scoped design is unaffected — what changes is the concrete type the
  handle wraps.
- 2026-07-30 — Condensed.
