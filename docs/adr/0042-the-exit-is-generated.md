# ADR-0042: The exit is generated, and what it does not carry is named

- **Status:** Working — `sqlb eject` writes it, `example/blog/ejected` is a
  committed one, and `pgtest/eject_test.go` serves it beside the generated
  resources and compares the answers
- **Confidence:** High that the objection is the real one — §12.6 of the
  adoption review is the sharpest thing said about sqlb and it is not about a
  feature. High that the fidelity line is drawn in the right place, because the
  comparison test says so request by request. Medium that the *ejected* code is
  what an adopter would want to live with, since nobody has yet had to. Low on
  the emitted `support.go` staying this small if the operators grow
- **Decided:** 2026-07-31
- **Last reviewed:** 2026-07-31

## Context

**The strongest objection to sqlb is not a missing feature.** §12.6 of the
[adoption review](../review-adoption-existing-app.md) put it plainly: sqlc and
chi are cheap to reverse because they own almost nothing — sqlc's output is Go
functions you can stop calling, chi is a mux — while sqlb owns the schema, the
migrations, the wire format, the client and the CLI. Reversing it is not a
day-4 change, and the review's own evidence is that the evaluated team reverses
readily when something does not fit. The concentration *is* the risk, and no
amount of feature work answers it.

**The mitigation that already exists is half of one.** [ADR-0010](0010-codegen-is-optional.md)
makes code generation optional, so the *runtime* is reversible: `Describe` over
structs you already have, a two-method `Executor`, and you can stop calling the
builder without touching a model. What is not reversible that way is the half
the review named — the schema DSL owning migrations, and the generated wire
format once two clients ship against it.

**A library with no consumers cannot answer this with a promise.** It can answer
it with an artefact: the way out, generated from the same declaration as the way
in, and kept working by the same CI that keeps everything else working. That is
what [#19](https://github.com/jryannel/sqlb/issues/19) asks for.

## Decision

**`sqlb eject` writes a package that imports pgx and the standard library and
nothing else.** Six files: the schema as DDL, the row structs with the `sqlb`
tags removed, one function per statement with the SQL written out, a small
shared file of request parsing and WHERE assembly, `net/http` handlers, and a
README. Deleting sqlb from `go.mod` afterwards is a supported end state.

**The fidelity line is between the surface and the engine.**

*Out whole:* CRUD and list at the same paths, the same status codes, the same
JSON envelope; the filter operators that are one SQL fragment each — `eq`, `ne`,
`lt`, `lte`, `gt`, `gte`, `in`, `nin`, `isnull`, `notnull`, `between`, `like`,
`ilike`, `contains`, `startswith`, `endswith` — plus the bare `?column=value`
shorthand; `?sort`, `?search`, `?page`/`?per_page`, `?count=exact`; the declared
ceilings; the RFC 9457 error shape with its `allowed` lists.

*Not out, and refused by name rather than ignored:* keyset cursors, `?select`,
`?expand`, the JSON filter tree, and the array and document operators. Each is
the engine rather than the surface — reproducing them means emitting a copy of
sqlb, which is not an exit but a fork with a different import path. A request
carrying one gets a 400 saying so.

**Two properties survive the loss of the machinery they were implemented in.**
Capabilities stay opt-in: a column that never declared `Filterable` is not
filterable in the exit, and a `Hidden` one has no spelling at all — that is a
security property, not a convenience, because a column outside the grammar
cannot be probed through it. And [ADR-0030](0030-declared-scope-is-required.md)'s
obligation stays compulsory: a table that declared `Scoped` or `SoftDelete`
refuses to register without a `Confine` function, and a scoped table with a
create endpoint refuses without an `Assign` one. Function fields instead of hook
registrations — the same seam with the machinery removed.

**The exit is tested, and that is the load-bearing half.** `pgtest/eject_test.go`
stands the ejected blog package up beside the generated resources it came from,
points both at one database, and sends both the same requests: the bodies are
compared byte for byte, and the two known differences — huma's `$schema` link
and `next_cursor` — are subtracted explicitly rather than tolerated by a loose
comparison. `mise run eject-check` fails when the committed exit no longer
matches the schema.

**`eject -check` is deliberately not part of `sqlb check`.** Generated code is
stale when it disagrees with the schema and there is one right answer. An
ejected package is *meant* to be edited, so the day a project takes the exit is
the day it deletes the gate rather than fixes it. Two verbs keep that
distinction visible in CI.

## Consequences

**Buys.** The concentration objection gets a concrete answer: hard to reverse
becomes reversible on demand, and the reversal is demonstrably a working server
rather than a plan. It also makes the wire format's independence checkable — the
comparison test would fail the moment the generated resource and a plain
implementation of the same contract disagreed, which is a claim
[compatibility.md](../compatibility.md) makes and has not been able to test.
Committing an ejected package additionally gives a reader one place to see, in
plain Go, what the generic runtime does per request.

**Costs.** A second emitter over the same declaration, which has to keep up: an
operator added to the filter grammar and not to the exit is a gap the comparison
test will notice, and one added to both is work done twice. The emitted
`support.go` is a few hundred lines that every ejected project owns a copy of —
deliberately, but a fork of it is not a fork anybody upstream can fix. And the
exit's fidelity is a promise with a boundary, so the boundary has to be
maintained honestly: the README lists the gaps, and the moment that list is
wrong the feature is worse than not having it.

**Not a migration path.** Eject is a door, not a supported dual-run mode. The
emitted code is a starting point that assumes it will be edited; nothing keeps
it and the sqlb resources in step afterwards, and nothing should.

## What would change our mind

- **Nobody ever runs it.** Then it is a claim rather than a tool — which is
  arguably still worth its weight for the objection it answers, but the CI cost
  should shrink to the compile check.
- **The gaps are the parts people actually need.** If the first real eject
  immediately hand-writes cursors and `?expand` back in, the line was drawn in
  the wrong place and those belong in the emitter, not in the README.
- **The comparison test becomes flaky in the interesting way.** If keeping the
  two implementations byte-identical starts constraining what sqlb's own
  handlers may do, the test should compare status codes and shape rather than
  bytes — and this record should say so before that happens quietly.
- **Someone asks for the support file as a package.** That is the design being
  rejected here; if it is asked for twice, the argument against it is weaker
  than it looks.

## Cost of change

Low. Nothing in sqlb depends on the ejected package — it is output, like the
TypeScript client — so narrowing what eject emits, or deleting the verb, breaks
nothing that is running. The one thing that would be expensive is the opposite
direction: a project that has *taken* the exit and edited it cannot be given a
better one, which is why the emitted code is written to be read and changed
rather than regenerated.

## Revisions

- 2026-07-31 — Written with the implementation, against
  [#19](https://github.com/jryannel/sqlb/issues/19) and §13.6 of the adoption
  review. Three things the issue did not have: the fidelity line stated as
  surface-versus-engine rather than as a list, the two properties that had to
  survive (opt-in capabilities and the mount-time obligation), and the
  observation that a comparison test between the exit and the generated resource
  tests the *generated* side too.
