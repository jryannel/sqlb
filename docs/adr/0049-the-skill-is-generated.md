# ADR-0049: The agent skill is generated where it can be gated, and static only where no check is possible

- **Status:** Working — both static skills are written
  ([`skills/`](../../skills/README.md)), and the emitter is
  `Options.SkillDir`, covered by `sqlb check` and exercised by
  `example/tasks`, whose emitted skill is committed
- **Confidence:** High that a static skill alone is the wrong answer for sqlb,
  because the thing an agent gets wrong is which capabilities a project
  declared and that is per-project by construction. High that the trust boundary
  is drawn in the right place, because the guard that enforces it has failed on
  purpose. Medium that generating into `.claude/skills/` is worth owning a format
  sqlb does not control. Low on the content — no measurement yet says a skill
  changes what an agent writes, which remains the first thing that would kill it
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

- **Nothing loads it.** Answered on 2026-08-03, and the answer is **no accuracy
  effect anywhere it has been looked for**: 100 direct questions and 80
  task-shaped ones across 60 runs, with the control arm at ceiling in every
  round. What survives is a cost effect — roughly 3× fewer tool calls and 2–6×
  less wall clock. That is a real benefit and a much smaller claim than this
  record was built on. The emitter stays because it is cheap, gated, and buys
  latency; if the latency stops mattering, it should go.
- **The trigger does not fire.** Still open, and only partly investigable from
  here — see the 2026-08-03 note. What is now known is that the mechanism exists
  and the emitted path participates in it; what is not known is whether a model
  chooses to load this skill in competition with everything else installed. The
  listing has a context budget and descriptions of lesser-used skills get dropped
  to stay inside it, which is a trigger failure sqlb cannot fix from the emitter.
- **It is too large to load speculatively.** Measured once and acted on: the
  per-column table went, taking 62% of the document with it, and the cost is now
  ~290 bytes per resource. Still linear and still uncapped, so the condition
  stands rather than closes — if a schema arrives that puts this back over about
  40 KB, the answer is an index with per-resource detail on demand rather than
  another round of trimming.
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
- 2026-08-03 — The emitter landed as `Options.SkillDir`, and the record moves to
  Working. Three things the design above got wrong or left open.

  **Hidden columns are absent, not labelled.** This record originally said the
  skill would carry which columns are `Hidden`. It does not, because the
  manifest's reason for omitting them — a name is itself information — applies
  more strongly to a file read as instructions, and building on
  `BuildManifest()` makes the safe answer the default one. The skill says the
  column lists are the wire surface rather than the table, and points at the
  declaration.

  **The trust boundary needed a guard, and it has one.** `TestSkillCarriesNoComments`
  injects an instruction-shaped string as both a table and a column comment and
  requires it to be absent while the column is still described — so it cannot
  pass by dropping the table. Proven both ways per
  [ADR-0016](0016-guards-proven-both-ways.md): with the comment carried through
  on purpose, it failed and named the leak.

  **The schema package cannot be derived, so it is configured.** The emitted
  commands want the pattern `sqlb generate` takes, and the emitters are given a
  registry rather than the argument that produced one. `SkillSchemaPackage` is
  the override and `go generate ./...` is the fallback — a real command for any
  project carrying the directive, rather than an invented path.

  What has not changed is the thing to watch. The skill is committed, gated and
  correct, and none of that is evidence that it changes what an agent writes.
