# ADR-0033: An array is its element type plus a flag, and the slice stays plain

- **Status:** Exploring — nothing is built. This records the shape before the
  first line of it, which is the order [the README](README.md) asks for and the
  same order [ADR-0026](0026-vectors-declare-their-index.md) used
- **Confidence:** Medium on the shape — the failure points are read off the code
  rather than guessed, and the two decisions that matter are both forced by
  constraints this repository already enforces. Low on the operator names, which
  become wire format on the first request and have nobody's use behind them yet
- **Decided:** 2026-07-29
- **Last reviewed:** 2026-07-29

## Context

Two independent outside evaluations have now named array columns as the
cheapest schema gap with the most real call sites, and the second one
([review-adoption-multi-app.md](../review-adoption-multi-app.md)) puts a number
on it: nine array columns across eight files, nineteen queries using an array
operator, and — the part that decides more than the count does — the columns are
*concentrated*. Five of the six affected table structs belong to one
application, and they carry both of its headline lists. So the gap does not
merely cost a feature; it removes an application from the set that could pilot
sqlb at all.

The same evaluation ranks the fix third of six upstream asks and describes
closing it as taking "the schema-gap objection from twenty tables to the vector
column."

### Where it fails today

Three places, and the third is the one that matters most:

- **`schema.Type`** has fourteen constants
  ([`schema/type.go`](../../schema/type.go)) and none of them is an array, so
  there is no field constructor to write.
- **`migrate.sqlType`** ends in `column %q has unknown type %q`
  ([`migrate/ddl.go`](../../migrate/ddl.go)), so nothing renders even if a
  declaration existed.
- **`introspect`** does not map `text[]`, so a table carrying one cannot round
  trip. This is the expensive one. The adoption loop for an existing database is
  `introspect.Registry` → `codegen.RenderSchema` → `migrate.Diff` comes back
  empty ([ADR-0014](0014-migrations-and-import.md)), and a dropped column makes
  `Diff` non-empty forever. One `text[]` anywhere in a module means that module
  cannot be adopted at all — not that it adopts with a gap.

### The half that is not obvious

The declaration is the visible problem and the smaller one. `database/sql`
cannot scan a Postgres array literal into a `[]string`: the driver hands back
`{a,b,"c d"}` as bytes and the conversion fails. It cannot bind one either —
`driver.Value` has no slice case beyond `[]byte`.

The usual answer is `pq.Array`, and it is unavailable here. `mise run
deps-check` fails any package outside `rest` and the blog example that acquires
a third-party transitive dependency, and that invariant is the reason importing
sqlb costs a consumer nothing. So supporting arrays means writing the Postgres
array-literal codec, in both directions, against the standard library alone.

That cost is real and it is bounded. It also lands in two places that already
exist and are already single choke points: `compiler.bind`
([`compile.go`](../../compile.go)) is the one function every bind parameter
passes through, and the scan target assignment in
[`exec.go`](../../exec.go) is the one place a result column is pointed at a
struct field.

### Why the jsonb answer does not work here

The DSL's current advice for a list-shaped column is `schema.JSON`, and for a
greenfield schema it is a defensible answer. It fails on the case that motivates
this record.

An adoption target already has `text[]` in Postgres. Declaring it as `jsonb`
does not describe that database, so `Diff` renders an `ALTER` that rewrites the
column, and the adoption loop's premise — that the first diff against the live
database is empty — is gone. The evaluation's Gate 0 is explicitly a
*zero-migration* probe; "adopt this module by rewriting two production columns
first" is not the same offer.

It also loses on the wire, where the reason is `tsBaseType` in
[`codegen/tsclient.go`](../../codegen/tsclient.go): `TypeJSON` emits `unknown`,
because a jsonb column can hold anything. An array of text is exactly the case
where the generated client *could* say `string[]` and the whole argument of
[ADR-0028](0028-typescript-client.md) is that it should.

## Decision

### An array is its element type with a flag

