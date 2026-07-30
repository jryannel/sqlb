# ADR-0036: The wire spells a column the way the schema does

- **Status:** Working — this is what has shipped since `v0.1.0`; what changes
  here is that it is now a stated policy rather than an observed behaviour
- **Confidence:** High that a single spelling is right and that the mapping layer
  is the thing to refuse. Medium that *snake_case* is the right single spelling,
  which is the part a majority of camelCase adopters would reopen
- **Decided:** 2026-07-29
- **Last reviewed:** 2026-07-29

## Context

Three things are wire format and only one of them is written down.

[compatibility.md](../compatibility.md) freezes the filter grammar — the URL
syntax and its operator names — and says why: a deployed client has requests
built against it. The other two have exactly the same property and appear
nowhere:

- **Field naming.** `jsonTag` in `codegen/models.go` emits the column name
  verbatim, so a `created_at` column is `created_at` in every response, every
  request body, the generated TypeScript, the generated Dart and the OpenAPI
  document.
- **The list envelope.** `{items, page, per_page, has_more, next_cursor, total?}`,
  declared once in `rest/list.go` and shared by every resource.

Both are decided in practice and undecided in writing, which is the state that
produces a "we never promised that" argument later — and it is the state 1.0
exists to leave, because after 1.0 a wire format is revisable only by a major
version.

**The cost of the naming rule is real and measured.** One evaluation counts
1,848 snake_case JSON tags against 334 camelCase in the codebase it examined, so
85% already matched and the residual would have to be renamed on both sides of
the wire. The other found camelCase throughout — `workPackageId`, `orgId`,
`hasMore` — plus a `{data, total, cursor, hasMore}` envelope, and lists both as
cost rather than as a blocker because backward compatibility was waived. Between
them they cover the two positions an adopter can be in, and neither of them is
"this is fine, no change needed".

So the question this record settles is not whether the rule is free. It is not.
It is whether the rule is *right*, and what the alternative would cost.

## Decision

### The wire spelling of a column is the column's own name

No transformation, no configuration, no per-field override. `created_at` is
`created_at` everywhere the API can be seen.

The reason is not that snake_case is better than camelCase — it is not, and this
record does not claim it. The reason is that **there is one spelling**, and
therefore no place for two of them to disagree.

That property is what the generated clients are built on. ADR-0028's argument
for generating a client from the model rather than from the OpenAPI document is
that the document cannot express the operator vocabulary; the argument for the
generated client having *nothing in it* is this one. A client that renamed
fields would carry a mapping table, that table would be the one thing in the
generated code with a reason to drift, and the first time it drifted a column
would silently arrive as `undefined` in a UI rather than failing anywhere a test
would see.

The same spelling reaches five surfaces — the JSON body, the OpenAPI document,
the filter grammar's parameter names, the TypeScript client and the Dart client.
`?created_at=gte.2026-01-01` names the same thing the response does. Introduce a
second spelling and that stops being true for exactly one of the five, because
the filter grammar's parameters are column names by construction.

### The list envelope is one shape, and every resource has it

```json
{ "items": [...], "page": 1, "per_page": 25, "has_more": true,
  "next_cursor": "eyJrIjpb…", "total": 42 }
```

`next_cursor` is absent when there is no next page. `total` is absent unless
`?count=exact` asked for it, because counting costs a second query.

One shape rather than one per resource, for the reason above and one more: the
cursor pager in the Dart client and the query-key factory in the TypeScript one
are written once against this envelope. A per-resource envelope would make both
of them per-resource too.

### What an adopter whose front end is camelCase does

Renames, on both sides, in one coordinated release — and the honest thing to say
is that this is the cost and there is no discount available.

What makes it bounded rather than open-ended is that **the generated client is
the only consumer that has to be right**. A field renamed in the schema is
renamed in `client.gen.ts` by regeneration, and every call site that used the old
name fails to compile. The rename is mechanical and the compiler enumerates the
work; what it is not is invisible.

A team that cannot take that break has two positions available, and both are
better than a mapping layer inside sqlb:

- **Rename the columns instead.** The wire follows the schema, so a
  `RenamedFrom` migration moves both together. This is the only option that ends
  with one spelling.
