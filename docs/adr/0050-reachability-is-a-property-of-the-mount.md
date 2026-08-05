# ADR-0050: Reachability is a property of the mount

- **Status:** Working
- **Confidence:** Medium
- **Decided:** 2026-08-05
- **Last reviewed:** 2026-08-05

## Context

A table has one model and one `Expose`, and every capability is declared on the
column: `Filterable`, `Sortable`, `Hidden`. That is the right shape for a table
served one way, which is nearly all of them.

It is the wrong shape for a table served two ways. A headless shop reads
`products` from a public storefront and from an admin panel behind a shared
secret; the admin surface exists precisely to serve `cost_price_minor`,
`supplier` and `internal_notes`, and the storefront must not. `Hidden` hides a
column from both, because it is a property of the model and there is one model.
`Expose` cannot add a second resource, because `schema/table.go` assigns
`t.rest = &r` and a second call replaces the first. So the split left the
schema-first path for one of its two halves — and the public/admin pair is not
an adoption path or a legacy-struct case, which is what
[structs-first](../start/structs-first.md) is otherwise for
([#148](https://github.com/jryannel/sqlb/issues/148)).

The mechanism was already half-built and known to work. `rest.Options.Computed`
is per-resource column reachability with the general rationale written into its
own doc comment — *"a model is shared"* — and it was added for cost rather than
for disclosure ([#92](https://github.com/jryannel/sqlb/issues/92)). An ordinary
column is shared the same way.

## Decision

Reachability is a property of the mount, and `rest.Options.Columns` says it.

A resource that names columns serves those and no others: absent from the
response, absent from the `SELECT`, not filterable, not sortable, not searched,
not nameable in `?select`, not settable by a body, and not named in the list a
rejection offers back. `filter.Options.Columns` carries it into the parser and
into `Apply`'s default projection, so what a request may name and what the
database is asked for cannot disagree. Empty means every column, which is what
a generated resource emits and what every existing mount relies on.

`Expose` stays singular and the emitters keep one resource per table. The
privileged half is a hand-written `rest.Resource` call over the generated model.

## Consequences

**Buys.** A public and a privileged surface over one table, in one process, over
one model. The narrowed half keeps the generated model, the typed column facade,
the manifest and the drift gate; only the mount is hand-written, where the
alternative — a second `Describe`d struct — gives up all four. And the OpenAPI
parameters now follow the resource rather than the model, which also closes a
gap `Options.Computed` had left: a resource that declined a computed column was
still publishing a filter parameter for it.

**Costs.** Three, and they are the shape of the boundary rather than bugs.

The **response schema** in the OpenAPI document is the model's Go type,
registered once as a component and shared by every mount of it, so it still
lists the columns the narrowed resource does not serve. Runtime responses omit
them; a client generated from the document carries optional fields that are
always absent. Narrowing it needs a per-resource Go type.

The **create and update body types** are the caller's, so a narrowed mount
reusing the wide resource's bodies documents fields it will not write. It does
not write them — a column outside the list is cleared off the row a body
produced, exactly as a `ReadOnly` one is — but a resource narrowed for
disclosure usually wants `Ops` without the write operations.

And the narrowing is **a mount-time argument rather than a schema property**,
which is the thing the report asked not to be true. A model with no field for
`cost_price_minor` has no code path that can return it, this year or next; a
model that has the field and a mount that declines it is one `Options` value
away from serving it. That is a weaker guarantee, honestly weaker, and the
schema-side version is what would replace it.

## What would change our mind

- **A second surface routinely wants its own clients.** If narrowed mounts
  accumulate and each one grows a hand-written TypeScript or Dart client beside
  it, the emitters are the thing that should have changed, and `Expose`
  appending with a `Columns` allowlist on `schema.REST` is the shape — two
  generated resources, both on the drift gate.
- **The response schema's width causes a real disclosure.** It has not yet: the
  values are absent and the parameters are gone. If a document reader treats the
  schema as the surface — an agent deciding what to request, a generator
  emitting a client somebody reads — the per-resource schema stops being a
  nicety.
- **`Columns` gets used for cost rather than disclosure.** If resources narrow
  to avoid reading wide rows, this is a projection feature wearing a visibility
  hat, and `?select` with a server-side default is the thing being asked for.

## Cost of change

**Widening is free.** Dropping `Columns` from a mount restores the wide surface,
and a schema that never sets it generates exactly what it generated before.

**Narrowing an existing resource is a wire break**, in the way
[ADR-0039](0039-a-schema-edit-is-an-api-edit.md) means: a deployed client
holding a filter or a response field loses it with no DDL in sight. `sqlb
impact` sees it only for generated resources, since the narrowed mount is
hand-written and not in the contract snapshot — which is the one place this
decision costs the gate something real.

**Replacing it with the schema-side version is additive.** `schema.REST` gaining
a `Columns` and `Expose` appending would leave `rest.Options.Columns` as the
thing codegen writes into the generated mount, which is the arrangement
`Options` already has with `schema.REST` everywhere else.

## Revisions

- 2026-08-05 — Written, against [#148](https://github.com/jryannel/sqlb/issues/148).
  The record exists mostly to name what was *not* built and why the weaker
  answer was taken first: the reporter had no view on which of the three options
  was right, and this is the one whose cost is a paragraph rather than a
  redesign of every emitter.
