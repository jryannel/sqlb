# ADR-0010: Code generation is optional

- **Status:** Working
- **Confidence:** Medium
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

[ADR-0004](0004-schema-as-go-dsl.md) makes the Go schema DSL the source of
truth, with everything generated from it. That is a good end state and a poor
starting position: adopting it in an existing project means importing the
schema, handing over DDL control, and regenerating models that already exist.

The engine does not actually need any of that. It reflects over struct tags, and
column names are derived from field names when no tag is present.

## Decision

The schema DSL and the generator are optional. Any tagged — or untagged — struct
works with the query builder. Metadata the builder cannot infer is supplied at
runtime with `sqlb.Describe[T]()`:

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

## Consequences

**What this buys.** sqlb can be layered over structs another generator already
produced without editing them, which makes adoption incremental instead of a
migration. It also keeps the engine honest: anything the engine needs must be
expressible without the schema package, which stops the two from fusing.

**What this costs.** Two ways to say the same thing, which can disagree — a
struct tag and a description are both authoritative, and nothing checks them
against the database. `Describe` mutates the cached model without locking, which
is correct at `init` and wrong afterwards, and that constraint is documented
rather than enforced.

## What would change our mind

- If a `Describe` call after startup causes a data race in practice, add a
  `sync.Once`-guarded freeze that panics on late mutation. A lock on the read
  path is not the answer — it would tax every query for a startup-only concern.
- If the two configuration routes drift confusingly, make the generator emit
  `Describe` calls rather than tags so there is only one mechanism.
- If nobody uses the runtime-only path, this is carrying weight for nothing.

## Cost of change

Low either way, which is why this is a comfortable decision to hold loosely.
Removing `Describe` breaks anyone on the runtime-only path but nothing else;
keeping it costs a modest amount of ongoing consistency work between two ways of
saying the same thing.

The one thing that would be expensive is letting the two mechanisms diverge in
meaning. Keep them describing the same model, or collapse them.

## Alternatives considered

**Tags only.** Simplest, but requires editing structs you may not own, which
rules out the incremental-adoption story entirely.

**A config file instead of `Describe`.** Rejected: a mistyped column in YAML
fails at request time; in Go it fails at startup with the valid names listed.

## Revisions

- 2026-07-27 — Written.
