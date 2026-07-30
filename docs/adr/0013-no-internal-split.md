# ADR-0013: No public/internal package split

- **Status:** Working
- **Confidence:** Medium
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

Package `sqlb` exports 189 identifiers: some are the daily API, some are public
only because another package needs them, some are the compiler's vocabulary. Go
offers `internal/` to make that distinction compiler-enforced.

Two facts decided against it. The genuinely internal machinery — compiler,
scanning, model building, escaping — is already unexported within the package, 36
identifiers of it; `internal/` would restate a boundary that already holds. And
the obvious extraction fails: `Expr` and `Raw` must stay public as the documented
escape hatch, so hiding the remaining eight node types buys only field renaming
while forcing `Pred.Expr()` to return a type callers cannot name.

## Decision

No `internal/` packages. Keep the flat layout and express the distinction as
documented tiers plus a `v0` version:

- **Stable** — query builder, predicates, typed column facade, hooks, mutations,
  `Describe`, the `filter` package, the `schema` DSL. Changes are breaking
  changes and treated as such.
- **Provisional** — `Model`, `ColumnInfo`, `ModelOf`, `Selectable`, `Selection`,
  `Dialect`, `Postgres`. Public because `filter` and generated code cross a
  package boundary. Expect these to move.
- **Escape hatch** — `Expr` and the node types. Use `Raw`, `RawPred`, `RawSel`;
  the rest is compiler vocabulary and will change without ceremony.

## Consequences

**Buys.** No ceremony or import gymnastics, and no premature boundary in a pre-1.0
library still moving. Tiers communicate intent per identifier, where `internal/`
works only per package.

**Costs.** Tiers are convention, not a compiler check — someone can depend on
`Binary` and be broken with only a doc comment to point at. A reader cannot tell
the tiers apart without consulting the docs.

## What would change our mind

- Someone outside the module depends on a node type and is broken by a compiler
  change — extract the AST behind `internal/` and accept the `Pred.Expr()`
  awkwardness.
- The generator needs broad access to model internals — that is a second consumer
  for the Provisional tier and a reason to give it a real package and contract.
- At v1.0, revisit properly: promote Provisional to Stable, or hide it.

## Cost of change

Low now, rising slowly. Introducing `internal/` later is mechanical inside the
module. The cost lands on external users: anything they imported that moves
becomes uncompilable with no deprecation path, because `internal/` is absolute.
If it is going to happen, it should happen before there are external users.

## Revisions

- 2026-07-27 — Written, after auditing the exported surface.
- 2026-07-30 — Condensed.
