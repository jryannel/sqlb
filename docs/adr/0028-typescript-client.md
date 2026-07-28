# ADR-0028: The TypeScript client is generated from the model, and stops at the query key

- **Status:** Working — all four layers are built and emitted into
  `example/tasks/web`, whose typecheck is a gate in `mise run ci`
- **Confidence:** Medium — the type-level claims are asserted rather than
  assumed, since `web/src/refusals.ts` marks twelve illegal requests
  `@ts-expect-error` and a widened type would fail the build, and the encoder's
  output is tested against the grammar; Low still on the key shape and on
  `queryOptions`, because no application has yet lived with either
- **Decided:** 2026-07-28
- **Last reviewed:** 2026-07-28

## Context

[Vision](../vision.md) lists a TypeScript client as the first unbuilt thing, and
describes it as fed by the OpenAPI document. The document is real and precise per
resource ([ADR-0007](0007-generated-rest-handlers.md)), so the obvious move is to
point `openapi-typescript` at it and write no generator at all.

That option is good enough to be the reason this record exists. Three things
argue against it, and two of them come from reading hand-written clients rather
than from reasoning.

**The document is lossy exactly where the value is.** A filter parameter is
documented as `array<string>` with the operator vocabulary in its description,
because that is the most OpenAPI can say about `?status=eq.published`. A
generic generator therefore emits `status?: string[]`, and `status=bogus.x`
compiles. The grammar that [ADR-0003](0003-one-ast-two-producers.md) makes safe
on the server arrives at the client as prose.

**Two hand-written clients confirm the shape of the loss.** Both are SPAs
against Go backends with the same author, and both hand-roll a list endpoint
with `URLSearchParams` and half a dozen bare `string` parameters, with a comment
explaining in prose which values are legal. That function is the single most
generatable thing in either codebase and the place where a typo compiles.

**Both also show what a generator must not own.** Session storage, token
refresh, `401` → redirect, JWT decoding, a blob-download dance. None of it is
derivable from a schema, and all of it is load-bearing.

The sharpest evidence is about cache keys. One client imposes a key factory and
an architecture test to keep keys and fetchers together, and its README documents
the two production bugs that motivated it. The other writes keys inline: 31
string-literal keys across 9 files, no factory. Its change-feed subscriber and
its mutation handlers maintain two separate invalidation lists, and they have
drifted — a send performed by the operator refreshes the two views its comment
says feed the learning loop, and the same send arriving over the event stream
does not, because the subscriber's list is missing them and `['draft', id]` and
`['drafts', id]` are one character apart. Nothing catches it.

So the discipline works, and it costs an arch test to maintain. Without it, the
failure is silent staleness. Both are the signature of a missing artefact rather
than a missing rule.

One smaller thing: neither hand-written client surfaces `filter.Error.Allowed`.
Both flatten the error body to a message string.
[ADR-0011](0011-actionable-errors.md)'s property dies at the client boundary in
both, which means a generated error type recovers an existing guarantee rather
than adding a feature.

## Decision

Generate the client from the same model metadata `codegen` already consumes —
not from the emitted OpenAPI document — and emit it into the consuming repo, the
way `models_gen.go` is emitted.

Four layers, each usable without the one above it:

1. **Row and body types**, from the model's columns. Hidden columns are absent
   from the row type entirely, as they are from the facade in
   [ADR-0009](0009-typed-column-facade.md).
2. **Typed request parameters.** `where` admits only filterable columns, with
   the operator set narrowed by column type; `sort` is a union of the sortable
   columns and their `-` forms; `select` and `expand` are generic and narrow the
   response type.
3. **Transport functions**, which encode those parameters into the URL grammar
   and take an injected request function.
4. **A key factory and `queryOptions` factories** for list and item reads.

And explicitly not: the client shell, hooks, mutation helpers, optimistic
updates, or a published npm package.

### Wire names are not camel-cased