`FieldDesc.Type` keeps naming the *element*, and gains `Array bool` beside it.
The spelling is a modifier:

```go
schema.Text("tags").Array().Filterable()
schema.Enum("labels", "red", "green").Array()
```

Not `TypeTextArray` as a parallel constant per element type. The reason is not
the combinatorics in the switch statements, which would be tolerable — it is
that the filter parser needs the element type *back*. `?tags=has.urgent` binds a
`text`, not a `text[]`, so a fused constant would be split apart again at the
one place that most needs to get it right. Keeping `TypeEnum`'s value list, the
varchar length and the enum union in `client.gen.ts` attached to the element is
the same property, obtained for free.

`Nullable()` continues to mean the column may be NULL. A NULL *element* is
refused by `schema.Validate`: `{a,NULL,b}` versus `NULL` is a distinction
neither generated client can express, and a column that admits both produces two
different absences that a UI has to tell apart.

The element set is the scalar types — text, varchar, enum, the numerics, bool,
uuid and the three time types. Not `jsonb`, not `bytea`, and one dimension only.
`introspect` **refuses** anything outside that set rather than dropping it, for
the reason the round-trip exists: a silently dropped column produces a `Diff`
that proposes deleting production data.

### The Go type is the plain slice

`[]string`, not a named `sqlb.TextArray`. sqlb owns the encoding and decoding
at the two choke points named above, and the struct the application holds stays
something it could have written itself.

This is decided by the adoption path rather than by taste. Both evaluations put
`sqlb.Describe` over *existing sqlc structs* at the first gate, and sqlc in pgx
mode already emits `[]string` for a `text[]` column. A required named type would
make the array feature absent from precisely the path that is supposed to prove
the library cheaply — and, in the concrete case, absent from the two headline
lists of the one application whose arrays created the gap.

Nothing in the model layer changes to allow this: `isScannable` in
[`model.go`](../../model.go) already accepts any type implementing
`sql.Scanner` or `driver.Valuer`, and a bare slice field is already read as one
column rather than decomposed into several.

### Three operators, and `contains` is not one of them

| Request | SQL | Bound value |
|---|---|---|
| `?tags=has.urgent` | `$1 = ANY(tags)` | the **element**, a scalar |
| `?tags=hasany.a,b` | `tags && $1` | an array |
| `?tags=hasall.a,b` | `tags @> $1` | an array |
| `?tags=isnull` / `notnull` | unchanged | none |

`has` is the shape the census counts nineteen of, and it binds a scalar — so the
operator that covers the observed usage needs none of the encoding half of the
codec. That is the sequencing this record recommends below.

**`contains` stays text-only.** It is already an `opPattern` operator gated by
`isTextColumn` in [`filter/filter.go`](../../filter/filter.go), where it means
`ILIKE '%x%'`. Array containment must not reuse the spelling. Two operators with
one name, distinguished by the type of the column they are applied to, is
exactly the ambiguity the generated clients exist to remove — and it would land
on the column type where narrowing is most valuable. The existing refusal,
`operator %q needs a text column, but %s is %s`, keeps doing its job unchanged
for an array column.

`eq` compares whole arrays and is allowed. `in`, the ordering operators and
`between` are refused: a list of arrays has no spelling in the grammar, and
Postgres's array ordering is not a thing an API should offer.

### `Sortable` and `Searchable` are refused; `Filterable` requires a GIN index

`schema.Validate` refuses `Sortable()` on an array — Postgres will order arrays
happily, but keyset pagination would then have to encode one into the cursor,
and the cursor payload is wire format ([ADR-0027](0027-keyset-pagination.md)).
`Searchable()` is refused for the same reason `contains` is: search is a text
operation.

`Filterable()` on an array column requires a GIN index on it, checked by
`schema.Validate`. An array filter without one is a sequential scan, which is
the failure mode [ADR-0026](0026-vectors-declare-their-index.md) exists to name:
the query *works*, so nothing reports it and the plan is only visible to someone
who looks. `AddIndex{Method: "gin"}` already renders, and the opclass gap in the
index DSL does not bite here — `array_ops` is the default.

