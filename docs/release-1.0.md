# The road to 1.0

What has to be true before `v1.0.0`, and how we would know it is.

This is a plan, not a schedule. It has no dates in it, because the gating item
is not work — it is evidence, and evidence arrives when it arrives.

## What 1.0 means here

[compatibility.md](compatibility.md) says semantic versioning applies from
`v1.0.0`. That is the whole promise and it is worth stating plainly, because
everything below is downstream of it:

**After 1.0, every surface under *Frozen* stops being revisable.** Not "revisable
with a migration note" — revisable only by a major version, which for a library
with real consumers is a thing you do approximately once. The filter grammar,
the cursor payload, `Executor`, the shape of generated DDL: these are already
frozen in spirit. 1.0 is where the spirit becomes a bill.

So the question 1.0 answers is not "is it feature-complete". It is: **have we
found the mistakes that are expensive to keep?** A missing feature after 1.0 is
an additive release. A wrong wire format after 1.0 is permanent.

That framing decides the ordering below. Anything that would change a frozen
surface is a 1.0 blocker. Anything additive is not, however much someone wants
it.

## The proof gate

The library's own [readiness review](review-adoption-readiness.md) and both
outside evaluations reach the same conclusion by different routes: the thing
sqlb is missing is not a feature, it is **elapsed time under someone else's
traffic**. One author, no observed consumers, and a design that reads well is
still a design nobody has been hurt by.

**1.0 ships when two real applications have been ported onto a sqlb branch and
that branch has run.** Not a pilot endpoint, not a spike — a branch that a
person could merge.

Two, specifically, and not one:

- **A single multi-tenant product** — the shape the first evaluation describes.
  It exercises depth: one schema, a tenant boundary that must hold, a web client
  and a mobile client both regenerated, and a wire-format cutover that has to be
  sequenced across all three.
- **A multi-app monorepo** — the shape the second describes. It exercises
  breadth: many schemas beside a shared platform layer, coexistence with sqlc
  rather than replacement of it, and the question of whether module isolation
  ([ADR-0015](adr/0015-module-isolation.md)) survives contact with a `core/`.

The two fail differently, which is the point. A port that only proves the depth
case leaves the isolation claim untested; one that only proves breadth leaves
the client-regeneration claim untested. Neither alone is evidence for the other.

### What "ported" has to mean

A port counts as evidence when all of these hold. Anything less is a spike, and
spikes are useful but they are not the gate.

1. **The schema is the source of truth for the ported modules.** `sqlb check`
   passes in the consuming repository's CI, so the generated code cannot drift
   from the declaration.
2. **`sqlb migrate -check` is green against the real migration history.** The
   history builds the declared schema — that is the claim
   [ADR-0014](adr/0014-migrations-and-import.md) makes, and the port is where it
   either holds against a seven-month history or does not.
3. **Every generated client compiles and its refusals file holds.** A widened
   type is the one failure [ADR-0028](adr/0028-typescript-client.md) claims
   cannot happen, and a real schema is a much harder test of that than
   `example/tasks`.
4. **The tenant boundary is enforced at startup, not by review.** Every `Scoped`
   model mounts or refuses ([ADR-0030](adr/0030-declared-scope-is-required.md)).
5. **The existing test suite passes**, including whatever architecture tests the
   consuming repository has. Where a rule had to be rewritten, that is a finding
   and goes in the report.
6. **A written report**, in the shape the two evaluations already use: what
   worked, what was friction, what was a gap, and what the port had to route
   around. A port that produced no findings is a port that was not honest.

### What the ports are allowed to conclude

**"Do not adopt" is a passing result for the gate.** The gate is evidence, not
endorsement. If a port concludes that sqlb is the wrong tool for that codebase
and says clearly why, 1.0 can still ship — the surfaces were exercised and the
mistakes were found. What 1.0 cannot ship on is *no port*, or a port that
stopped early because something was missing.

## What has to change before the freeze

Ordered by whether it touches a surface that stops being revisable. Everything
in stream A and B is a blocker. Everything below is negotiable.

### A. The hole that is a security bug, not a gap — **closed**

**`?expand` did not run the target's hooks.** Both evaluations found it
independently, and [ADR-0030](adr/0030-declared-scope-is-required.md) recorded
it under Consequences: a `BeforeQuery` hook confines a model's own reads, and an
expansion reached the target through a join that hook never saw. On a
tenant-scoped schema that was a cross-tenant read behind a capability the schema
declared safe.

It is fixed. The expansion runs the target's hooks and requalifies their
predicates onto the join alias, so the hook that satisfies the mount check is
the hook the join carries. Neither of the two answers this plan proposed — carry
the scope predicate, or refuse `Expandable` on an unprotected `Scoped` target —
turned out to be the cheap one; the first is what landed, and it needed no
judgement about which schemas are confined by something the package cannot see.

A predicate that cannot be requalified with certainty fails the query rather
than being dropped, which is the one new refusal. The composite foreign key
remains the stronger arrangement and is now belt-and-braces rather than the only
thing holding.

### B. The driver question, decided rather than answered

