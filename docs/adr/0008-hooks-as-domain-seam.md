# ADR-0008: Hooks are the domain-logic seam

- **Status:** Working
- **Confidence:** Medium
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

A generated data layer has to leave somewhere for domain logic, or teams route
around it. The common failure is that generated CRUD is all-or-nothing: as soon
as one endpoint needs to normalise an email or stamp an owner, it gets written
by hand and the generated version is abandoned.

Multi-tenant scoping is the sharpest case. `WHERE org_id = $1` has to be on
every read, and forgetting it once is a cross-tenant data leak.

## Decision

Register hooks per model: `BeforeQuery`, `Before`/`AfterCreate`,
`Before`/`AfterUpdate`, `Before`/`AfterDelete`.

`BeforeQuery` is the load-bearing one. It receives the `*Builder` itself and may
amend it, so one registration constrains every read of that model — including
reads issued by generated REST handlers. Terminal methods clone the builder
before running hooks, so a hook's predicates cannot accumulate across repeated
executions of the same query value.

## Consequences

**What this buys.** Tenant scoping and soft-delete filtering become one
registration each instead of a rule every call site must remember. A hook
returning an error aborts the operation, so a missing tenant fails closed.
Generated handlers stay useful even when a resource has domain rules.

**What this costs.** Hooks are registered in a process-global registry keyed by
type, which is invisible action-at-a-distance: reading a query does not tell you
what will actually execute. It also makes test isolation manual — `Reset` exists
for that. Hook order is registration order, which is implicit.

## What would change our mind

- If global registration causes test bleed or surprises in practice, move the
  registry onto a `*DB` handle so hooks are scoped to a connection. Go 1.27
  generic methods make this straightforward and it is the likely end state.
- If people need to bypass a hook for a legitimate admin path, that is a real
  gap — the answer is probably an explicit unscoped builder, not a way to
  disable hooks globally.
- If hook order ever matters and registration order is not enough, add explicit
  priorities.

## Cost of change

Wide but mechanical. Moving the registry from a package global onto a `*DB`
handle touches every registration site and the signature of every terminal
method, but the behaviour is unchanged and the compiler finds all of it.

Removing hooks entirely is the expensive direction: tenant scoping would have to
move back to individual call sites, and the guarantee that it cannot be
forgotten is exactly what would be lost.

## Alternatives considered

**Middleware on the HTTP layer only.** Rejected: it does not constrain queries
issued from Go code, so the scoping guarantee would be partial.

**Row-level security in Postgres.** Complementary rather than an alternative,
and worth adding as defence in depth. Not sufficient alone because it cannot
normalise input or reject a request with a useful error.

## Revisions

- 2026-07-27 — Written.
- 2026-07-27 — Added clone-before-hooks after finding that running the same
  builder twice applied a hook's predicates twice.
