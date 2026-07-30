# ADR-0008: Hooks are the domain-logic seam

- **Status:** Working
- **Confidence:** Medium
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

A generated data layer has to leave somewhere for domain logic, or teams route
around it. The common failure is that generated CRUD is all-or-nothing: as soon
as one endpoint needs to normalise an email or stamp an owner, it gets written by
hand and the generated version is abandoned.

Multi-tenant scoping is the sharpest case. `WHERE org_id = $1` has to be on every
read, and forgetting it once is a cross-tenant data leak.

## Decision

Register hooks per model: `BeforeQuery`, `Before`/`AfterCreate`,
`Before`/`AfterUpdate`, `Before`/`AfterDelete`.

`BeforeQuery` is the load-bearing one. It receives the `*Builder` and may amend
it, so one registration constrains every read of that model, including reads
issued by generated REST handlers. Terminal methods clone the builder before
running hooks, so a hook's predicates cannot accumulate across repeated
executions of the same query value.

## Consequences

**Buys.** Tenant scoping and soft-delete filtering become one registration each
instead of a rule every call site must remember. A hook returning an error aborts
the operation. Generated handlers stay useful when a resource has domain rules.

**Costs.** Hooks are action-at-a-distance: reading a query does not tell you what
will execute, and hook order is registration order.

Two limits, both narrowed since:

- **Registration is default-open**, where row-level security is default-deny. An
  unregistered model served every tenant's rows with no failure signal.
  [ADR-0030](0030-declared-scope-is-required.md) closes this where handlers are
  generated, by making a schema declaration an obligation the mount checks — not
  for queries written in Go, and it proves only that a hook exists.
- **Write hooks are a thinner seam than this record implied.** `BeforeCreate`
  receives a bare row and `BeforeUpdate` cannot read its own assignments.
  [ADR-0021](0021-hooks-receive-an-event.md) closed most of the gap by wrapping
  generated writes in a transaction, so `TxFrom` reaches the database. A hook on
  an ordinary read still has no executor.

`On[T]()` reaches a process default, so registry scoping
([ADR-0020](0020-transaction-scoped-handle.md)) helps only those who use it.

## What would change our mind

- People need to bypass a hook for a legitimate admin path — the answer is
  probably an explicit unscoped builder, not a way to disable hooks globally.
- Hook order starts to matter and registration order is not enough — add explicit
  priorities.

## Cost of change

Removing hooks entirely is the expensive direction: tenant scoping moves back to
individual call sites, and the guarantee that it cannot be forgotten is exactly
what is lost. Adjusting what a hook receives has proved cheap — moving the
registry onto a `*DB` handle touched neither registration sites nor terminal
signatures.

## Revisions

- 2026-07-27 — Written.
- 2026-07-27 — Added clone-before-hooks after the same builder run twice applied a
  hook's predicates twice.
- 2026-07-27 — Registry scoping landed (ADR-0020); cost-of-change estimate was
  too high and is revised down.
- 2026-07-27 — Narrowed the claim: write hooks cannot read the database or
  inspect their statement. ADR-0021 fixed the first half.
- 2026-07-28 — Narrowed again: "fails closed" was about a hook's body, not its
  absence. ADR-0030 closes the absence where handlers are generated.
- 2026-07-30 — Condensed.
