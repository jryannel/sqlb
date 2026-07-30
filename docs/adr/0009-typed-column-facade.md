# ADR-0009: A generated typed column facade; predicates stay untyped

- **Status:** Working
- **Confidence:** Medium
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

[ADR-0005](0005-runtime-query-engine.md) makes the engine reflective, so
`sqlb.F("titel")` is a runtime error. That is the design's largest cost, and it
bites hardest in the workflow this project targets: an agent writing queries, for
which a compile error is a fast correction signal and a runtime error is a slow
one. Since codegen already emits models, a typed facade is nearly free.

## Decision

Generate a typed column set per table: `PostCols.Status` is a
`sqlb.Col[PostStatus]`, `PostCols.Title` is a `sqlb.TextCol[string]`. Predicate
construction is type-checked; the builder stays generic.

- **`Col[T]` does not embed `Field`.** Embedding promoted every operator onto
  every column, which made `Contains` callable on an integer. Pattern operators
  live on `TextCol[T ~string]`.
- **Nullable columns are typed as their base type**, so the comparand is a value
  and NULL is expressed with `IsNull`.
- **Hidden columns are omitted**, so a predicate against one cannot be written.

Update statements are wrapped too, since `Set(string, any)` checks neither name
nor type. The select builder is not wrapped — twenty-odd chainable methods would
each need their return type re-wrapped for safety the column set already gives.

## Consequences

**Buys.** Misspelled columns, wrong comparand types and text operators on
non-text columns fail at compile time, for one small generated file per table.

**Costs.** Predicates stay untyped (`sqlb.Pred`, not `Pred[T]`), so a column from
the wrong table compiles and fails at the database. The facade is a second thing
the generator must keep in step with the model.

## What would change our mind

- Cross-table column mixing turns out to be a common mistake rather than a
  theoretical one — then reconsider `Pred[T]`, knowing join conditions reference
  two tables and cannot be `Pred[T]` for any single `T`.
- Wrapping update statements proves to be too much generated code at scale — drop
  the wrapper and rely on schema validation at generation time.

## Cost of change

Removal is cheap: the facade is additive, so deleting it breaks only call sites
that use it. `Pred[T]` is the expensive direction — it changes the AST, every
combinator, the filter package's intermediate representation and the join API.

## Revisions

- 2026-07-27 — Written.
- 2026-07-27 — Un-embedded `Field` from `Col[T]` and added `TextCol` after a
  compile check showed `ViewCount.Contains("x")` was accepted.
- 2026-07-30 — Condensed.
