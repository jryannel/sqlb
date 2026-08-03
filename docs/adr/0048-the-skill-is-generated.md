# ADR-0048: The agent skill is generated where it can be gated, and static only where no check is possible

- **Status:** Exploring — both static skills are written
  ([`skills/`](../../skills/README.md)); the emitter is not. The emitter's shape
  is settled by the four that exist; what a skill has to *say* to change an
  agent's output is not
- **Confidence:** High that a static skill alone is the wrong answer for sqlb,
  because the thing an agent gets wrong is which capabilities a project
  declared and that is per-project by construction. Medium that generating into
  `.claude/skills/` is worth owning a format sqlb does not control. Low on the
  content — no measurement yet says a skill changes what an agent writes
- **Decided:** 2026-08-03
- **Last reviewed:** 2026-08-03

## Context

**The claim that this codebase suits agents currently rests on the agent finding
things.** [vision.md](../vision.md) argues it was a design goal rather than a
retrofit: one file to edit, mistakes as compile errors, rejections that name the
alternative, and the reasoning written down. Every one of those holds. But the
written-down half is 5,400 lines of procedural prose in `docs/` plus 49 decision
records, and a corpus is not a briefing. An agent that has not read
[ADR-0006](0006-capabilities-are-opt-in.md) writes a filter on a column that
never declared `Filterable`, gets a 400 at runtime, and has spent a round trip
learning something the declaration could have told it before it wrote the line.

**The ecosystem has settled on a convention, and it is worth reading what that
convention assumes.** Supabase ships two static skills — one for the products,
one for Postgres best practices — installed with `npx skills add
supabase/agent-skills` at project scope, so the repository carries them and
cloud agents pick them up. Static works there because Supabase's surface is the
same in every repository: Supabase's Postgres is Supabase's Postgres, and a
skill describing it is true before it knows anything about your project.

**sqlb's surface is not the same in any two repositories, because the schema
*is* the surface.** Which tables exist, which columns declared each capability,
which resources are mounted and at what path, whether a table is `Scoped` — that
is the whole of what an agent needs and none of it is knowable from a static
file. A skill that says "capabilities are opt-in" is a footnote. A skill that
says "`invoices.status` is filterable and sortable; `invoices.internal_note` is
`Hidden` and has no wire spelling" is the answer.

**And the repo's own convention cuts against writing rules down at all.** A
skill is a written-down rule that fires when a model notices it, which is
strictly weaker than `generate-check`. `.github`, `impact-check`, `eject-check`
and the rest exist because a convention that is only documented drifts. Any
skill sqlb ships has to answer that objection rather than ignore it.

## Decision

**`sqlb generate` emits the project-specific skill, and `generate-check` gates
it.** One more `Dir`+`File` pair on `codegen.Options`, alongside the TypeScript,
Dart, Go-client and CLI outputs; `codegen/eject_readme.go` is the precedent that
prose is already a generated artefact here, not just Go. It carries the tables
and modules that exist, per column which of `Filterable`/`Sortable`/
`Selectable`/`Hidden` is declared, the mounted resources and their paths, the
schema package's import path, and the two commands that follow an edit —
`sqlb generate` and `sqlb migrate -name …`. Being gated is the load-bearing
half: a skill that has drifted from the schema is worse than no skill, because
it is confidently wrong about the one thing it exists to know, and this is the
only version of the idea that cannot drift.

**The frontmatter description names the project's real tables.** The description
is the trigger, not documentation. "Use this when working with sqlb" does not
fire on "add a due date to invoices", which is the sentence that actually
arrives.

**Two static skills, and only two.** The adoption procedure — `survey`, the
route and query census, the ratio, the pilot — which today is spread across
[surveying-a-codebase.md](../surveying-a-codebase.md),
[with-sqlc.md](../with-sqlc.md),
[refactoring-from-sqlc.md](../refactoring-from-sqlc.md) and four adoption
reviews, and which an agent doing it cold either over-claims or abandons at the
survey. And the query boundary: where the builder ends and `Raw` or sqlc begins.

**The query boundary is the one that earns the exception to preferring a check.**
When a query or a response reaches past the row, the builder degrades, and a
model's instinct is to keep torturing it rather than drop out. Nothing will ever
catch that: the bad code compiles, passes its tests, and answers the request. It
is written down because there is no check to prefer.

**Distribution for the static two is the ecosystem's, not sqlb's.** `npx skills
add` from a repository path — the *user's* invocation, so Node does not become a
build dependency of a Go library. That is the same line the split between the
`ci` and `pages` workflows holds, and it holds for the same reason.

**Two things are deliberately not skills.** `best-practices.md`, because 15 of
its 22 rules are marked **Enforced** — schema validation reports every
authoring mistake at once with the valid alternatives named
([ADR-0011](0011-actionable-errors.md)), which beats a document a model may not
have loaded; the 7 *Recommended* ones are a linter request. And the gate and
release procedure, which is a fact about *this* repository, belongs in
`CLAUDE.md`, and ships nowhere.

### A generated skill is generated from source, and one path into it is not

