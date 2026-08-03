# ADR-0039: A schema edit is an API edit, and the break is diffed

- **Status:** Exploring — the reasoning is settled; nothing here is built. This
  decides the shape before `sqlb impact`
  ([#21](https://github.com/jryannel/sqlb/issues/21)) is written
- **Confidence:** High that the check is a registry diff beside `migrate.Diff`
  and must read capabilities the DDL diff ignores; Medium that stating-not-gating
  is the right default; lower on the type-and-nullable classification, which is
  where a lazy answer claims coverage it does not have
- **Decided:** 2026-07-30
- **Last reviewed:** 2026-08-03

## Context

Two things called "compatibility" have to be kept apart.
[compatibility.md](../compatibility.md) is about **sqlb's own** surface — the
`Executor` interface, the filter grammar, the DDL shape. This record is about the
**generated application's** REST contract: when someone edits their `schema.go`,
sqlb re-derives the REST surface from it, so the edit *is* an edit to their API.

It is sqlb's problem because of [ADR-0007](0007-generated-rest-handlers.md): the
document, the parameter list, the response schema and the rejection allow-list
all derive from the model's capabilities and cannot disagree, which makes sqlb
the one component that knows how a schema edit changes the contract.

**DB-safe and API-safe are different judgments, and often inverted:**

| Change | Migration | REST contract |
|---|---|---|
| Rename `title`→`headline` (with `RenamedFrom`) | clean, reversible `RENAME` | **breaks** every client reading or writing `title` |
| Stop exposing a column, or drop `OpRead` | no DDL at all | **breaks** clients, and no DDL diff can see it |
| Add a nullable filterable column | safe | additive |
| `NULL` → `NOT NULL` on an exposed column | blocking (a lock) | readers fine; the **create body now requires it** |
| Drop a column | destructive, commented out | breaking |
| Widen `int4` → `int8` | rewrites the table | readers fine; a narrow client overflows |

Two facts fall out. The cleanest migration this project can emit — a declared
rename — is a hard API break, so the API check is not a by-product of
`migrate.Diff`. And the sharpest breaks produce *no DDL*: un-exposing a column or
dropping an `Op` changes the contract while leaving the database untouched.

## Decision

**It is us, scoped the way `migrate` is scoped.** sqlb checks the contract it
*generates*. The moment a consumer writes a custom handler, reshapes a response
in a hook, versions their own `/v1`, or fronts the surface with a BFF, the true
contract is no longer a function of the schema and no longer sqlb's to judge —
the same boundary [ADR-0014](0014-migrations-and-import.md) draws by speaking
about the DDL it emits rather than a database it never reads.

**The contract is a registry diff, a sibling of `migrate.Diff`, not derived from
it.** `restcompat.Diff(old, new *schema.Registry) []Break` is a pure function
over two registries, DB-free and golden-testable. For each exposed model it walks
the response fields, filter and sort parameters, the create-body required/optional
split, the patch-body fields and the `Op` set, classifying each delta `Breaking`,
`Additive` or `Neutral`.

The load-bearing word is **capabilities**. `migrate.Diff` ignores them because
they emit no SQL, which is exactly why it is the wrong engine: the un-expose and
drop-`Op` breaks live in the capability set. Building on top of it would inherit
that blindness.

**The break is stated, not gated — but gateable.** Destructive DDL is gated
because it is irreversible; lock hazards are stated because whether they matter
depends on a row count the schema does not hold. An API break is the second kind:
whether it matters depends on whether there are deployed clients and whether the
deployment versions its API. So `restcompat.Diff` reports by default and a flag
turns any `Breaking` into a failure — ADR-0014's `Migration.Blocking` hook
transplanted.

**Diffed against a checked-in contract snapshot.** "Backward compatible relative
to *what*" needs a concrete, reviewable answer. The descriptor is committed and
diffed against the current registry on codegen — the same move the shadow
database makes for drift, checked in so the comparison is something a reviewer
reads.

**The surface is `sqlb impact`**, which prints the blast radius one line per
break, in the allow-list voice this project uses for rejections: what broke, for
whom, and what the additive alternative would have been.

## Consequences

**Buys.** The break a schema edit causes is legible at edit time, with no server
running and no client deployed to discover it at runtime — the same shift
ADR-0014 made for data loss. It catches the two breaks nothing else can: the
rename that is a clean migration and a wire break, and the un-expose that is no
migration at all. And it composes with the generated clients, because the check
and the client read the same capabilities.

**Costs.** A second diff engine to keep correct beside `migrate.Diff`, over the
same registries but a different projection — two functions that must not quietly
diverge on a type change. A checked-in snapshot is a new artefact with its own
format to freeze, and freezing a format is the most expensive thing ADR-0014
records.

**The classification must be proven both ways, or it lies.** A nullable column
going `NOT NULL` is compatible for readers and breaking for writers; type
widening is compatible for readers and an overflow for a narrow client. A
classifier that reports one side and forgets the other "fires sometimes", which
[ADR-0016](0016-guards-proven-both-ways.md) says reads as coverage it does not
have — a green `impact` that missed the writer break is worse than no check,
because it was believed. Either each change is classified on both bodies with a
test in each direction, or it reports `Unknown` rather than `Neutral`.

## What would change our mind

- **Nobody consumes the snapshot** — teams see a break and regenerate anyway.
  Then the artefact is ceremony and the check collapses to a warning printed
  during codegen, with no file to maintain.
- **The default should have been to gate**, because a port shipped a breaking
  change to a real client and the report scrolled past. The reason not to start
  there is that a greenfield schema breaks its contract constantly and a wall
  would train people to pass the flag reflexively.
- **The two diffs drift** on a shared change — then their shared core should be
  one function they both call, not two that agree by coincidence.
- **The snapshot format needs to change after a team has committed one** — the
  ADR-0014 lesson repeating.
- **OpenAPI-diff turns out to be what people want**, because they publish the
  spec and hand-tweak operations the registry does not describe. Then that is a
  second check for the surface a team hand-extends; the registry diff stays the
  source of truth for what sqlb generates.

## Cost of change

**Free today, because none of it is built.** **Asymmetric once a snapshot is
committed anywhere real**: after a team commits a `contract.json` and gates CI on
it, the format is a permanent artefact the way the migration history is. The
classification rules are cheaper than the format, because a stricter rule only
turns a green build red, which is the safe direction. **Widening is additive**;
narrowing — deciding something previously `Breaking` is fine — is the direction to
move slowly, because a client may have been versioned on the old answer.

## Revisions

- 2026-08-03 — **The contract has a schema-level property, and the walk had no
  place to put one.** This record describes the diff as a walk "for each exposed
  model", and the implementation followed it exactly: every `Break` carried a
  resource path, and every comparison matched columns by column name. Then
  [ADR-0036](0036-the-wire-is-the-column-name.md)'s amendment added `WireCase`,
  which belongs to the schema rather than to any resource and respells every
  field of every one of them — and passed straight through, because both
  snapshots recorded the same column names.

  The fix is small and the shape is the point: a `Break` may now have an empty
  resource, and the snapshot records the schema's wire case beside its
  resources. What generalises is the question to ask of the next contract
  property — *whose* is it — because a per-resource walk cannot report a
  property that is not per resource, and will be silent rather than wrong.
- 2026-07-30 — Written before implementation, prompted by the question of whether
  REST backward compatibility is in scope at all. It is, bounded to the generated
  surface, and the check is a sibling of `migrate.Diff` rather than derived from
  it, because the sharpest breaks are invisible to a DDL diff. Gives scope to
  `sqlb impact`, which the road to 1.0 had named without defining.
- 2026-07-30 — Condensed.
