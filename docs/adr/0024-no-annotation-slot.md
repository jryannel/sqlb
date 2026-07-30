# ADR-0024: No annotation slot until something can consume one

- **Status:** Working — the schema stays closed, which is the current state
- **Confidence:** Medium — the reasoning is structural, but no third party has
  ever tried to extend this and been refused, so the demand is inferred
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

An outside review suggested an annotation slot on `Table`, comparing with ent,
where `entoas`, `entgql` and `entproto` hang their own config — "that is why ent
has an ecosystem and sqlb has a feature list." The observation is correct and the
causation is the right way round. The question is whether the slot is the missing
part here.

**sqlb already has an annotation, and it is typed.** `Expose(schema.REST{...})`
is config attached to a table, meaningless to the database, read downstream. It
works because it crosses the boundary **as a value with a known shape**: codegen
reads its fields and writes a `rest.Options` literal, and the manifest can
describe it because it knows what it means. A `map[string]any` could do neither.

**The slot is the smaller half of the feature.** In ent the thing that reads an
annotation is a generator with a plugin and template system. `codegen.render` is
a fixed sequence of four emitters named in the source — no plugin interface, no
exported templates, no way to add a fifth. An annotation added today would be a
field only sqlb's own in-tree emitters could read, and those are better served by
a typed field than an untyped bag. The extensible generator is the load-bearing
half, and it is much larger work.

**The demand is inferred, not observed.** The project is pre-1.0 with one author
and no observed consumers. An extension point designed against imagined
extensions is designed against imagined requirements, and `schema` is in the
**Stable** tier ([ADR-0013](0013-no-internal-split.md)).

## Decision

**No annotation slot. The schema stays a closed, typed vocabulary.** New config
on a table is added the way `schema.REST` was: a typed field, with a consumer
written at the same time in the same repository.

If third-party extensibility is wanted later, the order is the opposite of the
one the review implies: **an extensible generator first**, with a stable view of
the registry to read, and **the annotation slot second**, shaped by what that
generator actually needs. That way the slot is answerable from a real consumer
rather than guessed at — and it may turn out the emitter interface is the whole
feature and the slot is never needed.

## Consequences

**Buys.** Every declaration keeps a known meaning, so `Validate` can reject an
incoherent one, `Lint` can warn, `BuildManifest` can describe it, and
`RenderSchema` can round-trip it back to source. That last one is the adoption
loop, and it works because the set of things a registry can hold is closed — an
opaque bag breaks it, since the renderer cannot emit Go source for a value it
cannot name.

**Costs.** Anyone wanting to hang their own config forks or opens a pull request.
Zero cost today, and it converts directly into a barrier the day someone does
want it. "Can I make sqlb emit X" answered with "send a patch" scales badly with
one maintainer. The review's causal claim is accepted rather than disputed.

## What would change our mind

- **Someone asks** — one concrete request to attach config sqlb does not
  understand, with the consumer they intend to write. Deliberately a low bar.
- A second in-tree consumer wants config that does not belong in `schema.REST` —
  the same pressure from inside, and the generator work earns its keep.
- The four-emitter list stops being enough — a plugin interface becomes the
  natural next step, and the slot follows it.

## Cost of change

Asymmetric, which is the whole reason to decline now rather than never. Adding
the slot later is additive: no existing schema changes, no generated code
changes. Removing or narrowing one after it ships is near-impossible — the moment
one extension writes into it, its shape is frozen, and `schema` is Stable-tier.

## Revisions

- 2026-07-27 — Written, prompted by an outside review. Recorded as a decision to
  decline for now rather than left implicit, because the review is right that the
  slot is what ent's ecosystem hangs from.
- 2026-07-30 — Condensed.
