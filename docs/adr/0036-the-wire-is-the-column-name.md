# ADR-0036: The wire spells a column the way the schema does

- **Status:** Working — this is what has shipped since `v0.1.0`; what changes is
  that it is now a stated policy rather than an observed behaviour
- **Confidence:** High that a single spelling is right and that the mapping layer
  is the thing to refuse; Medium that *snake_case* is the right single spelling
- **Decided:** 2026-07-29
- **Last reviewed:** 2026-07-29

## Context

Three things are wire format and only one is written down.
[compatibility.md](../compatibility.md) freezes the filter grammar because a
deployed client has requests built against it. The other two have the same
property and appear nowhere: **field naming**, where `jsonTag` emits the column
name verbatim across every response, request body, generated client and the
OpenAPI document; and **the list envelope**,
`{items, page, per_page, has_more, next_cursor, total?}`.

Both are decided in practice and undecided in writing, which is the state that
produces a "we never promised that" argument later.

**The cost is real and measured.** One evaluation counts 1,848 snake_case JSON
tags against 334 camelCase — 85% already matching, with the residual to rename on
both sides. The other found camelCase throughout plus a
`{data, total, cursor, hasMore}` envelope, and lists both as cost rather than
blocker. Between them they cover the two positions an adopter can be in, and
neither is "no change needed".

## Decision

**The wire spelling of a column is the column's own name.** No transformation, no
configuration, no per-field override.

The reason is not that snake_case is better than camelCase — it is not, and this
record does not claim it. The reason is that **there is one spelling**, so there
is no place for two of them to disagree. That property is what the generated
clients are built on: a client that renamed fields would carry a mapping table,
that table would be the one thing in the generated code with a reason to drift,
and the first time it drifted a column would arrive as `undefined` in a UI rather
than failing anywhere a test would see.

The same spelling reaches five surfaces — the JSON body, the OpenAPI document,
the filter grammar's parameter names, and both clients. `?created_at=gte.…` names
the same thing the response does. A second spelling breaks that for exactly one
of the five, because filter parameters are column names by construction.

**The list envelope is one shape, and every resource has it:**

```json
{ "items": [...], "page": 1, "per_page": 25, "has_more": true,
  "next_cursor": "eyJrIjpb…", "total": 42 }
```

`next_cursor` is absent when there is no next page; `total` is absent unless
`?count=exact` asked, because counting costs a second query. One shape rather
than one per resource, because the Dart cursor pager and the TypeScript key
factory are written once against it.

**An adopter whose front end is camelCase renames, on both sides, in one
coordinated release** — that is the cost, and there is no discount. What makes it
bounded is that the generated client is the only consumer that has to be right: a
renamed field is renamed by regeneration, and every old call site fails to
compile. A team that cannot take the break has two better options than a mapping
layer inside sqlb: **rename the columns instead**, since a `RenamedFrom`
migration moves both together; or **put the transformation in the transport**,
which is already theirs — a mapping layer that will drift, but visibly, in the
repository where whoever owns the decision can see it.

**Frozen at 1.0:** the naming rule, the envelope's key names, and which may be
absent. Not the values — a new optional key breaks no client that ignores unknown
keys.

## Consequences

**`compatibility.md` grows two entries under *Frozen*.** That is the concrete
deliverable; the rest is the reasoning behind them.

**A `Hidden` column has no wire spelling at all** — the JSON tag is `-`, so it is
absent from the body, absent from the TypeScript type, and unmentionable in a
filter.

**A column name is now an API decision.** It always was; saying so changes who
has to think about it and when. `RenamedFrom` makes the database half mechanical;
the client half is a regeneration and a compile error per call site.

**This closes a door some adopters need open.** Both evaluations found the cost
payable and neither team has paid it. If a port fails on this specifically, that
is exactly the finding the gate exists to produce.

## What would change our mind

- **A port stalls on the rename** — not "found it expensive" but "could not ship
  it". The answer is not a mapping layer in the client; it is a *schema-level*
  naming policy: one setting applied identically to all five surfaces, so there is
  still exactly one spelling per deployment. That is much smaller than a per-field
  override.
- The envelope's key names collide with a common convention expensively — same
  shape of answer: one choice per deployment, not per resource.
- Someone wants `data` rather than `items` for its own sake. That is not evidence.
- A second wire format is genuinely needed — that is a second *adapter*, not a
  configuration of this one.

## Cost of change

**Free until someone deploys a client; permanent afterwards, which is now.** With
no observed consumers this could be changed for the price of a regeneration
today, and that will not be true of the first port — which is why the record is
written before it.

**Asymmetric in the useful direction.** A deployment-wide naming policy is
additive later; a per-field mapping layer is not, because it makes the generated
client's contents depend on configuration, and every guarantee
[ADR-0028](0028-typescript-client.md) makes is about the client having no
contents to be wrong.

The obvious alternative — a `json:` casing option — fails on the filter grammar:
parameter names are column names, so a camelCase body with snake_case query
parameters is two spellings in one API, and making the grammar follow would break
compatibility.md's freeze on it.

## Revisions

- 2026-07-29 — Written. The behaviour predates this record; what is new is that
  it is a policy with a stated cost and a stated escape, which is what
  [the road to 1.0](../release-1.0.md) put in the pre-freeze set. Prompted by
  both adoption evaluations measuring the rename cost and neither finding it
  written down anywhere.
- 2026-07-30 — Condensed.
