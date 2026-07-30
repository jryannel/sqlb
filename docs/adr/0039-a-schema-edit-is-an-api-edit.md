# ADR-0039: A schema edit is an API edit, and the break is diffed

- **Status:** Exploring — the reasoning is settled; nothing here is built. This
  record decides the shape before `sqlb impact`
  ([#21](https://github.com/jryannel/sqlb/issues/21)) is written, so the shape is
  a decision rather than whatever the first implementation happened to do
- **Confidence:** High that the check is a registry diff and belongs beside
  `migrate.Diff`, and that it must read capabilities the DDL diff ignores.
  Medium that stating-not-gating is the right default, which is the part a team
  with deployed clients would reopen. Lower on the type-and-nullable
  classification, which is where a lazy answer claims coverage it does not have
- **Decided:** 2026-07-30
- **Last reviewed:** 2026-07-30

## Context

The question came in two halves: is REST backward compatibility sqlb's problem
at all, and if it is, can a schema change be *checked* for whether it breaks the
API — the way [ADR-0014](0014-migrations-and-import.md) checks whether a change
loses data.

Two things called "compatibility" have to be kept apart first, because
conflating them is the whole confusion. [compatibility.md](../compatibility.md)
is about **sqlb's own** surface — the `Executor` interface, the filter grammar
as a wire format, the DDL shape. That is the library promising not to break its
consumers. This record is about the **generated application's** REST contract:
when someone edits their `schema.go`, sqlb re-derives the REST surface from it,
so the edit *is* an edit to their API. These are unrelated axes and the freeze
in `compatibility.md` says nothing about the second one.

The reason it is sqlb's problem at all is [ADR-0007](0007-generated-rest-handlers.md):
the OpenAPI document, the parameter list, the response schema and the rejection
allow-list all derive from the model's capabilities, and *cannot disagree*,
because they are one function of the registry. That is exactly what makes sqlb
the one component that knows how a schema edit changes the contract. Answering
"not us" would contradict the record that the generated surface exists under.

What makes the check non-trivial — and separate from the migration check — is
that **DB-safe and API-safe are different judgments, and often inverted:**

| Change | Migration | REST contract |
|---|---|---|
| Rename `title`→`headline` (with `RenamedFrom`) | clean, reversible `RENAME` | **breaks** every client reading or writing `title` |
| Stop exposing a column, or drop `OpRead` | no DDL at all | **breaks** clients, and no DDL diff can see it |
| Add a nullable filterable column | safe | additive: a new optional parameter and field |
| `NULL` → `NOT NULL` on an exposed column | blocking (a lock) | readers unaffected; the **create body now requires it** |
| Drop a column | destructive, commented out | breaking: the field and its filter parameter vanish |
| Widen `int4` → `int8` | rewrites the table | readers fine; a client with a narrow integer overflows |

Two facts fall out of that table. The cleanest migration this project can
emit — a declared rename — is a hard API break, so the API check is not a
by-product of `migrate.Diff`. And the sharpest API breaks produce *no DDL*:
un-exposing a column ([ADR-0006](0006-capabilities-are-opt-in.md) exposure run
backwards) or dropping an `Op` changes the contract while leaving the database
untouched. A check built on the DDL diff would be blind to precisely the breaks
that need catching.

## Decision

### It is us, scoped the way `migrate` is scoped

sqlb checks the contract it *generates*, and says so. The moment a consumer
writes a custom handler, reshapes a response in a `BeforeQuery` hook, versions
their own `/v1` and `/v2`, or fronts the surface with a BFF, the true client
contract is no longer a function of the schema and no longer sqlb's to judge.
This is the same boundary [ADR-0014](0014-migrations-and-import.md) draws for
migrations: `migrate` speaks about the DDL it emits, not about a production
database it never reads. The scope is honest because it is the surface sqlb
actually owns, and dishonest scope is the failure this project exists to remove.

### The contract is a registry diff, a sibling of `migrate.Diff` — not derived from it

The contract is a pure function of `(columns + types) + (capabilities + Ops)`
over the registry, so it is diffable the same way DDL is. The shape is
`restcompat.Diff(old, new *schema.Registry) []Break`: a pure function over two
registries, DB-free and golden-testable, mirroring the symmetry ADR-0014 calls
its whole design. For each exposed model it walks the response fields
(`Selectable`), the filter parameters (`Filterable`), the sort parameters, the
create-body required/optional split, the patch-body fields, and the `Op` set,
and classifies each delta as `Breaking`, `Additive`, or `Neutral`.

The load-bearing word is **capabilities**. `migrate.Diff` ignores them because
they emit no SQL — and that is exactly why it is the wrong engine here. The
un-expose and drop-`Op` breaks live in the capability set, not the column set,
so `restcompat.Diff` reads what `migrate.Diff` throws away. Building it *on top*
of `migrate.Diff` would inherit that blindness; the two are siblings over the
same registries, not a stack.

### The break is stated, not gated — but gateable

Follow the precedent already set for the two hazards this project knows about.
Destructive DDL is *gated* — commented out until a flag — because it is
irreversible. Lock hazards are *stated* — emitted live with the lock named —
because whether a scan matters depends on a row count the schema does not hold.
An API break is the second kind: whether it matters depends on facts the schema
also does not hold — *are there deployed clients, and is this deployment
versioning its API?* A greenfield project breaks its contract hourly and should
not be stopped; a project with shipped clients wants a wall.

So `restcompat.Diff` reports by default and a flag (`--api-compat=error`, or a
CI mode) turns any `Breaking` into a failure. That flag is the
[ADR-0014](0014-migrations-and-import.md) `Migration.Blocking` hook transplanted:
"the hook for a project that does know" what the generator cannot.

### Diffed against a checked-in contract snapshot

"Backward compatible relative to *what*?" needs a concrete, reviewable answer,
not a guess. sqlb emits the contract descriptor as a committed artefact and
diffs the current registry against it on codegen — the same move the shadow
database makes for drift ([ADR-0014](0014-migrations-and-import.md)): declared
history versus current state, checked in so the comparison is something a
reviewer reads rather than something a tool asserts about a state nobody can
see. The snapshot is the contract, not the OpenAPI JSON, for the reason the next
section gives.

### The surface is `sqlb impact`

The CLI name is already reserved ([#21](https://github.com/jryannel/sqlb/issues/21),
named in [the road to 1.0](../release-1.0.md) as an adoption argument with no
scope written down). This record is that scope: `sqlb impact` runs
`restcompat.Diff` between the checked-in snapshot and the current schema and
prints the blast radius of the edit, one line per break, in the allow-list voice
[the vision](../vision.md) uses for rejections — *what broke, for whom, and what
the additive alternative would have been.*

## Consequences

**What this buys.** The break a schema edit causes is legible at edit time,
from the schema file, with no server running and no client deployed to discover
it at runtime — the same shift ADR-0014 made for data loss. It catches the two
breaks nothing else can: the rename that is a clean migration and a wire break
([ADR-0036](0036-the-wire-is-the-column-name.md) makes a rename a wire break by
construction), and the un-expose that is no migration at all. And it composes
with the generated clients: a break flagged here is the same break the
regenerated TypeScript or Dart client turns into a compile error
([ADR-0028](0028-typescript-client.md)), so the check and the client agree
because they read the same capabilities.

**What this costs.** A second diff engine to keep correct beside `migrate.Diff`,
over the same registries but a different projection of them — two functions that
must not quietly diverge in how they read a type change. A checked-in snapshot
is a new artefact in the repository with its own format to freeze, and freezing
a format is the most expensive thing ADR-0014 records. And the classification
has a genuinely hard core: the reader/writer asymmetries below are where a
half-built classifier is worse than none.

**The classification must be proven both ways, or it lies.** A nullable column
going `NOT NULL` is compatible for readers and breaking for writers, because the
create body's required set grows; type widening is compatible for readers and a
possible overflow for a narrow client. A classifier that reports one side and
forgets the other "fires sometimes", which
[ADR-0016](0016-guards-proven-both-ways.md) says reads as coverage it does not
have — a green `impact` that missed the writer break is worse than no check,
because it was believed. Either each such change is classified on both the read
and the write body, with a test in each direction, or it is not claimed at all
and reports `Unknown` rather than `Neutral`.

## What would change our mind

- **If nobody consumes the snapshot** — teams run `sqlb impact`, see a break,
  and regenerate anyway without versioning — then the artefact is ceremony and
  the check should collapse to a warning printed inline during codegen, with no
  file to maintain. This is the same failure mode ADR-0014 watches for with
  destructive changes uncommented without reading.
- **If the default should have been to gate.** If a port ships a breaking change
  to a real client because the report scrolled past, "stated not gated" was
  wrong for the surface that clients coupled to, and the default flips —
  breaking becomes a refusal with an opt-out, the way destructive DDL already
  is. The reason not to start there is that a greenfield schema breaks its
  contract constantly and a wall would train people to pass the flag reflexively.
- **If `restcompat.Diff` and `migrate.Diff` drift** on how they read a shared
  change — a type widening one calls safe and the other calls a rewrite — the
  two projections have a shared core that should be one function they both call,
  not two that agree by coincidence.
- **If the snapshot format needs to change after a team has committed one**, that
  is the ADR-0014 lesson repeating: decide the format deliberately and early,
  because reconciling against committed snapshots is the expensive direction.
- **If OpenAPI-diff turns out to be what people actually want** — because they
  publish the spec and hand-tweak operations the registry does not describe —
  then the snapshot should be the OpenAPI document and the diff should run over
  it, accepting the edge cases that come with it. The registry diff stays the
  source of truth for the surface sqlb generates; the OpenAPI diff would be a
  second check for the surface a team hand-extends.

## Cost of change

**Free today, because none of it is built and there are no consumers.** The
whole record is a proposal, so revising it costs an edit to this file.

**Asymmetric once a snapshot is committed anywhere real.** Before that, the
classification rules and the snapshot format are as free to rewrite as
`migrate.Diff` was before its first migration was applied. After a team commits
a `contract.json` and gates CI on it, the format is a permanent artefact the way
the migration history is: changing its shape or the meaning of `Breaking` means
reconciling against snapshots already in other people's repositories. The
classification rules are cheaper to change than the format, because they produce
a report rather than a stored file — a rule that gets stricter only makes a
green build red, which is the safe direction.

**Widening is additive.** Adding a new break category, or teaching the
classifier a change it currently reports `Unknown`, breaks nothing that ran
before. Narrowing — deciding something previously `Breaking` is actually fine —
is the direction to move slowly, because a client may have been versioned on the
strength of the old answer.

## Alternatives considered

**Say it is not us.** The tidy boundary: sqlb generates, the consumer owns their
API. Rejected because [ADR-0007](0007-generated-rest-handlers.md) already makes
the contract a function sqlb computes, so sqlb is the *only* component that can
answer the question cheaply. "Not us" would leave the one tool that knows the
answer refusing to say it.

**Derive the check from `migrate.Diff`.** The obvious reuse, and wrong on
consequence: the DDL diff cannot see a capability change, so it would miss
un-expose and drop-`Op` — the breaks with no migration, which are exactly the
ones a migration-shaped mind fails to anticipate. Reusing it would build the
blindness in.

**Diff the OpenAPI document instead of the registry.** It captures hand-tweaked
operations the registry does not describe, and there are off-the-shelf rules for
it. Rejected as the *primary* engine because it needs the Huma registration to
run, drifts from the pure-function-over-registries purity every other diff in
this project has, and OpenAPI diffing carries a long tail of edge cases. Kept as
a named future for teams that publish and extend the spec, per *What would
change our mind*.

**Gate by default, like destructive DDL.** Rejected for the reason above: a
greenfield schema breaks its contract as a matter of course, and a wall on every
break trains people to pass the opt-out without reading — the same failure that
would defeat the destructive guard. Stating with an opt-in wall is the version
that stays read.

**Just add a section to `compatibility.md`.** The zero-code answer: write down
that a schema edit is an API edit and let the reader work out the blast radius.
Rejected as insufficient rather than wrong — it is worth writing regardless, but
a prose warning does not enumerate the breaks in a specific edit, and enumerating
them from the schema is the entire value.

## Revisions

- 2026-07-30 — Written, before implementation. Prompted by the question of
  whether REST backward compatibility is in scope at all. It is, bounded to the
  generated surface, and the check is a registry diff beside `migrate.Diff`
  rather than derived from it, because the sharpest breaks — rename, un-expose,
  drop-`Op` — are invisible to a DDL diff. Gives scope to `sqlb impact`
  ([#21](https://github.com/jryannel/sqlb/issues/21)), which the road to 1.0 had
  named without defining.
