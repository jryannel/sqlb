# ADR-0030: A declaration that rows are confined is an obligation, not a comment

- **Status:** Working — the check refuses at mount, and `example/tasks` passes it
  without a hook being added or moved
- **Confidence:** High that the hole is real and that startup is where to close
  it; Low that one bit per hook is the final shape of the check
- **Decided:** 2026-07-28
- **Last reviewed:** 2026-07-29 (the expansion gap is closed; see Revisions)

## Context

[ADR-0008](0008-hooks-as-domain-seam.md) offers hooks as the answer to
multi-tenant scoping and says a hook "fails closed" — true of a hook that runs,
and silent about a hook nobody wrote.

That is the hole. Row-level security, the mechanism hooks replace, is
default-deny: a new table is unreachable until someone writes a policy.
`BeforeQuery` is default-open. Add a table, expose it, forget the registration,
and the resource serves every tenant's rows with a `200`. The system whose pitch
is that a column declaring nothing is unreachable had, one layer up, a boundary
that was opt-out.

`example/tasks` made it concrete: its hook file is careful, well argued and
*manual* — five models enumerated by hand. Everything about it is correct, and
nothing about it notices a sixth table.

## Decision

A schema declaration that says rows are confined becomes an obligation on the
resource that exposes the table, and `rest.Resource` refuses to mount a resource
whose obligations no hook satisfies.

```go
schema.Ref("workspace", Workspace).Filterable().ReadOnly().Scoped()
schema.SoftDelete()
```

Neither writes a predicate. `Scoped` is inert at runtime, exactly as `SoftDelete`
already was — a generated tenant predicate that reads the wrong context key is
worse than none, because it looks like a boundary. What changes is what happens
when nobody writes the hook.

**The obligation follows the exposed operations**, because a `BeforeQuery` hook
constrains what a request can see and says nothing about what it can overwrite by
id:

| Exposed | Requires |
|---|---|
| `OpList`, `OpRead` | `BeforeQuery` |
| `OpUpdate` | `BeforeUpdate` |
| `OpDelete` | `BeforeDelete` |
| `OpCreate` | `BeforeCreate` |

This is the arrangement `example/tasks` reached by hand, made compulsory — and
the example needed no change to pass, which is the strongest available evidence
that the mapping is real rather than one an author would work around. `OpCreate`
earns its row because the tenant column is `ReadOnly` and therefore absent from
the create body, so without a `BeforeCreate` the insert reaches the database with
no tenant at all.

**`Scoped` implies `ReadOnly`, and the validator enforces it.** A tenant column a
request may write is not a tenant column. `Nullable` is rejected too, since a row
whose tenant is NULL falls outside every predicate and lands in everyone's the
day somebody writes `IS NULL OR = $1`. `Immutable` is deliberately not enough: it
closes the update and leaves the create open. This half is worth more than the
hook check — it makes one shape of the leak unrepresentable rather than reported.

**The check proves a hook exists, not that it works.** A `BeforeQuery` that logs
and returns `nil` satisfies it, and one registration satisfies two declarations.
The alternative — executing the application's hooks at startup against a
fabricated context and inspecting the builder — proves more, and mostly observes
its own fake context being refused by the fail-closed hooks this project
recommends. One bit, honestly described, beats a check that can be fooled in a
way nobody expects. What the bit buys is the case that actually happens: nobody
wrote it at all.

## Consequences

**Buys.** The failure mode changes from a silent cross-tenant read at runtime to
a named error at startup, listing every unmet obligation with the registration
that would satisfy it — [ADR-0011](0011-actionable-errors.md)'s property applied
to the author rather than the client. The boundary becomes something a schema
states and something else checks.

**Costs.** A new table in a multi-tenant schema fails at startup until its hooks
exist. That is the intended cost, and it will be experienced as friction on the
day someone is adding a table in a hurry. The escape hatch is deliberately not a
flag: the way to say these rows are not confined is to not declare that they are.
The check reads the registry at mount, so a program that registers hooks after
mounting is refused — correct, since its first request would have run unscoped,
but a real ordering constraint that did not exist before.

**Where the boundary is not held.** `sqlb.Query[T]()` in application code
bypasses this entirely — the check is at the REST mount, not on the query path.
Deliberate: a per-query check costs on every read, and legitimate unscoped admin
paths exist.

`?expand` used to reach a `Scoped` table without running its hooks, which was the
sharper hole because it was reachable from a request. **It is now closed** — an
expansion runs the target's `BeforeQuery` hooks and requalifies their predicates
onto the join alias, so the hook that satisfies the mount check is the hook the
join carries. A predicate that cannot be requalified with certainty fails the
query rather than being dropped.

The composite foreign key argument is unchanged: a key carrying the confining
column makes a cross-tenant reference unrepresentable in the *data*, which still
holds for a statement someone writes by hand. It is now belt-and-braces rather
than the only thing holding. Row-level security remains the floor worth having.

## What would change our mind

- People register empty hooks to get past the check — it is a tax rather than a
  boundary, and the answer is to make it prove more, not to relax it. Watch for a
  `BeforeQuery` whose body is `return nil`.
- A legitimate deployment scopes through RLS or middleware — today it must strip
  the declaration, losing the documentation value. That is the case for an
  explicit `Options.ScopedElsewhere`, not added because an unused escape hatch is
  the thing most likely to be reached for reflexively.
- Two declarations on one table start being met by one hook that handles only one
  — the single bit per hook kind is too coarse, and the next step is asking the
  builder which columns a hook's predicate names.
- The register-before-mount ordering surprises someone in a way the error does
  not resolve — move the check to first use, at the cost of failing on a request.
- Hooks on an expansion target are commonly written with `RawPred` — the refusal
  is a tax, and the answer is a way to write a scope that survives
  requalification.
- `Scoped` wants a table-level form — this becomes a `TableDef` method and the
  column form becomes sugar over it.

## Cost of change

**The check is cheap to remove** — one call in `Resource` and one file in `rest`.
**The declarations are cheap to add and awkward to remove**, since removal is
silent, the same asymmetry every other capability has. **The `Scoped` ⇒
`ReadOnly` rule is the expensive one to reverse**, because schemas will be
authored against it: relaxing later is safe, tightening further is not.

## Revisions

- 2026-07-28 — Written. Prompted by a design review naming tenant scoping as
  default-open where the model it replaces is default-deny, and by
  `example/tasks`, whose correct manual enumeration would not have noticed a
  sixth table.
- 2026-07-29 — The expansion gap is closed by running the target's hooks, which
  is better than either answer this record proposed (refuse the combination, or
  lint it): it needs no judgement about schemas confined by something the package
  cannot see. It went unbuilt because a comment in `expand.go` asserted it could
  not be done — the type parameter and the unqualified predicates were both true
  and neither was fatal.
- 2026-07-30 — Condensed.
