# ADR-0023: A mixin contributes columns; carrying behaviour needs codegen

- **Status:** Working as a decision — `schema.Group` ships and user-defined
  column mixins work today. The behaviour-carrying half is deliberately unbuilt
  and is not in 1.0; a `Group` is columns, and a bundle that also registered
  hooks would need codegen
- **Confidence:** High that splitting it this way is right — the column half has
  been used and the behaviour half still has no consumer asking for it. Medium
  on the sketched fix, which remains untried
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

An outside review recommended adding a mixin mechanism:

> `Timestamps()` and `SoftDelete()` are hardcoded mixins. Letting a user define
> their own bundle of columns + hooks + capabilities is a small change that
> removes a whole class of "can you add a helper for X" pressure later.

Half of that already exists, and the half that does not is not small.

### User-defined mixins already work

`schema.Group` is exported and satisfies `FieldSpec`, so a bundle of columns is
an ordinary function returning one. `Timestamps` and `SoftDelete` are not
privileged — they are two such functions that happen to live in the package.
Writing one outside it and mixing it with a built-in:

```go
func Auditable() schema.Group {
    return schema.Group{
        schema.Text("created_by").ReadOnly().Filterable(),
        schema.Timestamp("archived_at").Nullable().Sortable(),
    }
}

r.Table("invoices",
    schema.UUIDv7("id").PrimaryKey(),
    Auditable(),
    schema.Timestamps(),
)
```

```
  id           uuid        pk,default,filter,readonly
  created_by   text        filter,readonly
  archived_at  timestamptz sort
  created_at   timestamptz default,sort,readonly
  updated_at   timestamptz default,sort,readonly
```

Columns and capabilities compose exactly as the review asks for, today, with no
change. So "add a mixin mechanism" is already answered for the part of a mixin
that is columns.

### What a mixin cannot carry, and what that cost

A `Group` contributes `[]*Field` and nothing else. It cannot add an index, a
check constraint, or a hook. Both built-in mixins wanted the third, and
`SoftDelete` is the case where wanting it and not having it shipped a bug.

Its doc comment claimed the REST layer filtered rows with a non-null
`deleted_at` out of list responses. Nothing did. A table declaring `SoftDelete`
and exposing `OpList` returned deleted rows, and `OpDelete` hard-deleted through
an endpoint whose schema said otherwise — fixed in #4 by correcting the comment,
because the alternative was making a column name load-bearing in the runtime.
The behaviour now lives in a hand-written registration:

```go
// example/blog/hooks.go — what schema.SoftDelete cannot say for itself
func RegisterHooks() {
    sqlb.On[Post]().BeforeQuery(func(_ context.Context, q *sqlb.Builder[Post]) error {
        q.Where(sqlb.F("deleted_at").IsNull())
        return nil
    })
}
```

The mixin adds a column; a human elsewhere is trusted to add the meaning. That
gap is the actual finding, and it is not about extensibility — it is about the
built-in mixins too.

### Why the schema package cannot simply register the hook

Two independent reasons, and the second is the hard one.

**The dependency direction.** `schema` imports nothing from the engine (checked:
zero imports of `github.com/jryannel/sqlb`), and capabilities cross to the
runtime as struct tags rather than as a Go dependency. That is what
[ADR-0010](0010-codegen-is-optional.md) means by codegen being optional and what
`deps-check` enforces.

**Hooks are keyed by the Go model type, which does not exist yet.**
`sqlb.On[Post]()` needs `blog.Post`, the generated row struct. At declaration
time there is only `blogschema.Post`, a `*TableDef` — the two are deliberately
different types in different packages so that both can be called `Post`. A
schema-level mixin cannot name the type its hook would attach to, because
codegen has not produced it. No amount of loosening the import graph fixes that
ordering.

## Decision

**Leave `Group` as the mixin mechanism for columns. Do not extend it to hooks.
If a mixin is to carry behaviour, the carrier is codegen, not the schema
package.**

Concretely, and in the order the evidence supports:

1. **Say plainly that `Group` is the mixin mechanism.** It is exported, it
   works, and nothing documents it as the extension point — which is why the
   review read the built-ins as hardcoded. This is a documentation change and
   costs nothing.
2. **Let a group contribute table-level declarations**, not only fields —
   an index or a check travelling with the columns that need them. `SoftDelete`
   wants a partial index on `deleted_at`; today the caller must remember it.
   This stays inside the schema package and takes no new dependency.