- 2026-08-03 — **Measured against the corpus, and it found a size problem.**
  Twelve `studio_*` application databases, 565 tables, of which 404 are domain
  tables once the 161 goose-per-module bookkeeping tables are excluded the way
  `sqlb survey` excludes them. Each was introspected, every addressable table
  exposed, and the skill rendered.

  Two corrections to this record. The second corpus is **twelve applications of
  a modular monolith**, not "eleven schemas" — the earlier wording conflated it
  with `subject-go`, which is one application of 84 tables. And what was measured
  is the *artefact*: whether it is fit to load at real scale. Whether it changes
  what an agent writes is still untested, and no amount of this substitutes.

  **The frontmatter guard holds.** The description stayed between 403 and 633
  characters across schemas from 6 to 139 tables, so capping the name list at
  twelve was the right call and is now evidence rather than caution.

  **The document is too large, and that is the finding.** It is linear in exposed
  resources at 760–870 bytes each with no cap: 7 KB at 6 resources, 25 KB at 29,
  38 KB at 44, 96 KB at 127. It crosses the size of the *hand-written*
  `sqlb-queries` skill at about twelve tables, and at the corpus median it is
  three to four times larger. A 96 KB instruction file is not something a model
  loads on the chance it is relevant, which was the entire premise of putting the
  trigger in the frontmatter.

  Where the bytes are, measured by section: the per-resource **column tables are
  44–49%** of the document, the capability tables 27–31%, the resource index 9%.
  So the column tables — added late, and the part that describes what a *response*
  carries rather than what a *request* may name — are roughly half the cost of
  the artefact and the least load-bearing half of its content. Dropping them
  nearly halves it.

  **A near-miss worth recording**, because it is the shape of error this kind of
  measurement invites. The first pass reported that only 67% of tables keep a
  primary key through introspect, with 161 lost to identity columns — which read
  as a serious adoption finding against
  [ADR-0034](0034-one-column-addresses-a-row.md)'s priorities. Checking what
  those tables *were* showed all 161 are migration bookkeeping and none is a
  domain table. Excluding them, 27 of 404 domain tables (6.7%) are not addressable
  by a single column, which is ADR-0034's own estimate rather than a challenge to
  it. The lesson is that a corpus count over a database includes tables no
  adoption would ever declare, and a ratio that does not exclude them measures
  the migration runner.

  **That 67% is superseded and kept only for the lesson.** Re-measured against the
  same twelve databases after
  [ADR-0048](0048-auto-incrementing-keys.md) landed: **542 of 542 tables with a
  single-column primary key now keep it through introspect**, up from 377. Making
  an auto-incrementing key declarable closed the whole gap, bookkeeping tables
  included. What survives from the paragraph above is the methodological point,
  not the number.
- 2026-08-03 — **The per-column table is gone, and the measurement is why.** Re-run
  over the same twelve applications: 96 KB → 37 KB at 127 resources, 38 KB → 15 KB
  at 44, 25 KB → 12 KB at 29. A 62% reduction at the top end rather than the ~50%
  predicted, because a table has more columns than it has capability entries, so
  the section being removed grew faster than the ones kept. The cost is now ~290
  bytes per resource, and the document crosses the hand-written `sqlb-queries`
  skill at about 22 resources rather than 12.

  **Enum values stayed**, and that is the one line of the old table worth keeping.
  Everything else it carried — types, nullability, which columns are read-only —
  describes what a *response* holds, is already in the generated models, and does
  not change whether a request is accepted. An enum does: `?status=eq.active`
  against `todo|in_progress|blocked|done` is a rejection, and the valid list is
  not guessable from the column name. So one `Values:` line per resource, and
  none at all where nothing is constrained.

  Two guards came out of this rather than the feature. `TestSkillCarriesNoColumnTable`
  exists because a regression that reinstates that table doubles every generated
  skill in every project, which is not the kind of thing a reviewer notices in a
  diff. And `TestSkillOmitsHiddenColumns` was strengthened: with the column table
  gone its old assertion matched a capability row instead, so it had started
  passing for the wrong reason — the exact failure
  [ADR-0016](0016-guards-proven-both-ways.md) is about, found by deleting the
  thing it was meant to be watching.