This is [ADR-0026](0026-vectors-declare-their-index.md)'s central argument, that
a column whose access needs an index should declare it, arriving at a case that
costs a fraction as much to get right. That is a reason to do arrays *first*.

## Consequences

**What this buys.** The adoption loop stops failing closed on a whole module for
one column. `Diff` renders `text[]`, `introspect` reads it back, and the
fixpoint CI enforces holds for a schema that has one. The generated clients gain
`string[]` where they would have had `unknown`, and `?tags=has.urgent` is a
filter a hand-written handler had to spell for itself.

**What this costs.**

*A codec this project has to own and fuzz.* Quoting, escaping, embedded commas,
braces and quotes, the empty array, and the difference between an empty array
and NULL. It is stdlib-only and about two hundred lines, and it is the kind of
code that is wrong in exactly the cases nobody writes a test for. [`filter`
already carries a fuzz target](../../filter/) and this needs one on the same
footing.

*Ten switch statements that grow an arm, and no compiler to check they all did.*
`Array` is a struct field, so a site that ignores it keeps compiling and renders
`text` where it meant `text[]`. The emitters are worse than the engine here: a
missed arm in `tsclient.go` produces a client type that is silently *wider* than
the server, which is the one failure [ADR-0028](0028-typescript-client.md)
claims cannot happen. The `@ts-expect-error` refusals file is what makes that
claim testable and it has to grow with this
([ADR-0016](0016-guards-proven-both-ways.md)).

*Wire format, from the first request.* `has`, `hasany` and `hasall` are in the
filter grammar, which [compatibility.md](../compatibility.md) freezes. The
confidence line on this record is Low for that reason and no other.

*A Stable-tier struct change.* `Array` lands on `schema.FieldDesc`, and `schema`
is Stable tier under [ADR-0013](0013-no-internal-split.md).

## What would change our mind

- **If `has` turns out to be the only operator anyone reaches for**, then
  `hasany` and `hasall` — and with them the array-valued bind parameter, and
  with that the entire *encoding* half of the codec — were not worth building.
  The decoding half is still required, because scanning a result is not
  optional. Watch the first real schema's query log before building the second
  and third operators.
- **If a real sqlc struct does not present as a plain slice.** The
  plain-slice decision rests on sqlc emitting `[]string` for `text[]`, which is
  its default in pgx mode. A schema whose elements are nullable can produce
  `[]pgtype.Text` instead, which sqlb cannot scan without the dependency it
  refuses to take. If that shape is common in the codebases actually adopting,
  the argument for the plain slice weakens and a named type wins on the merits.
- **If the GIN requirement is experienced as a tax rather than a boundary** —
  someone adding an index to satisfy a check and never querying through it —
  then it should become a lint that reports the pairing rather than a refusal,
  which is the retreat [ADR-0030](0030-declared-scope-is-required.md) names for
  its own check.
- **If a schema wants two dimensions, or an array of jsonb.** Both are refused
  here on the grounds that nobody has asked. The first one to ask is evidence,
  not a nuisance.
- **If `Sortable` is asked for with a real use behind it.** Refusing now and
  allowing later is additive; the reverse is not, which is why the refusal is
  the starting position rather than the conclusion.

## Cost of change

**Declining today is free**, and stays free until the first line is written.
Nothing declares an array, so reverting this record to "use jsonb, or stay on
sqlc for that module" costs nothing but the paragraph explaining why.

After that the bill splits.

*Cheap.* The codec, the bind and scan wrapping, the DDL spelling, the
`introspect` mapping. All of it is internal: no caller wrote any of it, and it
can be rewritten under a green test suite.

*Expensive.* The three operator names, from the first request a deployed client
sends. And `Array` on `FieldDesc`, which is Stable tier — a schema that declares
`.Array()` breaks if the spelling changes.

