# ADR-0009: A generated typed column facade; predicates stay untyped

- **Status:** Working
- **Confidence:** Medium
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

[ADR-0005](0005-runtime-query-engine.md) makes the engine reflective, so
`sqlb.F("titel")` is a runtime error. That is the single largest cost of the
design, and it matters most in exactly the workflow this project is aimed at:
an agent writing queries, for which a compile error is a fast correction signal
and a runtime error is a slow one.

Since codegen is already emitting models, a typed facade is nearly free.

## Decision

Generate a typed column set per table: `PostCols.Status` is a
`sqlb.Col[PostStatus]`, `PostCols.Title` is a `sqlb.TextCol[string]`. Predicate
construction is type-checked; the builder stays generic.

Three supporting choices:

- **`Col[T]` does not embed `Field`.** Embedding promotes every operator onto
  every column, which made `Contains` callable on an integer. The pattern
  operators live on `TextCol[T ~string]` instead.
- **Nullable columns are typed as their base type.** `published_at` is
  `*time.Time` on the model but `Col[time.Time]` here, so the comparand is a
  value and NULL is expressed with `IsNull`.
- **Hidden columns are omitted from the facade**, so a predicate against one
  cannot be written at all.

Update statements are wrapped too, because `Set(string, any)` checks neither the
column name nor the value type. The select builder is not wrapped.

## Consequences

**What this buys.** Misspelled columns, wrong comparand types and text operators
on non-text columns all fail at compile time. The cost is one small generated
file per table rather than a whole generated builder API.

**What this costs.** Predicates remain untyped (`sqlb.Pred`, not `Pred[T]`), so
a column from the wrong table still compiles and fails at the database. The
facade is a second thing the generator must keep in step with the model.

## What would change our mind

- If cross-table column mixing turns out to be a common mistake rather than a
  theoretical one, revisit `Pred[T]` — but see the join problem below.
- If wrapping update statements per table proves to be too much generated code
  at scale, drop the wrapper and rely on schema validation to catch bad column
  names at generation time instead.

## Cost of change

Low in the direction of removal — the facade is additive, so deleting it breaks
only the call sites that use it, and the engine is untouched.

High in the direction of `Pred[T]`. Binding predicates to their model changes
the AST, every combinator (`And`, `Or`, `Not`, `If`), the filter package's
intermediate representation, and the join API. That is the expensive change, and
it is the one to think hard about before attempting.

## Alternatives considered

**Bind predicates to their model (`Pred[T]`).** Would close the cross-table
hole. Rejected because join conditions reference two tables and so cannot be
`Pred[T]` for any single `T`; the type-erasing escape hatch that would require
reopens the hole anyway. Cross-table mistakes also fail loudly on first
execution, unlike a typo in a rarely-hit branch.

**Wrap the select builder as well.** Rejected: twenty-odd chainable methods
would each need their return type re-wrapped, for safety the column set already
provides.

## Revisions

- 2026-07-27 — Written.
- 2026-07-27 — Un-embedded `Field` from `Col[T]` and added `TextCol` after a
  compile check showed `ViewCount.Contains("x")` was accepted.
