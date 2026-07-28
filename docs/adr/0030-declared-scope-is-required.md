# ADR-0030: A declaration that rows are confined is an obligation, not a comment

- **Status:** Working — the check refuses at mount, and `example/tasks` passes
  it without a hook being added or moved
- **Confidence:** High that the hole is real and that startup is where to close
  it; Low that one bit per hook is the final shape of the check
- **Decided:** 2026-07-28
- **Last reviewed:** 2026-07-28

## Context

[ADR-0008](0008-hooks-as-domain-seam.md) offers hooks as the answer to
multi-tenant scoping, and names the case precisely: `WHERE org_id = $1` has to
be on every read, and forgetting it once is a cross-tenant leak. `BeforeQuery`
receives the builder, so one registration covers every read of a model,
including the ones generated handlers issue. That record then says a hook
"fails closed", and it is true of a hook that runs.

It says nothing about a hook that was never written.

That is the hole. Row-level security — the mechanism hooks are offered as an
ergonomic alternative to — is default-deny: a new table is unreachable until
someone writes a policy, and the failure mode of forgetting is an empty result
set and a bug report. `BeforeQuery` is default-open. Add a table, expose it,
forget the registration, and the resource serves every tenant's rows with a
`200` next to them. No error, no empty page, no signal of any kind. The system
whose pitch is that capabilities are opt-in and a column that declares nothing
is unreachable had, one layer up, a boundary that was opt-out.

[`example/tasks`](../../example/tasks/) is where this became concrete rather
than theoretical. Its hook file is careful, well argued, and *manual*: five
models enumerated by hand across `scopeReads`, `scopeWrites` and three
`BeforeCreate` registrations. Everything about it is correct. Nothing about it
notices a sixth table.

The schema already knew the fact that was missing. `schema.SoftDelete` has the
same shape and the same gap — it adds a `deleted_at` column and stops, so a
table that declares one and filters nothing returns deleted rows from every
list endpoint, which the tasks example documents as a hazard the author has to
remember.

## Decision

A schema declaration that says rows are confined becomes an obligation on the
resource that exposes the table, and `rest.Resource` refuses to mount a
resource whose obligations no hook satisfies.

Two declarations carry the obligation:

```go
schema.Ref("workspace", Workspace).Filterable().ReadOnly().Scoped()
schema.SoftDelete()
```

Neither writes a predicate. `Scoped` is inert at runtime in exactly the way
`SoftDelete` already was — nothing on the request path reads either, and the
reasoning in [ADR-0016](0016-guards-proven-both-ways.md) about generated
behaviour applies unchanged: a generated tenant predicate that silently reads
the wrong context key is worse than no predicate, because it looks like a
boundary. What changes is not who writes the hook. It is what happens when
nobody does.

### The obligation follows the exposed operations

A `BeforeQuery` hook constrains what a request can *see* and says nothing about
what it can overwrite by id. So the requirement is per operation:

| Exposed | Requires |
|---|---|
| `OpList`, `OpRead` | `BeforeQuery` |
| `OpUpdate` | `BeforeUpdate` |
| `OpDelete` | `BeforeDelete` |
| `OpCreate` | `BeforeCreate` |

This is not a design invented here. It is the arrangement `example/tasks`
arrived at by hand, with its own comment explaining why reads and writes are
separate registrations, made compulsory. The example needed no change to pass:
the declarations were added to its schema and every obligation was already met,
which is the strongest available evidence that the mapping is the real one and
not a rule the author would end up working around.

`OpCreate` needs its own justification, since a create has no rows to confine.
The tenant column is `ReadOnly`, so it is absent from the generated create body
and a `BeforeCreate` hook is the only thing that can supply it; without one the
insert reaches the database with no tenant at all.

### `Scoped` implies `ReadOnly`, and the validator enforces it

A tenant column a request may write is not a tenant column — the create body
would carry it and the caller would choose which tenant to write into. So
`schema.Validate` rejects a `Scoped` column that is not `ReadOnly`, and one
that is `Nullable`, since a row whose tenant is NULL falls outside every
tenant's predicate and lands in everyone's the day somebody writes
`IS NULL OR = $1`. `Immutable` is deliberately not enough: it closes the update
and leaves the create open.

This half is worth more than the hook check. It makes one shape of the leak
unrepresentable rather than merely reported.

### The check proves that a hook exists, and not that it works

This is the decision's real limit and it is stated here rather than discovered
later. A `BeforeQuery` hook that logs a line and returns `nil` satisfies the
obligation. Two declarations on one table — `Scoped` and a soft delete — are
satisfied by one registration, so a hook that scopes the tenant and forgets
`deleted_at` passes.

The alternative was considered and rejected. Executing the application's hooks
at startup against a fabricated context, then inspecting the resulting builder
for a predicate naming the declared column, would prove more. It also runs user
code for a request that never happened, and the fail-closed hooks this project
recommends return an error when the context carries no claims, so the probe
would mostly observe its own fake context being refused. One bit, honestly
described, beats a check that can be fooled in a way nobody expects.

