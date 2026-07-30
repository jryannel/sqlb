# ADR-0028: The TypeScript client is generated from the model, and stops at the query key

- **Status:** Working — all four layers are built and emitted into
  `example/tasks/web`, whose typecheck is a gate in `mise run ci`
- **Confidence:** Medium — twelve illegal requests are marked `@ts-expect-error`
  in `web/src/refusals.ts`, so a widened type fails the build; Low still on the
  key shape and on `queryOptions`, since no application has lived with either
- **Decided:** 2026-07-28
- **Last reviewed:** 2026-07-28

## Context

[Vision](../vision.md) describes the TypeScript client as fed by the OpenAPI
document, so the obvious move is to point `openapi-typescript` at it and write no
generator. Three things argue against it:

**The document is lossy exactly where the value is.** A filter parameter is
documented as `array<string>` with the operator vocabulary in prose, because that
is the most OpenAPI can say about `?status=eq.published`. A generic generator
emits `status?: string[]`, and `status=bogus.x` compiles.

**Two hand-written clients confirm the shape of the loss.** Both hand-roll a list
endpoint with `URLSearchParams` and bare `string` parameters, with a comment
explaining which values are legal — the most generatable function in either
codebase and the place where a typo compiles. Both also show what a generator
must *not* own: session storage, token refresh, `401` → redirect, JWT decoding.

**The sharpest evidence is about cache keys.** One client imposes a key factory
and an architecture test, and documents the two production bugs that motivated
it. The other writes 31 string-literal keys across 9 files; its change-feed
subscriber and its mutation handlers keep two invalidation lists that have
drifted, because `['draft', id]` and `['drafts', id]` are one character apart.
Nothing catches it. Both are the signature of a missing artefact, not a missing
rule.

Smaller, but telling: neither client surfaces `filter.Error.Allowed`. Both
flatten the error body to a string, so [ADR-0011](0011-actionable-errors.md)'s
property dies at the client boundary — a generated error type recovers an
existing guarantee rather than adding a feature.

## Decision

Generate the client from the same model metadata `codegen` already consumes — not
from the emitted OpenAPI document — and emit it into the consuming repo, the way
`models_gen.go` is emitted. Four layers, each usable without the one above:

1. **Row and body types.** Hidden columns are absent from the row type entirely.
2. **Typed request parameters.** `where` admits only filterable columns with the
   operator set narrowed by column type; `sort` is a union of sortable columns
   and their `-` forms; `select` and `expand` narrow the response type.
3. **Transport functions**, encoding those parameters into the URL grammar and
   taking an injected request function.
4. **A key factory and `queryOptions` factories** for list and item reads.

Explicitly not: the client shell, hooks, mutation helpers, optimistic updates, or
a published npm package.

- **Wire names keep the `json` tag spelling.** Camel-casing needs a runtime
  mapping layer, and the point of the emitted client is types plus a URL encoder
  with nothing between the response and the caller.
- **The transport is injected** — auth, refresh, retry and redirect-on-401 stay
  the application's. This is [ADR-0007](0007-generated-rest-handlers.md)'s seam
  argument in the other language.
- **`queryOptions`, not hooks.** Hooks bake in React and get copied out and
  edited; a `queryOptions` object is spread and overridden, which is composition
  rather than a fork.
- **An expanded collection keeps its envelope.** `?expand=author` resolves to
  `author: Author`; `?expand=tasks` to `tasks: Collection<Task>`. Typing the
  reverse as `Task[]` is the one shortcut to refuse — it reintroduces the silent
  truncation the envelope prevents, one layer further out.
- **An infinite-query factory is the second emitter, not a maybe.**
  `next_cursor` is exactly what `getNextPageParam` wants
  ([ADR-0027](0027-keyset-pagination.md)), so no application has to know how
  paging works. Both observed clients hand-roll it from `has_more` and an offset.