- **Put the transformation in the transport**, which is already yours: the
  generated client takes a request function, and a project that must serve
  camelCase can convert in that one function. It is a mapping layer, it will
  drift, and it is *visible* — in the consuming repository, where whoever owns
  the decision can see it.

### What is frozen, precisely

At 1.0: the naming rule, the envelope's key names, and which of them may be
absent. Not the *values* — adding a field to the envelope is additive, and a new
optional key breaks no client that ignores unknown keys.

## Consequences

**`compatibility.md` grows two entries under *Frozen*.** That is the concrete
deliverable of this record; the rest is the reasoning behind them.

**A `Hidden` column has no wire spelling at all**, which is a property of this
rule rather than a separate one: the JSON tag is `-`, so it is absent from the
body, absent from the TypeScript type, and unmentionable in a filter.

**A column name is now an API decision.** It always was; saying so changes who
has to think about it and when. Renaming a column is a wire break, and
`RenamedFrom` exists to make the *database* half of that mechanical — the client
half is a regeneration and a compile error per call site.

**This closes a door that some adopters need open.** Both evaluations reached
the same conclusion — that the cost is payable — but neither team has paid it
yet, and the ports named in [the road to 1.0](../release-1.0.md) are where that
stops being a projection. If a port fails on this specifically, that is exactly
the kind of finding the gate exists to produce, and this record's Confidence line
is where it would land.

## What would change our mind

- **If a port stalls on the rename** — not "found it expensive" but "could not
  ship it" — the answer is not a mapping layer in the client. It is a
  *schema-level* naming policy: one setting that changes the column name the
  wire uses, applied identically to all five surfaces, so there is still exactly
  one spelling per deployment. That is a different feature from a per-field
  override and much smaller than one.
- **If the envelope's key names turn out to collide with a common convention**
  in a way that costs more than renaming, the answer is the same shape: one
  choice per deployment, not per resource.
- **If someone wants `data` rather than `items`** for its own sake, that is not
  evidence. The name is arbitrary and the cost of changing it is not.
- **If a second wire format is ever genuinely needed** — a legacy surface beside
  a new one — that is a second *adapter*, not a configuration of this one. `rest`
  is one way to mount the query layer and it was never the only possible one.

## Cost of change

**Free until someone deploys a client; permanent afterwards, which is now.**
There are no observed consumers, so today this could be changed for the price of
a regeneration. That will not be true of the first port, which is why the record
is written before it rather than after.

**Asymmetric in the useful direction.** A deployment-wide naming policy is
additive later — nothing that names a column today changes meaning if a future
version can also name it differently. A per-field mapping layer is not additive
in the same way, because it makes the generated client's contents depend on
configuration, and every guarantee ADR-0028 makes is about the client having no
contents to be wrong.

## Alternatives considered

**A `json:` casing option in `codegen.Options`.** The obvious answer, and the
first evaluation asks for it by name. It fails on the filter grammar: parameter
names are column names, so a camelCase body with snake_case query parameters is
two spellings in one API, and the client would have to know both. Making the
filter grammar follow too would break
[compatibility.md](../compatibility.md)'s freeze on it.

**Per-field `JSONName` in the schema DSL.** Everything the option above gets
wrong, per column, plus a second source of truth for a name the database already
holds. This is the mapping table, moved into the declaration where it looks
tidier and drifts identically.

**Emit both spellings.** Accept either on input, emit one. Doubles the input
surface, makes "which one is canonical" a question every reader has to answer,
and puts the ambiguity in the one place — the filter grammar — where the whole
project's argument is that there is none.

**Say nothing and keep shipping it.** The status quo, and it is genuinely
tempting because the behaviour would not change. It loses at exactly one moment:
the first time an adopter says "we assumed we could configure that", and the
answer has to be found by reading `jsonTag`.

## Revisions

- 2026-07-29 — Written. The behaviour is unchanged and predates this record;
  what is new is that it is a policy with a stated cost and a stated escape,
  which is what [the road to 1.0](../release-1.0.md) put in the pre-freeze set as
  stream D. Prompted by both adoption evaluations measuring the rename cost and
  neither finding it written down anywhere.
