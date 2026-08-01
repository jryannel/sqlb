# ADR-0033: An array is its element type plus a flag, and the slice stays plain

- **Status:** Working — all three steps are built. `example/tasks` declares a
  `text[]`, and a real Postgres round-trips the codec, runs the six operators
  and reads an array column back through `introspect` unchanged
- **Confidence:** High on the shape, which survived implementation with one
  correction. Low on the operator names, which are wire format from the first
  request with nobody's use behind them yet — and the negations added on
  2026-08-01 doubled the surface that is expensive to rename
- **Decided:** 2026-07-29
- **Last reviewed:** 2026-08-01 (the negated operators were added; the codec's
  rationale remains superseded by ADR-0040 and the decision untouched)

## Context

Two outside evaluations named array columns as the cheapest schema gap with the
most real call sites. The second puts a number on it: nine array columns across
eight files, nineteen queries using an array operator — and the columns are
*concentrated*. Five of the six affected table structs belong to one application
and carry both of its headline lists, so the gap does not merely cost a feature;
it removes an application from the set that could pilot sqlb at all.

It fails in three places: `schema.Type` has no array constant, `migrate.sqlType`
ends in `unknown type`, and **`introspect` does not map `text[]`** — the
expensive one, because the adoption loop needs `Diff` to come back empty and a
dropped column makes it non-empty forever. One `text[]` anywhere in a module
means that module cannot be adopted at all.

**The half that was not obvious:** `database/sql` cannot scan a Postgres array
literal into a `[]string` and cannot bind one either. `pq.Array` was unavailable
under the stdlib-only invariant, so supporting arrays meant writing the
array-literal codec in both directions — bounded work, landing in two places that
are already single choke points, `compiler.bind` and the scan assignment.

**That invariant was inverted, and the codec is gone.**
[ADR-0040](0040-the-driver-is-a-dependency.md) took a direct pgx dependency, pgx
decodes and encodes arrays natively, and `array.go` was deleted along with the
449 lines and the public `sqlb.EncodeArray` that went with it. A `[]string` is
now bound and scanned with nothing in between.

That does not make the work wasted or this record wrong. The codec is what let
arrays ship under the constraint that held when it was written, and every
decision below — the element-type-with-a-flag shape, the three operators, the
refusal of NULL elements — is independent of who does the decoding and survived
the deletion unchanged. What did change is where arrays are tested: the engine's
own suite cannot test a codec it no longer contains, so `pgtest/array_test.go`
is the whole of their coverage now.

**Why jsonb does not answer this.** For a greenfield schema it is defensible. An
adoption target already has `text[]`, and declaring it `jsonb` makes `Diff`
render an `ALTER` that rewrites the column — so the zero-migration probe is gone.
It also loses on the wire: `TypeJSON` emits `unknown` in the TypeScript client,
where an array of text is exactly the case that could say `string[]`.

## Decision

**An array is its element type with a flag.** `FieldDesc.Type` keeps naming the
element and gains `Array bool` beside it:

```go
schema.Text("tags").Array().Filterable()
schema.Enum("labels", "red", "green").Array()
```

Not `TypeTextArray` per element type — the filter parser needs the element type
*back*, since `?tags=has.urgent` binds a `text`, and a fused constant would be
split apart again at the one place that most needs to get it right. Keeping the
enum's value list and the varchar length attached to the element comes free.

`Nullable()` still means the column may be NULL. A NULL *element* is refused by
`schema.Validate`: `{a,NULL,b}` versus `NULL` is a distinction neither generated
client can express. The element set is the scalar types, one dimension only, and
`introspect` **refuses** anything outside it rather than dropping it.

**The Go type is the plain slice** — `[]string`, not a named `sqlb.TextArray`.
Decided by the adoption path: both evaluations put `sqlb.Describe` over existing
sqlc structs at the first gate, and sqlc in pgx mode emits `[]string` for a
`text[]`. A named type would make arrays absent from exactly the path meant to
prove the library cheaply.

**Three operators, and `contains` is not one of them:**

| Request | SQL | Bound value |
|---|---|---|
| `?tags=has.urgent` | `$1 = ANY(tags)` | the **element**, a scalar |
| `?tags=hasany.a,b` | `tags && $1` | an array |
| `?tags=hasall.a,b` | `tags @> $1` | an array |

`has` is the shape the census counts nineteen of, and it binds a scalar — so the
operator covering observed usage needs none of the encoding half of the codec.

**`contains` stays text-only.** Two operators with one name, distinguished by
column type, is exactly the ambiguity the generated clients exist to remove. `eq`
compares whole arrays and is allowed; `in`, the ordering operators and `between`
are refused.

**`Sortable` and `Searchable` are refused; `Filterable` requires a GIN index.**
Sorting would make keyset pagination encode an array into the cursor, which is
wire format ([ADR-0027](0027-keyset-pagination.md)). An array filter without a
GIN index is a sequential scan — the failure mode
[ADR-0026](0026-vectors-declare-their-index.md) exists to name, arriving at a
case that costs a fraction as much to get right.

## Consequences

**Buys.** The adoption loop stops failing closed on a whole module for one
column. The generated clients gain `string[]` where they had `unknown`, and
`?tags=has.urgent` is a filter a hand-written handler had to spell for itself.

**Costs.**

*A codec this project owns and fuzzes* — quoting, escaping, embedded commas and
braces, the empty array versus NULL. About two hundred lines of the kind of code
that is wrong in exactly the cases nobody writes a test for.

