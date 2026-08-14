# ADR-0057: A read is a declared Query, and a row-scoped write is a declared Mutation

- **Status:** Working — codegen emits both (`codegen/query.go`,
  `codegen/mutation.go`), live-tested through the fully generated `Register`
  as well as hand-mounted; still one example, no committed code yet
- **Confidence:** Medium that the split is right; Low on the current spelling
  settling, since the naming and the typing of `Writes` both changed shape
  within the same session that built this
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
fetch, no lock, the transaction reached through `sqlb.TxFrom`). The split
matters the same way naming the read does — a declaration should say which
shape it is rather than leave a reader checking whether `Path` contains
`{id}`.

`sqlb.Query[T]` is the read builder every hook and `Do` func in this codebase
already calls. A `TableDef` method plainly named `Query` reads as one more way
to run that, not as a declaration — which is why the method is `AddQuery`,
not `Query`; the type stays named `Query`. `AddAction` and `AddMutation`
followed for consistency alone, not because either collides with anything —
worth saying plainly, since [docs/adr/README.md](README.md) now states that a
method's name is implementation, not architecture, and nothing here revises
[ADR-0043](0043-declared-actions.md)'s decision by picking a different one.

## Decision

**`Query{Name, Path, Params, Reads, Summary, Description}`**, declared with
`TableDef.AddQuery` and mounted with `rest.Query[In, Out]`: a `GET` with no
fetch, no lock, and no obligation check. `Do` is
`func(ctx, sqlb.Executor, In) (Out, error)`, free to call `sqlb.Query[T]`
itself and inherit whatever hooks the `Executor` carries — proven, not
assumed: a query mounted against a workspace-scoped table stayed correctly
isolated between two tenants with no scoping code of its own, because `Do`
shared the same hooked handle every generated read already uses.

**`Mutation{Name, Path, Body, Writes, Touches}`**, declared with
`TableDef.AddMutation` and mounted with `rest.Mutation[T, In]` — byte for byte
`Action`'s item-form envelope. `Action` is unchanged and keeps its item form;
this is additive, not a narrowing of it (see *Open questions*).

**`Query.Reads []*TableDef`** names tables besides the one the query is
declared on — that one is implicit, which is also why it has to be: a query
cannot name its own table inside that table's own not-yet-assigned
initializer. Typed, unlike `Action.Touches`, because a table is already a
named Go value everywhere in this schema style, so nothing is lost by
requiring one.

**`Mutation.Writes` stays `[]string`.** Built typed (`[]*Field`) and reverted
in the same session: a column is normally an inline expression with no named
handle, so typing `Writes` costs a rewrite — pulling the referenced column out
to a package-level var — per column referenced, not per table the way `Reads`
does. `TableDef.AddField`/`AddFields` were added as the escape hatch that
would make that cost affordable if this is ever revisited; nothing requires
using them today.

## Consequences

**Buys.** A name for the read case ADR-0043 deferred, and a name for the
row-scoped write that previously shared both a spelling and a validation path
with the collection case. `Query.Reads` is a seam a table-scoped
invalidation feature (over the outbox change feed `rest.Event` already
carries) can read without inventing anything new. Codegen now emits both —
`Register` grows `mutations Mutations` and `queries Queries` the same way it
already grows `actions Actions` — verified against real Postgres through the
generated `Register` directly, not only through the hand-mounted path.

**Costs.** `Query`'s generated result type is fixed at `[]T` — every row of
the table the query reads, filtered — because `Do`'s actual return type is
the contract this record deliberately left undeclared, and a generated field
still needs one concrete signature. A query wanting a different result (a
different model, an aggregate) is not generated; it stays hand-mounted, same
as before codegen knew about `Query` at all. Codegen does not reach
TypeScript, Dart, the CLI, `sqlb.json` or `sqlb impact` yet — Go only. And
`Action`'s item form still compiles: a table can declare the same shape two
ways today, and nothing refuses the redundant one.

## What would change our mind

- ~~Wiring `Query`/`Mutation` into codegen finds a `Params`/`Body` shape the
  field vocabulary cannot express.~~ **Did not fire.** The one schema wired
  (`example/tasks2`) needed nothing the field vocabulary lacked. One example
  is not the third real case ADR-0043's own trigger asks for, so this stays
  open for whenever a second or third schema tries it.
- A real project's `Writes` wants typed columns often enough that the
  `AddField` ceremony stops looking like a rare cost. The sample behind this
  record's own reversal is one session, not evidence.
- `Action`'s item form and `Mutation` are both reached for, in practice, for
  the same shape — meaning the split added a name without removing a choice,
  and the fix is retiring `Action`'s item form. (Confirmed, for now, as *not*
  the answer: told explicitly that Action stays, on the grounds that its form
  may still change — see Revisions.)
- A generated `Query` needs a result narrower or wider than `[]T` often
  enough that the fixed shape is the wrong default rather than a reasonable
  first cut.

## Cost of change

Widening — more of the field vocabulary in `Params`/`Body`, wiring codegen —
is additive. Narrowing has no price yet: nothing here is committed, nothing
is deployed, and the honest cost of reversing any part of it today is
deleting a prototype. That stops being true at the first commit, and does not
survive a second application adopting either type.

## Open questions I had to answer myself

- **Whether `Query` needs an item form** — a read scoped to one row, the
  counterpart to `Mutation`. Not built, not requested; the one query
  prototyped (`overdue`) is collection-shaped, so there is no evidence either
  way.
- **Whether `Reads` ever drives real client-cache invalidation**, or stays
  documentation-only the way `Action.Touches` does. Left open — building it
  is a further codegen change this record does not make.
- **Whether a generated `Query`'s result should default to `[]T`.** Decided
  unilaterally, while wiring codegen, because a generated field needs one
  concrete signature and `[]T` is the shape the one query built has. Not
  tested against a query that wants anything else.

`Action`'s item form is no longer an open question here: told explicitly it
stays, on the grounds that its form may change later — see Revisions.

## Revisions

- 2026-08-14 — Written, after prototyping and live-testing `AddMutation`,
  `AddQuery` and `AddAction` together in `example/tasks2` against real
  Postgres, including a test proving tenant isolation held on the query with
  no scoping code of its own.
- 2026-08-14 — **Wired into codegen** (`codegen/query.go`,
  `codegen/mutation.go`): `Register` grows `mutations Mutations` and
  `queries Queries` alongside `actions Actions`, each conditionally present.
  Verified two ways — `example/tasks2`'s hand-mounted group path (unaffected,
  now referencing the generated `CompleteTaskInput`/`OverdueTaskParams`
  instead of hand-duplicated ones) and a throwaway server calling the
  generated `Register` directly with no group — both against real Postgres.
  Existing schemas with no `Query`/`Mutation` regenerate byte-identical
  (`example/tasks`, `example/blog` both checked). Also told explicitly:
  `Action` stays as it is, because its form may still change — so the
  redundancy with `Mutation`'s item form is a known, accepted state, not an
  oversight.
