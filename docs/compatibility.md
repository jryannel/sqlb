# Compatibility

What a tagged release promises, and what it deliberately does not.

sqlb is pre-1.0 and has one author. It no longer has no consumers, and that is
what has changed since this document was written at `v0.1.0`: the breaks below
are not hypothetical any more. `v0.7.0` removed the default hook registry
because the ambient one had switched a real adopter's tenant boundary off
without a compile error, and the three unannounced breaks recorded at the
bottom of this page all came out of consumer reports rather than a plan.

That is the reason this document exists, and the reason it is maintained rather
than archived: an unreleased `main` reads as unknown risk, whereas a tag with a
stated blast radius is something a reader can decide against.

Semantic versioning applies from `v1.0.0`. Until then a minor bump may break a
surface listed under **Will move**, and each break is described in [the release
notes](releases.md) with the mechanical edit that fixes it.

What has to be true before that version — and why the gating item is evidence
rather than features — is [the road to 1.0](release-1.0.md).

## Frozen

These are the surfaces worth freezing early, because they are the ones other
code and other *systems* couple to. Breaking them would invalidate stored data
or deployed clients, not just call sites.

- **`Executor`** — `Query` and `Exec` over pgx's types, and nothing more.
  `*pgxpool.Pool`, `*pgx.Conn` and `pgx.Tx` satisfy it as they stand, and so does
  any wrapper written over them. Widening this interface would break every
  implementation at once, so it grows by adding optional interfaces that are
  type-asserted for, never by adding methods.

  **This entry broke once, deliberately, before 1.0.** It used to be
  `QueryContext` and `ExecContext` over `database/sql`.
  [ADR-0040](adr/0040-the-driver-is-a-dependency.md) redefined it, because the
  driver was structurally blocking two things sqlb had already committed to.
  That was a pre-1.0-or-never change: after the tag the same work is a major
  version and a hand migration for every consumer. What it bought, and what it
  cost, is [below](#the-driver).
- **The filter grammar** — the URL syntax (`?status=eq.draft`, `?order=`,
  `?select=`, `?limit=`) and its operator names. This is a wire format: a
  deployed client or an agent driving the API off `sqlb.json` has requests built
  against it. New operators are additive; existing spellings do not change
  meaning. `has`, `hasany` and `hasall` were added for array columns
  ([ADR-0033](adr/0033-array-columns.md)) and are frozen from here on, as are
  their negations `nhas`, `nhasany`, `nhasall` and `nhasdoc`, added later for
  the frontend-parity reason that record's 2026-08-01 revision gives; `contains`
  was *not* extended to mean array containment, and will not be — one name
  meaning two things depending on the column it is applied to is the ambiguity
  the generated clients exist to remove.
- **The wire spelling of a column** — the column's own name, verbatim, in the
  JSON body, the OpenAPI document, the filter grammar's parameter names and both
  generated clients. There is one spelling and no way to configure a second, so
  `?created_at=gte.…` names the same thing the response does. A `Hidden` column
  has no spelling at all. Renaming a column is therefore an API change as well
  as a schema change; `RenamedFrom` makes the database half mechanical and the
  client half is a regeneration plus a compile error per call site
  ([ADR-0036](adr/0036-the-wire-is-the-column-name.md)). More precisely: there is
  **one spelling per deployment**, computed from the column name by the schema's
  declared `WireCase` — `Verbatim` unless the schema says otherwise, so this
  reads as "the column's own name" for every schema that has not chosen. What is
  frozen is that there is exactly one and that it is derived, not which
  derivation a deployment picked; changing a deployment's `WireCase` is a
  breaking change for that deployment, exactly as renaming a column is.
- **The list envelope** — `{items, page, per_page, has_more, next_cursor?,
  total?}`, one shape for every resource. `next_cursor` is absent when there is
  no next page and `total` only when `?count=exact` asked for it. The key names
  and which of them may be absent are frozen; *adding* an optional key is
  additive and breaks no client that ignores unknown ones.
- **The generated DDL's shape** — `migrate.Diff` output for a given pair of
  schemas may improve, but a migration already written and applied is never
  reinterpreted.
- **The cursor payload** — `?cursor=` and the `next_cursor` field are wire
  format for the same reason the filter grammar is, and so is what a cursor
  decodes to: a client holds one across requests, so changing the payload's
  shape breaks a request already in flight rather than a call site. It is
  base64url of JSON and has room for a version field, but that field has to
  arrive before it is needed. Nothing today reads the payload, which is not a
  reason to treat it as private. See
  [ADR-0027](adr/0027-keyset-pagination.md).

## Will move

Named in advance, so the break is a documented plan rather than a surprise.

- ~~**Hook registration.**~~ Moved twice, and the second time it broke on
  purpose. After `v0.1.0` it became `sqlb.On[T]()` over a process-default
  `Registry` with `OnIn[T](r)` for a scoped one
  ([ADR-0020](adr/0020-transaction-scoped-handle.md)). The subtlety this entry
  used to warn about — which registry a statement uses is decided by the
  *dynamic type* of the executor, so passing a raw pool where a scoped
  `*sqlb.DB` was meant silently used the default — turned out to be the whole
  problem rather than a footnote to it, and it switched a real adopter's tenant
  boundary off without a compile error or a failing request.

  So the default registry is **gone** ([ADR-0047](adr/0047-no-default-hook-registry.md)).
  `On[T](r)` takes the registry and `OnIn` is deleted; `rest.PublishChanges[T](r, p)`
  takes it too and `PublishChangesIn` is deleted; `sqlb.New` gives each handle an
  empty registry of its own, which `DB.WithHooks` is how you fill. Every call
  site that did not name a registry is now a compile error, deliberately: the
  failure this prevents is silent, so its migration must not be. The mechanical
  edits are `On[T]()` → `On[T](reg)`, `OnIn[T](reg)` → `On[T](reg)`,
  `PublishChangesIn` → `PublishChanges`, and a `WithHooks(reg)` on the handle.
- ~~**A computed column's nullability.**~~ Landed: `schema.Computed` now
  defaults to nullable, so the generated field is a pointer unless the
  declaration calls `NotNull()`. It moved because the old default was the one
  reading the expression cannot satisfy — a correlated subquery that matches
  nothing is `NULL`, and the failure was a 500 at scan time on rows a fixture is
  unlikely to contain, from a declaration `generate` and the drift gate were
  both happy with ([#147](https://github.com/jryannel/sqlb/issues/147)). The
  mechanical edit is `NotNull()` on every computed column whose expression
  genuinely cannot produce a `NULL`; leaving it off is the safe direction, since
  a pointer scans a non-null value fine. Stored columns are untouched, as is the
  structs-first path, where the Go field's own type has always carried this.
- **Terminal call signatures**, when Go 1.27 arrives. `sqlb.Collect[R](ctx, db,
  b)`, `filter.Apply(b, q)` and the `db` threaded through every terminal call
  all gain method forms, because a method on a concrete type cannot introduce a
  type parameter before then. These are additive — the functions stay.
- **Nested `?expand`.** One level resolves today. If nesting lands it arrives as
  a longer name — `?expand=list.workspace` — under a depth limit, so nothing a
  request can send today changes meaning.
- ~~**`schema.Action`, the referential one.**~~ Landed with declared actions
  ([ADR-0043](adr/0043-declared-actions.md)), which needed that noun for a
  domain verb. The foreign-key type is now `schema.RefAction`; the constants
  every call site actually writes — `schema.Cascade`, `schema.SetNull` and the
  rest — are unchanged, so a schema breaks only if it named the type. The
  mechanical edit is `schema.Action` → `schema.RefAction` in a foreign-key
  position.
- **Backwards cursors.** Paging goes forward only. If `?before=` lands it is a
  new parameter alongside `?cursor=`, so again nothing a request can send today
  changes meaning. [ADR-0027](adr/0027-keyset-pagination.md) says what would
  make it worth building.
- **The `sqlb` command's verb names**, which are still settling. `v0.8.0` moved
  one: `sqlb-survey` became `sqlb survey`, a second binary folded into the one
  command tree ([ADR-0032](adr/0032-sqlb-command.md)). The mechanical edit is
  `./cmd/sqlb-survey …` → `./cmd/sqlb survey …`, and it is the cheap kind of
  break — a script that invoked the old name stops with "no such file" rather
  than doing something subtly different.

  Listed here rather than under *Not covered* because a command line is
  something a person memorises and a CI file hardcodes, so it deserves the
  announcement even though the code behind it is a build-step tool. What it
  does not get is the *Frozen* promise: the boundary ADR-0032 states is that
  needing no schema package is a fact about a verb's arguments rather than a
  reason for a separate binary, and any verb still on the wrong side of that
  moves the same way this one did.

- **The emitted agent skill** — its path, and everything about the document's
  shape. `Options.SkillDir` writes `<SkillDir>/<SkillName>/SKILL.md`, defaulting
  to `sqlb-schema`, and both halves of that are expected to move: the `SKILL.md`
  frontmatter and directory convention belong to the agent tooling rather than to
  sqlb, so a change there is a change to this output with no deprecation window
  sqlb is in a position to offer
  ([ADR-0049](adr/0049-the-skill-is-generated.md)).

  This is the cheapest kind of break to own, and that is the reason it is
  allowed to be here at all: the file is generated, so the mechanical edit is
  `sqlb generate`, and `sqlb check` names the file when it has drifted. The
  emitter is also opt-in — a project that never sets `SkillDir` has no exposure.
  What would *not* be cheap is the reverse, so it is stated here rather than
  discovered: if this emitter is ever removed, the verb has to delete the file
  rather than stop writing it. A stale skill still loads, and an instruction file
  that is confidently wrong about a schema it no longer describes is worse than
  an absent one.

### Three that broke without being listed here first

`v0.6.0` broke three surfaces that were not under *Will move*, and the honest
version is that all three came out of consumer reports rather than a plan. The
[release notes](releases.md#v060) carry each with its mechanical edit, which is
the other half of the promise above; this is the half that was missed, recorded
where the announcement should have been.

- **A computed column is opt-in.** `sqlb.Query[T]()` no longer projects declared
  computed columns; `WithComputed(names…)` asks for them, and
  `rest.Options.Computed` is the hand-written mount's form. A generated resource
  opts into its own table's, so generated endpoints are unchanged. It broke
  because the default charged every reader of a shared model for one screen's
  aggregates, and made an existence check by id fail for want of a bind it had
  no business supplying.
- **The generated Go client is its own package.** `cli.New` takes a
  `*client.Client` from the emitted `client` package. Regenerate, then the edit
  is in the four-line main. It broke because a program wanting the typed encoder
  could not take it without also taking cobra.
- **A nil member of `OneOf` widens the set** rather than binding a `NULL` that
  could never match. Hand-written Go only — the filter grammar has a separate
  `isnull` operator and never routes a nil through `OneOf`.

One behavioural change landed with cursors and is worth stating plainly, because
it affects requests that do not use them: **every list is now ordered
deterministically**, since `filter.Apply` appends the primary key when the sort
does not already settle ties. Responses that were previously in an arbitrary
order within a tie group now have a defined one, and paging no longer repeats or
skips rows across pages. No request changes meaning; some get a different — and
correct — row order.

## Not covered

Anything under `introspect`, `migrate`, `codegen` or `pgtest` that is reached
only from a build step or a test. These are tools, not a runtime surface, and
they change with less ceremony.

What is *not* in this category is the set of files a generator writes into the
repository, because those are checked in and reviewed: `v0.8.0` adds
`runtime.gen.ts` and `runtime.gen.dart` beside the clients, which arrive on the
next regeneration and want committing with it. Nor are the command's verb
*names*, which are [above](#will-move).

## The driver

**sqlb depends on pgx v5, and `database/sql` is not the contract.**
[ADR-0040](adr/0040-the-driver-is-a-dependency.md) decides it and the engine is
built that way: `Executor` is `Query` and `Exec` over `pgx.Rows` and
`pgconn.CommandTag`, and there is one driver rather than two.

This document previously said the opposite, in as many words, and that reversal
is the point rather than an embarrassment: the answer it gave was correct for a
library that extends the standard library, and sqlb turned out to be aiming at
something else. Every evaluation of sqlb so far has asked the driver question
first, which is why it is answered here at length in both directions.

**What you pass.** A `*pgxpool.Pool` is the ordinary case. A `pgx.Tx` is the
interesting one: it is an `Executor` like any other, so sqlb writes join a unit
of work the application opened itself, rather than opening a second transaction
against the same pool. `sqlb.New(tx)` knows it is inside one — `InTx` reports
true and a `WithTx` on it joins rather than nesting — and deliberately does not
take over the boundary, so `AfterCommit` refuses there rather than queueing
callbacks behind a commit sqlb will never perform.

**What it bought**, since the bill below is not free:

- **A shared transaction.** This was impossible before and is the largest
  mechanical cost of adoption that disappears. A library holding a `pgx.Tx` and
  sqlb holding a `*sql.Tx` were in two transactions even against one pool —
  `stdlib.OpenDBFromPool` shares connections, not transaction handles.
- **Arrays at no cost.** `array.go` was 449 lines of array-literal codec written
  because `database/sql` has no array case in either direction
  ([ADR-0033](adr/0033-array-columns.md)). It is deleted. A `[]string` binds as
  `text[]` and scans back from one, and `sqlb.EncodeArray` — a public function
  whose only job was rendering `{a,b}` — is gone with nothing in its place.
- **Per-connection type codecs.** Registering a binary codec on `AfterConnect`
  is a pgx API with no `database/sql` spelling, and
  [ADR-0026](adr/0026-vectors-declare-their-index.md) had to specify a vector
  column over pgvector's *text* form for exactly that reason. Measured, a 50-row
  page of 1536-dimension embeddings cost 2.7× the time and 21× the memory
  through the bridge, because the text literal is parsed element by element in
  Go.
- **`CopyFrom` and `pgx.Batch` are reachable**, through `DB.Tx()`. Reach rather
  than speed: sqlb's multi-row `VALUES` already ran within ~10% of the same
  statement hand-written over pgx, and the whole gap was `CopyFrom`'s absence.
  sqlb still has no builder for either.

**What it cost**, stated plainly:

- **Every consumer inherits pgx.** "Importing sqlb costs nothing" is no longer
  true and has been removed from the pitch rather than softened. What holds
  instead is that the list is short and checked: `mise run deps-check` fails on
  any dependency of the engine that is not pgx or something pgx itself pulls in,
  and on `rest` anything that is not huma.
- **sqlb does not run on another driver.** A population
  [ADR-0001](adr/0001-postgres-only.md) had already made small, but not zero.
- **`Executor` broke, and there was no deprecation path** that preserved both,
  because the point was to have one.

**What would change this.** The optional-interface path this document used to
name as the growth path was the rejected alternative rather than the plan:
type-asserting a narrow capability delivers the capability without the
positioning, and pgvector's binary codec only helps if it is on by default. What
would send it back to `database/sql` is in ADR-0040's *What would change our
mind* — and it is now an expensive question rather than a cheap one, since
reversing costs a second `Executor` break on top of the first and `array.go`
would have to be written again.
