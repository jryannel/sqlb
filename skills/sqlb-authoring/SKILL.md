---
name: sqlb-authoring
description: Use when writing or editing a sqlb schema.Field declaration in Go, or asking a question about the DSL's own vocabulary — does Col[T] have Lt/Gt, what does Scoped actually enforce, how do you read a Hidden column from trusted code, how do you get an unhooked handle inside a hook. Covers column types, capability flags (Filterable/Sortable/Searchable/Expandable/Hidden/ReadOnly/Computed/Scoped), predicates, hooks and escape hatches — the DSL's whole surface, not one project's schema.
---

# Writing a sqlb schema

This is the *authoring* direction. It answers "what can this DSL do" — the
same question every session pays a source read to answer, because prose in
`docs/` and doc comments isn't indexed the way a lookup table is.

It is **not** the *consuming* direction: which columns *this project's*
schema actually declared `Filterable` on is a different, per-project question,
answered by the generated `sqlb-schema` skill (`Options.SkillDir`,
[ADR-0049](../../docs/adr/0049-the-skill-is-generated.md)) — load that one
instead when the question is "can I filter `tasks.author_id`", not "does
`Filterable` exist". This skill can be hand-maintained where that one cannot:
the DSL's vocabulary is the same in every repository, so there is no
per-project fact to drift out from under it the way capability lists drift.
It still rots on a rename, the same risk `sqlb-queries` carries — every
symbol below is grounded at a file:line so a stale entry is checkable rather
than trusted.

Also not this skill: **where the builder ends and `Raw`/sqlc begins**, and the
four traps that compile and are wrong at runtime — that's
[`sqlb-queries`](../sqlb-queries/SKILL.md). This one is about declaring
columns; that one is about querying them once declared.

## Column types

Constructors live in `schema/field.go`, each returning `*Field` for chaining.

| Constructor | Postgres type | Notes |
|---|---|---|
| `Text(name)` | `text` | `field.go:231` |
| `Varchar(name, size)` | `varchar(size)` | `field.go:346` |
| `Int(name)` / `BigInt(name)` / `SmallInt(name)` | `integer` / `bigint` / `smallint` | `field.go:232-246` |
| `Serial(name)` / `BigSerial` / `SmallSerial` | serial variants | legacy auto-increment; prefer `Identity()`/`IdentityAlways()` below — `field.go:272-278` |
| `Float(name)` / `Real(name)` | `double precision` / `real` | `field.go:234,299` |
| `Numeric(name, precision...)` | `numeric` | `field.go:320` |
| `Bool(name)` | `boolean` | `field.go:337` |
| `UUID(name)` | `uuid` | `field.go:338` |
| `UUIDv7(name)` | `uuid`, server-generated v7 | `field.go:558` |
| `Timestamp(name)` / `Date(name)` / `Time(name)` | `timestamptz` / `date` / `time` | `field.go:339-341` |
| `JSON(name)` | `jsonb` | `field.go:342` |
| `Bytes(name)` | `bytea` | `field.go:343` |
| `Vector(name, dim)` | `vector(dim)` (pgvector) | not queryable through the builder — see "Not expressible" below |
| `Enum(name, values...)` | `text` + `CHECK` | values become the skill/manifest's `Values:` line |
| `Computed(name, type, expr)` | generated column | `field.go:471`, `FromSQL(sql)` builds `ComputedExpr` at `field.go:539` |
| `Ref(name, target)` | foreign key to `target` | `field.go:567`; `ExternalRef(relation, target)` at `field.go:608` for a table outside this registry |
| `Timestamps()` | `Group` — `created_at`/`updated_at` pair | `field.go:711` |
| `SoftDelete()` | `Group` — one nullable `deleted_at`, `ReadOnly` | `field.go:744`; see the capability table below |

`Identity()` / `IdentityAlways()` (`field.go:880,896`) are the declarable
auto-incrementing primary key ADR-0048 added — prefer these to `Serial` for a
new table; they're what makes the skill emitter's introspect round-trip keep
the primary key.

## Predicates — what `Col[T]` and `Field` can do

