# ADR-0007: Generate per-resource REST handlers and OpenAPI

- **Status:** Exploring
- **Confidence:** Low
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

Given a schema and a filter grammar, the REST surface can be delivered two ways.
One generic handler can serve every exposed table by dispatching on the path,
which means adding a table costs zero lines. Or each resource gets a generated
handler with a typed filter struct and a precise OpenAPI operation.

The trade is boilerplate elimination against client-side typing. A generic
handler cannot describe itself precisely in OpenAPI, because the filter grammar
is compositional — `?age=gte.18` is not a fixed parameter set — so the generated
client ends up with loose types.

## Decision

Generate a handler per exposed resource, plus a precise OpenAPI document and a
TypeScript client derived from the schema's capability flags.

## Consequences

**What this buys.** End-to-end typing into the frontend: a filter that does not
exist fails at the client's compile step rather than as a 400 at runtime. Each
resource can carry its own middleware, and the generated handler is ordinary
readable Go that can be stepped through in a debugger.

**What this costs.** Considerably more generated code than one dispatcher, and
the generator has to produce a correct OpenAPI schema for a compositional filter
grammar, which is the hardest part of the whole generator. Nothing here is built
yet, hence Low confidence.

## What would change our mind

- If the OpenAPI generator for the filter grammar turns out to be
  disproportionately hard, fall back to one generic handler plus a
  hand-maintained spec, and accept looser client types.
- If generated handler volume becomes a build-time or review problem at
  realistic table counts (say fifty tables), reconsider the dispatcher.
- If in practice nobody consumes the generated TypeScript client, most of the
  benefit evaporates and the generic handler wins on simplicity.

## Cost of change

Free today, since nothing is built.

After handlers are generated and a TypeScript client is in use, moving to a
generic dispatcher changes every generated client type, which means a
coordinated frontend change. The runtime supports both shapes, so the cost is
entirely in the generated surface and its consumers — not in the engine.

## Alternatives considered

**One generic handler (PostgREST-style).** Zero per-resource code, and the
runtime already supports it — `filter.Parse` plus `filter.Apply` is the whole
implementation. Genuinely close, and the fallback if the spec generator proves
too hard.

**Generic handler plus generated OpenAPI.** The best of both if the spec
generator works. Effectively the same bet as the chosen option, minus the
per-resource Go.

## Revisions

- 2026-07-27 — Written.
