# ADR-0053: The manifest describes what a client cannot guess, and an uncurated browser is carried while a curated admin stays authored

- **Status:** Working — the manifest is built by `Registry.BuildManifest`, emitted
  as `sqlb.json` and gated by `sqlb check`. **Revised 2026-08-14**: sqlb also
  carries a generic, uncurated data/schema/action browser; nothing about the
  original rule for what the manifest holds changed
- **Confidence:** High that a *curated* admin — one that guesses a row label, a
  field order, a widget — should stay authored per application; Django's admin
  is twenty years of evidence that carrying one becomes a framework. **Low that
  an *uncurated* browser is worth carrying**, because none has been built
  against `sqlb.json` yet and that is the only test that counts, same as before
- **Decided:** 2026-08-09
- **Last reviewed:** 2026-08-14

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

**sqlb enables a curated UI and carries none of it. The manifest is where the
enabling happens.** This half of the record is unchanged — see below for what
now carries.

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

**An uncurated browser needs none of the facts the manifest declines to carry,
so it is carried.** The comparison the rest of this record makes is to Django's
`ModelAdmin`, which is curated: it guesses a row label, a field order, a widget.
Convex's dashboard is the other shape — a grid over raw rows, addressed by
primary key, with no `__str__` to get wrong. Every fact that grid needs is
already in `sqlb.json`: column types and enums render a cell, `references`
renders a foreign key as a link to the row it points at, and `ActionManifest`
already carries a typed `Body` per declared action, which is enough to render
an invoke form without guessing a field the way the curated case would have to.
So sqlb carries this one: a browser reading `sqlb.json` for schema and calling
the generated REST API for data and actions, built and released separately from
the engine the way `pgtest` already is a separate module from `.` — the
component-vocabulary-and-browser-support-matrix cost this record raised against
a carried UI is real regardless of curated or not, and paying it once, off the
engine's cadence, is the same trade `pgtest` already made for a different
reason.

**It calls the REST API, not the database, and that is what makes it safe to
carry.** The same sentence as the *Buys* paragraph below applies here first:
a browser that fetches through the generated endpoints inherits row scoping for
free, the same as `?expand=` does. It does not solve the permission question the
next paragraph raises — it inherits it, which is different from answering it.

**Logs are out of scope for the browser and for the manifest.** `Executor` is
already an interface a wrapper can trace ([`example_trace_test.go`](../../example_trace_test.go)),
and an application that wants OpenTelemetry wires it there today, pointed at
Uptrace, Jaeger, or anything else that reads OTel — sqlb declares nothing new
and the browser does not attempt to render spans itself. This is "instrument,
don't carry," the same posture the rest of this record already takes, not an
exception to it.

## Consequences

**Buys.** The engine's own module stays Go and SQL, with no frontend build step
or browser matrix in `.`'s release cadence — the browser is a separate module,
same as `pgtest`. A curated UI is still authored per application, in its own
stack, at its own cadence. Both shapes inherit the hook boundary for free
because both call the same endpoints — the row scoping in `notes/hooks.go`
applies to a grid exactly as it applies to `?expand=`, which is precisely what
Django's admin does *not* give you without a per-`ModelAdmin` `get_queryset`
override. *There is now something in the box* for anyone evaluating sqlb in an
afternoon, which was the sharpest cost the original record named.

**Costs.** *The claim is still untested for the browser* — the manifest is
asserted to be sufficient for a grid and nobody has built the thing that would
prove it, same gap as before, now on new ground. *The permission question is
inherited, not answered*: a browser calling the REST API with a caller's own
credentials shows only what that caller could already fetch, so an operator
wanting a cross-tenant view needs a credential that already sees everything,
which this record does not design. *The rule is still a judgement call at the
margin* for the curated case: "cannot guess" is a property of the guesser, and
an agent is a better guesser than a generic renderer at the curated end of the
spectrum — only the uncurated end, where there is nothing to guess, is settled.

## What would change our mind

- **An admin written against `sqlb.json` reaches for a fact that is not there.**
  The first one is the measurement, and the fact it wanted is the field to add.
  Deliberately a low bar, and the same shape as ADR-0024's trigger.
- **The same guess is made differently by two clients** — the TypeScript admin
  and the Dart app disagree about which column names a row. Then a label is a
  contract rather than a choice, and this record is wrong about which side of the
  line it sits on.
