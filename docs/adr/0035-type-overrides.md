# ADR-0035: A type override changes the Go type and nothing else

- **Status:** Working — overrides land in the models, the typed facade, the REST
  bodies and the manifest, and a test pins each of the four things they must not
  reach
- **Confidence:** High on the boundary, which is what this record is actually
  about. Medium on the matching rules, which are the part a second real schema
  would most likely revise
- **Decided:** 2026-07-29
- **Last reviewed:** 2026-07-29

## Context

`schema.Type.GoType()` is a closed switch. A `uuid` column is emitted as
`string`, a `numeric` as `float64`, and there is no way to say otherwise.

Both outside evaluations name this, and the first makes the cost concrete
enough to quote: `uuid → string` in a codebase that uses
`github.com/google/uuid.UUID` touches the tenant middleware, every filter
registry, the list-query helper and every use-case signature. sqlc has an
`overrides:` block for exactly this and it is one of the reasons a team can
adopt sqlc incrementally — the generated code meets the codebase where it is
rather than demanding a rewrite at the boundary.

The evaluation's workaround is `sqlb.Describe[T]()` over structs sqlc already
generated, which sidesteps the whole question by never generating a struct. That
is the right move for a pilot and it is not a position: it means the schema-first
path — the one this project is actually about — is unavailable to a codebase
whose id type is not `string`.

## Decision

`codegen.Options` gains `Types []TypeOverride`. An override replaces the Go type
emitted for the columns it matches, and changes nothing else:

```go
codegen.Options{
    Types: []codegen.TypeOverride{
        {Type: schema.TypeUUID, GoType: "uuid.UUID", Import: "github.com/google/uuid"},
        {Table: "invoices", Column: "amount", GoType: "decimal.Decimal",
         Import: "github.com/shopspring/decimal"},
    },
}
```

### What "and nothing else" means, precisely

This is the whole record. An override is a **rendering** decision, and four
things that look adjacent are not downstream of it:

| | Decided by | An override… |
|---|---|---|
| The SQL type in DDL | `schema.Type` | …does not reach `migrate` at all |
| The filter grammar's coercion | the *runtime* Go type | …changes it, and that is correct |
| The wire — JSON, OpenAPI, TS, Dart, CLI | `schema.Type` | …must not change it |
| Capabilities | the declaration | …does not touch them |

Rows two and three are the ones worth being careful about, because they point in
opposite directions and both are right.

**Coercion follows the override, and has to.** `filter.Coerce` reads the model's
reflect.Type, so `?id=eq.019…` against an overridden column parses into a
`uuid.UUID` rather than a string. That is not a special case — `Coerce` already
delegates to `encoding.TextUnmarshaler`, which is how it has always handled
wrapper types reached through `Describe`. An overridden type that cannot be
coerced is a type that cannot be filtered, and the failure is the existing "values
of type X cannot be used in a filter" rather than something new.

**The wire does not follow the override, and must not.** A `uuid.UUID` is a
quoted string in JSON, so the generated TypeScript stays `string`, the OpenAPI
document stays `format: uuid`, and the CLI flag still takes text. This falls out
rather than being enforced by hand: every client emitter maps from `schema.Type`,
not from the Go type, so an override is invisible to all three by construction.
It is stated here because it would be easy to "fix" that later and break
[ADR-0028](0028-typescript-client.md)'s claim in the process — the client
describes the API, and the API did not change.

### Matching, most specific first

Three fields select columns and any combination is legal:

- `Column` alone — every column with that name, in any table.
- `Table` + `Column` — one column.
- `Type` — every column of that logical type.

More specific wins, and specificity is ordered `Table+Column` > `Column` >
`Type`. Two overrides of equal specificity matching the same column is an error
rather than a last-one-wins: a config that contradicts itself should say so at
generation time, which is cheap, rather than silently pick.

An override with none of the three set matches everything, and is refused. So is
one with no `GoType`.

### Nullable and array columns compose, rather than being special

`Nullable` still means a pointer and `Array` still means a slice, applied to
whatever the override named. `uuid.UUID` overridden on a nullable column emits
`*uuid.UUID`; on an array column, `[]uuid.UUID`. Neither needs the override to
know, because the wrapping happens after the base type is chosen — the same
place it happened before.

