# ADR-0023: A mixin contributes columns; carrying behaviour needs codegen

- **Status:** Working as a decision — `schema.Group` ships and user-defined column
  mixins work today. The behaviour-carrying half is deliberately unbuilt and is
  not in 1.0
- **Confidence:** High that splitting it this way is right; Medium on the
  sketched fix, which remains untried
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

An outside review recommended a mixin mechanism — a user-defined bundle of
columns, hooks and capabilities. Half of that already exists, and the half that
does not is not small.

**User-defined mixins already work.** `schema.Group` is exported and satisfies
`FieldSpec`, so a bundle of columns is an ordinary function returning one.
`Timestamps` and `SoftDelete` are not privileged — they are two such functions
that happen to live in the package, and a user-written `Auditable()` composes
with them today.

**What a mixin cannot carry cost something real.** A `Group` contributes
`[]*Field` and nothing else — no index, no check constraint, no hook.
`SoftDelete`'s doc comment claimed the REST layer filtered out rows with a
non-null `deleted_at`. Nothing did: a table declaring it and exposing `OpList`
returned deleted rows. The behaviour now lives in a hand-written
`BeforeQuery` registration — the mixin adds a column, and a human elsewhere is
trusted to add the meaning.

**The schema package cannot simply register the hook**, for two reasons. `schema`
imports nothing from the engine, which is what `deps-check` enforces and what
makes codegen optional ([ADR-0010](0010-codegen-is-optional.md)). And hooks are
keyed by the Go model type, which does not exist at declaration time — there is
only `blogschema.Post`, a `*TableDef`. No loosening of the import graph fixes
that ordering.

## Decision

**Leave `Group` as the mixin mechanism for columns. Do not extend it to hooks. If
a mixin is to carry behaviour, the carrier is codegen.**

1. **Say plainly that `Group` is the mixin mechanism.** It is exported, it works,
   and nothing documented it as the extension point — which is why the review
   read the built-ins as hardcoded. Costs nothing.
2. **Let a group contribute table-level declarations**, not only fields — an
   index or a check travelling with the columns that need them. `SoftDelete`
   wants a partial index on `deleted_at`; today the caller must remember it.
3. **Treat "a mixin implies a hook" as a codegen question.** Codegen knows both
   the declaration and the generated type, so it is the only layer that *can*
   emit `sqlb.On[Post]().BeforeQuery(...)`, and generated code is committed,
   readable and deletable. Not decided here — recorded so the next person does
   not re-derive that the schema package is the wrong place.

## Consequences

**Buys.** The documentation change removes the predicted pressure at no design
cost, because the feature was already there. Table-level contributions close the
gap `SoftDelete` exposed without touching the import graph. Naming codegen as the
carrier keeps the schema package a description of a database rather than a place
where runtime behaviour hides.

**Costs.** Soft delete stays a two-part declaration, and a table that declares one
half without the other is still wrong in a way nothing catches — a lint rule is
the obvious mitigation and is not written. Extending `Group` also widens
`FieldSpec`, whose method stays unexported, so mixins remain functions returning
`Group` rather than user-defined types.

## What would change our mind

- A second mixin wants behaviour — multi-tenancy bundling `org_id` with its
  scoping hook, say. One case is a fixable bug; two is a pattern, and point 3
  stops being deferred.
- The two-part declaration bites again — the answer is a lint rule before it is a
  mechanism.
- Third-party extensions are wanted. An annotation slot
  ([ADR-0024](0024-no-annotation-slot.md)) would subsume this, and should be
  decided on those terms rather than arrived at through mixins.

## Cost of change

Cheap in the direction taken; the expensive move is the one not being made.
Documenting `Group` and adding table-level contributions are additive. Making the
schema package register hooks would reverse the dependency direction `deps-check`
enforces and put runtime behaviour in the file people read to learn what the
database looks like. Codegen is the reversible version of the same idea.

## Revisions

- 2026-07-27 — Written, prompted by an outside review whose premise — that mixins
  are hardcoded — did not hold. The real gap is that a bundle carries columns
  only, which is what let `SoftDelete` ship a comment describing behaviour
  nothing implemented.
- 2026-07-30 — Condensed.