- 2026-08-03 — **A/B'd against agents, and the result is narrower than the
  premise.** Twenty runs on one model, ten per round, control given the 328-line
  schema declaration and treatment given the same plus the skill inlined. Both
  arms could read the schema; neither had any other sqlb context.

  **Round 1, ten direct questions** — is `author_id` filterable on tasks, what
  does `?search` cover on workspaces, how do you complete a task, and so on.
  **Both arms scored 50/50.** No accuracy difference at all. What differed was
  cost: the control averaged 4.0 tool calls and 59 s, the treatment 1.0 and 9 s.
  So on a schema small enough to read, the skill buys a 6× round-trip saving and
  nothing else — and this record's original premise, that an agent gets these
  facts *wrong*, is simply not true for a capable model asked directly.

  **Round 2, one task-shaped request** where the wrong answer is silently
  plausible: list tasks *created by* a user, finished, newest first, with the list
  expanded. `author_id` is not filterable, and nothing about the request says so.
  **Two of five control runs emitted the unfilterable filter and reported
  "NOTES: none".** Five of five treatment runs caught it. That is the failure the
  emitter exists for, reproduced — but at n=5 per arm, 2/5 against 0/5 is
  Fisher p ≈ 0.44, which establishes the failure mode and not its rate.

  **The size decision cost something, and only this found it.** One treatment run
  used *zero* tool calls, correctly reported that no filterable creator column
  exists — and named it `created_by`, because it does not exist in the skill. The
  document lists what is filterable, so it can say a column is not, but it can no
  longer say what that column is *called*. That is a direct consequence of
  dropping the per-column table one commit earlier. It is not obviously the wrong
  trade — 96 KB → 37 KB for one wrong identifier in one run of five — but it is a
  real cost and it should be re-examined if a cheaper way to carry non-filterable
  column names appears.
- 2026-08-03 — **Powered up to 20 per arm with a second trap, and the round-2
  effect did not replicate.** Same model, same isolation, one prompt carrying two
  traps of different kinds: a column that is not `Filterable` (`author_id` on
  tasks) and an *operation* that is not exposed (no delete on comments). The
  second was added because the first only tested capabilities, and an absent
  operation fails by a different mechanism.

  **Control caught both traps in 20 of 20 runs. Treatment caught both in 20 of
  20.** Zero misses on either side, 80 trap-instances scored. The 2-of-5 silent
  failure measured the day before was an artefact of the prompt, not of the
  missing skill: asking for one request with one NOTES line lets an agent skip
  the check, and asking for two requests with two NOTES lines primes it to look.
  A Wilson 95% interval on 0/20 puts the control's true miss rate below about
  16%, so a small effect cannot be excluded — but the effect this record was
  built on is not there.

  **What is left is cost, and it is consistent.** Control averaged 3.5 tool calls
  and 47 s; treatment 1.1 and 19 s — 3.2× and 2.4×. Round 1's ratios were larger
  (4.0 vs 1.0 calls, 59 s vs 9 s). The control's real cost is *understated*: two
  control runs spawned research subagents of their own, one of them burning 22
  tool calls and two minutes, and that work does not appear in the parent's
  counters.

  **The naming cost recurred at a measurable rate.** Two of 25 treatment runs
  across both days named the creator column `created_by` — correctly reporting
  that no such filter exists while inventing the identifier — because the skill
  lists filterable columns and therefore cannot name an unfilterable one. About
  8%, and the cleanest argument yet for finding a cheap way to carry
  non-filterable column names.

  **What this changes.** The honest case for the emitter is now latency and
  round-trips, not correctness. That is worth ~290 bytes per resource of a gated,
  generated file, and it is a weaker claim than "an agent gets this wrong" — which
  three rounds of measurement have failed to reproduce against a model that can
  read the declaration. The premise should not be restated in this record or
  anywhere else without new evidence.
