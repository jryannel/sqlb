# ADR-0005: The query engine is reflective, not generated

- **Status:** Working
- **Confidence:** Medium
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

Given that we generate code anyway ([ADR-0004](0004-schema-as-go-dsl.md)), the
query builder could be generated per table — `Users.Age.Gte(18)` as real
generated Go, the way ent does it. The alternative is a generic engine that
reflects over struct tags at runtime.

The trade is compile-time safety against generated-code volume. A generated
builder catches a hallucinated column name at compile time, which matters a lot
when an agent is writing the queries. It also means every table adds hundreds of
lines of generated API surface.

## Decision

The query engine is generic and reflective: `sqlb.Query[T]()` builds a model
from `db` and `sqlb` struct tags, cached per type. Column references are
strings at the core (`sqlb.F("age")`).

Compile-time safety is recovered separately by a thin generated facade — see
[ADR-0009](0009-typed-column-facade.md) — rather than by generating the builder.

## Consequences

**What this buys.** The engine is one small package rather than a template that
has to produce a correct API for every table. It works on any tagged struct,
including ones sqlb did not generate, which is what makes
[ADR-0010](0010-codegen-is-optional.md) possible. Adding a builder feature
benefits every table at once, with no regeneration.

**What this costs.** `sqlb.F("titel")` compiles and fails at runtime. Reflection
on the scan path is slower than generated field access, though the model is
cached and the cost has not been measured against real query latency. The engine
must validate column names itself, since the compiler will not.

## What would change our mind

- If the typed facade proves insufficient in practice and column-name typos
  still reach production, generate the builder after all.
- If profiling shows reflective scanning is a meaningful share of request time
  on realistic result sets, generate scan functions per model while keeping the
  builder generic.
- Go 1.27 generic methods do not change this decision, but they do let
  `Collect[R]` and a `*DB` handle read better. See
  [ADR-0013 when written](.), or the README.

## Cost of change

Moderate and mechanical. Switching to generated builders means writing the
generator and migrating every query call site — large but rote, and a
compatibility shim could stage it. The reflective engine would stay regardless,
because [ADR-0010](0010-codegen-is-optional.md) depends on it.

Going the other way, deleting the typed facade, is cheap: it is additive, and
removing it only breaks call sites that opted into it.

## Alternatives considered

**Generated builders per table (ent-style).** Best compile-time errors. Lost on
generated-code volume and because it would make the engine unusable on structs
sqlb did not generate.

**Hybrid: reflective engine, generated typed facade.** This is what we ended up
with, arrived at from the reflective side rather than chosen up front.

## Revisions

- 2026-07-27 — Written.
- 2026-07-27 — Added the typed facade as the mitigation once ADR-0009 landed;
  the original decision accepted runtime column errors outright.
