# ADR-0035: A type override changes the Go type and nothing else

- **Status:** Working — overrides land in the models, the typed facade, the REST
  bodies and the manifest, and a test pins each of the four things they must not
  reach
- **Confidence:** High on the boundary, which is what this record is about;
  Medium on the matching rules, which a second real schema would most likely
  revise
- **Decided:** 2026-07-29
- **Last reviewed:** 2026-07-29

## Context

`schema.Type.GoType()` is a closed switch: a `uuid` column is `string`, a
`numeric` is `float64`, and there is no way to say otherwise.

Both outside evaluations name this. The cost is concrete: `uuid → string` in a
codebase using `github.com/google/uuid.UUID` touches the tenant middleware, every
filter registry, the list-query helper and every use-case signature. sqlc has an
`overrides:` block for exactly this, and it is one reason a team can adopt sqlc
incrementally.

The workaround — `sqlb.Describe[T]()` over structs sqlc already generated —
sidesteps the question by never generating a struct. That is right for a pilot
and it is not a position: it means the schema-first path is unavailable to any
codebase whose id type is not `string`.

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

**What "and nothing else" means** — this is the whole record:

| | Decided by | An override… |
|---|---|---|
| The SQL type in DDL | `schema.Type` | …does not reach `migrate` at all |
| The filter grammar's coercion | the *runtime* Go type | …changes it, and that is correct |
| The wire — JSON, OpenAPI, TS, Dart, CLI | `schema.Type` | …must not change it |
| Capabilities | the declaration | …does not touch them |

Rows two and three point in opposite directions and both are right. **Coercion
follows the override** because `filter.Coerce` reads the model's reflect.Type and
already delegates to `encoding.TextUnmarshaler` — an overridden type that cannot
be coerced is one that cannot be filtered, with the existing error. **The wire
does not follow it**: a `uuid.UUID` is a quoted string in JSON, so TypeScript
stays `string` and OpenAPI stays `format: uuid`. That falls out rather than being
enforced, since every client emitter maps from `schema.Type` — stated here
because it would be easy to "fix" later and break
[ADR-0028](0028-typescript-client.md)'s claim in the process. The client
describes the API, and the API did not change.

**Matching is most specific first**: `Table`+`Column` > `Column` > `Type`. Two
overrides of equal specificity matching one column is an error rather than
last-one-wins — a config that contradicts itself should say so at generation
time. An override matching everything, or with no `GoType`, is refused.

**Nullable and array compose** rather than being special: `*uuid.UUID`,
`[]uuid.UUID`. The wrapping happens after the base type is chosen, as before.

**An enum column cannot be overridden.** The generated named string type *is* the
feature — it is what makes `PostStatusPublished` exist and carries the value set
into the TypeScript union and the CLI's `--help`. Refusing is the additive
direction.

## Consequences

**The dependency lands in the consumer, which is where it belongs.** The emitters
produce text, so `Import` is a string in a generated import block and sqlb's own
dependency budget is untouched.

**A wrong override fails at compile time, in the consumer.** sqlb cannot resolve
the import — the type lives in a module it does not depend on — so
`GoType: "uuid.UUId"` produces Go that does not compile. A bad error, but a loud
one, arriving one command later.

**The manifest reports the overridden type**, because `goType` exists for a
reader deciding how to call the generated code.

**Overrides are per-project, not per-schema.** The same schema generated into two
repositories with different id conventions should be able to differ. Putting them
in the declaration would make a rendering preference part of what `migrate` and
`introspect` read.

## What would change our mind

- People reach for an override to fix the wire rather than the Go type — wanting
  camelCase JSON, say. The answer is not to widen this: an override that changed
  both would make the generated client wrong about the server.
- The enum refusal is experienced as a tax — the likely real want is a *named*
  enum type from a package the consumer already has, which is a different feature.
- Matching by `Type` alone is too blunt — the escape already exists, and the
  signal to watch is a config with more exceptions than rules.
- A consumer needs the SQL type to move with the Go type — that is not an
  override, it is a missing `schema.Type`.

## Cost of change

Cheap now, expensive later, and the asymmetry is in the config rather than the
code. The mechanism is one resolver threaded through four emitters. What is not
cheap is the *shape of the override struct*, because it lands in a project's
checked-in `sqlb.go` — renaming a field breaks every project that wrote one.
That is the argument for three matchers and two outputs, each with a job no other
field could do. Refusing the enum override is the cheap direction.

## Revisions

- 2026-07-29 — Written and implemented together, prompted by both adoption
  evaluations and by [the road to 1.0](../release-1.0.md) putting it in the
  pre-freeze set: adding overrides after 1.0 is additive for the library and a
  regeneration-and-rewrite for anyone who generated against the fixed mapping.
- 2026-07-30 — Condensed.
