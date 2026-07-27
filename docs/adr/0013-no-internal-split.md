# ADR-0013: No public/internal package split

- **Status:** Working
- **Confidence:** Medium
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

The module exports 189 identifiers from package `sqlb`. Some are the API people
use daily; some are public only because another package in the module needs
them; some are the compiler's own vocabulary. Go offers `internal/` to make that
distinction compiler-enforced, and the question is whether to use it.

Two facts decided it.

First, the genuinely internal machinery — the compiler, scanning, model
building, identifier and pattern escaping — is already unexported within package
`sqlb`, 36 identifiers of it. Lowercase is a finer-grained tool than `internal/`
and it is already doing the job. `internal/` only adds enforcement across
*package* boundaries, so it would restate a boundary that already holds.

Second, the obvious candidate for extraction does not survive contact. Moving
the AST nodes to `internal/ast` fails because `Expr` and `Raw` must stay public:
they are the documented escape hatch, appearing in `SetExpr`, `GroupByExpr`,
`Coalesce` and `OrderByDesc`. With those two public, hiding the remaining eight
node types buys only the freedom to rename their fields — while forcing
`Pred.Expr()` to return a type callers cannot name, and splitting the compiler
away from the `Pred` it serves.

## Decision

No `internal/` packages. Keep the flat layout, and express the public/mechanism
distinction as documented tiers plus a `v0` version, not as package structure:

- **Stable** — the query builder, predicates, the typed column facade, hooks,
  mutations, `Describe`, the `filter` package, the `schema` DSL. Changes here are
  breaking changes and will be treated as such.
- **Provisional** — `Model`, `ColumnInfo`, `ModelOf`, `Selectable`, `Selection`,
  `Dialect`, `Postgres`. Public because `filter` and generated code need them
  across a package boundary. Expect these to move.
- **Escape hatch** — `Expr` and the node types (`Raw`, `Binary`, `Unary`,
  `Call`, `Cast`, `BetweenExpr`, `List`, `Param`, `Column`). Public on purpose;
  use `Raw`, `RawPred` and `RawSel`. The rest is the compiler's vocabulary and
  will change without ceremony.

## Consequences

**What this buys.** No ceremony, no import gymnastics, and no premature boundary
in a pre-1.0 library whose shape is still moving. Tiers communicate intent at
the granularity that actually matters — per identifier — where `internal/` can
only work per package.

**What this costs.** Tiers are a convention, not a compiler check: someone can
depend on `Binary` and be broken later with only a doc comment to point at. The
surface is also large enough that a reader cannot tell the tiers apart without
consulting the documentation.

## What would change our mind

- If someone outside the module depends on a node type and is broken by a
  compiler change, that is the signal the convention is insufficient — extract
  the AST behind `internal/` and accept the awkwardness around `Pred.Expr()`.
- If the generator ends up needing broad access to model internals, that is a
  second consumer for the Provisional tier and a reason to give it a real
  package with a real contract.
- At v1.0, revisit properly. The tiers are a v0 device; a stable release should
  either promote Provisional to Stable or hide it.

## Cost of change

Low now, and rising slowly. Introducing `internal/` later is mechanical — move
files, fix imports — and the compiler finds every call site inside the module.

The cost lands on external users, not on us: anything they were importing that
moves becomes uncompilable with no deprecation path, because `internal/` is
absolute. That argues for doing it before there are external users, if it is
going to be done at all.

## Alternatives considered

**`internal/ast` plus `internal/compile`.** The textbook answer. Rejected
because `Expr` and `Raw` must remain public, which strands the interface in one
package and its implementations in another, and leaves `Pred` holding a type its
own callers cannot name.

**A separate `sqlbcore` module for the engine.** Rejected outright: two modules
to version and release, for one library of five thousand lines.

## Revisions

- 2026-07-27 — Written, after auditing the exported surface rather than
  reasoning about it in the abstract.