`Col[T]` (`expr.go:474`) is the typed facade a generated model exposes;
`Field` (`expr.go:159`) is the untyped one `sqlb.F("name")` returns. Every
`Col[T]` method below is a one-line forward to the matching `Field` method, so
the two have the same repertoire — **yes, `Lt`/`Gt` exist**, on both:

| Method | Renders | Where |
|---|---|---|
| `Eq` / `Neq` | `=` / `<>` | `expr.go:226-227`, `Col[T]` at `490-491` |
| `Gt` / `Gte` / `Lt` / `Lte` | `>` `>=` `<` `<=` | `expr.go:228-231`, `Col[T]` at `492-495` |
| `Between` / `NotBetween` | `BETWEEN` / `NOT BETWEEN` | `expr.go:379,384`, `Col[T].Between` at `496` |
| `OneOf` / `NotOneOf` | `IN` / `NOT IN` | `expr.go:259,293`, `Col[T]` at `508,511` |
| `IsNull` / `NotNull` | `IS NULL` / `IS NOT NULL` | `expr.go:239,244`, `Col[T]` at `504-505` |
| `EqField(other)` / `Col[T].EqCol(other)` | column-to-column `=` | `expr.go:234`, `Col[T]` at `499` |
| `Has` / `HasAny` / `HasAll` (+ `Not…`) | array `@>` / `&&` variants | `expr.go:324-371` |
| `Contains` / `StartsWith` / `EndsWith` | `ILIKE '%v%'` etc. | `expr.go:402-408` |
| `Like` / `ILike` | `LIKE` / `ILIKE` | `expr.go:391,396` |
| `ContainsJSON(doc)` / `NotContainsJSON(doc)` | jsonb `@>` | `expr.go:427,442` |
| `Cast(typ)` | returns `Expr`, not `Pred` | `expr.go:453` — usable in a `SELECT`, not a comparison; see `sqlb-queries` Trap 3 for why a day-filter needs `RawPred` instead |
| `Asc()` / `Desc()` | `ORDER BY` | `expr.go:639,642`, `Col[T]` at `514,517` |

`Col[T]` has no `Cast` — it hangs off `Field` only, reached via
`c.Field().Cast(...)`.

## Capability flags — effect, and where it's enforced

Every flag below is opt-in: undeclared means the wire rejects the request and
names what would have been accepted (ADR-0006/ADR-0011). All are chainable
methods on `*Field` in `schema/field.go` unless noted.

| Flag | Effect | Escape hatch / enforcement point |
|---|---|---|
| `Filterable()` | column usable in `?filter=` | `field.go:909` |
| `Sortable(nulls...)` | column usable in `?sort=`; optional `NullsFirst`/`NullsLast` overrides Postgres's direction-following default | `field.go:931` |
| `Searchable()` | column joins the `?search` fan-out; **implies `Filterable`** | `field.go:955` |
| `Expandable()` | a `Ref` column resolves inline via `?expand`; refused if the column isn't a `Ref` (`registry.go:232`) | `field.go:962` |
| `Hidden()` | column dropped from every REST response *and* from the generated typed-column facade — `Col[T]` for it doesn't exist, so a predicate against it doesn't compile | **`sqlb.F("column_name")`** still reaches it — untyped, from trusted server code (hook, action). `Hidden` closes only the compiled path and the wire; it grants no reach `F` doesn't already have. Source: `field.go:1117` (`LookupKey`'s doc comment), which states this explicitly ("a declaration about Go... where `sqlb.F` already grants the same reach untyped") |
| `WriteOnly()` | same response-omission as `Hidden`, but stays in the generated create/update bodies and the typed facade | `field.go:1092` — for a value written once and never read back through REST (an authored answer key, e.g.), as opposed to `Hidden`'s "never" on both directions |
| `LookupKey()` | declares a `Hidden` column is found *by* its value (`WHERE token_hash = $1` is intended, not a leak); refused on a non-`Hidden` column | `field.go:1122` |
| `ReadOnly()` | unwritable through REST create/update bodies; application code, hooks and actions are unaffected | `field.go:1028` |
| `Immutable()` | writable at create, not at update, through REST; same "REST only" boundary as `ReadOnly` — pair with a `BEFORE UPDATE` trigger for a real guarantee | `field.go:1032-1049` |
| `Computed(name, type, expr)` | a generated column; `FieldDesc.Computed()` reports `d.Expr != ""` | `field.go:471`, `field.go:1309` |
| `Scoped()` | declares this column confines every row to one tenant; **every exposed operation must be constrained by a hook** or the resource refuses to mount | see next section — this is one of the four named gaps |
| `SoftDelete()` (Group) | adds a nullable `deleted_at`, `ReadOnly`; writes no predicate itself — a hook must filter it, same obligation shape as `Scoped` | `field.go:718-748` |
| `PrimaryKey()` | implicitly `ReadOnly` + `Filterable`; refused if also `Hidden`/`WriteOnly` (a response needs it to address the row) | `field.go:754`, refusal at `registry.go:207-211` |
| `Nullable()` | column may be `NULL`; refused together with `Scoped` (`registry.go:228`) | `field.go:819` |
| `Unique()` | unique index | `field.go:841` |
| `Default(d *Default)` | column default | `field.go:862` |

