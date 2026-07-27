# ADR-0002: Queries are values, not statements

- **Status:** Working
- **Confidence:** High
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

The project exists because static query generators cannot express *"this WHERE
clause exists only when the user typed something in the search box."* A tool
like sqlc compiles a fixed SQL string into a typed function; a data view with
optional filters, a search box, user-chosen sorting and pagination has a
combinatorial number of such strings.

In practice teams work around this by concatenating SQL, which reintroduces
every problem the typed generator was adopted to solve.

## Decision

A query is a value that can be built up incrementally. `Where` appends rather
than replacing; the zero `Pred` is a no-op that is skipped; construction is
separate from execution, and `SQL()` renders without running anything.

```go
q := sqlb.Query[Post]().Where(sqlb.If(search != "", sqlb.F("title").Contains(search)))
```

## Consequences

**What this buys.** Conditional filters need no branching at the call site.
The same value can be handed to a hook to be amended, produced by the REST
filter parser, inspected in a test, or printed for `EXPLAIN`. This one property
is what makes hooks, the filter grammar and query introspection all possible.

**What this costs.** Builders mutate in place and return themselves, so a
shared base query can be aliased by accident; `Clone` exists but has to be
remembered. Errors are sticky rather than returned per call, which means a
mistake surfaces at the terminal method rather than at its source.

## What would change our mind

- If in-place mutation causes a real aliasing bug in practice, switch to
  copy-on-write: every method returns a new builder. It is slower and allocates
  more, but the footgun disappears.
- If sticky errors make a failure hard to trace, attach the call site to the
  recorded error.

## Cost of change

Reversing this wholesale is not a refactor — hooks, the filter grammar and query
introspection all rest on it, so removing it is a different project.

The narrower change is cheap: moving from in-place mutation to copy-on-write is
mechanical, and touches only the builder methods plus the hook signature, which
would return a query instead of amending one. A day's work, and a breaking
change for anyone who has written hooks.

## Alternatives considered

**Immutable builders (copy-on-write).** Genuinely close. Rejected for now
because hooks receive a `*Builder` and amend it, which reads far better as
mutation than as "return the modified query". Revisit if aliasing bites.

**Functional options** — `Query(Where(...), OrderBy(...))`. Rejected: options
have to be collected before the call, which is exactly the branching we are
trying to remove.

## Revisions

- 2026-07-27 — Written.