### An enum column cannot be overridden

`Enum` generates a named string type with one constant per value, and that type
*is* the feature — it is what makes `PostStatusPublished` exist and a typo fail
to compile, and it is what carries the value set into the TypeScript union and
the CLI's `--help`. An override would replace it with something that has none of
that, so it is refused with a message saying why.

Refusing is the additive direction: allowing it later breaks nobody.

## Consequences

**The dependency lands in the consumer, which is where it belongs.** The emitters
produce text ([ADR-0010](0010-codegen-is-optional.md)), so `Import` is a string
in a generated import block and sqlb's own dependency budget is untouched.
`deps-check` continues to pass for the same reason it passes for the cobra CLI:
nothing in this repository imports what it emits.

**A wrong override fails at compile time, in the consumer.** sqlb does not
resolve the import or check that the type exists — it cannot, because the type
lives in a module sqlb does not depend on. `GoType: "uuid.UUId"` produces Go that
does not compile, which is a bad error but a loud one and it arrives one command
later.

**The manifest reports the overridden type.** `sqlb.json` describes what was
generated rather than what the default mapping would have produced, because its
`goType` field exists for a reader deciding how to call the generated code.

**Overrides are per-project, not per-schema.** They live in `codegen.Options`
rather than in the schema DSL, and that is deliberate: the same schema generated
into two repositories with different id conventions should be able to differ.
Putting them in the declaration would make a rendering preference part of the
thing `migrate` and `introspect` read.

## What would change our mind

- **If people reach for an override to fix the wire rather than the Go type** —
  wanting `camelCase` JSON, say — the answer is not to widen this. The wire is a
  separate decision with a separate record, and an override that changed both
  would make the generated client wrong about the server.
- **If the enum refusal is experienced as a tax**, the likely real want is a
  *named* enum type in a package the consumer already has, which is a different
  feature: the constants would have to come from there too, and sqlb would have
  to check the value set matched.
- **If matching by `Type` alone turns out to be too blunt** — a schema wanting
  `uuid.UUID` for keys and `string` for an external reference — the escape is
  already there (`Table`+`Column` wins), and the signal to watch is a config with
  more exceptions than rules.
- **If a consumer needs the SQL type to move with the Go type**, that is not an
  override, it is a missing `schema.Type`. Adding one is the honest fix; making
  this mechanism reach `migrate` would let a rendering preference change what the
  database is.

## Cost of change

**Cheap now, expensive later, and the asymmetry is in the config rather than the
code.** The mechanism is one resolver threaded through four emitters and could be
rewritten under a green suite. What is not cheap to change is the *shape of the
override struct*, because it lands in a project's checked-in `sqlb.go` — renaming
a field breaks every project that wrote one.

That is the argument for the fields being few and obvious. Three matchers and two
outputs is a surface a reader can hold, and every one of them has a job that
could not be done by another.

**Refusing the enum override is the cheap direction.** Allowing it later is
additive; withdrawing it once a schema depends on it is not.

## Alternatives considered

**Put it in the schema DSL** — `schema.UUID("id").GoType("uuid.UUID")`. Reads
well and is wrong: `migrate` and `introspect` read the same declaration, and a
rendering preference sitting next to the SQL type invites exactly the confusion
the boundary section above exists to prevent. It also makes the schema
non-portable between two consumers that disagree.

**A `GoType` interface the consumer implements** — a function on Options rather
than a table of rules. More powerful, and it loses the manifest: a function
cannot be serialised, so `sqlb.json` could no longer say what was generated, and
the agent-facing description is one of the things this project is for.

**Do nothing, and tell people to use `Describe`.** The status quo, and it is a
real answer for a pilot. It fails as a position, because it says the schema-first
path is for codebases whose id type is `string` — which is a smaller claim than
this project makes everywhere else.

## Revisions

- 2026-07-29 — Written and implemented together, prompted by both adoption
  evaluations naming it and by [the road to 1.0](../release-1.0.md) putting it in
  the pre-freeze set: adding overrides after 1.0 is additive for the library and
  a regeneration-and-rewrite for anyone who already generated against the fixed
  mapping.
