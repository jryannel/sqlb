# ADR-0007: One generic handler, and an OpenAPI document generated per resource

- **Status:** Working
- **Confidence:** Medium
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-30

## Context

Given a schema and a filter grammar, the REST surface can be delivered two ways.
One generic handler can serve every exposed table by dispatching on the path,
which means adding a table costs zero lines. Or each resource gets a generated
handler with a typed filter struct and a precise OpenAPI operation.

The trade looked like boilerplate elimination against client-side typing. A
generic handler cannot describe itself precisely in OpenAPI, the reasoning went,
because the filter grammar is compositional — `?age=gte.18` is not a fixed
parameter set — so the generated client ends up with loose types.

That framing turned out to contain a false step. The grammar is compositional,
but the **columns are not**: they are finite, known at registration, and each one
admits a documented operator vocabulary. Enumerating one query parameter per
filterable column describes the surface exactly, without describing the grammar
at all. `sort` and `select` become comma-separated arrays whose items enumerate
the capable columns. Nothing about that needs a handler generated per resource.

What made it buildable is that [Huma](https://huma.rocks) keeps parameters set
on an operation explicitly, and hands an input struct's `Resolve` method the raw
query values — so `filter.Parse` still owns validation while the document is
written separately, from the model's capabilities.

## Decision

**One generic handler, instantiated per resource through generics.**
`rest.Resource[T, C, U]` registers the exposed operations for a model on a
`huma.API`. The handlers are ordinary Go, written once; the OpenAPI operation is
built per resource from `sqlb.Model`.

Generics rather than reflection, for a reason that is not stylistic: hooks are
keyed by type. `BeforeQuery` on a model must apply to that model's REST reads —
it is how tenant scoping stops being something each handler remembers
([ADR-0008](0008-hooks-as-domain-seam.md)) — and a reflective dispatcher holding
a `reflect.Type` cannot call `On[T]()`.

**Codegen still emits per resource, but only what generics cannot express:** the
request bodies. A create body differs from the row (read-only columns absent,
defaulted ones optional) and a patch body differs again (everything optional,
immutable columns absent, and an omitted field distinguishable from one set to
`null`). Those are three different JSON Schemas over the same table, and no
single Go type serves all three honestly. The generated file is `rest_gen.go`:
two body types per writable resource, and one `rest.Resource` call per exposed
table.

**`rest` takes a `huma.API` rather than building a router.** The application
chooses chi, gin, echo or `net/http`, and keeps its own middleware. This also
keeps the engine's dependency claim intact: `rest` depends on huma, and nothing
else in the module depends on either.

**`rest` also offers a default that *does* build the router.** `rest.NewServer`
assembles a huma.API on `net/http` — via humago, so no third-party router is
pulled in — has huma serve the OpenAPI document and its docs page, and hands back
the huma.API and the mux for a generated Register and any hand-written routes. It
is a front door over the seam above, not a replacement for it: an application
that wants a different router, a different huma adapter, or its own huma.Config
still builds the huma.API itself. The seam is what lets the default exist without
foreclosing the alternatives — huma stays the substrate, and the common path
stops being a router-plus-adapter assembly each application repeats.

**Considered and declined (2026-07-30): removing the huma dependency.** Under
recurring adoption feedback — huma appears in a consumer's module graph even when
they never mount a REST surface — going huma-free was scoped in full: `net/http`
plus an in-house OpenAPI generator. It was declined. Reimplementing what huma
does well — a battle-tested reflect-to-JSON-Schema generator, request-body
validation, content negotiation — is a large, permanent surface to own, and
dropping huma would inherit a validation gap the database only partly covers (a
document could state a `minimum` or `enum` that nothing enforces before the
write). The module-graph cost is accepted as the smaller price. The paths weighed
are under *Alternatives considered*, and what would reopen the question is under
*What would change our mind*.

## Consequences

**What this buys.** End-to-end typing into the frontend: a filter that does not
exist fails at the client's compile step rather than as a 400 at runtime. Adding
a table costs one generated registration, not a generated handler. The response
schema, the parameter list and the allow-list in a rejection all derive from the
same capability flags, so they cannot disagree with what the parser enforces.

**What this costs.** A dependency on Huma's shape — specifically on
`Operation.Parameters` surviving registration, and on a handler-returned
`StatusError` being written as-is. Both are load-bearing and neither is exotic,
but a major Huma version could move them. The generated bodies are real
generated code with real volume, roughly forty lines per writable resource.

**What is deliberately not done.** `?expand` is parsed and validated but performs
no join, so `Options.Expandable` should stay empty until it does. There is no
TypeScript client yet; the document is the input to one.

## What would change our mind

- If the per-column parameter list gets unwieldy at realistic column counts — a
  fifty-column table documents fifty parameters — collapse the rarely-used ones
  behind a single documented `filter` parameter and accept looser typing there.
- If Huma's parameter or error hooks change shape in a way that needs
  workarounds rather than adaptation, the same design runs on `net/http` with a
  hand-written document generator; the handlers do not change. Weighed in full on
  2026-07-30 and declined — the reimplementation is battle-tested code to re-own —
  but the escape hatch is real if a future Huma version forces it.
- If huma in the module graph becomes a hard adoption blocker rather than a
  grumble — a consumer who imports the engine and never mounts REST refusing the
  transitive dependency — the nested `rest` module (see *Alternatives*) reappears
  before an in-house generator does: it keeps huma's machinery and costs only a
  second release tag.
- If in practice nobody consumes the generated document, most of the benefit of
  documenting per column evaporates, and a single opaque `filter` parameter is
  simpler.

## Cost of change

Moving off Huma costs the `rest` package and nothing else: the engine, the
filter grammar, the generated bodies and the generated clients — which read
`sqlb.Model`, not the document ([ADR-0028](0028-typescript-client.md),
[ADR-0031](0031-dart-client.md)) — are all independent of it. That is the main
reason the adapter is its own package, and it is why the huma-free path scoped on
2026-07-30 would have touched one package rather than the surface. The reason it
was declined is not cost of change but cost of *ownership*: the replacement is
machinery huma already maintains.

The expensive surface is the **response and error shape**, not the handlers.
`{items, page, per_page, has_more, total}` and the RFC 9457 problem document
with its `allowed` field are what clients parse. Changing either is a
coordinated client migration, and is a breaking API change even though half of
it is an error path. Adding fields to both is free.

## Alternatives considered

**Generated handler per resource.** The original decision here. Rejected once it
became clear the OpenAPI precision it was bought for does not require it: the
parameters come from the model either way, so the generated Go was volume
without benefit. Its one real advantage — per-resource middleware — is available
anyway, since Huma operations carry their own.

**One generic handler with a hand-maintained spec (PostgREST-style).** Zero
per-resource anything, and the runtime already supported it. Rejected because a
hand-maintained document drifts from the schema silently, which is the failure
this project exists to remove.

**A reflective dispatcher over the registry.** Tempting — one route table, no
generics — but it cannot run type-keyed hooks, so tenant scoping would have to
move into middleware and be re-applied per resource. That trades a compile-time
guarantee for a convention, on the one axis where a mistake is a data leak.

**A nested Go module for `rest`.** Would keep the root module literally
dependency-free while keeping huma. Rejected first for the reason
[ADR-0013](0013-no-internal-split.md) gives — two modules to version and release
in lockstep is a standing tax, and the `deps-check` gate already proves per
package what the module boundary would have proved by construction — and, when
reconsidered on 2026-07-30 under adoption feedback, left rejected. Unlike
`pgtest`, an internal test module nobody imports, a *published* `rest` module
pays that tax for real: two release tags in lockstep and a version skew a
consumer can pin themselves into. It stays the fallback, though — of the two ways
to answer the module-graph complaint, it is the cheaper, because it keeps huma's
machinery instead of rebuilding it.

**Dropping huma entirely, on `net/http` with an in-house document generator.**
Scoped in full on 2026-07-30 and declined. It means re-owning a battle-tested
reflect-to-JSON-Schema generator, request-body validation and content
negotiation, and it inherits a validation gap: the document could state a
constraint — `minimum`, `enum` — that nothing enforces before the database, where
huma enforces it before the handler. Query-parameter validation would survive
untouched (it already lives in `filter.Parse`, not huma), but body validation
would fall back to the database's CHECK constraints ([ADR-0017](0017-enums-as-text-and-check.md)),
a coarser 422 arriving later. The module-graph cost huma imposes is real but
smaller than that surface to own. Folded here rather than kept as its own record,
since it is a road not taken.

## Revisions

- 2026-07-30 — Added `rest.NewServer`, a batteries-included default that builds
  the huma.API on `net/http` (humago, so no chi) and serves the document and docs
  page, so the common REST path is one call rather than the router-plus-adapter
  assembly each app repeated. The huma.API seam is unchanged and remains the
  advanced path — this widens the surface, it does not narrow it.
- 2026-07-30 — Reviewed under adoption feedback that huma appears in a consumer's
  module graph even without a REST surface. Two ways out were scoped in full — a
  nested `rest` module, and dropping huma for an in-house `net/http` generator —
  and both declined in favour of keeping huma; the reasoning is under
  *Alternatives considered*. Status stays Working; the module-graph cost is
  accepted, and the nested module is recorded as the fallback if it stops being
  acceptable.
- 2026-07-27 — Rewritten after building it. The decision reversed from
  "generated handler per resource" to "one generic handler plus a generated
  document", which was the alternative this record already named as best-if-it-
  works. Status Exploring → Working, Confidence Low → Medium. The open question
  it flagged — an OpenAPI schema for a compositional grammar — dissolved once we
  stopped trying to describe the grammar and described the columns instead.
- 2026-07-27 — Written.