- **The key factory exists because the change feed will consume it.**
  [ADR-0012](0012-change-feed-outbox.md) delivers table-plus-row-key
  invalidations, which is only mechanical if something derives the key. If sqlb
  generates both the keys and the subscriber, the two lists that drifted in the
  observed client cannot disagree, because there is one list.
- **Emitted into the repo, not published to npm.** A client generated against the
  server it talks to cannot drift from it.

## Consequences

**Buys.** A misspelled column, an operator its type does not accept, and a sort
on a column that did not opt in all fail at the TypeScript compile step —
[ADR-0009](0009-typed-column-facade.md)'s property carried across the wire.
ADR-0011's `allowed` list reaches the caller. And the change feed gets a consumer
whose invalidation is structural rather than conventional.

**Costs.** A second toolchain in a repository whose pitch is a Go module: CI needs
Node, and the emitted client takes `@tanstack/react-query` as a peer dependency.

The generated slice is a minority of a real client — in one observed service, 5
of 20 methods are schema CRUD, and the other's API is command-shaped throughout.
The generator must compose with hand-written code rather than own the namespace,
which rules out a single client object.

Adoption is a migration, not a drop-in: both observed clients page by offset and
assume a `total` that is always there. And the key factory only fixes the half of
invalidation the schema knows about — a computed view is not a table, so its key
cannot be generated, and without a way to declare that dependency the generated
feed reproduces the observed bug for derived views.

**What building it changed.** The key factory sits in the dependency-free file,
because a change-feed subscriber needs keys and not a query client. `Where` is a
type alias, not an interface, for the implicit index signature. The item endpoint
offers no `select`, because `rest` registers it with
`RejectUnknownQueryParameters`. And the raw-parameters escape hatch was emitted
from the start, partly so that reaching for it is observable.

## What would change our mind

- Callers reach for raw parameters more often than the typed `where` — most
  likely when a filter is assembled from user input at runtime, which is the case
  this project exists for. Then the typed layer is an overlay, not the primary
  API.
- Generated `queryOptions` get copied out and edited — the seam is wrong; revisit
  it rather than add options.
- Response narrowing needs enough generic machinery that its type errors become
  unreadable — drop the narrowing and return the full row type.
- A second framework needs something other than `queryOptions` — the TanStack
  layer becomes one of several opt-in emitters and layer 3 is the real product.
- ADR-0012 lands with a payload that cannot be mapped onto keys mechanically —
  the key factory shrinks to what list and detail views need.
- A client team that does not run Go needs the SDK — the no-npm decision is the
  one to reopen, and the likeliest of these to happen.

## Cost of change

Cheap, and no longer free: one consumer exists in the repository and regenerates
from the schema, so `tsc` names what stops compiling.

**The layers are cheap in both directions** — stopping at layer 2 is a smaller
generator rather than a wrong one, and deleting the TanStack layer breaks only
the call sites that used it.

**The key shape is expensive once anything consumes it.** Keys become `as const`
tuples embedded in call sites, handlers and tests, and prefix invalidation is
structurally typed: a wrong prefix compiles and silently matches nothing — the
exact bug the factory exists to prevent. Snake_case is likewise expensive to
reverse: camel-casing later means a runtime mapper, which this design refuses, or
a breaking rename of every field.

## Revisions

- 2026-07-28 — Written before implementation, prompted by reading two
  hand-written clients — one with a key factory and an architecture test, one
  with a live invalidation bug the factory would have made unrepresentable.
- 2026-07-28 — Reverse expansion landed, so expand-narrowing has two shapes.
  Recorded that the reverse must not be typed as a bare array.
- 2026-07-28 — Built; Exploring → Working. The layering, snake_case wire names,
  injected transport and `queryOptions` unit all survived contact unchanged. Node
  is now pinned in `mise.toml`.
- 2026-07-28 — Renumbered from 0026 and reconciled with keyset pagination, which
  made the infinite-query factory a concrete second emitter.
- 2026-07-30 — Condensed.
