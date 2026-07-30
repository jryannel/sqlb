# ADR-0007: One generic handler, and an OpenAPI document generated per resource

- **Status:** Working
- **Confidence:** Medium
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-30

## Context

The REST surface can be one generic handler dispatching on the path, or a
generated handler per resource with a typed filter struct and a precise OpenAPI
operation. The apparent trade was boilerplate against client-side typing: a
generic handler supposedly cannot describe itself precisely, because the filter
grammar is compositional.

The grammar is compositional, but the **columns are not** — they are finite,
known at registration, and each admits a documented operator vocabulary. One
query parameter per filterable column describes the surface exactly without
describing the grammar. [Huma](https://huma.rocks) makes this buildable: it keeps
explicitly-set operation parameters and hands an input struct's `Resolve` the raw
query values, so `filter.Parse` still owns validation.

## Decision

**One generic handler, instantiated per resource through generics.**
`rest.Resource[T, C, U]` registers the exposed operations for a model on a
`huma.API`. The OpenAPI operation is built per resource from `sqlb.Model`.

Generics rather than reflection, because hooks are keyed by type: a reflective
dispatcher holding a `reflect.Type` cannot call `On[T]()`, and that is how tenant
scoping stops being something each handler remembers
([ADR-0008](0008-hooks-as-domain-seam.md)).

**Codegen emits only what generics cannot express: the request bodies.** Create,
patch and row are three different JSON Schemas over one table, and no single Go
type serves all three honestly. `rest_gen.go` holds two body types per writable
resource plus one `rest.Resource` call per exposed table.

**`rest` takes a `huma.API` rather than building a router**, so the application
keeps its own router and middleware. `rest.NewServer` is a batteries-included
default over that seam: it assembles a huma.API on `net/http` via humago, serves
the document and docs page, and hands back the API and the mux.

huma stays in the engine's module. Both ports flagged that huma appears in a
consumer's module graph, and that huma's `go 1.25.0` becomes the toolchain floor
even for consumers who never mount REST. That cost is accepted:
[ADR-0040](0040-the-driver-is-a-dependency.md) has already retired "importing
sqlb costs nothing", and a dependency the product is built on belongs in the
product's `go.mod`.

## Consequences

**Buys.** End-to-end typing into the frontend — a filter that does not exist
fails at the client's compile step, not as a runtime 400. Adding a table costs
one generated registration. Response schema, parameter list and rejection
allow-list all derive from the same capability flags, so they cannot disagree.

**Costs.** A dependency on Huma's shape — `Operation.Parameters` surviving
registration, and handler-returned `StatusError` being written as-is. huma sets
the module's Go floor at 1.25 for every consumer. Generated bodies run roughly
forty lines per writable resource.

**Not done.** `?expand` is parsed and validated but performs no join, so
`Options.Expandable` should stay empty until it does.

## What would change our mind

- The per-column parameter list gets unwieldy at realistic column counts — a
  fifty-column table documents fifty parameters. Collapse the rare ones behind a
  single `filter` parameter and accept looser typing there.
- Huma's parameter or error hooks change shape in a way that needs workarounds
  rather than adaptation. The same design runs on `net/http` with a hand-written
  document generator; the handlers do not change.
- Nobody consumes the generated document — then per-column documentation buys
  little, and one opaque `filter` parameter is simpler.

## Cost of change

Moving off Huma costs the `rest` package and nothing else: the engine, the filter
grammar, the generated bodies and the generated clients read `sqlb.Model`, not
the document. The expensive surface is the **response and error shape** —
`{items, page, per_page, has_more, total}` and the RFC 9457 problem document with
its `allowed` field. Changing either is a coordinated client migration. Adding
fields to both is free.

## Revisions

- 2026-07-27 — Written; rewritten after building it. Reversed from "generated
  handler per resource" to one generic handler plus a generated document, once
  describing the columns replaced describing the grammar.
- 2026-07-30 — Added `rest.NewServer` as a default over the huma.API seam.
- 2026-07-30 — Reviewed under adoption feedback about the module graph. Two exits
  were scoped and declined: a nested `rest` module (built and reverted the same
  day) and dropping huma for an in-house `net/http` generator. Both were
  defending "importing sqlb costs nothing", which ADR-0040 had already retired.
  The nested module is closed, not held as a fallback; what would reopen it is
  sqlb ceasing to be the thing you build the whole application with.
- 2026-07-30 — Unpinned the `go` directive from `1.25.7` to huma's actual floor,
  `1.25.0`. The patch pin came from `go mod init`, not a requirement.
- 2026-07-30 — Condensed.