What the bit does buy is the case that actually happens: nobody wrote it at
all.

## Consequences

**What this buys.** The failure mode changes from a silent cross-tenant read at
runtime to a named error at startup, listing every unmet obligation at once
with the registration that would satisfy it and the declaration that asked for
it — [ADR-0011](0011-actionable-errors.md)'s property, applied to the author
rather than to the client. The boundary becomes something a schema states and
something else checks, rather than something a hook file remembers.

**What this costs.** A new table in a multi-tenant schema now fails at startup
until its hooks exist, which is the intended cost and will still be experienced
as friction on the day someone is adding a table in a hurry. The escape hatch
is deliberately not a flag: the way to say these rows are not confined is to
not declare that they are, which leaves the claim and the reality in the same
place.

The check reads the registry at mount time, so a program that registers hooks
after mounting its resources is refused. That program's first request would
have run unscoped, so the refusal is correct, but it is a real ordering
constraint that did not exist before.

**Where the boundary is not held.** `sqlb.Query[T]()` in application code
bypasses this entirely — the check is at the REST mount, not on the query path.
That is deliberate (a per-query check costs on every read, and legitimate
unscoped admin paths exist) and it means this closes the *generated* hole, not
every hole. Hooks still run on those queries; nothing verifies that they were
registered.

**Row-level security remains the floor worth having.** ADR-0008 already called
RLS complementary. This makes hooks default-deny at the boundary that generates
handlers, which narrows the gap RLS was covering, and does not remove the
argument for running it underneath: a missed hook then costs an empty result
rather than everything.

## What would change our mind

- If people register empty hooks to get past the check, the check is a tax
  rather than a boundary, and the answer is to make it prove more rather than
  to relax it. Watch for a `BeforeQuery` whose body is `return nil`.
- If a legitimate deployment scopes through RLS or middleware rather than
  hooks, it has to strip the declaration from its schema today, which loses the
  documentation value. That is the case for an explicit
  `Options.ScopedElsewhere` — an opt-out that is visible at the mount site
  rather than absent from the schema. Not added now because nobody has asked
  and an unused escape hatch is the thing most likely to be reached for
  reflexively.
- If two declarations on one table start being met by one hook that only
  handles one of them, the single bit per hook kind is too coarse, and the next
  step is asking the builder which columns a hook's predicate names — a check
  against the compiled AST rather than against the registry.
- If the ordering constraint (register before mount) surprises anyone in a way
  the error does not resolve, the check should move to first use rather than
  mount, at the cost of failing on a request instead of at startup.
- If `Scoped` turns out to want a table-level form — a table confined by
  something that is not a column of its own, expressed today by declaring it on
  the key the hook narrows — this becomes a `TableDef` method and the column
  form becomes sugar over it.

## Cost of change

Low in both directions, and asymmetric between the two halves.

**The check is cheap to remove.** It is one call in `Resource` and one file in
`rest`. Deleting it returns the project to where it was, and no call site
depends on it.

**The declarations are cheap to add and awkward to remove.** `Scoped` is a
column marker travelling the existing capability tag, so it costs a
regeneration and nothing else; the runtime already round-trips capabilities
through that tag and this is one more. Removing it later means removing it from
every table that adopted it, and the removal is silent — which is the same
asymmetry every other capability has.

**The `Scoped` ⇒ `ReadOnly` rule is the expensive one to reverse**, because
schemas will be authored against it. Relaxing it later is safe; tightening it
further is not.

## Alternatives considered

**Generate the hook from the declaration.** `TenantScoped("org_id")` emitting
`q.Where(F("org_id").Eq(ctxValue))`. Rejected for the reason `SoftDelete` is
inert: the generator would have to guess where the tenant comes from — a
context key, a claim, a header — and a scoping predicate that reads the wrong
key is a boundary that looks like it is holding. The example's own hooks are
four lines each and every one of them makes a decision the generator could not.

**Report it through `schema.Lint`.** The lint package exists for exactly this
kind of advice, and this was nearly a twelfth rule. It lost because lint is
advisory by construction — `unindexed-filter` costs a slow query, and a missing
scope hook costs a cross-tenant read. A diagnostic nobody gates on is a comment.

**Check on the query path instead of at mount.** Refuse a query against a
`Scoped` model when no hook ran. Rejected: it pays on every read to catch a
mistake that is fixed once, and it turns a legitimate unscoped admin query into
a runtime failure.

**Require the marker to name the confining column and verify the predicate.**
The sharpest version, and the one to revisit if empty hooks appear. It needs
either startup execution of user hooks against a fake context, or predicate
inspection after the fact — the second is real and is what the third revisit
trigger describes.

## Revisions

- 2026-07-28 — Written. Prompted by a design review that named tenant scoping
  as default-open where the model it replaces is default-deny, and by
  `example/tasks`, whose hook file is a correct manual enumeration that nothing
  would have noticed the sixth table missing from.