Row types keep the `json` tag spelling, which is snake_case. Camel-casing needs
a runtime mapping layer, and the whole point of the emitted client is that it is
types plus a URL encoder with nothing between the response and the caller. One
of the observed clients documents using snake_case DTOs directly as a deliberate
choice; the other camel-cases its parameters and pays for a translation step at
every call site.

### The transport is injected

The generated functions take a request function rather than constructing one.
Auth, refresh, retry, error surfacing and redirect-on-401 stay the
application's, because that is where both observed clients put them and neither
one's version is derivable.

This is [ADR-0007](0007-generated-rest-handlers.md)'s seam argument in the other
language: `rest` mounts onto a `huma.API` the application built rather than
handing it a router, and the client does the same with `fetch`.

### `queryOptions`, not hooks

TanStack Query's `queryOptions()` is the emitted unit. Hooks bake in React and
are the thing people copy out and edit; a `queryOptions` object is spread and
overridden — `{...postQueries.list(p), staleTime: 30_000}` — which is
composition rather than a fork. Vision names copy-out-and-edit as the signal
that a seam is in the wrong place, and it applies here unchanged.

It also keeps the layer honest: both observed clients keep their transport free
of TanStack coupling even though both depend on it.

### An expanded collection is an envelope, and the generated type keeps it

Expansion narrows the response type by direction, because the two directions do
not return the same shape. `?expand=author` yields an object; `?expand=tasks`
yields `sqlb.Collection[T]` — `{items, has_more}`, capped, and told whether there
was more ([ADR-0022](0022-references-declare-their-inverse.md)). So layer 2
resolves a forward expansion to `author: Author` and a reverse one to
`tasks: Collection<Task>`.

Typing the reverse as `Task[]` is the one shortcut to refuse. That envelope
exists because a bare array cannot say it was truncated, and a client type that
drops `has_more` reintroduces the defect one layer further out: the caller
renders fifty of two hundred tasks with nothing in the type suggesting there are
more. It is the same failure in a different language, and the generator is the
last place it can be prevented rather than documented.

It also means the emitter has three response envelopes rather than two.
`Collection<T>` is a strict subset of `Page<T>` — items and `has_more`, without
`page`, `per_page`, `next_cursor` or `total` — so they are one generic with the
paging fields optional, not two hand-written shapes that can drift.

### Cursors make an infinite-query factory the second emitter, not a maybe

`next_cursor` is on every list response that has a next page
([ADR-0027](0027-keyset-pagination.md)), which is the shape
`infiniteQueryOptions` already wants: `getNextPageParam` returns it, and returns
`undefined` when it is absent. So the list emitter produces both factories from
one params type, and no application has to know how paging works. Both observed
clients hand-roll this from `has_more` and an offset counter, which is the
arithmetic cursors exist to replace.

This is also what will first test ADR-0027's one Medium — that no client has yet
had to hold a cursor across a sort change. The sort is in the key, so changing it
discards the accumulated pages rather than resuming from a cursor taken under the
old ordering. That is the correct behaviour, and it is worth confirming under a
real client rather than assuming.

### The key factory exists because the change feed will consume it

[ADR-0012](0012-change-feed-outbox.md) delivers invalidation events — table plus
row key — and expects clients to refetch. That contract is only mechanical if
something derives the key from the table and row key. If sqlb generates both the
keys and the subscriber that maps events onto them, the two lists that drifted in
the observed client cannot disagree, because there is one list.

This is the part of the record that is worth building before the feed rather than
after: the feed's value depends on it, and the key shape is the expensive thing
to change later (see Cost of change).

### Emitted into the repo, not published to npm

No `@sqlb/client` package. The generated client cannot drift from the server it
was generated against if it is generated against that server, which is the
property `models_gen.go` already has. The fetch wrapper is inlined rather than
imported.

## Consequences

