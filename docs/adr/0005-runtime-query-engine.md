# ADR-0005: The query engine is reflective, not generated

- **Status:** Working
- **Confidence:** Medium
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

Since we generate code anyway ([ADR-0004](0004-schema-as-go-dsl.md)), the query
builder could be generated per table — `Users.Age.Gte(18)` as real Go, the way
ent does it. The trade is compile-time safety against generated-code volume: a
generated builder catches a hallucinated column name at compile time, which
matters when an agent writes the queries, but adds hundreds of lines of API
surface per table.

## Decision

The query engine is generic and reflective: `sqlb.Query[T]()` builds a model from
`db` and `sqlb` struct tags, cached per type. Column references are strings at
the core (`sqlb.F("age")`). Compile-time safety is recovered separately by a thin
generated facade ([ADR-0009](0009-typed-column-facade.md)).

## Consequences

**Buys.** The engine is one small package rather than a template that must
produce a correct API per table. It works on any tagged struct, including ones
sqlb did not generate — which is what makes
[ADR-0010](0010-codegen-is-optional.md) possible. Builder features benefit every
table at once, with no regeneration.

**Costs.** `sqlb.F("titel")` compiles and fails at runtime. Reflective scanning is
slower than generated field access, though unmeasured against real query latency.
The engine must validate column names itself.

## What would change our mind

- The typed facade proves insufficient and column-name typos still reach
  production — then generate the builder after all.
- Profiling shows reflective scanning is a meaningful share of request time on
  realistic result sets — then generate scan functions, keeping the builder
  generic.

## Cost of change

Moderate and mechanical. Generated builders mean writing the generator and
migrating every call site — large but rote, and stageable behind a shim. The
reflective engine stays regardless, because ADR-0010 depends on it. Deleting the
typed facade is cheap: it is additive.

## Revisions

- 2026-07-27 — Written.
- 2026-07-27 — Added the typed facade as the mitigation once ADR-0009 landed.
- 2026-07-30 — Condensed.