3. **Treat "a mixin implies a hook" as a codegen question, not a schema one.**
   Codegen already knows both sides — the declaration and the generated type —
   so it is the only layer that *can* emit
   `sqlb.On[Post]().BeforeQuery(...)` from a declaration. It is also where the
   decision is reversible, since generated code is committed and readable.

Point 3 is deliberately not decided here. It is recorded as where the answer
would live, so the next person does not re-derive that the schema package is the
wrong place.

## Consequences

**What this buys.** The documentation change removes the pressure the review
predicted, at no design cost, because the feature was already there. Table-level
contributions close the concrete gap that `SoftDelete` exposed, without touching
the import graph. And naming codegen as the carrier for behaviour keeps the
schema package a description of a database rather than a place where runtime
behaviour hides.

**What this costs.** Soft delete stays a two-part declaration — a mixin and a
registration — and a table that declares one without the other is still wrong in
a way nothing catches. #4 corrected the comment; it did not make the mistake
impossible. A lint rule ("this table declares `deleted_at` and exposes `OpList`
but no registration is visible") is the obvious mitigation and is not written.

Extending `Group` to table-level declarations also widens `FieldSpec`, whose
method stays unexported — so mixins remain functions returning `Group` rather
than user-defined types. That is a real limit, and it is chosen: an open
interface here is the extension slot the review separately asks for, and that is
a larger decision than this one.

## What would change our mind

- **If a second mixin wants behaviour.** `SoftDelete` is one case, and one case
  is a fixable bug rather than a missing mechanism. A second — multi-tenancy as
  a mixin, say, bundling `org_id` with its scoping hook — makes it a pattern,
  and point 3 stops being deferred.
- **If the two-part declaration bites again.** If a table ships with
  `SoftDelete` and no registration a second time, the answer is a lint rule
  before it is a mechanism, and this record should say so instead.
- **If third-party extensions are ever wanted.** The review's separate suggestion
  of an annotation slot on `Table` — ent's approach, where `entoas` and `entgql`
  hang their own config — would subsume this: a mixin able to carry an
  annotation could carry anything. That is a different decision about whether
  this project wants an ecosystem, and it should be made on those terms rather
  than arrived at through mixins.

## Cost of change

**Cheap in the direction taken; the expensive move is the one not being made.**

Documenting `Group` and adding table-level contributions are additive. Nothing
breaks: existing mixins return `Group` and keep returning it, and a group that
contributes no index behaves exactly as now.

Making the schema package register hooks would be expensive and hard to undo. It
would mean `schema` importing the engine, which reverses the direction
`deps-check` enforces, and it would put runtime behaviour in the file people read
to learn what the database looks like. Codegen is the reversible version of the
same idea: generated registrations are committed, readable, and deletable.

Reversing point 2 later means finding groups that contribute an index, which the
type system makes greppable.

## Alternatives considered

**Export `FieldSpec.fields()` so users can define mixin types.** Genuinely
close, and the smallest possible change. Rejected for now because `Group`
already covers every use it would enable, and an exported interface is a
compatibility surface: once a user type implements it, its method set is frozen,
and point 2 above is exactly the kind of widening that would break it. Worth
revisiting once the shape of table-level contributions has settled.

**Register hooks from the schema package.** Rejected on the ordering argument —
the Go model type does not exist at declaration time — not merely on dependency
hygiene. This is not a matter of taste.

**Make the runtime read `deleted_at` by name.** The alternative considered in #4
and rejected there: it would make a column name load-bearing in `rest/`, which
is the PostgREST failure mode the project's whole capability model exists to
avoid. Recorded here because it is the shortest path to making `SoftDelete` mean
something, and it is the wrong one.

**Do nothing.** Defensible. `Group` works, the review's premise was mistaken,
and the `SoftDelete` gap is already documented at the call site. This record
exists mostly to stop the next reader concluding "mixins are hardcoded" from the
same evidence.

## Revisions

- 2026-07-27 — Written, prompted by an outside review. The review's premise —
  that mixins are hardcoded and user bundles need a new mechanism — was tested
  and did not hold: `schema.Group` is exported and composes. The real gap is
  that a bundle carries columns only, which is what let `SoftDelete` ship a
  comment describing behaviour nothing implemented.
