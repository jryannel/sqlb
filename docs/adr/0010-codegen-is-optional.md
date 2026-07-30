# ADR-0010: Code generation is optional

- **Status:** Working
- **Confidence:** Medium
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

[ADR-0004](0004-schema-as-go-dsl.md) makes the Go schema DSL the source of truth.
That is a good end state and a poor starting position: adopting it in an existing
project means importing the schema, handing over DDL control, and regenerating
models that already exist. The engine needs none of that — it reflects over
struct tags, and derives column names from field names when no tag is present.

## Decision

The schema DSL and the generator are optional. Any struct works with the query
builder. Metadata the builder cannot infer is supplied at runtime:

```go
sqlb.Describe[Invoice]().
    PrimaryKey("id").
    Defaulted("id").
    Filterable("customer_id", "paid").
    Sortable("amount_due").
    Hidden("memo")
```

Descriptions merge onto struct tags, so a partly tagged model can be completed.
Naming a column that does not exist panics at startup, listing the ones that do.

Every capability the generator can emit has a runtime form, including relations —
`Relation("Customer", "customer_id")` is the no-codegen half of `?expand`. That
is the test this decision must keep passing: a capability reachable only from
generated tags would quietly make the generator mandatory again.

## Consequences

**Buys.** sqlb can be layered over structs another generator produced without
editing them, making adoption incremental instead of a migration. It also keeps
the engine honest — anything it needs must be expressible without the schema
package, which stops the two from fusing.

**Costs.** Two ways to say the same thing, which can disagree; nothing checks
either against the database. `Describe` mutates the cached model without locking,
which is correct at `init` and wrong afterwards — documented, not enforced.

## What would change our mind

- A `Describe` call after startup causes a real data race — add a
  `sync.Once`-guarded freeze that panics on late mutation. Not a lock on the read
  path: that taxes every query for a startup-only concern.
- The two routes drift confusingly — make the generator emit `Describe` calls
  rather than tags, so there is one mechanism.
- Nobody uses the runtime-only path — then this carries weight for nothing.

## Cost of change

Low either way. Removing `Describe` breaks only the runtime-only path. The
expensive failure is letting the two mechanisms diverge in *meaning* — keep them
describing the same model, or collapse them.

## Revisions

- 2026-07-27 — Written.
- 2026-07-30 — Condensed.
