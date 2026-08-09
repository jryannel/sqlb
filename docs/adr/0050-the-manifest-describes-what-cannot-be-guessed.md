# ADR-0050: The manifest describes what a client cannot guess, and a UI is authored rather than carried

- **Status:** Working — the manifest is built by `Registry.BuildManifest`, emitted
  as `sqlb.json` and gated by `sqlb check`. What is new here is the rule that
  decides what may go into it, not the artefact
- **Confidence:** High that this repository should not carry a UI — the
  maintenance surface is unlike anything else here, and Django's admin is twenty
  years of evidence that a carried admin becomes a framework. **Low that the
  manifest is sufficient**, because no admin UI has been written against it and
  that is the only test that counts
- **Decided:** 2026-08-09
- **Last reviewed:** 2026-08-09

## Context

Django's admin is the one thing sqlb has no answer to, and the obvious move is a
fifth emitter beside the TypeScript and Dart clients. It is the wrong move: a UI
is HTML, CSS, a component vocabulary, a theme, a build step and a browser support
matrix, none of which exist in this repository and all of which would acquire a
release cadence tied to frontend churn rather than to the schema.

The seam already exists and was built for a different reason.
[`schema/manifest.go`](../../schema/manifest.go) describes every table, every
column, and "exactly what a client may filter, sort, search and expand on each
exposed resource" — capabilities in their wire spelling, inverse relations with
their order and cap, declared actions, filter operators, page limits.

**What was missing was the rule.** Every field in that document happens to be
something a client would otherwise get wrong, but nothing said so, so the next
addition had no test to pass. The comparison with Django's `ModelAdmin` makes the
question concrete: `list_filter` maps onto `Filterable` and belongs; does
`list_display`? Does `__str__`?

**The consumer is an agent authoring source, not a runtime renderer.** That is
the fact that decides it. A generic runtime admin — pointed at `sqlb.json`,
rendering a schema it has never seen — must be told which column names a row,
because no human is in the loop. An agent writing an admin against the generated
TypeScript SDK is in the loop at authoring time, picks `title` over `id` without
being told, and bakes the choice into ordinary source a human can edit. The
guess is also guarded: `tsRowTypes` emits a row interface, so a renamed column
fails `tsc` rather than drifting silently.

## Decision

**sqlb enables a UI and carries none. The manifest is where the enabling
happens.**

**The manifest carries what a competent author cannot guess and would get wrong
silently.** That is the admission test, and it explains every field already
there: capabilities are opt-in ([ADR-0006](0006-capabilities-are-opt-in.md)) so a
guess is a 400; the wire spelling is derived
([ADR-0036](0036-the-wire-is-the-column-name.md)) so a guess is a 400 that reads
like a typo; an inverse relation is declared on the other side
([ADR-0022](0022-references-declare-their-inverse.md)) so reading this table does
not reveal it; a declared action ([ADR-0043](0043-declared-actions.md)) is
invisible in a CRUD surface, and its absence leads an agent to PATCH the status
the verb exists to own.

**It carries nothing an author can decide and the compiler will check.** No row
label, no `list_display`, no column order, no widget hints. A row label is
declined on this test rather than on [ADR-0024](0024-no-annotation-slot.md)'s —
it would be a typed field and so not the untyped bag 0024 refuses — and the test
it fails is that the guess is right, visible on screen the moment it is wrong,
and caught by the typecheck when the schema moves.

**What a caller may do is not describable here, and that is by design.** Hooks
are code ([ADR-0008](0008-hooks-as-domain-seam.md)), so no static document can
report a caller's rights; and because a hook adds a predicate rather than vetoing
([`notes/hooks.go`](../../example/fxapp/notes/hooks.go)), a row in another tenant
is a 404 and not a 403. An authored admin therefore cannot grey out a button it
lacks permission for, and must not try to infer one. There is no permission
registry.

## Consequences

**Buys.** The repository stays Go and SQL. A UI is authored per application, in
its own stack, at its own cadence, and inherits the hook boundary for free
because it calls the same endpoints — the row scoping in `notes/hooks.go` applies
to an admin exactly as it applies to `?expand=`, which is precisely what Django's
admin does *not* give you without a per-`ModelAdmin` `get_queryset` override.

**Costs.** *There is no admin in the box*, and "generate one with an agent" is a
worse answer than a working page for anyone evaluating sqlb in an afternoon.
*The claim is untested* — the manifest is asserted to be sufficient and nobody
has built the thing that would prove it. *The rule is a judgement call at the
margin*: "cannot guess" is a property of the guesser, and an agent is a better
guesser than the generic renderer this rule was written against.

## What would change our mind

- **An admin written against `sqlb.json` reaches for a fact that is not there.**
  The first one is the measurement, and the fact it wanted is the field to add.
  Deliberately a low bar, and the same shape as ADR-0024's trigger.
- **The same guess is made differently by two clients** — the TypeScript admin
  and the Dart app disagree about which column names a row. Then a label is a
  contract rather than a choice, and this record is wrong about which side of the
  line it sits on.
- **A runtime generic renderer becomes wanted** — one binary pointed at any
  `sqlb.json`. Every "authorable" fact above becomes undescribable-and-required
  at once, and the rule inverts rather than bends.
- **`sqlb check` stops being enough to call the manifest a contract**, because a
  consumer outside the repository broke on a shape change no gate caught.

## Cost of change

**Adding a manifest field later is additive and cheap**: `ManifestVersion` exists
for the incompatible case, and a JSON consumer ignores keys it does not read.
**Adding a `schema` declaration is not** — `schema` is Stable tier
([ADR-0013](0013-no-internal-split.md)) and ADR-0024's asymmetry applies in full:
additive today, near-impossible to remove once one consumer reads it.

**Carrying a UI is the expensive direction and the least reversible.** Not
technically — an emitter can be deleted — but socially: an admin that ships is
adopted, and removing it breaks applications that built their operations around
it. This is the asymmetry that decides the record, and it runs the useful way.

## Open questions I had to answer myself

- **Whether "the description a client reads" is `sqlb.json` or the OpenAPI
  document.** They overlap, both are emitted, and no record ranks them. I assumed
  the manifest, because it carries the capability lists OpenAPI is lossy about
  ([ADR-0028](0028-typescript-client.md)) — but an admin generator would plausibly
  read the OpenAPI document first, and then this record has named the wrong seam.
- **Whether an authored admin is the only kind in scope.** The whole decision
  rests on it. A runtime generic renderer needs the row label, and I took
  "authored by an agent against the TS SDK" as the case to design for on one
  sentence of intent, not on anything written down.
- **Whether the drift guard generalises past TypeScript.** `tsc` catches a
  renamed column in an authored admin. The CLI prints JSON, so an agent reading
  that output has no equivalent, and I did not check whether the Dart client's
  types bite the same way.
- **Whether `sqlb.json` is a frozen surface.** [compatibility.md](../compatibility.md)
  mentions it once, as something an agent drives the API off, and does not list
  it among the surfaces that are frozen or expected to move. I have written this
  record as though it is a contract; nothing says it is.
- **Whether `ManifestVersion = "1"` already promises something.** It is described
  as bumped on incompatible change, which implies a compatibility policy that is
  not written down anywhere I found.

## Revisions

- 2026-08-09 — Written, prompted by a comparison with Django's admin. The record
  changed shape twice while being written: from "generate an admin", to "add the
  row label the manifest lacks", to the admission test above — once the consumer
  was identified as an authoring agent rather than a runtime renderer, the label
  stopped being a contract and the rule turned out to be the thing worth
  recording.
