# ADR-0003: One predicate AST, two producers

- **Status:** Working
- **Confidence:** High
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

There are two ways a query gets built: a developer writes it in Go, or a client
sends filter parameters over HTTP. The obvious implementation gives each its own
path — hand-written queries go through a builder, and the REST layer assembles
SQL from query parameters.

That is how the boilerplate and most of the injection risk gets in. Two paths
means two escaping strategies, two places to enforce authorisation, and two
things to keep in sync when a column changes.

## Decision

There is one predicate AST. Go code produces it through `sqlb.F(...)`, and the
`filter` package produces it by parsing URL parameters. Both feed the same
builder, the same compiler, and the same hooks.

The filter parser never emits SQL text. It emits `sqlb.Pred` values.

## Consequences

**What this buys.** Bind-parameter discipline is enforced in exactly one place.
A `BeforeQuery` hook constrains HTTP-driven queries and hand-written ones
identically, so tenant scoping cannot be bypassed by going through the REST
layer. The filter grammar gets joins, grouping and aggregates for free as the
builder grows.

**What this costs.** The AST has to serve both, so it carries nodes the filter
grammar will never produce and cannot be tuned narrowly for either. The filter
parser has to do type coercion up front, because the AST holds typed Go values
rather than strings.

## What would change our mind

- If the filter grammar needs an expression the builder cannot represent, and
  modelling it distorts the AST, the grammar may need a compilation step of its
  own — but it should still terminate in `Pred` values.
- If coercion at parse time proves too rigid for a column type we care about,
  consider a deferred-coercion node that resolves at compile time.

## Cost of change

Small in lines, large in risk. Splitting the producers apart is easy to do and
hard to undo, because it reintroduces two escaping paths and two places to
enforce authorisation — precisely the class of bug the single AST exists to
prevent. The cost is not the work; it is that the resulting bugs are security
bugs and they surface late.

## Alternatives considered

**Separate REST query compiler.** Rejected: it duplicates escaping and
authorisation, which is where the security bugs live.

**Make the REST layer call the database directly, PostgREST-style.** Rejected:
no place for Go domain logic, which is the point of the project.

## Revisions

- 2026-07-27 — Written.
