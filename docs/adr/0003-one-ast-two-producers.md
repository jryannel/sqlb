# ADR-0003: One predicate AST, two producers

- **Status:** Working
- **Confidence:** High
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-30

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
`filter` package produces it by parsing a request. Both feed the same builder,
the same compiler, and the same hooks.

The filter parser never emits SQL text. It emits `sqlb.Pred` values.

The `filter` package accepts more than one wire format — the URL grammar and a
JSON expression tree (`ParseFilterTree`) — but they are two frontends over one
compiler, not two producers. Each turns its own wire format into typed operands
and hands them to a single internal `applyOp`, which is the only place an
operator becomes a `Pred`. A JSON filter and the URL filter that means the same
thing compile to the byte-identical statement, and a test asserts it, so the two
cannot drift into two escaping strategies through the back door. Adding a
frontend is adding a parser; it is not adding a place where SQL is built.

## Consequences

**What this buys.** Bind-parameter discipline is enforced in exactly one place.
A `BeforeQuery` hook constrains HTTP-driven queries and hand-written ones
identically, so tenant scoping cannot be bypassed by going through the REST
layer. The filter grammar gets joins, grouping and aggregates for free as the
builder grows. And a second wire format — the JSON tree — cost a parser and a
value coercer, not a second compiler: it reuses the column gate, the operator
table, the coercion and the budget the URL frontend already had.

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
- 2026-07-30 — Added a JSON expression-tree frontend (`ParseFilterTree`)
  alongside the URL grammar. This is the "second grammar" the change-our-mind
  section anticipated, and it lands the way that section required: it terminates
  in `Pred`, sharing one `applyOp` compiler with the URL parser rather than
  growing a second path. The two producers stay two — Go and the filter package
  — the filter package simply reads two wire formats now.