## The four gaps this skill exists for

### `Col[T]` has the full comparison set, not just `Eq`

Covered above — `Lt` `Gt` `Lte` `Gte` all exist on both `Field` and `Col[T]`
(`expr.go:228-231,492-495`). There's no reduced facade; if a comparison
doesn't compile, the column's type is the reason (comparing a `T` the
operator doesn't accept), not a missing method.

### What `Scoped` enforces, and where

`Scoped()` (`field.go:1178`, doc at `1127-1177`) is a declaration that the
column confines the table's rows to one tenant. It **writes no predicate
itself** — same as `SoftDelete` — what it changes is what happens when the
confining predicate is *missing*: [`rest.Resource`](../../rest/rest.go)
refuses to mount the resource at startup rather than serve every tenant's
rows with a 200 (ADR-0030). The check is at `rest/scope.go:22-36` and the
refusal fires from `rest/rest.go:458` (`"%s exposes %s but %s declares no
Scoped column"`). It checks that a hook *exists*, not that it does anything
correct — a `BeforeQuery` that logs and returns nil satisfies it.

A table may declare at most one `Scoped` column (`registry.go:364`). It must
be `ReadOnly` (`registry.go:222` — otherwise a create request names its own
tenant) and must not be `Nullable` (`registry.go:228` — a NULL tenant is
outside every tenant's predicate, visible to nobody today and everybody the
day someone writes `IS NULL OR = $1`). Cannot be declared on an array column
(`registry.go:696`), a computed column (`registry.go:773`), or a vector
column (`registry.go:850`).

`OpSingleton` (the caller's-one-row resource) refuses to mount without a
`Scoped` column at all — `registry.go:521-530` and `rest/singleton.go:27` —
because a singleton addresses "the caller's row" entirely through the scope
hook; with no scope, GET answers an arbitrary row and PATCH reaches every
row.

### Reading a `Hidden` column from trusted server code

`Hidden()` removes the column from the typed facade and every REST response,
but grants no less reach than was there before: `sqlb.F("column_name")` —
untyped field access — still resolves it, same as any other column. This is
stated directly in `LookupKey`'s doc comment (`field.go:1117`): "a
declaration about Go, on the writer's side of the boundary, where `sqlb.F`
already grants the same reach untyped." Use it from a hook or action that
needs to compare against a hashed credential, for instance — the thing
`Hidden` closes is the compiled path (`Col[T]` doesn't exist for it) and the
wire (filter grammar 400s on it), not application code.

### An unhooked handle inside a hook

A hook's signature hands it only its own model — no `*DB`, no `Executor` — so
[`sqlb.TxFrom(ctx)`](../../db.go) (doc at `db.go:500-531`, func at `db.go:535`) is how it reaches
another model in the same transaction. The handle it returns carries **the
current request's registry**, so a write through it still runs that
request's hooks — including, if writing a *different* model, that model's
own scoping hook. To write past the current rules entirely — an inventory
decrement from an order hook that must not be narrowed by the buyer's own
scope — attach a second, empty registry:

```go
system := sqlb.NewRegistry()  // nothing registered — no rules apply
tx.WithHooks(system)
```

Documented at `db.go:527-530` and demonstrated at
[`docs/adr/0020-transaction-scoped-handle.md:33`](../../docs/adr/0020-transaction-scoped-handle.md)
(`tenant := db.WithHooks(sqlb.NewRegistry()) // hooks: scoped to this
handle`). `db.go` prefers `Update.One` over `Update.Exec` on the escalated
handle so that matching nothing refuses rather than silently committing
(#159).

`rest.Resource` also exposes a narrower version of the same idea —
`Options.Unscoped` releases one *named* scope for one mount, still refused at
startup if the release leaves a `Scoped` column with nothing confining it
(`rest/rest.go:295-317`). An unnamed `BeforeQuery` can never be released this
way; naming a scope is what makes it negotiable at all.

## Hooks — the full set

`hooks.go`, in the root package (not `schema`) — hooks are runtime seams, not
declarations. A `*Registry` (`NewRegistry()`,
`hooks.go:93`) holds per-model hook sets; there is no process-wide default
(ADR-0047) — build one, register into it, attach with `db.WithHooks(reg)`.

| Hook | Signature | Runs |
|---|---|---|
| `BeforeQuery` | `func(ctx, *Builder[T]) error` | before every read of `T` — including reads issued by generated REST handlers. The one that carries tenant scoping |
| `BeforeCreate` / `AfterCreate` | `func(ctx, *T) error` | around insert |
| `BeforeUpdate` | `func(ctx, *Update[T]) error` | before update |
| `AfterUpdate` | `func(ctx, []T) error` | after update, over the affected rows |
| `BeforeDelete` | `func(ctx, *Delete[T]) error` | before delete |
| `AfterDelete` | `func(ctx, int64) error` | after delete, given the row count |
| `AfterDeleteRows` | `func(ctx, []T) error` | after delete, given the rows themselves — separate from `AfterDelete` so a bulk delete only pays to materialise rows when something asked for them |

All defined at `hooks.go:36-50`. Hooks registered on one model run in
registration order; a hook returning an error aborts the operation and
reaches the caller unwrapped.

## Escape hatches, gathered

The three ways to reach past what the declared surface normally allows, in
one place because each is documented separately and none of the three
individual doc comments cross-reference the others:

| Need | Hatch | Cost |
|---|---|---|
| Read/write a `Hidden` or otherwise-undeclared column from Go | `sqlb.F("name")` | untyped — a typo compiles and fails at the database |
| Write SQL the builder has no spelling for (window functions, `WITH RECURSIVE`, jsonb edge cases) | `Raw` / `RawPred` / `RawSel` | contents unvalidated; see `sqlb-queries` for the four traps this invites |
| Run a statement past the current request's hooks (a different model's scoping, from inside a hook) | `tx.WithHooks(sqlb.NewRegistry())`, or `rest.Options.Unscoped` for one named scope on one mount | a fresh registry still can't defeat ADR-0030's mount-time check — a `Scoped` column with nothing confining it refuses to mount regardless of which handle asks |

## Not expressible — declared but unqueryable through the builder

`Vector(n)` columns have no predicate methods (no `<->`/`<#>`/`<=>` operator
support in the builder yet — reach them with `Raw`). Composite primary keys
aren't representable (a row is addressed by one column; `UniqueIndex` is the
named workaround). Range types, `EXCLUDE USING gist`, and `tsvector` full-text
search (`?search` is `ILIKE`, not `to_tsquery`) are the same shape of gap.
These are ADR-backed decisions, not oversights — check `docs/adr/` before
proposing a builder extension for one. `sqlb-queries` has the fuller list and
the reasoning.

## What this file does not say

- **Which columns *your* schema actually declared these flags on.** That's
  per-project and this file can't carry it without becoming the thing
  ADR-0049 argues against generating twice. Load the generated `sqlb-schema`
  skill for that.
- **Where the builder ends and `Raw`/sqlc begins**, and the four traps that
  compile and pass tests anyway. That's `sqlb-queries`.
- **Whether an existing codebase should adopt sqlb at all.** That's
  `sqlb-adoption`.
- **Migrations, introspection, or the REST/OpenAPI mount itself.** Those are
  `migrate`, `introspect`, and `rest/doc.go` — read those packages' own
  `doc.go` first, per `CLAUDE.md`'s orientation order.
