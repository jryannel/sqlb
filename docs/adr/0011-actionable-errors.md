# ADR-0011: Rejections name what would have been accepted

- **Status:** Working
- **Confidence:** High
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

Because capabilities are opt-in ([ADR-0006](0006-capabilities-are-opt-in.md)),
requests get rejected routinely — that is the design working. The caller most
likely to hit one is a program assembling a request against a schema it only
partly knows: a frontend, a client library, or an agent. For all three,
`400 column is not sortable` is a dead end costing a round trip and a guess.

## Decision

Every rejection carries what was wrong *and* the alternatives that would have
worked:

```
filter: sort=body: column is not sortable (allowed: title, status, view_count, published_at, created_at)
```

Parsing collects every problem in a request rather than stopping at the first, so
a malformed request takes one round trip to fix rather than one per mistake.
Schema validation follows the same rule.

The exception is `Hidden` columns, reported as unknown and never listed — the
diagnostic must not become an oracle.

## Consequences

**Buys.** A caller can correct itself from the response. The allow-list doubles
as discovery, reducing how much schema a client needs up front — which is what
makes the API pleasant to drive with an agent.

**Costs.** Error responses are larger, and they disclose the shape of the
resource. Intended for an exposed resource, but it makes the exposure decision
carry more weight.

## What would change our mind

- Disclosing the filterable column set is unacceptable for some resource — add a
  per-resource terse mode, but do not make terse the default.
- Allow-lists get long enough to be unhelpful — truncate with a count and a
  pointer to the OpenAPI document rather than dropping them.

## Cost of change

Additive changes are free; the response shape is not. Anything parsing the error
body — a generated client, an agent's retry logic — depends on the current
structure, so renaming or renesting fields is a breaking change even on an error
path. Adding a terse mode alongside costs almost nothing; replacing the current
one costs a client migration.

## Revisions

- 2026-07-27 — Written.
- 2026-07-30 — Condensed.
