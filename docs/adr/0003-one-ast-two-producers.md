# ADR-0003: One predicate AST, two producers

- **Status:** Working
- **Confidence:** High
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-30

## Context

A query gets built two ways: a developer writes it in Go, or a client sends
filter parameters over HTTP. Giving each its own path means two escaping
strategies, two places to enforce authorisation, and two things to keep in sync
when a column changes. That is where the injection risk lives.

## Decision

There is one predicate AST. Go code produces it through `sqlb.F(...)`; the
`filter` package produces it by parsing a request. Both feed the same builder,
compiler and hooks. The filter parser never emits SQL text — it emits
`sqlb.Pred` values.

The `filter` package reads two wire formats (the URL grammar and a JSON
expression tree via `ParseFilterTree`), but they are two frontends over one
compiler: both hand typed operands to a single internal `applyOp`. Equivalent
filters compile to byte-identical statements, and a test asserts it.

## Consequences

**Buys.** Bind-parameter discipline is enforced in exactly one place. A
`BeforeQuery` hook constrains HTTP-driven and hand-written queries identically,
so tenant scoping cannot be bypassed via REST. New builder features reach the
filter grammar for free, and a second wire format cost a parser, not a compiler.

**Costs.** The AST serves both, so it carries nodes the filter grammar will never
produce and cannot be tuned narrowly for either. The filter parser must coerce
types up front, because the AST holds typed Go values rather than strings.

## What would change our mind

- The filter grammar needs an expression the builder cannot represent and
  modelling it distorts the AST — it may need its own compilation step, but it
  must still terminate in `Pred` values.
- Parse-time coercion proves too rigid for a column type we care about.

## Cost of change

Small in lines, large in risk. Splitting the producers is easy to do and hard to
undo: it reintroduces two escaping paths and two authorisation points, and the
resulting bugs are security bugs that surface late.

## Revisions

- 2026-07-27 — Written.
- 2026-07-30 — Added the JSON expression-tree frontend. It lands as this record
  required: terminating in `Pred`, sharing one `applyOp` with the URL parser.
- 2026-07-30 — Condensed.
