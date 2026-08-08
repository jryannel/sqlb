# ADR-0052: A singleton is an operation that removes the {id}, and it is legal only on a Scoped table

- **Status:** Working
- **Confidence:** Medium
- **Decided:** 2026-08-08
- **Last reviewed:** 2026-08-08

## Context

A table keyed by the column that scopes it has one row per caller. Billing
subscriptions per org, settings per workspace, a profile per user, a sync target
per tenant — the reporting consumer's words were "every payments, webhook or
sync-target table in this platform has it"
([#166](https://github.com/jryannel/sqlb/issues/166)).

Neither exposed shape fitted:

- **`OpList`** answers `{items:[…]}` with one element for a resource that is
  definitionally singular. Every client unwraps `items[0]`, forever, and nothing
  in the document says that is what to do.
- **`OpRead`** at `/billing-subscriptions/{org_id}` asks the client to send back
  its own tenant id — a value the server already holds and the `BeforeQuery` hook
  already enforces. The segment is therefore redundant when it matches and a lie
  when it does not, and a mismatch is a 404 that means *you typed your own name
  wrong*.

The port's conclusion was to expose nothing and keep one hand-written handler.
That was the right call against what existed, and it is the evidence: the module
kept a handler *only* because the generated surface could not say "the caller's
row". This is the same family as [#101](https://github.com/jryannel/sqlb/issues/101)
and ADR-0050 — the mounts an adopting application actually reaches for, offered as
declarations rather than left as the residue of a port.

## Decision

**`OpSingleton` is an operation, and it removes the `{id}` segment from the whole
resource rather than adding a route beside it.**

- `GET <path>` answers the caller's one row, as a bare object, 404 when there is
  none.
- `OpUpdate` becomes `PATCH <path>` and `OpDelete` becomes `DELETE <path>`.
  `OpCreate` is `POST <path>`, unchanged.
- `OpList` and `OpRead` are refused alongside it: the first is the same route, the
  second is the question this exists to delete.
- **It is refused on a model with no `Scoped` column**, in `sqlb generate` and
  again at mount.
- A singleton needs no primary key, which is what lets a table keyed only by its
  tenant column be a resource at all.

It travels the road every other exposure decision travels: the manifest, the
generated mount, the OpenAPI document, the generated skill, the TypeScript, Dart
and Go clients, `restcompat`, and the exit.

## Why the Scoped requirement is the whole design

A singleton's row is **the row the scope hook leaves**. There is no key in the
path and no key predicate in the statement, so the handler builds `SELECT … FROM
billing_subscriptions` and `BeforeQuery` appends `WHERE org_id = $1`.

Read cold, those statements look under-constrained, and that reaction is correct:
without the hook they *are*. On an unconfined table the read answers an arbitrary
row and the `PATCH` reaches every row there is — which is the default-open outcome
ADR-0030 exists to close, arriving through a door it did not cover. So the chain
has to be airtight rather than conventional:

    OpSingleton ⇒ a Scoped column ⇒ ADR-0030's obligation ⇒ a registered hook

Each arrow is a startup refusal. The first is new here; the rest were already
compulsory. This also makes the singleton read the *strongest* case the obligation
check has: elsewhere a missing hook widens an answer the client could at least
narrow by naming a row, and here it chooses the answer at random.

Two rows is a 500 rather than a pick. A singleton matching two is a scoping bug,
and serving the first of them is exactly the silent wrong answer this package
refuses everywhere else.

## Alternatives rejected

**A `Singleton bool` modifier on `schema.REST` and `rest.Options`**, reinterpreting
`OpRead` as `GET <path>`. Fewer moving parts, and it was the shape reached for
first. Rejected because the manifest and `restcompat` would then describe two
different routes with the same word: `operations: ["read"]` at path `/settings`
tells a reader nothing about whether an `{id}` exists, and the whole value of the
manifest is that a consumer can compose a request from it. An operation is what
those documents are keyed by, so the distinction belongs in the operation.

**A second op for the singleton write** (`OpSingletonUpdate`). Rejected as
multiplication: each op bit costs an entry in two mirrored constant sets, the
manifest, `restcompat`, four client emitters, the CLI and the exit, and "the
resource has no `{id}`" is one fact rather than four.

**Allowing `OpRead` beside it.** A resource that serves both `GET /settings` and
`GET /settings/{id}` is coherent, and it re-admits the route the report was about.
Refusals are cheap to relax and expensive to tighten, so it is refused now.

**Re-reading through the read path** so a singleton `PATCH` returns computed
columns that need a bind. That is ADR-0041's open question and not this one; the
write response follows the same rule every other write does (#163).

## Consequences

- A table keyed only by its tenant column is now a resource, so `Options.Columns`,
  `Computed`, `Expandable` and the hooks all apply to it unchanged.
- A singleton reports **no** filterable, sortable or searchable columns in the
  manifest and emits no filter vocabulary in the clients, even where the columns
  declare those capabilities. The one `GET` rejects every query parameter but
  `?expand`, so publishing them would document requests that answer 400 — the
  failure [#143](https://github.com/jryannel/sqlb/issues/143) was.
- Adding `OpSingleton` to an existing resource is additive in `restcompat`;
  swapping `OpList` for it is a break, and is reported as one.

## What would change our mind

- **Nobody declares it.** If adopters keep the hand-written handler because the
  Scoped requirement does not fit their table — a settings row keyed by user in a
  schema whose scope column is the org, say — then the requirement is the thing to
  revisit, not the op.
- **A second shape wants the same route.** If `GET <path>` turns out to want to be
  the caller's row *and* a collection under some other declaration, the modifier
  design was right and this is the wrong axis.
- **The write ops go unused.** If every singleton in practice is read-only, the
  `PATCH`/`DELETE` reinterpretation is surface nobody needed and should be pulled
  back to a read-only shape.

## Cost of change

**Additive.** A schema that declares no `OpSingleton` generates exactly what it
generated before, and the constant is appended to both op masks so no existing
value changes.

**Removing it is a deprecation.** `schema.OpSingleton` and `rest.OpSingleton` are
public surface under [compatibility.md](../compatibility.md), and a resource that
declares one has no other spelling for what it serves.

## Revisions

- 2026-08-08 — Written, against
  [#166](https://github.com/jryannel/sqlb/issues/166). Deferred once from
  [#168](https://github.com/jryannel/sqlb/pull/168) on the grounds that a new op
  is "a release of its own"; that judgement was right about the size and is what
  this record is for.