- ~~**A runtime generic renderer becomes wanted** — one binary pointed at any
  `sqlb.json`.~~ **Fired, 2026-08-14.** The rule did not invert the way this
  bullet predicted, because the renderer wanted turned out to be Convex's
  uncurated grid rather than a Django-shaped one: nothing "authorable" above
  became required, since a grid over raw rows never asked for a row label in
  the first place. The bullet was right that this was the trigger; wrong that
  every authorable fact would become required by it — only a curated renderer
  would have proven that.
- **The uncurated browser reaches for a fact `sqlb.json` doesn't have.** Same
  admission test as the curated case, on new ground: the first missing fact is
  the one to add.
- **`sqlb check` stops being enough to call the manifest a contract**, because a
  consumer outside the repository broke on a shape change no gate caught.

## Cost of change

**Adding a manifest field later is additive and cheap**: `ManifestVersion` exists
for the incompatible case, and a JSON consumer ignores keys it does not read.
**Adding a `schema` declaration is not** — `schema` is Stable tier
([ADR-0013](0013-no-internal-split.md)) and ADR-0024's asymmetry applies in full:
additive today, near-impossible to remove once one consumer reads it.

**Carrying a UI is the expensive direction and the least reversible.** Not
technically — an emitter, or a separate module, can be deleted — but socially:
an admin that ships is adopted, and removing it breaks applications that built
their operations around it. This is the asymmetry that decided the record
before, and it is the asymmetry now being paid deliberately for the uncurated
browser: it ships as its own module precisely so deleting it, if the claim
above does not hold up, costs a module rather than an unwind inside `.`.

## Open questions I had to answer myself

- **Whether "the description a client reads" is `sqlb.json` or the OpenAPI
  document.** They overlap, both are emitted, and no record ranks them. I assumed
  the manifest, because it carries the capability lists OpenAPI is lossy about
  ([ADR-0028](0028-typescript-client.md)) — but an admin generator would plausibly
  read the OpenAPI document first, and then this record has named the wrong seam.
- ~~**Whether an authored admin is the only kind in scope.**~~ **Answered by the
  2026-08-14 revision: no.** It rested on the assumption that a runtime
  renderer needs a row label; an uncurated grid does not, so the two kinds
  coexist rather than one displacing the other.
- **Where the browser lives.** Assumed a separate module in this repository,
  the way `pgtest` is, so its own dependencies and build step never touch `.`'s
  `go.mod`. Not decided against a separate repository entirely, which would cut
  the tie further but cost the single-clone convenience `pgtest` currently
  gives.
- **Whether it authenticates as the caller or as a service credential that sees
  every row.** Assumed the caller's own credentials for v1, which is what makes
  the "inherits the hook boundary for free" claim true — an operator view that
  sees across tenants is a different credential model this record does not
  design, and building the browser only against caller credentials first is a
  bet that the gap gets noticed before it gets shipped around.
- **Whether logs belong in the browser's UI at all, even as an embed.** Assumed
  no: pointing at Uptrace or Jaeger directly, or shipping a Grafana dashboard
  config as an example, is cheaper than embedding a trace viewer, and neither
  needs a decision here since neither is sqlb code.
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
- 2026-08-14 — **Revised**, prompted by a request for a Convex-dashboard-style
  admin (schema, data, logs, function calling). The *What would change our
  mind* bullet about a wanted runtime renderer fired, but did not invert the
  rule the way it predicted: the renderer wanted was an uncurated grid, which
  needs no row label and so needed nothing the manifest was already declining
  to carry on the curated case's account. `ActionManifest.Body` (added by
  [ADR-0043](0043-declared-actions.md), after this record's first version) turned
  out to already be enough to render an action-invoke form too. Decision: sqlb
  now carries an uncurated data/schema/action browser, as a separate module off
  the engine's release cadence; a curated, per-application admin is still
  authored, unchanged. Logs are explicitly out of scope for both the browser
  and the manifest — an application traces `Executor` (already possible, see
  `example_trace_test.go`) and points the result at whatever OTel-reading tool
  it prefers; a Grafana dashboard config is a reasonable example to ship
  alongside the browser, but it is config for existing tooling, not sqlb code,
  and needs no decision here.
