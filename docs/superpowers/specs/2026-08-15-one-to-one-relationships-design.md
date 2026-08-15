# Cardinality-aware one-to-one relationships

Status: proposed
Date: 2026-08-15

## Problem

`docs/django-orm-comparison-2026-08-15.md` compared sqlb's relationship
model against Django's `ForeignKey`/`OneToOneField`/`ManyToManyField` and
found sqlb's one-to-many/many-to-one handling (`Ref` + `.Inverse()` +
`.InverseExpandable()`, [ADR-0022](../../architecture.md#references-declare-their-inverse))
at parity, but no dedicated support for one-to-one. A schema author can
already write `Ref("profile", User).Unique()`, and the constraint that makes
it structurally one-to-one exists in the database — but nothing downstream
knows it. The reverse side of `.Inverse()` still generates a capped
collection (`sqlb.Collection[Profile]` in Go, `{items, hasMore}` in the
generated clients, `{items, has_more}` over the wire) for a relation where at
most one row can ever exist.

That's not just an ergonomics tax — it's a correctness gap. The generated
type and the actual data shape disagree: a caller has to know out-of-band
that `.items` will never hold more than one element and unwrap it by hand.

## Scope

**In scope:**
- Schema-layer: deriving `Reference.OneToOne` from a single-column
  `Field.Unique()` on a `Ref`'s own FK column, with no new schema verb.
- A new schema-validation error when `ExpandOrder`/`ExpandLimit` is declared
  on an `Inverse` whose forward `Ref` is unique.
- REST/wire layer: unique-backed `Inverse` expansion returns the target row
  or `null`, not `{items, has_more}`.
- OpenAPI: the field's schema becomes `Target | null` for a unique-backed
  `Inverse`.
- Codegen: Go (`*Target` field instead of `sqlb.Collection[Target]`), the
  TypeScript client (`Target | null` instead of `{items, hasMore}`), and the
  Dart client (`Target?` getter instead of the paged-collection type).
- A new ADR recording the decision to break the Frozen list-envelope
  guarantee for this one relation shape, and a `compatibility.md` update
  carving out the exception — see "Compatibility" below.
- Tests proving the branch both fires correctly for a unique FK and stays
  off for a non-unique one, per
  [ADR-0016](../../architecture.md#guards-proven-both-ways).

**Explicitly out of scope (not precluded, just not this spec):**
- A new `.OneToOne()` schema verb. Considered and rejected in favor of
  inferring from `.Unique()`, since a unique FK is structurally one-to-one
  regardless of whether the author names it that way — this isn't a policy
  opt-in like `Filterable`/`Sortable` ([ADR-0006](../../architecture.md#capabilities-are-opt-in)),
  it's a fact already true of the constraint.
- Many-to-many. [ADR-0056](../../architecture.md#a-junction-is-a-table)
  refuses declared many-to-many sugar on grounds (junctions almost always
  carry payload columns) that don't apply to one-to-one; reopening it is a
  separate conversation with its own ADR-revisit process, not a code change
  bundled here.
- The other gaps named in the comparison doc (`bulk_update`, `auto_now`,
  backward cursor pagination, per-field Go-side validators). Each is
  independently scoped.
- Composite unique constraints. A table-level `Unique(a, b)`/
  `UniqueIndex(a, b)` that happens to include the FK column does **not**
  trigger one-to-one inference — only a single-column `Field.Unique()`
  directly on the Ref's own column does, since that's the only case where
  the column itself structurally cannot repeat.

## Design

### Declaration and inference

No new schema verb. `Ref(name, Target)` is unchanged; `Field.Unique()` is
unchanged. What's new is that `Registry.Validate()` (or the equivalent
build-time pass over the registry) sets `Reference.OneToOne = true` when a
`Field` carries both a non-nil `Reference` and a single-column `Unique`
constraint on that same field. Because `Ref()` and `.Unique()` both mutate
the same `*Field` value, call order doesn't matter:

```go
schema.Ref("profile", User).Unique()
```

is sufficient, and reads the same in either chain order.

### Validation: order/limit on a unique-backed inverse

[ADR-0011](../../architecture.md#actionable-errors) requires a rejection to
name what would have been accepted. When `.InverseExpandable(ExpandOrder(...))`
or `.InverseExpandable(ExpandLimit(...))` is declared on an `Inverse` whose
forward `Ref` is unique, validation now errors:

> `ExpandOrder`/`ExpandLimit` on `<table>.<inverse-name>` has no effect: the
> forward reference `<other-table>.<column>` is unique, so at most one row
> can ever match. Remove the `ExpandOrder`/`ExpandLimit` call.

### REST / wire layer

Forward-side expansion is already a single object and is unaffected.
Reverse-side (`Inverse`) expansion, when `Reference.OneToOne` is true,
switches from the correlated-subquery-with-cap machinery
([ADR-0022](../../architecture.md#references-declare-their-inverse)) to the
same `LEFT JOIN` + `json_build_object` mechanism forward expansion already
uses ([ADR-0025](../../architecture.md#expansion-is-one-statement)) — the
cardinality is now identical to a forward ref (zero or one row), so the
implementation can be identical too, rather than maintaining two capped-
collection code paths for what's now one shape. Output is the target row or
`null`; there is no `has_more`.

OpenAPI: the expand field's schema for a unique-backed `Inverse` becomes
`{ $ref: '#/components/schemas/<Target>' }` with `nullable: true`, replacing
the list-envelope schema.

### Codegen

Gated on `Reference.OneToOne`, three emitters change what they generate for
the *reverse* side only (forward-side codegen is already correct):

- **Go** (`codegen/models.go`, `inversesOf`): `Profile *Profile` with a
  plain `sqlb:"expands=user_id"` tag, instead of
  `Profile sqlb.Collection[Profile] \`sqlb:"expands=user_id,order=...,limit=..."\``.
  No `order`/`limit` tag components, since the new validation rule above
  makes them unreachable for a one-to-one relation.
- **TypeScript** (`codegen/tsclient.go`): the relation's type in the
  `${Type}Expand` typing becomes `Target | null` instead of
  `{ items: Target[]; hasMore: boolean }`.
- **Dart** (`codegen/dartclient.go`): the generated getter returns
  `Target?` instead of the paged-collection type; no `hasMore` accessor is
  generated for this relation.

`expandableRelations` (`codegen/models.go`) is unaffected in *which*
relations it lists — only the shape generated for a unique-backed one
changes.

## Compatibility

`docs/compatibility.md` lists **the list envelope**
(`{items, page, per_page, has_more, next_cursor?, total?}`) as Frozen, and
reverse-expansion output uses this same envelope today. Collapsing it to a
bare nullable object for unique-backed relations breaks that guarantee for
this one relation shape.

The project has broken a Frozen entry once before, deliberately:
[ADR-0040](../../architecture.md#the-driver-is-a-dependency) redefined
`Executor` pre-1.0 because holding it stable was "blocking two things sqlb
had already committed to," reasoning explicitly that "this was a
pre-1.0-or-never change: after the tag the same work is a major version and
a hand migration for every consumer." The same reasoning applies here: this
relation shape has had no time to accumulate deployed clients that read
`.items`/`.hasMore` off a one-to-one relation specifically (the shape has
never been correct for that case), so the cost of breaking it now is close
to zero and only grows.

This spec's implementation needs, alongside the code change:
- A new ADR recording the decision, following ADR-0040's shape: what was
  frozen, why breaking it now beats leaving it wrong until 1.0, and what it
  costs.
- A carve-out note added to `compatibility.md`'s Frozen list-envelope entry,
  mirroring how the `Executor` entry documents its own prior break.
- A release-notes entry with the mechanical fix: regenerate clients; code
  reading `.items`/`.hasMore` off a unique-backed `Inverse` relation now
  reads the value directly (Go: nil-check the pointer; TS/Dart: null-check).

## Testing

- **Schema**: a validation test asserting the new `ExpandOrder`/`ExpandLimit`
  rejection fires with the actionable message above.
- **Guard-proven-both-ways** ([ADR-0016](../../architecture.md#guards-proven-both-ways)):
  a test that a *non*-unique `Inverse` still gets the capped-collection
  envelope (proves the one-to-one branch didn't silently swallow the general
  case), and a test that removing `.Unique()` from a previously-unique `Ref`
  reverts codegen/wire shape back to collection-shaped.
- **migrate**: confirm `.Unique()` toggling the one-to-one signal produces no
  DDL change of its own — the underlying `UNIQUE` constraint is what
  triggers this, not a new schema primitive, so no new DDL surface is
  introduced.
- **rest** (pgtest-backed, since it needs a real Postgres instance): a
  one-to-one fixture table; `?expand=profile` on the owning resource
  returns the row object or `null`, never an envelope.
- **codegen**: golden-file tests for Go, TypeScript, and Dart, with a
  one-to-one fixture added to the existing codegen fixture set.
- **restcompat**: flagged as needing a fixture update, since this changes
  generated OpenAPI shape — the contract-diffing tests will need a baseline
  update reflecting the intentional break.

## Open questions for the implementation plan

None blocking. The one item worth a second look during implementation:
whether `Registry.Validate()` is the right place to derive `OneToOne`, or
whether it belongs earlier (at `Ref()`/`Unique()` call time) so that
codegen and REST mounting don't each need their own "is this unique"
lookup — likely a plan-time detail rather than a design-level fork.
