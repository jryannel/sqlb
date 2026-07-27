# ADR-0024: No annotation slot until something can consume one

- **Status:** Working — the schema stays closed, which is the current state
- **Confidence:** Medium — the reasoning is structural, but no third party has
  ever tried to extend this and been refused, so the demand is inferred
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

An outside review suggested an extension slot, conditionally:

> Consider an annotation/extension slot on `Table` if third-party extensibility
> is ever wanted.

and, comparing with ent:

> `Annotations()` is an open slot where third-party extensions (entoas, entrest,
> entgql, entproto) hang their own config. That is why ent has an ecosystem and
> sqlb has a feature list.

The observation about ent is correct, and the causation is the right way round.
The question is whether the slot is the part that is missing here.

### sqlb already has an annotation. It has exactly one, and it is typed.

`Expose(schema.REST{...})` is an annotation in everything but name: config
attached to a table, meaningless to the database, read by a consumer downstream.
It works, and the reason it works is instructive.

`schema.REST` is a struct. Codegen reads its fields and writes a `rest.Options`
literal; `rest.Op` mirrors `schema.Op` deliberately rather than importing it,
because nothing on the request path may import the schema package — that is what
keeps the runtime usable without the DSL. The exposure decision crosses the
boundary **as a value with a known shape**. The manifest can describe it for the
same reason: `sqlb.json` lists the operations and page limits because it knows
what they mean.

A `map[string]any` on `TableDef` could do neither. Nothing type-checks it,
nothing can render it into the manifest as anything but opaque JSON, and the
compiler stops helping at the point it is written.

### The slot is the smaller half of the feature

An annotation is only worth writing if something reads it. In ent, that
something is a generator with a plugin and template system: `entoas` and
`entgql` are code generators that ent's own generator invokes.

`codegen.render` is a fixed sequence:

```go
files := map[string][]byte{}
if name := opts.modelsFile();   name != "-" { ... renderModels(opts) }
if name := opts.columnsFile();  name != "-" { ... renderColumns(opts) }
if name := opts.restFile();     name != "-" { ... renderREST(opts) }
if name := opts.manifestFile(); name != "-" { ... renderManifest(opts) }
```

Four emitters, named in the source, with no plugin interface, no exported
templates and no way for a third party to add a fifth. So an annotation added
today would be a field that only sqlb's own emitters could read — and those four
are all in-tree, where a typed field on `TableDef` serves them better than an
untyped bag would.

**The extensible generator is the load-bearing half, and it is a much larger
piece of work than the slot.** Shipping the slot alone would be shipping the
half that does nothing.

### And the demand is inferred, not observed

The project is pre-1.0, has one author, and no observed consumers
([compatibility](../compatibility.md)). No third party has wanted to extend it
and been refused, because there is no third party. An extension point designed
against imagined extensions is designed against imagined requirements, and it
is a compatibility surface the moment anyone touches it — `schema` is in the
**Stable** tier ([ADR-0013](0013-no-internal-split.md)), where changes are
breaking changes and are treated as such.

## Decision

**No annotation slot. The schema stays a closed, typed vocabulary.**

A new kind of config on a table is added the way `schema.REST` was: as a typed
field, with a consumer written at the same time, in the same repository.

If third-party extensibility is genuinely wanted later, the order is the
opposite of the one the review implies:

1. **An extensible generator first** — a way for a third party to contribute an
   emitter to `codegen`, with a stable view of the registry to read.
2. **The annotation slot second**, shaped by what that generator actually needs
   to be told.

Doing it in this order means the slot's design is answerable from a real
consumer rather than guessed at. Doing it the other way produces a
`map[string]any` that the first real extension does not fit.

## Consequences

**What this buys.** The schema keeps a property that is doing real work: every
declaration in it has a known meaning, so `Validate` can reject an incoherent
one, `Lint` can warn about a bad one, `BuildManifest` can describe it, and
`RenderSchema` can round-trip it back to source. That last one is the adoption
loop — `introspect` → registry → `schema.go` — and it works because the set of
things a registry can hold is closed. An opaque bag breaks the round trip: the
renderer cannot emit Go source for a value it cannot name.

**What this costs.** Anyone wanting to hang their own config on a table forks or
opens a pull request. For a project with no consumers that is a cost of zero
today, and it converts directly into a barrier the day someone does want it. The
review's causal claim — that the slot is why ent has an ecosystem — is not wrong,
and this record accepts that consequence rather than disputing it.

It also means the answer to "can I make sqlb emit X" is "send a patch", which
scales badly with exactly one maintainer. That is a real risk and it is the
same risk the review names elsewhere about surface area.

## What would change our mind

- **Someone asks.** One concrete request to attach config sqlb does not
  understand — with the consumer they intend to write — turns this from inferred
  demand into observed demand, and the answer changes. This is the trigger, and
  it is deliberately a low bar.
- **A second in-tree consumer wants config sqlb cannot express.** If a TypeScript
  client generator or the change feed needs per-table settings that do not belong
  in `schema.REST`, that is the same pressure arriving from inside, and the
  extensible-generator work becomes worth doing on its own merits.
- **The four-emitter list stops being enough.** If `codegen.render` grows to the
  point that adding an emitter is routine, a plugin interface is the natural next
  step, and the slot follows it.

## Cost of change

**Asymmetric, and this is the whole reason to decline now rather than later.**

Adding the slot later is cheap: a new field on `TableDef` is additive, no
existing schema changes, no generated code changes. Nothing about deciding "not
yet" makes "yes" harder.

Removing or narrowing one after it ships is expensive and probably impossible.
An annotation slot is a public interface into which arbitrary third-party data
has been written; the moment one extension uses it, its shape is frozen, and
`schema` is a Stable-tier package. Changing `map[string]any` to a typed
alternative later would break every consumer at once.

That asymmetry — cheap to add, near-impossible to retract — is the argument for
waiting until there is a real consumer to design against, not the argument for
never.

## Alternatives considered

**Add the slot now, keep it undocumented.** Rejected: an exported field is
public whether or not it is documented, and this project's own tier table says
so. "Undocumented but exported" is how a compatibility surface is acquired by
accident.

**Add a typed extension struct instead of a bag** — an `Annotations` struct with
a field per known extension. Genuinely close, and it preserves validation, lint
and manifest support. It fails on the actual goal, though: a third party cannot
add a field to a struct in this repository, so it is not extensibility, it is
`schema.REST` with more rooms. If in-tree consumers multiply, this is the shape
to reach for, and it is not the review's suggestion.

**Extensible codegen without a slot** — let a third party contribute an emitter
that reads the existing typed registry. This is genuinely useful on its own and
is the half the decision above puts first. Most of what an extension would want
(names, columns, capabilities, relations, REST exposure) is already in the
registry; the slot is only needed for config sqlb does not model at all. It may
turn out that the emitter interface is the whole feature and the slot is never
needed.

**Follow ent fully** — plugin system, template overrides, annotation slot. The
honest reason this loses is scope, not design. The review's own strategic point
is that maintaining a schema DSL, codegen, a compiler, a migration engine, an
introspector, a REST layer and a filter grammar alone is the thing most likely
to kill the project. An extension API is another public surface to keep working,
and it is the one whose consumers are hypothetical.

## Revisions

- 2026-07-27 — Written, prompted by an outside review. Recorded as a decision to
  decline for now rather than left implicit, because the review is right that
  the slot is what ent's ecosystem hangs from, and a future reader should find
  the reasoning rather than the absence.
