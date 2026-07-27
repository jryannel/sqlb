# ADR-0011: Rejections name what would have been accepted

- **Status:** Working
- **Confidence:** High
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

Because capabilities are opt-in ([ADR-0006](0006-capabilities-are-opt-in.md)),
requests get rejected routinely — that is the design working. The question is
what a rejection tells the caller.

The caller most likely to hit one is a program assembling a request against a
schema it only partly knows: a frontend, a client library, or an agent. For all
three, `400 column is not sortable` is a dead end that costs a round trip and a
guess.

## Decision

Every rejection carries what was wrong *and* the alternatives that would have
worked:

```
filter: sort=body: column is not sortable (allowed: title, status, view_count, published_at, created_at)
```

Parsing collects every problem in a request rather than stopping at the first,
so a malformed request takes one round trip to fix rather than one per mistake.
Schema validation follows the same rule and reports every authoring mistake at
once.

The exception is `Hidden` columns, which are reported as unknown and never
appear in an allow-list — the diagnostic must not become an oracle.

## Consequences

**What this buys.** A caller can correct itself from the response. The
allow-list doubles as discovery, which reduces how much of the schema a client
needs to know up front. The same property makes the API pleasant to drive with
an agent, which is a primary goal rather than a side effect.

**What this costs.** Error responses are larger, and they disclose the shape of
the resource — column names and which are filterable. That is intended for an
exposed resource, but it does mean the exposure decision carries more weight.

## What would change our mind

- If disclosing the filterable column set is unacceptable for some resource,
  add a per-resource terse mode. Do not make terse the default: the discovery
  benefit is most of the value.
- If allow-lists get long enough to be unhelpful, truncate with a count and a
  pointer to the OpenAPI document rather than dropping them.

## Cost of change

Additive changes are free; the response shape is not. Anything that parses the
error body — a generated client, an agent's retry logic — depends on the current
structure, so changing field names or nesting is a breaking API change even
though it is only an error path.

Adding a terse mode alongside the current one costs almost nothing. Replacing
the current one costs a client migration.

## Alternatives considered

**Terse rejections.** Rejected: it optimises for response size over the
caller's ability to recover, and response size is not the constraint here.

**Silently ignore unknown parameters.** Rejected outright — a filter that is
dropped without comment returns more rows than the caller asked for, which is
the worst possible failure for an authorisation-adjacent parameter.

## Revisions

- 2026-07-27 — Written.
