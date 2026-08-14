# ADR-0057: A read is a declared Query, and a row-scoped write stays a declared Action

- **Status:** Working — codegen emits `Query` (`codegen/query.go`); `Mutation`
  shipped in v0.12.0 and was retired the same day, folded back into `Action`'s
  item form
- **Confidence:** High that `Query` is the right split for reads; High that
  `Mutation` was not needed
- **Decided:** 2026-08-14
- **Last reviewed:** 2026-08-14

## Context

[ADR-0043](0043-declared-actions.md) gave a row-scoped write a generated
envelope under the name `Action`, and its own *What would change our mind*
declined to cover the read case: *"Actions become a place to put reads… That
is a different feature with different questions about caching and the query
key, and it needs its own record."* This is that record.

Two shapes already lived inside `Action` with nothing forcing them to share a
name: an item form (fetch under `BeforeQuery`, lock when `Writes` is
non-empty, persist `Writes`, answer with the row) and a collection form (no
fetch, no lock, the transaction reached through `sqlb.TxFrom`). Naming the
read case mattered the same way a reader checking whether `Path` contains
`{id}` mattered for the write case — but whether the row-scoped write also
needed a *second declaration type* did not survive contact with a second
author.

## Decision

**`Query{Name, Path, Params, Reads, Summary, Description}`**, declared with
`TableDef.AddQuery` and mounted with `rest.Query[In, Out]`: a `GET` with no
fetch, no lock, and no obligation check. `Do` is
`func(ctx, sqlb.Executor, In) (Out, error)`, free to call `sqlb.Query[T]`
itself and inherit whatever hooks the `Executor` carries — proven, not
assumed: a query mounted against a workspace-scoped table stayed correctly
isolated between two tenants with no scoping code of its own.

**The row-scoped write stays `Action`, used in its item form.** `Mutation`
was built, wired into codegen, and shipped in `v0.12.0` as a same-named
second declaration for exactly the shape `Action`'s item form already
covers. It was retired the same day: an independent consumer (a real port,
not this codebase's own prototype) reported the identical finding this
record's own *What would change our mind* had named as the trigger —
swapping `Mutation` for `Action` on a live table changed the generated code
by exactly one identifier and nothing about behaviour, route or response
shape. The split-by-name argument above still holds for `Query`, because a
read and a row-scoped write are genuinely different shapes; it never held
for `Mutation`, which was never a different shape from `Action`'s item form
at all — it borrowed Convex's `query`/`mutation`/`action` split, and that
three-way split names differences that exist in a query-*language* RPC
surface and do not exist in sqlb's REST one, where a row-scoped write is
already exactly one verb, one envelope.

**`Query.Reads []*TableDef`** names tables besides the one the query is
declared on. Typed, unlike `Action.Touches`, because a table is already a
named Go value everywhere in this schema style.

## Consequences

**Buys.** A name for the read case ADR-0043 deferred. `Query.Reads` is a seam
a table-scoped invalidation feature can read without inventing anything new.
Codegen emits `Query` the same way it already emits `Action` — `Register`
grows `queries Queries` alongside `actions Actions`. Retiring `Mutation`
removes the "two doors, one destination" trap: picking between `Action` and
`Mutation` for a row-scoped verb read as a semantic choice and was not one.

**Costs.** `Query`'s generated result type is fixed at `[]T` — every row of
the table the query reads, filtered — because `Do`'s actual return type is
the contract this record deliberately left undeclared. A query wanting a
different result (a different model, an aggregate) stays hand-mounted, same
as before codegen knew about `Query` at all. Codegen does not reach
TypeScript, Dart, the CLI, `sqlb.json` or `sqlb impact` yet — Go only.
Retiring `Mutation` on the day it shipped is a real, if small, pre-1.0 break
for the one schema (`example/tasks2`) that had adopted it.

## What would change our mind

- A real project's `Writes` wants typed columns often enough that the
  `AddField` ceremony stops looking like a rare cost.
- A generated `Query` needs a result narrower or wider than `[]T` often
  enough that the fixed shape is the wrong default rather than a reasonable
  first cut.
- ~~`Action`'s item form and `Mutation` are both reached for, in practice,
  for the same shape.~~ **Fired.** See Revisions.

## Cost of change

Widening `Query` — more of the field vocabulary in `Params`, wiring codegen
further — is additive. `Mutation`'s removal already paid the cost this
section originally priced at "deleting a prototype"; it turned out to be one
schema's one call site, because nothing else had adopted it yet. That stops
being true the next time a row-scoped-write-as-its-own-type is tried and
shipped — the second reversal would not be free.

## Open questions I had to answer myself

- **Whether `Query` needs an item form** — a read scoped to one row, the
  counterpart to `Action`'s item form. Not built, not requested.
- **Whether `Reads` ever drives real client-cache invalidation**, or stays
  documentation-only the way `Action.Touches` does. Left open.
- **Whether a generated `Query`'s result should default to `[]T`.** Decided
  unilaterally, while wiring codegen. Not tested against a query that wants
  anything else.

## Revisions

- 2026-08-14 — Written, after prototyping and live-testing `AddMutation`,
  `AddQuery` and `AddAction` together in `example/tasks2` against real
  Postgres, including a test proving tenant isolation held on the query with
  no scoping code of its own.
- 2026-08-14 — Wired `Query` and `Mutation` into codegen: `Register` grew
  `mutations Mutations` and `queries Queries` alongside `actions Actions`.
  Told explicitly: `Action` stays as it is, because its form may still
  change — so the redundancy with `Mutation`'s item form was a known,
  accepted state, not an oversight.
- 2026-08-14 — **`Mutation` retired, folded back into `Action`'s item
  form.** A consumer port (sqlbcoach, onto `v0.12.0` — the tag `Mutation`
  shipped in) confirmed the exact risk this record's own trigger named, with
  a diff rather than a hypothesis. "The form may still change" turned out to
  be a reason to defer the call, not a reason to keep two public names live
  in the meantime. `schema.Mutation`, `TableDef.AddMutation`, `rest.Mutation`
  and `codegen/mutation.go` are deleted; `example/tasks2`'s `complete` verb
  is now a plain item-form `Action`.