The schema is first-party Go, so a `SKILL.md` derived from it carries only text
the project's own authors wrote. `sqlb introspect` breaks that assumption:
[`introspect/build.go`](../../introspect/build.go) reads a column's comment
off a live database and calls `f.Comment(…)`, so an adopted database's comments
become schema comments, and an adopted database is not first-party source.

If the emitter carried `Comment` strings, a column comment written by someone
outside the project would become an instruction in an agent's context. So the
emitter carries **structure** — names, types, capability flags, paths — and not
free text, until there is a reason strong enough to pay for sanitising it. This
is the one place where the skill emitter differs from every other emitter, which
pass comments through to DDL and OpenAPI without this concern, because DDL and
an OpenAPI document are read as data and a skill is read as instructions.

## Consequences

**Buys.** The vision's claim becomes an artefact rather than an argument. Opt-in
capabilities stop being a runtime surprise for the caller most likely to trip
over them. And because the file is tracked
([ADR-0018](0018-tooling-scoped-to-tracked-files.md)) it is diffable: a reviewer
sees the skill change in the same pull request as the schema change that caused
it, which is the property that makes generated output reviewable at all.

**Costs.** A fourth emitter over the same declaration, with the maintenance that
implies. A format sqlb does not control — `.claude/skills/` and `SKILL.md`
frontmatter are Anthropic's, and a change there is a change to sqlb's output
with no deprecation window sqlb is in a position to offer. And a directory sqlb
does not own, so a collision with the user's own skill or with the static one is
possible; hence the `sqlb-` prefix and nothing shorter.

**A new public surface.** [compatibility.md](../compatibility.md) has to say
whether the emitted path and shape are frozen or expected to move. Pre-1.0 they
are *Will move*, and the notes carry the mechanical edit — which for a generated
file is "run `sqlb generate`", the cheapest kind of break to own.

**Not a substitute for `CLAUDE.md`.** A skill briefs someone using sqlb. A
project's own conventions are still the project's, and nothing here should
tempt a project into deleting the file that carries them.

## What would change our mind

- **Nothing loads it.** The generated skill's value is invisible from inside the
  repository. If agents produce the same schema edits with and without it, it is
  weight, and the honest response is to delete the emitter rather than improve
  it. This is measurable: the second corpus is eleven schemas that can be edited
  both ways.
- **The skill format churns.** If `SKILL.md`'s shape moves more than about once
  a year, generating into it is generating at a moving target, and a doc plus a
  pointer is the better trade.
- **A static skill would have done.** If the generated content collapses toward
  "read the manifest", then `manifest.json` is the artefact and the skill should
  say where it is. Watch for the emitter's output becoming a restatement of the
  manifest in prose.
- **Free text turns out to be the point.** If what actually changes an agent's
  output is `Comment` strings rather than structure, the trust boundary above
  has to be paid for — sanitised, or fenced as quoted data — rather than
  side-stepped by omitting it.
- **Someone asks for the adoption skill to be generated too.** That is the
  design being rejected here, since a census is about code sqlb has not been
  told about yet; if it is asked for twice the argument is weaker than it looks.

## Cost of change

Low on sqlb's side. It is output, like the TypeScript client — deleting the
emitter breaks nothing that runs, and narrowing what it writes breaks nothing
either.

The asymmetry runs outward. A committed skill in someone's repository that stops
being generated leaves a stale file that still *loads*, and a stale skill is
worse than an absent one because it is authoritative about a schema it no longer
describes. So whatever removes the feature has to delete the file rather than
merely stop writing it, and that is the one part of this decision that is
expensive to get wrong later.

## Revisions

- 2026-08-03 — Written as a proposal, ahead of implementation, prompted by the
  ecosystem convention settling: Supabase's two static skills installed with
  `npx skills add supabase/agent-skills` at project scope. Two things were not in
  the original framing. That the injection path through `introspect`'s column
  comments is real rather than theoretical, which is why the emitter carries
  structure and not prose. And that the repository's existing preference for a
  failing check over a written-down rule is what *selects* which half is
  generated — it is the argument for the split, not an objection to be worked
  around.
- 2026-08-03 — `skills/sqlb-queries` written, which tested the record's central
  claim earlier than expected. Writing it required compiling every sample, and
  that caught three wrong signatures and one stale census row — `Field.ContainsJSON`
  has made jsonb containment first-class since `special-cases.md` was measured.
  Two consequences for the unbuilt half. The argument that a drifting skill is
  worse than none is now *observed* rather than reasoned: prose about an API
  rots in four days, which is the case for generating the project-specific half
  rather than writing it. And the traps turned out to be the payload — what
  earns a static skill its place is not the boundary table but the four failure
  modes that pass their tests, which suggests the emitter should carry structure
  and leave behaviour here.
- 2026-08-03 — `skills/sqlb-adoption` written, completing the static half. It
  turned out to need a section this record did not anticipate: **stop
  conditions**. The adoption procedure's failure mode is not a wrong ratio but a
  census run in the wrong order — routes before the database, when a blocked
  table means the route census cannot be acted on at all — and an evaluation
  that reports "sqlb replaces the API" rather than "the least novel third of
  it". So the skill's load-bearing content is where it says *stop*, which is a
  different shape from `sqlb-queries` and worth noting before a third static
  skill is proposed on the assumption they are all the same kind of document.