**What this buys.** A misspelled column, an operator its column type does not
accept, and a sort on a column that did not opt in all fail at the TypeScript
compile step — [ADR-0009](0009-typed-column-facade.md)'s property carried across
the wire, for the producer that currently has none of it.
[ADR-0011](0011-actionable-errors.md)'s `allowed` list reaches the caller. And
[ADR-0012](0012-change-feed-outbox.md) gets a consumer whose invalidation is
structural rather than conventional, which is what separates a live view from a
websocket and a to-do.

**What this costs.** A second toolchain in a repository whose pitch is a
stdlib-only Go module: CI needs Node to typecheck the emitted client, and the
"no dependencies to inherit" claim has to be stated as being about the Go module,
because the emitted client takes `@tanstack/react-query` as a peer dependency.

The generated slice is a minority of a real client. In one observed service, 5
of 20 methods are schema CRUD; the other application's API is command-shaped
throughout — `assign`, `reply`, `discard`, `regenerate` — and no schema
generator will produce those. The generator has to compose with hand-written
code rather than own the namespace, which constrains its output shape and rules
out generating a single client object.

Adoption is a migration, not a drop-in. `rest` returns
`{items, page, per_page, has_more, next_cursor?, total?}`, where `total` is
present only for `?count=exact` and `next_cursor` is the paging a client should
prefer ([ADR-0027](0027-keyset-pagination.md)). Both observed clients page by
offset and assume a `total` that is always there. The generated types are right
and will not typecheck against existing call sites.

And the key factory only fixes the half of the invalidation problem the schema
knows about. A computed view — a calibration report derived from drafts — is not
a table, so its key cannot be generated, and the subscriber cannot know it
depends on drafts. Without a way for the application to declare that dependency,
the generated feed reproduces the observed bug for derived views.

**What building it changed.** Four things the record did not anticipate, none
of them large enough to reverse a decision:

- The key factory sits in the dependency-free file rather than beside
  `queryOptions`. A change-feed subscriber needs keys and does not need a query
  client, and the layering claim — each usable without the one above — is only
  true if the keys are reachable without TanStack.
- `Where` is emitted as a type alias, not an interface, because TypeScript gives
  an object type alias an implicit index signature and an interface none, and
  the shared encoder takes `Record<string, unknown>`.
- The item endpoint has no `select`. `rest` registers it with
  `RejectUnknownQueryParameters`, so a params type offering one would generate
  requests the server refuses; the emitter reads the same exposure the handler
  does and offers `expand` alone.
- The raw-parameters escape hatch was emitted from the start, as `params`. This
  record names reaching for it as the signal that the typed layer is in the
  wrong place, so it exists partly to make that signal observable.

## What would change our mind

- If callers reach for a raw-parameters escape hatch more often than they use
  the typed `where` object — most likely when a filter is assembled from user
  input at runtime, which is the case this project exists for — then the typed
  layer is in the wrong place and belongs as an overlay on a params type rather
  than as the primary API.
- If generated `queryOptions` get copied out and edited, the seam is wrong. Same
  signal, same response as [ADR-0007](0007-generated-rest-handlers.md) names for
  handlers: revisit the seam, do not add options.
- If narrowing the response by `select` and `expand` needs enough generic
  machinery that its type errors become unreadable, drop the narrowing and
  return the full row type. An unreadable TypeScript error is worse than a
  slightly wide type, and this is the layer most likely to produce one.
- If users of a second framework need something other than `queryOptions` — a
  Svelte store, a Vue composable — the TanStack layer should become one of
  several opt-in emitters rather than the default, and layer 3 becomes the real
  product.
- If [ADR-0012](0012-change-feed-outbox.md) lands with an event payload that
  cannot be mapped onto keys mechanically, the key factory loses its main
  justification and should shrink to what list and detail views need on their
  own.
- If a client team that does not run Go needs the SDK, the no-npm-package
  decision is the one to reopen, and it is the likeliest of these to happen.
