# ADR-0002: Queries are values, not statements

- **Status:** Working
- **Confidence:** High
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

Static query generators cannot express *"this WHERE clause exists only when the
user typed something in the search box."* A view with optional filters, search,
user-chosen sorting and pagination has a combinatorial number of SQL strings.
Teams work around this by concatenating SQL, which reintroduces every problem the
typed generator was adopted to solve.

## Decision

A query is a value built up incrementally. `Where` appends rather than replacing;
the zero `Pred` is a skipped no-op; construction is separate from execution, and
`SQL()` renders without running anything.

```go
q := sqlb.Query[Post]().Where(sqlb.If(search != "", sqlb.F("title").Contains(search)))
```

## Consequences

**Buys.** Conditional filters need no branching at the call site. The same value
can be amended by a hook, produced by the REST filter parser, inspected in a
test, or printed for `EXPLAIN`. Hooks, the filter grammar and query introspection
all rest on this one property.

**Costs.** Builders mutate in place and return themselves, so a shared base query
can be aliased by accident; `Clone` exists but has to be remembered. Errors are
sticky, so a mistake surfaces at the terminal method rather than at its source.

## What would change our mind

- A real aliasing bug in practice — then switch to copy-on-write, accepting the
  extra allocation.
- Sticky errors proving hard to trace — then attach the call site to the error.

## Cost of change

Wholesale reversal is a different project: hooks, the filter grammar and query
introspection all depend on it. The narrower move to copy-on-write is a day's
work — builder methods plus the hook signature — and breaks anyone's hooks.

## Revisions

- 2026-07-27 — Written.
- 2026-07-30 — Condensed.