*Nine switch statements that grow an arm, with no compiler to check they all
did.* A site that ignores the flag keeps compiling and renders `text`. The
emitters are worse than the engine: a missed arm in `tsclient.go` produces a
client type silently *wider* than the server, which is the one failure
[ADR-0028](0028-typescript-client.md) claims cannot happen.

*Wire format from the first request*, which is the only reason confidence is Low.
And a Stable-tier struct change, since `Array` lands on `schema.FieldDesc`.

## What would change our mind

- **`has` turns out to be the only operator anyone reaches for** — then `hasany`,
  `hasall` and the entire *encoding* half of the codec were not worth building.
  Watch the first real schema's query log.
- **A real sqlc struct does not present as a plain slice.** A schema with
  nullable elements can produce `[]pgtype.Text`. If that shape is common in the
  codebases actually adopting, a named type wins on the merits.
- **The GIN requirement is experienced as a tax** — someone adding an index to
  satisfy a check and never querying through it. Then it becomes a lint rather
  than a refusal.
- **A schema wants two dimensions, or an array of jsonb.** The first to ask is
  evidence, not a nuisance.
- **`Sortable` is asked for with a real use behind it.**

## Cost of change

*Cheap:* the codec, the bind and scan wrapping, the DDL spelling, the
`introspect` mapping — all internal. *Expensive:* the operator names — six since
2026-08-01 — from the first request a deployed client sends, and `Array` on
Stable-tier `FieldDesc`.

*Asymmetric in the useful direction twice:* refusing `Sortable`/`Searchable` now
and allowing later is additive, and so is relaxing the GIN requirement to a lint
— the same reasoning [ADR-0017](0017-enums-as-text-and-check.md) used to start an
enum from text. The one decision with no useful asymmetry is the plain slice,
which is why it rests on the adoption argument rather than on reversibility.

## Sequencing

Three steps, each shippable alone, all three built — they landed together because
step 1 emits a model with a slice field that step 2 is what makes scannable:

1. **Declare, render, introspect.** No runtime behaviour. This alone removes
   arrays from the adoption blocker list.
2. **The codec**, at `compiler.bind` and the scan assignment, with a fuzz target.
3. **Operators and emitters** — `has` first, then `hasany`/`hasall`, then
   `string[]` in the clients and `--tags-has` in the CLI.

## Revisions

- 2026-07-29 — Written before implementation. The two decisions that carry the
  record are both forced by things this repository already does: the filter parser
  binds an element, and `deps-check` refused the library that would have supplied
  the codec.
- 2026-07-29 — Implemented, all three steps in one change. Three things the record
  did not anticipate, kept because what a record got wrong is the part worth
  keeping:
  - **The codec was wrong exactly as predicted, and the fuzz target found it in
    four seconds.** `strings.TrimSpace` is Unicode-aware and Postgres is not, so
    an element containing U+0085 read back a byte shorter.
  - **A nil slice binds as NULL, not `{}`.** The first implementation encoded
    both as the empty array, so every unset nullable array column would have been
    written empty. A real Postgres caught it; the unit tests could not, because
    encode and parse agreed about a question neither was asking.
  - **`contains` on an array column is refused with a different message** — it
    names the operators an array *does* take, because a caller who wrote
    `contains.urgent` almost certainly meant `has`.

  Arrived unchanged: the element-plus-flag spelling, the plain slice, the three
  operator names, the two refusals, and the GIN requirement.
- 2026-07-30 — Noted that ADR-0040 inverts the constraint that produced the
  codec. The decision is untouched: neither the wire format nor the schema DSL
  knows which library does the decoding.
- 2026-08-01 — **The three operators became six**, and the vocabulary gained
  `nhas`, `nhasany` and `nhasall` (with `nhasdoc` beside `hasdoc` for the same
  reason). This record listed no negation at all, which was not a considered
  omission — it simply did not come up, and an array column was filterable in
  one direction only.

  What forced it was the *other* frontend. The JSON filter tree gained a `not`
  group in the same change, so a tree could already negate anything; but the URL
  grammar conjoins by design and has nowhere to put a `not`, so without a
  negated operator the two frontends would have compiled different vocabularies
  — and [ADR-0003](0003-one-ast-two-producers.md)'s claim is that they compile
  the same predicate. That claim is what made this a blocker rather than a
  convenience.

  Three things worth recording:
  - **The `n` prefix is `nin`'s, not `notnull`'s.** The table already spelled
    negation two ways, so a rule had to be picked; `nin`'s generalises to any
    later `has*` operator without a second naming argument. `hasnone` reads
    better for `nhasany` alone and was rejected for that reason — it does not
    extend, and `hasnotall` is worse than `nhasall`.
  - **`not` takes exactly one child.** A child *list* would have to mean either
    `NOT (a AND b)` or `NOT a AND NOT b`, and those differ on exactly the rows a
    filter separates. Refusing the second child costs a wrapping group and buys
    a tree that is never read for a convention.
  - **A negation is not a complement, and this is the part that will surprise
    someone.** Each compiles to `NOT (…)`, so a NULL column matches neither
    direction. That is what `nin` already did, and it is proven against a real
    Postgres in `pgtest/array_test.go` rather than asserted from rendered SQL —
    the claim is about three-valued logic, which only Postgres can settle.

  Unchanged: everything the record already decided. The negations are additive,
  they reuse the operand shapes and the column-kind gate, and no positive
  operator moved.