- 2026-08-03 — **Went after the trigger, and found two bugs in this emitter
  instead.** The honest headline first: whether a model *chooses* to load this
  skill could not be tested from the session that wrote it, because a skills
  directory that did not exist when the session started is not watched. Invoking
  the emitted skill by name — plain and directory-qualified — failed in both
  forms. That is the documented behaviour rather than a broken path, but it means
  the answer needs a fresh session and is still owed.

  What the investigation did settle is worth more than the question it failed to
  answer.

  **The path is right for the ordinary case and late for the nested one.** A
  `.claude/skills` at the module root is a project skill, read at session start.
  A nested module's — `example/tasks/.claude/skills`, which is what this
  repository's own example emits — is discovered only after a file in that subtree
  has been read. It works; it arrives late. `Options.SkillDir` now says so, and
  says to point at the repository root when the skill should be offered from the
  first turn.

  **The description has a real ceiling, and the emitter was over it.** The
  trigger text is truncated past a documented limit, so an overrun is silent.
  Capping the table-name list at twelve was not enough: twelve module-qualified
  names blow the budget on their own, which a guard written against the real
  limit caught immediately. The bound is now characters as well as count.

  **And a doubled conjunction was shipping.** The truncation appended "and N
  more" as a *list member*, so the list-joining "and" doubled up:
  `…table_name_11 and and 28 more`. Invisible to a length check, invisible in
  review, and in the one field a model reads on every turn. It has its own
  assertion now, proven both ways, because the length guard demonstrably does not
  catch it.

  Both bugs existed for six commits and three rounds of behavioural measurement
  without being noticed. The measurement was looking at what agents did with the
  document's *body*; nothing had yet checked the field that decides whether the
  body is ever read. That asymmetry is the lesson worth keeping.
- 2026-08-03 — **A 16-registry adoption found two more, and one of them is the
  failure mode this record names as the reason the file is gated at all.**
  Reported as [#142](https://github.com/jryannel/sqlb/issues/142) and
  [#143](https://github.com/jryannel/sqlb/issues/143) against a repository of 80
  modules and 184 tables.

  **The skill was confidently wrong under a declared `WireCase`, and the gate
  could not catch it.** A camelCase registry got a capability table listing
  `org_id`, closing with a sentence asserting that the column names above *are*
  the JSON field names — so an agent doing exactly what the file said wrote
  `?org_id=eq.…` and got a 400, having consulted the document that told it this
  would be accepted. Worse than guessing, since guessing from the camelCase model
  would have been right. `generate-check` was green throughout, because the file
  was a faithful render of a manifest that was itself wrong: `BuildManifest`
  reported the *column* name in the REST section, including in the worked example
  requests. Fixed in the manifest, so anything else reading it for a wire
  contract is fixed with it, and `Manifest.WireCase` plus `ColumnManifest.Wire`
  now carry the mapping for a renderer that needs the other spelling. The closing
  sentence is conditional: it is doing real work — it is what tells an agent not
  to go looking for a mapping layer — so it is kept and made true rather than
  deleted.

  This is the first bug in the emitter that the gate structurally could not have
  found, and it is worth stating plainly next to the claim above: **being gated
  proves the file matches the schema, not that it is right about it.** Every
  guard on this emitter until now was of the first kind.

  **`SkillDir` had no per-registry component**, so every registry pointed at one
  directory wrote the same path. That is fine for one module and silent
  last-writer-wins for several — and the doc comment recommended the repository
  root, which is the placement that does it. The failure is order-dependent:
  which module's `sqlb check` is red is a function of iteration order, and the
  message it prints, "run: sqlb generate", is advice that reddens the other one.
  `Options.SkillName` is the fix, defaulting to `sqlb-schema`, and the doc
  comment now says "one registry" out loud. Detecting the collision inside one
  invocation was rejected as the primary fix: `sqlb generate` resolves exactly
  one package, so the two writers are always two invocations.

  **Module-local placement is better than a workaround**, which is the part of
  the report worth keeping rather than the bug. A nested `.claude/skills` is
  directory-scoped by the harness, so sixteen skills all named `sqlb-schema` are
  sixteen correctly-scoped skills — and that is the right semantics anyway, since
  "which columns are filterable" is a per-registry question a merged root-level
  skill would answer for the wrong module. Confirmed from this repository while
  writing the fix: `example/tasks/.claude/skills` was announced as newly
  available and scoped to `example/tasks/`.

  **And one data point against the open question above.** The 2026-08-03 note
  says a skills directory that did not exist when a session started is not
  watched. The reporter observed the opposite — `sqlb generate` created one
  mid-session and the harness picked it up immediately — and so did the session
  above. One harness and one version each, so the doc comment now describes it as
  a possibility rather than a rule, and the question stays open.