- If the declared-dependency seam for derived keys turns out to need more than a
  single registration call, that is a sign the invalidation model belongs in the
  application and sqlb should emit keys but not the subscriber.

## Cost of change

Cheap, and no longer free. One consumer exists (`example/tasks/web`), it is in
the repository, and it regenerates from the schema — so a change to the emitter
reaches every call site in the same run that produced them, and `tsc` names the
ones that stop compiling.

The asymmetry is between the layers and the key shape.

**The layers are cheap in both directions.** Each builds on the one below and
each is independently useful, so stopping at layer 2 is a smaller generator
rather than a wrong one, and adding layer 4 later breaks nothing. Deleting the
TanStack layer after shipping breaks only the call sites that used it.

**The key shape is expensive to change once anything consumes it**, and this is
the thing to get right before the first adopter. Keys become `as const` tuples
embedded in call sites, invalidation handlers and tests, and prefix invalidation
is structurally typed: a wrong prefix still compiles and silently matches
nothing. That is the same failure mode the observed client has today, so a key
migration would reintroduce the exact bug the factory exists to prevent, and
TypeScript would not flag it.

Snake_case is likewise expensive to reverse: camel-casing later means either a
runtime mapper — which is the one thing this design refuses — or a breaking
rename of every field on every row type.

## Alternatives considered

**`openapi-typescript` (or orval, or Kubb) against the emitted document.**
Genuinely close, and the reason this record exists rather than a comment. Zero
maintenance, a real ecosystem, and it works today with no generator at all. It
loses on the two things the model knows and the document cannot say: the
operator vocabulary per column, and the response narrowing implied by `select`
and `expand`. If the typed `where` object fails to earn its keep, this is the
fallback, and it is a good one.

**Emit types only, and let the application write its own fetch calls.**
Tempting as the minimal version. It loses because encoding the filter grammar
into a URL — `eq.`/`gte.` prefixes, repeated parameters conjoining — is the
fiddly part, and it is precisely what both hand-written clients open-code. The
encoder is the smallest piece with the highest return.

**Generate hooks.** Rejected above. Frameworks change faster than schemas, and a
hook is a fork waiting to happen.

**Publish an npm runtime package.** Rejected for version skew: a published
client can be a version behind the server it talks to, and nothing detects it.
Emitting into the repo makes that unrepresentable. This is the alternative most
likely to win later, if a consumer needs the client without running the
generator.

**Camel-case the wire types.** Lost to the runtime-mapper cost. Noted because it
is the convention most TypeScript codebases expect, so it will be asked for.

## Revisions

- 2026-07-28 — Written, before any implementation exists. Prompted by reading
  two hand-written clients against Go backends, one of which has a key factory
  and an architecture test, and one of which has a live invalidation bug that the
  factory would have made unrepresentable.
- 2026-07-28 — Reverse expansion landed
  ([ADR-0022](0022-references-declare-their-inverse.md)), so expand-narrowing has
  two shapes rather than one: an object forward, a `Collection<T>` envelope back.
  Recorded that the reverse must not be typed as a bare array, since that
  reproduces on the client exactly the silent truncation the envelope was
  introduced to prevent on the server.
- 2026-07-28 — Built, and the status moved from Exploring to Working. Four
  layers in two emitted files, wired into `example/tasks/web` with a
  hand-written transport beside them, and gated by `mise run test-ts`. The four
  design details the implementation settled are under Consequences; the
  layering, the snake_case wire names, the injected transport and the
  `queryOptions` unit all survived contact unchanged. Node is now pinned in
  `mise.toml`, which is the second toolchain this record said it would cost.
- 2026-07-28 — Renumbered from 0026, and reconciled with keyset pagination
  ([ADR-0027](0027-keyset-pagination.md)), which landed the same day: the
  response envelope carries `next_cursor`, and an infinite-query factory became
  a concrete second emitter rather than something to consider later.
