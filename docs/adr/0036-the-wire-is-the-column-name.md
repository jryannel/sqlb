# ADR-0036: The wire spells a column the way the schema does

- **Status:** Amended 2026-08-02 — the decision holds; the *derivation* is now a
  declared function of the column name rather than the identity function. See
  [Amendment](#amendment-2026-08-02--the-spelling-is-a-declared-function-not-the-identity-function).
- **Confidence:** High that a single spelling is right and that a per-field
  mapping is the thing to refuse. The Medium confidence in *snake_case as the one
  spelling* is what the amendment resolves — the answer is that it is the right
  **default** and was the wrong **only**.
- **Decided:** 2026-07-29
- **Last reviewed:** 2026-08-02

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

> **Read the [Amendment](#amendment-2026-08-02--the-spelling-is-a-declared-function-not-the-identity-function) with this section.**
> Everything below is unchanged and still the reasoning; one sentence of it — "no
> transformation" — is narrowed there to "no *per-field* transformation".

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
  override. **This trigger fired on 2026-08-02 — see the amendment.**
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

## Amendment 2026-08-02 — the spelling is a declared function, not the identity function

**The trigger this record wrote for itself fired, in the form it predicted.**
[#116](https://github.com/jryannel/sqlb/issues/116) is the third adoption data
point and the first on the far side of the split: a 68-table, 236-route
application whose wire is camelCase at **100%** — 615 JSON tags, 0 snake_case,
all 33 golden contract files — consumed by 39 TypeScript services and 10
hand-written Dart files. It is one of **six** applications in the same position,
which is the part that decides it: the break is not paid once, it is paid six
times, and it is the same break each time.

The original text answered that adopter with two escapes. Both are worse than
the mapping this record set out to refuse, and the second is worse in exactly
the way the record was written to prevent.

**"Rename the columns instead" was wrong advice and is withdrawn.** camelCase
identifiers in Postgres are reachable only double-quoted — `SELECT "createdAt"`
— because an unquoted `createdAt` folds to `createdat`. Taking that advice
renames 615 columns into a form that breaks every hand-written query, every
`psql` session, every sqlc query file, every `pg_dump` a human reads and every
ad-hoc analytics query, permanently and for every future author. For a
Postgres-only library that costs strictly more than the problem, and unlike a
wire format it is not reversible. This is a correction, not a trade-off: the
advice should not have been there whatever this record decides.

**"Put the transformation in the transport" inverts this record's own
argument.** The objection to a mapping layer was that it is *"the one thing in
the generated code with a reason to drift, and the first time it drifted a
column would arrive as `undefined` in a UI rather than failing anywhere a test
would see."* That describes a hand-written transport mapping far better than a
generated one. Inside sqlb the mapping would be generated, total, and covered by
the round trip the schema already has; in the adopter's transport it is
hand-written, partial, and outside every gate sqlb ships. An in-flight adoption
has that file today and reports it as the least-checked code in the port. The
decision as written pushed the drift to the place with the fewest guarantees.

### What changes

One sentence. **"The spelling is the column name" becomes "the spelling is a
declared total function of the column name, `Verbatim` by default."**

```go
schema.New("app").WireCase(schema.Camel)   // default: schema.Verbatim, today's behaviour
```

Applied by the same pure function at all five surfaces — response and request
bodies, the filter grammar's parameter names, `?sort`, the OpenAPI document and
both generated clients — and to the rejection messages, so a 400 names the
accepted set in the spelling the caller can actually type.

### What does not change

- **One spelling per deployment.** Still exactly one, still derived from the
  column, still no table anywhere. What changed is that the derivation is
  `camel(name)` rather than `name`. Nothing gains a second source of truth,
  because both sides compute the same function from the same input.
- **No per-field override.** That is the part with a reason to drift and it
  stays refused. This is a property of the *schema*, not of a column.
- **Frozen at 1.0**, as before — with the freeze restated below.
- **[ADR-0028](0028-typescript-client.md)'s guarantee.** The client still has no
  contents that can be wrong: it is emitted from a spelling computed at
  generation time, not from a mapping consulted at runtime.

### The failure case, which is why this is a declaration and not a flag

`snake → camel` is **not** total-and-invertible over arbitrary column names. It
round-trips for `created_at → createdAt → created_at` and `pos_x → posX →
pos_x`. It does not for digit boundaries — `pos_x_2 → posX2 → pos_x2` — and
acronyms have the same hazard depending on the convention.

sqlb's usual answer applies: **refuse at build time.** `Validate` computes the
wire name and its inverse for every column and fails the schema if any column
does not round-trip, naming the column and both spellings. That turns an
ambiguity into a compile-time error on a schema nobody has deployed, rather than
a wrong parameter name in a shipped client. `WireCase(Camel)` is then either
provably safe for a given schema or refused outright — the shape
[ADR-0016](0016-guards-proven-both-ways.md) asks
for, and the same bar the fixpoint holds the round trip to.

Note what this rules out: a lossy policy cannot be adopted quietly. A schema
with `pos_x_2` in it does not get a mangled wire name; it gets a build failure
naming the column, and the author renames the column or stays `Verbatim`.

### The seam, so the cost is knowable before it is paid

The two directions are not symmetric, and only one of them touches the runtime.

**Column → wire** is entirely a code-generation decision. The response body is
`json.Marshal` over a generated struct, so `codegen.jsonTag` already decides the
body's spelling by itself; OpenAPI, TypeScript, Dart and the Go CLI all read the
same descriptor. Nothing on the request path is involved.

**Wire → column** is the runtime half, and it is small but real: the filter
grammar, `?sort` and `?expand` resolve a caller-supplied name against the
model's columns, which are keyed on the SQL name today. The rejection messages
list that same set.

The constraint to respect is that **the request path may not import `schema`** —
that is what keeps the runtime usable without the DSL, and it is why `rest.Op`
mirrors `schema.Op` by hand. So the wire spelling must cross into the runtime as
*data on the generated model*, the way capabilities already cross in the `sqlb`
struct tag, rather than as a policy the runtime evaluates. The runtime is then
told each column's wire name; it never computes one.

That boundary is what keeps this additive rather than architectural, and it is
the thing an implementation must not shortcut.

### Cost of change, restated

The original said "free until someone deploys a client; permanent afterwards,
which is now." That is still true and is now the argument *for* moving: the
amendment is additive — `Verbatim` remains the default, so every existing
deployment is unaffected and no generated artefact changes byte-for-byte — while
the thing it enables becomes unavailable the moment a camelCase adopter ships a
transport mapping and builds on it.

`compatibility.md`'s **Frozen** entry changes from "the column's own name,
verbatim" to "one spelling per deployment, computed from the column name by the
schema's declared `WireCase`, which is `Verbatim` unless the schema says
otherwise". What is frozen is that there is exactly one and that it is derived —
not which derivation a given deployment chose. Changing a deployment's
`WireCase` is a breaking change *for that deployment*, exactly as renaming a
column is.

### Why this was accepted on adoption grounds rather than architectural ones

Worth recording plainly, because the reasoning is not "the architecture wanted
this". The architecture was content: one spelling, derived, no table. The
argument that moved it is that the literal choice was blocking ports at a rate
the escapes could not relieve, and was doing so six applications at a time. This
record's own [Consequences](#consequences) anticipated it — *"This closes a door
some adopters need open. If a port fails on this specifically, that is exactly
the finding the gate exists to produce."* The gate produced the finding. This is
it.

## Revisions

- 2026-08-02 — **Amended.** The "port stalls on the rename" trigger fired
  ([#116](https://github.com/jryannel/sqlb/issues/116)). The spelling becomes a
  declared function of the column name, `Verbatim` by default; the per-field
  override stays refused; "rename the columns instead" is withdrawn as advice
  that would permanently damage a Postgres schema. Not yet implemented — this
  records the decision, the required build-time round-trip guard, and the seam.
- 2026-07-29 — Written. The behaviour predates this record; what is new is that
  it is a policy with a stated cost and a stated escape, which is what
  [the road to 1.0](../release-1.0.md) put in the pre-freeze set. Prompted by
  both adoption evaluations measuring the rename cost and neither finding it
  written down anywhere.
- 2026-07-30 — Condensed.