*Asymmetric, twice, and in the useful direction both times.* Refusing
`Sortable` and `Searchable` now and allowing them later is additive; allowing
them first and withdrawing them breaks callers. Requiring the GIN index now and
relaxing to a lint later is additive; relaxing first and requiring later breaks
schemas that were previously valid. Both refusals are therefore the cheap
direction to be wrong in — the same reasoning
[ADR-0017](0017-enums-as-text-and-check.md) used to start an enum from text.

The one decision with no useful asymmetry is the plain slice. Accepting
`[]string` now and *also* accepting a named type later is additive; so is the
reverse. That is why it is decided on the adoption argument rather than on
reversibility — nothing about the ordering protects a wrong answer here.

## Alternatives considered

**Decline it; keep telling people to use jsonb.** The status quo, and the
strongest alternative on a greenfield schema, where it is genuinely a fine
answer. It loses on adoption and only on adoption: a database that already has
`text[]` cannot be described by a schema that says `jsonb`, so the empty-first-
diff property that makes [ADR-0014](0014-migrations-and-import.md)'s loop worth
having is lost for the whole module. If sqlb did not claim to adopt existing
databases, this would be the answer.

**A parallel constant per element type** — `TypeTextArray`, `TypeIntArray`, and
so on. Loses because the filter parser has to recover the element type to bind
`has`'s operand, so the fusing is undone at the site where a mistake is most
expensive. It would also grow `widens`, `tsBaseType`, `dartType` and the CLI
flag renderer by an arm per element rather than a branch per site.

**A named `sqlb.TextArray` type.** Genuinely close, and it wins on two real
things: the codec becomes reachable to an application that wants it directly,
and a generated model is self-describing about which columns are arrays. It
loses to the sqlc structs. Both evaluations put `Describe` over stock sqlc
output at the first gate precisely because it costs days rather than weeks, and
a named type makes arrays the one feature that path cannot reach — in the exact
application whose array columns are why this record exists. If the nullable-
element shape in *What would change our mind* turns out to be common, this
alternative wins and should be revisited on that evidence.

**`schema.Opaque("tags", "text[]")`** — a passthrough type sqlb renders and does
not understand. [ADR-0026](0026-vectors-declare-their-index.md) considered the
same escape hatch for vectors and reached the same place: it buys the DDL and
stops. No Go type, so the column cannot be scanned; no capability model, so it
cannot be filtered; no client type, so it is `unknown` on the wire. It is
[ADR-0024](0024-no-annotation-slot.md)'s argument again — the slot is the small
half of the feature.

**Reusing `contains` for array containment.** Rejected above and recorded here
because it is the obvious spelling and someone will suggest it: the name is
taken by a text pattern operator, and overloading it by column type puts an
ambiguity into the one vocabulary whose entire purpose is that there is none.

## Sequencing

Three steps, each shippable on its own, in an order where the first is worth
having even if the other two never happen:

1. **Declare, render, introspect.** No runtime behaviour: `Array` on the
   descriptor, the DDL arm, the `pg_catalog` mapping and its refusals. This
   alone removes arrays from the adoption blocker list, because the complaint is
   that a module *cannot be adopted*, not that its arrays cannot be filtered.
2. **The codec**, at `compiler.bind` and the scan assignment, with its fuzz
   target.
3. **Operators and emitters** — `has` first, since it needs only step 2's
   decoding half, then `hasany`/`hasall`, then `string[]` in the TypeScript and
   Dart clients and the `--tags-has` flag in the CLI.

## Revisions

- 2026-07-29 — Written, before any implementation, prompted by the second
  outside evaluation ranking arrays as the cheapest gap with the most call
  sites. The two decisions that carry the record — the element-type-plus-flag
  spelling and the plain slice — are both forced by things this repository
  already does: the filter parser binds an element, and `deps-check` refuses the
  library that would otherwise supply the codec.
