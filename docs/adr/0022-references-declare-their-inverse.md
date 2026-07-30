# ADR-0022: A reference declares the name its target knows it by

- **Status:** Working — built and running against a real Postgres, in the engine,
  the schema DSL, codegen, the manifest and both REST endpoints
- **Confidence:** Medium — the SQL, the envelope and the cap are exercised
  against Postgres and over HTTP; untested at real cardinality
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-28

## Context

An outside review argued that `schema.Ref` is a column rather than a
relationship, and that a one-sided FK cannot tell the runtime the reverse
cardinality. That premise does not hold: the registry is a runtime value, a
reference carries a pointer to its target `*TableDef`, so inbound edges are found
by walking it — and cardinality follows from whether the FK column is unique.
Forward expansion is fully determined by what the schema records today.

Three things *are* missing, all on the reverse side:

- **A name.** Deriving one from the target table gives `posts` for both
  `posts.author_id` and `posts.reviewer_id`. An author's posts and the posts they
  reviewed are not the same set, and the distinction exists only in the head of
  whoever wrote the schema.
- **An exposure decision.** `Expandable()` sits on the referencing column and
  speaks for the forward direction. Deriving the reverse would make it reachable
  by default — the inversion of [ADR-0006](0006-capabilities-are-opt-in.md).
- **Anything across a module boundary.** `ExternalRef` records free text and no
  `*TableDef`, on purpose ([ADR-0015](0015-module-isolation.md)).

## Decision

**A reference may declare the name its target knows it by, and that declaration
is what makes the reverse relation exist.**

```go
schema.Ref("author", Author).Expandable().Inverse("posts").InverseExpandable()
```

Absent `Inverse`, the reverse relation does not exist — not addressable, not in
the manifest, not an error to omit. One side declares, so a module can gain an
inbound relation without its owner changing a line. Exposure stays opt-in in both
directions and separately. `ExternalRef` cannot declare an inverse, as a
compile-time distinction. Cardinality stays derived — a second source could only
disagree with the first.

**The mechanism is a model field; the DSL only emits it.** As with forward
expansion, the declaration compiles to a `sqlb:"expands=<column>"` field read by
reflection, so `Describe` users can express one too:

```go
// on Author, from Post's declaration
Posts []Post `db:"-" json:"posts,omitempty" sqlb:"expands=author_id,order=-created_at,limit=50"`
```

Direction follows from the field's type — a struct means a column of mine, a
slice means a column of theirs — so no second `collects=` keyword can disagree
with it. `.Inverse` without `.InverseExpandable()` names a reverse nothing can
reach: it goes in the manifest and emits no field.

**The reverse is a correlated subquery, not a join.** Joining a collection
multiplies the base rows, so recovering the page needs a `GROUP BY` over every
selected column, and two collections cross-product each other. A subquery in the
projection composes by addition instead, and stays one statement — which keeps
[ADR-0025](0025-expansion-is-one-statement.md)'s snapshot argument intact.
Columns are listed explicitly, so `Hidden` survives. `pgtest` checks the plan
keeps a `SubPlan` rather than collapsing into a join.

**The value is the list envelope, not a bare array.** `{"items": […],
"has_more": true}`. A bare array cannot say it is partial, and it will be
partial. No `total` — counting is a second aggregate per base row, and
`?count=exact` on the child's own endpoint is where a caller pays for one
deliberately.

**The cap is declared and defaults to 50.** Past it, the caller follows the child
resource's own endpoint filtered by the foreign key — `/tasks?list_id=eq.<id>` —
which is paging and filtering that already exist. That escape hatch only works if
the FK is `Filterable`, so `schema.Lint` says so when it is not.

**Ordering is total**, carrying the target's primary key as a tiebreaker. Under a
`LIMIT` a non-total order does not reshuffle the result, it decides which
children the caller never sees ([ADR-0027](0027-keyset-pagination.md)).

**Nesting is not this record.** One level, as forward expansion is today.

## Consequences

**Buys.** Reverse expansion becomes expressible — the half of `?expand` no amount
of join-writing would have unblocked. The manifest describes a relation from both
ends, so an agent reading `sqlb.json` sees the graph. A named reverse is also the
prerequisite for relation-spanning predicates later.

**Costs.** A second name to keep true, living on the referencing table but
shaping the target's API surface, so reading `authors` in isolation no longer
tells you what its endpoint exposes. Two methods added to a DSL whose density is
a stated virtue.

**It costs per row, which forward expansion does not.** One subquery per base row
per relation: a 50-row page expanding two collections at the default cap can
build 5,000 child rows in one statement. The FK index `schema.Lint` requires is
what keeps that from being a sequential scan per base row — which promotes that
lint from hygiene to the thing the cost model rests on.

**Two response shapes for one parameter.** `?expand=list` yields an object or
`null`; `?expand=tasks` yields `{items, has_more}`. Wrapping the forward case in
an envelope it has no use for would be the worse trade.

## What would change our mind

- Forward expansion turns out to be all anyone asks for — `?expand=author` is the
  common shape and `?expand=posts` pages badly. If `example/tasks` reads worse
  than the two requests it replaced, retire this record rather than defend it.
- The cap and order need to vary per request. PostgREST spells it
  `?tasks.limit=5&tasks.order=…`, and it is an extension to a wire format
  [compatibility.md](../compatibility.md) freezes — a decision of its own.
- Clients unwrap `{items, has_more}` at every call site — the replacement is not
  a bare array but a refusal naming the child endpoint.
- Two references to one table stay rare enough that deriving the name and erroring
  on collision would do. It wins only if reverse expansion is acceptable by
  default, which contradicts ADR-0006.
- `?expand` is dropped from the roadmap — this record retires with it.

## Cost of change

Sharply asymmetric, which is why it was decided before building. Today, renaming
or removing `Inverse` costs the schema files that use it and a regeneration.
After `?expand` ships, the relation name is **in the URL**, and
[compatibility.md](../compatibility.md) freezes that as a wire format — the
caller it hurts most, an agent generating requests from the manifest, is the one
this project is trying to serve. Reverting is cheaper than renaming: a declared
inverse nothing expands can be deleted quietly.

The envelope is expensive once anything reads it — unwrapping later breaks the
payload, not the URL. And the cap is cheap to raise and expensive to lower:
lowering truncates responses that were whole, silently.

## Revisions

- 2026-07-27 — Written, prompted by an outside review whose stated premise —
  underivable reverse cardinality — did not survive a test against the registry.
- 2026-07-28 — Forward expansion shipped, needing none of this: a reference
  targets a primary key, cardinality is refused structurally at model build, and
  exposure stayed opt-in in two halves. ADR-0025 owns the SQL shape.
- 2026-07-28 — Designed the reverse at the layer forward expansion shipped in: the
  subquery, the envelope, the cap and the total order.
- 2026-07-28 — Built the same day. Two collections produce two `SubPlan`s and no
  `GROUP BY`; the cap and `has_more` behave over HTTP; `Hidden` holds; a cycle
  does not recurse. Postgres decided one thing: NULLs lead a descending order,
  asserted rather than worked around. Building it added the capability asymmetry
  and a second lint rule. Still open: nesting, per-request order or cap, and any
  evidence at large child-table cardinality.
- 2026-07-30 — Condensed.
