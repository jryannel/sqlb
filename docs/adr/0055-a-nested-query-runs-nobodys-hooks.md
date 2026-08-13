# ADR-0055: A query nests inside another, and a nested one is refused unless it has been resolved

- **Status:** Working — built in the engine, with the guard proved in both
  directions; untested against a real Postgres
- **Confidence:** Medium — the SQL and the refusal are exercised, the ergonomics
  of the two-step resolve are not yet worn in
- **Decided:** 2026-08-13
- **Last reviewed:** 2026-08-13

## Context

[ADR-0002](0002-queries-are-values.md) says a query is a value, and the whole
package leans on it: predicates are added conditionally, hooks amend a query
they are handed, and `filter` assembles one from a request. One thing a value
could not do was stand inside another query. `Field.OneOf` takes values, so a
set the *database* computes had to be written as `RawPred` — the escape hatch
whose contents are not validated, in the position where a mistake is a wrong
answer rather than a syntax error. `schema/field.go` reaches for exactly this,
declaring computed columns as `EXISTS (SELECT 1 FROM stars s …)` in text.

Enumerating the set instead has a ceiling that is now explicit: one bind
parameter per member until the protocol's 65,535 runs out, which `filter` caps
at 100 values and `InsertRows` refuses outright.

The obstacle was never the SQL. It is that **a nested query is compiled, not
run**, and hooks apply when a query runs. A model whose reads are confined by a
`BeforeQuery` scope — the mechanism [ADR-0030](0030-declared-scope-is-required.md)
makes mandatory for a mounted resource — would contribute its rows to somebody
else's `WHERE` clause with the confinement silently absent. Nothing in the
response shows it: the outer query returns the right *shape*, over a set chosen
by an unscoped read.

## Decision

**A `*Builder[T]` can be nested, and a nested query that would have run confined
is refused unless it has already been resolved.**

```go
sub, err := sqlb.Query[Post]().Select(sqlb.F("author_id")).Resolved(ctx, db)
authors, err := sqlb.Query[Author]().Where(sqlb.F("id").InQuery(sub)).All(ctx, db)
```

`Exists`, `NotExists`, `Field.InQuery` and `Field.NotInQuery` are the operators.
`Subquery` is an interface with unexported methods, so the set of
implementations is closed exactly as [`Expr`](../../expr.go)'s is: a nested query
compiles into the surrounding statement's compiler and shares its bind
numbering, which a type outside the package could not do.

Three things follow from the position rather than from taste:

- **The refusal is computed, not registered.** `subUnresolved` runs the model's
  query hooks and asks whether they yield predicates *for this handle in this
  context*. Testing for a registry entry instead would refuse every nested query
  in any application that uses hooks at all, because `hooksFor` materialises an
  empty set for every model it is asked about — and a handle that dropped a
  named scope with `WithoutScope` ([ADR-0054](0054-a-named-scope-is-releasable-at-the-mount.md))
  correctly has nothing to be missing.
- **The walk enumerates.** Every clause that can carry an expression is visited,
  and each nested query is asked for its own nested queries, because a clause
  left out is one whose subquery skips the check — this fails open by omission.
  `Raw` holds text rather than nodes and is therefore invisible to it, which is
  what `Raw` means everywhere else in this package.
- **A nested SELECT qualifies to its own table, always.** `Builder.compile`
  qualifies only when a statement joins; nesting is the other way a bare name
  becomes ambiguous, and `WHERE id IN (SELECT id FROM …)` left to Postgres's
  resolution rules is how a subquery over the same table as its parent silently
  becomes correlated.

**Auto-resolving the inner query was rejected.** Resolution has to produce a new
value — the receiver is untouched on every exec path — so doing it inside the
outer query's compile would mean rewriting a caller's expression tree, and a
rewriter that misses a node type fails open in exactly the way the guard exists
to prevent. Refusing needs the same walk and nothing more.

## Consequences

**Buys.**

- A set the database computes, with the model validating both sides — one fewer
  reason to reach for `RawPred`, in the position where its lack of validation
  costs most.
- The bind-parameter ceiling stops being a ceiling on the *question*: `InQuery`
  matches against a million rows and binds nothing.
- The scope hole is closed before anyone can fall into it, rather than after.

**Costs.**

- Nesting a confined model is two steps and an error message teaches you so.
  That is friction in the exact place people will first try it.
- `Builder` carries a `resolved` flag, which survives `Clone` and can therefore
  be carried onto a query that has since been widened. Adding predicates only
  narrows a conjunction, so this is sound today and would stop being sound if a
  disjunctive `Where` were ever added.
- A cycle is now expressible (`q.Where(F("id").InQuery(q))`) and is caught by a
  depth backstop in the compiler and a visited set in the walk, rather than by
  being impossible.

## What would change our mind

- If the two-step resolve is worked around in practice — callers reaching for
  `RawPred` to avoid it — the friction is buying nothing and auto-resolution is
  worth its rewriter.
- If a nested query is wanted somewhere the walk does not reach (a `Raw`
  fragment, a computed column's declared SQL), the guard is weaker than this
  record claims and the refusal should move to where the text is assembled.
- If correlated subqueries are asked for — a nested query referring to the outer
  row — the unconditional qualification above is exactly wrong for them and they
  need a way to name the outer table deliberately.

## Cost of change

Cheap to widen, expensive to narrow, and the asymmetry is the usual one. Adding
operators (`= ANY`, a scalar subquery in a projection) is additive. Removing the
refusal later is a one-line deletion; adding one after applications have nested
unscoped queries means auditing every call site, which is the position this
record exists to avoid.

## Open questions I had to answer myself

- **Whether `EXISTS` should narrow its subquery's projection to `SELECT 1`.** It
  does not. Postgres does not evaluate the projection under `EXISTS`, so the
  cost is a longer statement rather than more work, and rewriting a caller's
  query to save characters is the kind of cleverness that surprises someone
  reading a log.
- **Whether the width check for `IN` belongs at build time or compile time.** At
  compile time: `Field.InQuery` returns a `Pred`, which has no error channel,
  and a compile-time failure reaches the caller through `SQL()` like every other
  one.
- **Whether writes should be guarded too.** Yes, and they are the sharper case —
  a nested query in an `UPDATE … WHERE` chooses which rows change. `Update` and
  `Delete` check on their own resolve paths.
- **Whether `Builder.SQL()` should refuse a nested confined query.** No. `SQL()`
  has never applied hooks and says so; making it refuse would mean it needed an
  `Executor`, which is the signature it exists to not have.

## Revisions

- 2026-08-13 — Written.