[compatibility.md](compatibility.md#the-driver) now *answers* this — pgx works
through `database/sql`, and that is the contract. It is a good answer and it is
not the same thing as a decision, because both evaluations independently call
the flip **the single largest mechanical cost of adoption**, and one names a pgx
path as one of three things that would change its verdict.

The port is what settles it. Both target codebases are pgx-native today, so
stream B is not work to schedule — it is a question the ports answer:

- If flipping to `database/sql` costs a day per app and regresses nothing, the
  answer stands and `compatibility.md` gets a paragraph saying it was measured.
- If it degrades pooling, or `pgvector`, or a background job runner, then
  `Executor` needs the optional interface its own doc says it grows by — and
  that has to land **before** the freeze, because afterwards it is a major
  version.

**This is the one item where the plan deliberately does not pre-decide.** Doing
the work now on a guess is how you acquire a permanent interface for a problem
you did not have.

### C. Type overrides — **closed**

`schema.Type.GoType()` was a closed switch with no override mechanism, and the
first evaluation made the cost concrete: `uuid → string` where the codebase uses
`uuid.UUID` touches middleware, every filter registry, and every use-case
signature.

`codegen.Options.Types` is the sqlc `overrides:` equivalent
([ADR-0035](adr/0035-type-overrides.md)). The record is mostly about the
boundary rather than the mechanism, because that is the part that would have
been easy to get wrong: an override reaches the models, the typed facade, the
REST bodies and the manifest; it reaches filter coercion too, and must; and it
does not reach the SQL type or the wire, which falls out of every client
emitter mapping from `schema.Type` rather than from the Go type.

### D. The wire format, stated as policy — **closed**

Three things ship as wire format. The filter grammar was already frozen in
`compatibility.md`; the other two were decided in practice and undecided in
writing, which is the state that produces a "we never promised that" argument
later.

Both are now stated, with their cost and their escape
([ADR-0036](adr/0036-the-wire-is-the-column-name.md)):

| | Rule |
|---|---|
| Field naming | The column's own name, verbatim, on all five surfaces. One spelling, no configuration |
| List envelope | `{items, page, per_page, has_more, next_cursor?, total?}`, one shape per resource |

The record is explicit that the rename cost for a camelCase front end is real
and undiscounted — one evaluation counts 334 tags — and names the two positions
an adopter can take instead, both of which keep exactly one spelling per
deployment. It also names the escape it *would* build if a port stalls on this:
a deployment-wide naming policy, not a per-field mapping layer, because every
guarantee [ADR-0028](adr/0028-typescript-client.md) makes is about the generated
client having no contents to be wrong.

### E. Schema gaps, and which of them 1.0 needs

Not all of these block. The test is whether a port can complete without them.

| Gap | Blocks a port? | Position for 1.0 |
|---|---|---|
| **Array columns** | Was yes | **Done** — [ADR-0033](adr/0033-array-columns.md) |
| **pgvector** | Yes, for one module | Scope that module out of the port, or build it. [ADR-0026](adr/0026-vectors-declare-their-index.md) is Exploring/Low and the shape is recorded |
| **`tsvector` / full text** | Probably | **Decided: not in 1.0** ([ADR-0037](adr/0037-search-is-ilike-until-it-cannot-be.md)). The blocker is not the column type, it is that a `tsvector` is database-maintained and `migrate` renders neither generated columns nor triggers |
| **Composite primary keys** | Yes, ~15 tables | [ADR-0034](adr/0034-one-column-addresses-a-row.md) states the refusal and concedes it is wider than its own argument. Narrow it: a table never addressed, expanded or cursor-paged needs no key |
| **Generated columns, triggers, backfills** | No | `migrate.Diff` renders DDL only; hand-written migrations interleave. Document the asterisk rather than close it |
| ~~**`Security` on `rest.Options`**~~ | No | **Done** — every generated operation carries the resource's requirement. It documents; middleware still enforces |
| **Parent-scoped routes** (`/projects/{id}/tasks`) | No, but every consumer notices | **Decided: flat, deliberately** ([ADR-0038](adr/0038-collections-are-flat.md)). The one real cost is that a missing parent is an empty page rather than a 404 |

The row still worth arguing about is **composite primary keys**. ADR-0034's own
text concedes the refusal is wider than its own argument — a table never
addressed, expanded or cursor-paged needs no key at all — and narrowing it is
cheaper than defending it. It is additive, so it can land after 1.0 if a port
shows the narrow form is enough.

### F. ADR hygiene — the records must be true at 1.0 — **closed**

Six records sat at **Exploring**, and they were not the same kind of thing. Four
described shipped behaviour and now say so; two are genuinely unbuilt and now say
*that*, with "not in 1.0" in the status rather than left for a reader to infer
from a confidence line.

| ADR | Was | Now |
|---|---|---|
| [0004](adr/0004-schema-as-go-dsl.md) schema as Go DSL | Exploring | **Working** — every artefact is generated from it |
| [0014](adr/0014-migrations-and-import.md) migrations by diff | Exploring | **Working** — the loop closes and CI enforces the fixpoint |
| [0019](adr/0019-pgbouncer-in-the-path.md) PgBouncer | Exploring/Low | **Working** — the carve-outs are tested against a real pooler, not reasoned |
| [0023](adr/0023-mixins-carry-behaviour.md) mixins | Exploring | **Working as a decision** — the column half ships, the behaviour half is out of scope |
| [0012](adr/0012-change-feed-outbox.md) change feed | Exploring/Low | **Not in 1.0**, said in the status |
| [0026](adr/0026-vectors-declare-their-index.md) vectors | Exploring/Low | **Not in 1.0** unless a port needs it |

Four decisions had **no record at all**. All four now have one: type overrides
([0035](adr/0035-type-overrides.md)), the wire-format policy
([0036](adr/0036-the-wire-is-the-column-name.md)), full-text search
([0037](adr/0037-search-is-ilike-until-it-cannot-be.md)) and parent-scoped routes
([0038](adr/0038-collections-are-flat.md)).

**The index now has no Exploring row whose subject is shipped**, which was Phase
1's gate.

## Deliberately not in 1.0

Named so that "it is missing" is not mistaken for "it was forgotten".

- **The change feed** ([ADR-0012](adr/0012-change-feed-outbox.md)). The biggest
  unbuilt item in [the vision](vision.md), and the one most likely to change
  shape on contact with traffic. Freezing an outbox format on a guess is exactly
  the mistake 1.0 exists to avoid.
- **Nested `?expand`, and backwards cursors.** Both are already named in
  `compatibility.md` under *Will move*, both are additive, and neither changes
  the meaning of a request that can be sent today.
- **Declared actions** ([#18](https://github.com/jryannel/sqlb/issues/18)) and
  **computed fields** ([#17](https://github.com/jryannel/sqlb/issues/17)). The
  two largest items on the roadmap, and both additive — a schema that does not
  declare one is unaffected. Computed fields in particular decide whether the
  generated path survives a real domain, which makes them the strongest
  candidate for 1.1 and a poor reason to hold 1.0.
- **`sqlb eject`** ([#19](https://github.com/jryannel/sqlb/issues/19)) and
  **`sqlb impact`** ([#21](https://github.com/jryannel/sqlb/issues/21)). Both are
  adoption arguments rather than features. Worth building; not worth blocking a
  freeze.
- **A pgx-native `Executor`** — unless stream B says otherwise.

## Sequencing

Four phases, each with a gate. The point of the gates is that work stops if one
fails, rather than continuing on momentum.

### Phase 1 — Make the records true — **done**

Stream F plus the two cheap items. Nothing here needed a port and nothing here
was risky, which is why it went first: it is the work that makes the next phase
legible to someone who is not the author.

- ~~Every ADR reaches Working or documents its scope.~~
- ~~ADRs written for type overrides, wire-format policy, full text,
  parent-scoped routes.~~
- ~~`Security` on `rest.Options`.~~
- ~~`compatibility.md` gains the envelope and naming rule under *Frozen*.~~

**Gate: passed.** The ADR index has no Exploring row whose subject is shipped.

### Phase 2 — Close the hole and unblock the ports

- ~~**Stream A**: the `?expand` scope fix.~~ Done.
- ~~**Stream C**: type overrides in `codegen.Options`.~~ Done.
- Whichever of stream E's rows the two target codebases actually hit — decided
  by reading them, not by guessing here.

**Gate:** a port can begin without a known blocker in front of it. Not "without
friction" — without a blocker.

### Phase 3 — The two ports, in parallel

They are independent and answer different questions, so running them in sequence
buys nothing but calendar time. Each produces a written report.

**Gate:** both branches run, both reports exist. Stream B is decided by what
they measured.

### Phase 4 — Freeze

- Every finding from both ports triaged: fixed before 1.0, scheduled for 1.1, or
  written down as a known limitation.
- `compatibility.md` rewritten from "what `v0.1.0` promises" to the 1.0 contract.
- Tag.

**Gate:** nothing in the *Frozen* list has changed since Phase 1, or if it has,
the freeze restarts. That is the whole purpose of the phase.

## What would delay 1.0

Stated in advance, so the decision to slip is a decision rather than a drift:

- **A port cannot complete** for a reason that is a sqlb design flaw rather than
  a missing feature. A missing feature is a scheduling problem; a design flaw
  found at 1.0 is the system working.
- **Stream B goes the other way** — the ports show `database/sql` is not enough.
  `Executor` gains an optional interface, and that has to settle before a freeze.
- **A frozen surface is found to be wrong.** The filter grammar, the cursor
  payload and the response envelope are the three that would hurt most, and all
  three get their first real exercise during the ports.
- **Only one port happens.** One is a pilot. The gate asks for two because they
  fail differently.

## What this plan does not claim

It does not claim 1.0 makes sqlb mature. It makes sqlb *finished enough to stop
changing underneath people*, which is a smaller and more honest thing. The
readiness review's own exit criterion — six months of someone other than the
author running it against production traffic — is not met by two ports on
branches, and 1.0 does not pretend otherwise.

What two ports do buy is the difference between "the design is good" and "the
design survived contact with a codebase it did not choose." That is the evidence
1.0 is short of today, and it is the only item on this page that cannot be
written.
