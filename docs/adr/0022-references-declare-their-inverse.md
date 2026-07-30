# ADR-0022: A reference declares the name its target knows it by

- **Status:** Working — built and running against a real Postgres, in the engine,
  the schema DSL, codegen, the manifest and both REST endpoints
- **Confidence:** Medium — the SQL, the envelope and the cap are exercised
  against Postgres and over HTTP; what is untested is a large table, since no
  consumer has yet expanded a collection at any real cardinality
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-28

## Context

An outside review of this repository argued that `schema.Ref` is a column rather
than a relationship, and that this blocks `?expand` regardless of any other
work. The claim as stated was:

> a one-sided FK column does not tell the runtime the reverse cardinality — so
> it cannot know whether to produce an object or an array, or how to aggregate

That is worth taking seriously, because `?expand` is the third item on the
not-built list and has been for the life of the project. It is also, as stated,
**wrong**, and finding out why is what makes the remaining gap clear.

### What the registry already knows

The registry is a runtime value holding every table, and a reference carries a
pointer to the `*TableDef` it targets. So the inbound edges of a table are found
by walking it. Declaring three references to one table and scanning for them:

```go
author := r.Table("authors", schema.UUIDv7("id").PrimaryKey(), schema.Text("name"))
r.Table("posts",
    schema.UUIDv7("id").PrimaryKey(),
    schema.Ref("author", author),   // the writer
    schema.Ref("reviewer", author), // ...and the reviewer
)
r.Table("profiles",
    schema.UUIDv7("id").PrimaryKey(),
    schema.Ref("author", author).Unique(),
)
```

```
authors ← posts.author_id     (relation "author")   reverse=collection
authors ← posts.reviewer_id   (relation "reviewer") reverse=collection
authors ← profiles.author_id  (relation "author")   reverse=single
```

Reverse cardinality is therefore **not** missing. It follows from whether the
foreign key column is unique: a non-unique FK means many rows may point at one
target, so the reverse is a collection; `.Unique()` makes it one row. Nothing
needs declaring for a runtime to decide between an object and an array.

The forward direction was never in doubt either. A reference targets a primary
key, so `?expand=author` on `/posts` yields exactly one author. **Forward
expansion is fully determined by what the schema records today**, and is blocked
only by the joins not being written — not by a modelling gap.

### What is actually missing

Three things, all on the reverse side, and none of them cardinality.

**A name.** `?expand=` names a relation, and the reverse relation has no name.
Deriving one from the target table gives `posts` for both `posts.author_id` and
`posts.reviewer_id` — the same name for two different relations, in the same
breath. An author's posts and the posts an author reviewed are not the same set,
and no derivation rule distinguishes them, because the distinction exists only in
the head of whoever wrote the schema.

**An exposure decision.** [ADR-0006](0006-capabilities-are-opt-in.md) is that a
column cannot be reached through a capability it does not declare. `Expandable()`
sits on the referencing column and says the *forward* relation may be expanded.
There is no way to say authors may expand their posts, and deriving it would
make the reverse reachable by default — the exact inversion of the rule the
project is built on.

**Anything at all across a module boundary.** `ExternalRef` records a free-text
target and no `*TableDef`, on purpose ([ADR-0015](0015-module-isolation.md)):
resolving it would require the dependency modules exist to avoid. Nothing about
the other side is derivable, in either direction.

## Decision

**A reference may declare the name its target knows it by, and that declaration
is what makes the reverse relation exist.**

```go
schema.Ref("author", Author).Expandable().Inverse("posts").InverseExpandable()
```

Read as: posts have an author; authors have posts; both directions may be
expanded. Absent `Inverse`, the reverse relation does not exist — it is not
addressable, not in the manifest, and not an error to omit.

Three properties follow from where the declaration sits:

- **One side declares, as today.** The referencing table already owns the column,
  the constraint and the delete action; the reverse name is one more fact about
  the same relationship. The target table is not edited, so a module can gain an
  inbound relation without its owner changing a line.
- **Exposure stays opt-in in both directions**, and separately. `Expandable` and
  `InverseExpandable` are different decisions about different endpoints.
- **`ExternalRef` cannot declare an inverse**, and saying so is a compile-time
  distinction rather than a runtime surprise, matching how it already refuses
  `Expandable`.

Cardinality is still derived rather than declared, because it is already correct
and a second source for it could only disagree with the first.

One claim above was wrong and is corrected below: the target table is not
*declared* differently, but its generated struct does gain a field.

### The mechanism is a model field, and the DSL only emits it

The sketch put the declaration in the schema DSL, because that is where `Ref`
lives. Forward expansion shipped one layer down — an `expand` capability on the
foreign key column plus a `sqlb:"expands=<column>"` field beside it, read by
reflection, with the DSL and codegen emitting the pair rather than being the
mechanism ([relation.go](../../relation.go)). The reverse follows the same route,
because otherwise `Describe` users, who have no DSL at all, could never express
one.

So `.Inverse("posts").InverseExpandable()` on the referencing side emits a field
on the *target's* generated struct:

```go
// on Author, from Post's declaration
Posts []Post `db:"-" json:"posts,omitempty" sqlb:"expands=author_id,order=-created_at,limit=50"`
```

The field has to exist for a query to have somewhere to put the rows, so "the
target table is not edited" was true of the schema declaration and false of the
generated Go. What holds is the part that matters: the fact lives in one place,
and no second declaration can disagree with it. Since `ExternalRef` cannot be
expandable in either direction, the generated edit is always inside one module
([ADR-0015](0015-module-isolation.md)).

`.Inverse` without `.InverseExpandable()` names a reverse that nothing can
reach: it goes into the manifest and emits no field. The name is a fact about
the relationship; the exposure is a decision about an endpoint, and
[ADR-0006](0006-capabilities-are-opt-in.md) is that the second one is never
implied by the first.

### Cardinality decides the direction, so the tag does not have to

`expands=` names a column. Which model that column belongs to follows from the
field's type: a struct means a column of mine, a slice means a column of theirs.

```go
ListID string `db:"list_id" json:"list_id" sqlb:"filter,expand"`
List   *List  `db:"-"       json:"list,omitempty" sqlb:"expands=list_id"`
```
```go
Tasks  []Task `db:"-"       json:"tasks,omitempty" sqlb:"expands=list_id"`
```

A second keyword — `collects=`, `hasmany=` — would be a second statement of
something the type already says, and two statements of one fact can disagree.

One asymmetry came out of building it and is worth stating, because it looks
like an oversight. A forward relation requires the `expand` capability on its
own column *and* the field beside it, and refuses a half-written pair. A reverse
relation requires nothing of the column it joins on. That is not laxity: the
capability puts a relation into *this* resource's vocabulary, and the column in
question belongs to another table describing another endpoint. The field's
existence is the entire opt-in, and codegen emits it only from an explicit
`InverseExpandable()`.

### The reverse is a correlated subquery, not a join

[ADR-0025](0025-expansion-is-one-statement.md)'s `LEFT JOIN` cannot express
this. Joining a collection multiplies the base rows, so a page's row count
becomes a function of the data; recovering the page then needs a `GROUP BY` over
every selected column, and two expanded collections produce a cross product of
each other before the aggregates ever run.

A correlated subquery in the projection has none of that:

```sql
SELECT "lists"."id", …,
       (SELECT json_build_object(
                 'items',    coalesce(json_agg(x.o ORDER BY x.n) FILTER (WHERE x.n <= 50), '[]'::json),
                 'has_more', count(*) > 50)
          FROM (SELECT json_build_object('id', t."id", 'title', t."title", …) AS o,
                       row_number() OVER (ORDER BY t."created_at" DESC, t."id" DESC) AS n
                  FROM "tasks" AS t
                 WHERE t."list_id" = "lists"."id"
                 ORDER BY t."created_at" DESC, t."id" DESC
                 LIMIT 51) AS x) AS "__expand_tasks"
FROM "lists"
```

Each collection is one independent subquery, so *n* of them compose by addition
rather than by multiplication. It stays one statement, which keeps ADR-0025's
snapshot argument intact: a second query would read at a later snapshot, and a
child could appear or vanish between the two.

The columns are listed explicitly, so `Hidden` survives the expansion exactly as
it survives the join.

Postgres has now seen it. `pgtest` runs this shape, reads the JSON it produces,
and checks the plan keeps a `SubPlan` rather than collapsing into a join. One
thing the database decided rather than this record: under `ORDER BY … DESC`,
NULLs sort first, so a child with no value in the ordering column leads its
collection. That is Postgres's default and it is asserted rather than
worked around — a schema that wants otherwise orders by a NOT NULL column.

### The value is the list envelope, not a bare array

An expanded collection is `{"items": […], "has_more": true}`, which is the
shape `rest` already returns for a collection, minus the fields a subquery
should not pay for.

A bare array would be the obvious choice and it is rejected for one reason: it
cannot say it is partial, and it will be partial. The
[adoption review](../review-adoption-readiness.md)'s third finding was a
capability that answered with less than it advertised and returned 200; a
silently truncated array is that failure with a different mechanism.

There is no `total`. Counting is a second aggregate per base row, and
`?count=exact` on the collection's own endpoint is where a caller pays for a
count deliberately.

### The cap is declared, has a default, and its escape hatch is an existing endpoint

`limit=` defaults to 50. Past it, `has_more` is true and the caller follows the
child resource's own endpoint, filtered by the foreign key —
`/tasks?list_id=eq.<id>` — which is paging, filtering and sorting that already
exist rather than a second surface with its own rules.

That escape hatch only exists if the foreign key is `Filterable`, so
`schema.Lint` should say so when an inverse-expandable reference's column is
not: an expansion whose overflow is unreachable is a dead end, and lint is where
this project puts "this compiles and you will regret it".

### Ordering is total, and defaults to the primary key

A declared order carries the target's primary key as a tiebreaker; with no
declared order it is the primary key alone.
[ADR-0027](0027-keyset-pagination.md)'s argument applies unchanged and matters
more here: under a `LIMIT`, a non-total order does not merely reshuffle the
result, it decides *which children the caller never sees*, and decides it
differently between two runs of the same query.

### Nesting is still not this record

One level, as forward expansion is today. A child that expands its own relations
is a depth question, and depth wants a limit, a cycle rule and a cost argument
of its own.

## Consequences

**What this buys.** Reverse expansion becomes expressible, which is the half of
`?expand` that no amount of join-writing would have unblocked. The manifest can
describe a relation from both ends, so an agent reading `sqlb.json` sees the
graph rather than a list of columns. And a named reverse is the prerequisite for
relation-spanning predicates later — "posts whose author's org is X" — which is
today a hand-written join.

**What this costs.** A second name to keep true. The reverse name lives on the
referencing table but shapes the target's API surface, so reading `authors` in
isolation no longer tells you what its endpoint exposes; that is real action at a
distance, and the manifest is the mitigation rather than a fix.

It also adds two methods to a DSL whose density is a stated virtue
([ADR-0004](0004-schema-as-go-dsl.md)), for a feature that is not built. If
`?expand` is never finished, this is vocabulary that never paid for itself.

**And it costs per row, which forward expansion does not.** A forward expansion
is one join for the whole page; a reverse expansion is one subquery per base row
per relation. A 50-row page expanding two collections at the default cap can
build 5,000 child rows inside one statement, and the response carries all of
them. The index `schema.Lint` already requires on a foreign key stops that from
being a sequential scan per base row — which promotes that lint from hygiene to
the thing this feature's cost model rests on.

**Two response shapes for one parameter.** `?expand=list` yields an object or
`null`; `?expand=tasks` yields `{items, has_more}`. A generated client knows
which from the types, and a hand-written one has to read the manifest. The
alternative — one shape for both — would mean wrapping the forward case in an
envelope it has no use for, which is a worse trade than the asymmetry.

## What would change our mind

- **If forward expansion turns out to be what people actually ask for.**
  `?expand=author` on a list of posts is the common shape; `?expand=posts` on an
  author is rarer and pages badly. `example/tasks` is the honest test: if
  building its endpoints wants no reverse expansion, this record is solving a
  problem the project does not have, and forward-only expansion ships without it.
  **This trigger has not been met, and the work is proceeding anyway** — see the
  second revision. A list with its tasks is the screen `example/tasks` has not
  been asked for yet, so building it there is what turns this from an argument
  into evidence. If that example reads worse than the two requests it replaces,
  that is the trigger firing late, and the record should be retired rather than
  defended.
- **If the declared cap and order need to vary per request.** PostgREST spells
  that `?tasks.limit=5&tasks.order=created_at.desc`, and it is a real need on a
  screen with a "show more" control. It is also an extension to a wire format
  [compatibility.md](../compatibility.md) freezes, so it is a decision of its own
  and not a parameter to add quietly.
- **If clients unwrap `{items, has_more}` at every call site**, the envelope did
  not earn its shape. The replacement is not a bare array but a refusal: cap
  exceeded becomes an error naming the child endpoint, which keeps the honesty
  and loses the wrapper.
- **If a collision error proves sufficient.** Deriving the reverse name from the
  target table and failing loudly when two references collide would cost no
  vocabulary at all. If schemas with two references to one table stay rare, that
  is a smaller answer than this one, and it should win. What it cannot recover is
  the exposure decision — so it wins only if reverse expansion is also acceptable
  by default, which contradicts ADR-0006.
- **If `?expand` is dropped.** It has been unbuilt through six months of other
  work, and every surface currently refuses it rather than half-answering. A
  decision to remove it from the roadmap retires this record with it.

## Cost of change

**Sharply asymmetric, and the asymmetry is the reason to decide before building
rather than after.**

Now: nothing depends on it. Adding, renaming or removing `Inverse` costs the
schema files that use it and a regeneration.

After `?expand` ships: the relation name is **in the URL**.
[compatibility.md](../compatibility.md) freezes the filter grammar as a wire
format, on the grounds that a deployed client or an agent has requests built
against it. `?expand=posts` becoming `?expand=authored_posts` is then a breaking
change to that wire format, not a rename — and the class of caller that hurts
most, an agent generating requests from the manifest, is exactly the one this
project is trying to serve.

Reverting is cheaper than renaming: a declared inverse that nothing expands can
be deleted quietly, because no URL contains it.

Two more asymmetries come from the design above:

**The envelope is expensive once anything reads it.** `{items, has_more}` is in
every response body that expanded a collection, so unwrapping it later is a
breaking change to the payload rather than to the URL — the class
[compatibility.md](../compatibility.md) does not even cover, because it assumed
the wire format was the query string.

**The cap is cheap to raise and expensive to lower.** Raising 50 to 200 makes
previously-truncated responses complete; lowering it truncates responses that
were whole, silently, for callers who never asked for a change.

## Alternatives considered

**Derive the reverse and error on ambiguity.** Genuinely close, and cheaper —
no new vocabulary, and the cardinality work is already done. It loses on the
exposure decision: derivation makes every inbound relation expandable by default,
which is the ADR-0006 rule inverted. It also does nothing across a module
boundary. Worth revisiting if exposure control turns out to be a cost nobody is
willing to pay for.

**ent-style two-sided edges**, where `edge.To` on one side pairs with
`edge.From(...).Ref(...)` on the other and cardinality is read from which side
carries `.Unique()`. It is a coherent model and it is where the review's framing
comes from. Rejected because it restructures the DSL around entities and
relations, when sqlb's model is tables and columns compiled into a predicate AST
— a different centre of gravity, not a missing feature. Adopting the shape
without the rest of ent's model would import its ceremony and none of its
ecosystem.

**Declare the inverse on the target.** `Author.HasMany("posts", Post)` reads
better at the point of use, and puts the exposure decision on the table whose
endpoint it affects. Rejected because it duplicates a fact the referencing side
already states, so the two can disagree — and the failure is a schema that
compiles and describes a relationship the database does not have.

**A second statement per collection**, `WHERE list_id IN (…)` over the page's
keys. It is what an ORM without a query builder does, and it avoids the
subquery entirely. Rejected for the reason ADR-0025 rejected two statements
forward: the second one reads at a later snapshot, so the parent and its
children can disagree about what exists. It is also the shape that becomes N+1
the moment someone loops.

**`LEFT JOIN LATERAL … ON true` with the aggregate inside.** Equivalent, and
Postgres is likely to plan the two identically. The correlated scalar subquery
wins on being a projection expression, which is what the expansion code already
emits — the lateral spelling would put a collection expansion in the FROM clause
and a forward one in the SELECT clause for no gain.

**A bare array, uncapped.** The simplest thing that could work, and it makes one
row's response size a function of data nobody bounded. Rejected above.

**Do nothing and ship forward expansion only.** The strongest of the four, and
not exclusive with this record: forward expansion is unblocked today and could
land first. It is listed here because it may turn out to be the whole answer.

**This is what happened, and then it stopped being the answer.** Forward
expansion shipped first and on its own, exactly as this alternative proposed —
and for a few days the record sat waiting to see whether anything would want the
other direction. Something did: `example/tasks` had a screen that was an N+1 the
client had to write, and collections were designed and built the same day. So
this alternative was right about sequencing and wrong about sufficiency, which
is the most useful way for an alternative to lose. What it correctly predicted
is that the two directions are separable — forward needed no inverse, no
declaration and no part of this record, which is why it could land first at all.

## Revisions

- 2026-07-27 — Written, prompted by an outside review. The review's stated
  premise — that reverse cardinality is underivable — was tested against the
  registry and did not hold; the gap it pointed at is real but is naming and
  exposure, not cardinality.
- 2026-07-28 — **Forward expansion shipped, and none of this was needed for it.**
  The fourth alternative was taken: `?expand=author` on a list or an item is
  built, tested against a real Postgres, and documented in
  [ADR-0025](0025-expansion-is-one-statement.md), which owns the SQL shape — one
  statement, a `LEFT JOIN` and a `json_build_object` per relation, with `Hidden`
  holding across the join. This record keeps the declaration question and loses
  the half that turned out not to be a question at all.

  Three things this record claimed were checked by the implementation rather
  than by argument:

  - **Forward needed no inverse.** The prediction that forward expansion was
    "fully determined by what the schema records today" held: a reference targets
    a primary key, so the target is one row, and nothing had to be declared about
    the other side.
  - **Cardinality is structural.** `newRelation` requires the relation field to
    be a struct or a pointer to one, so a `[]Post` is refused at model build.
    The reverse direction is excluded by the type rather than by a check someone
    could forget — which is a stronger form of the same conclusion.
  - **Exposure stayed opt-in in two halves.** The `expand` capability on the
    column and the `expands=` field must both be present, and disagreeing halves
    are an error at model build rather than a request refused for a relation the
    model plainly has.

  What is unchanged: there is no inverse, no reverse relation, and no way to
  write `?expand=posts` on an author. The naming problem that motivated this
  record — two references from one table to another, both wanting to be called
  "posts" on the far side — is untouched, because nothing has yet tried to name
  a reverse. The trigger in **What would change our mind** therefore still
  stands as written, and is now answerable with evidence rather than by
  speculation: `example/tasks` and `example/blog` both expand forward, and
  neither has yet wanted to expand back.
- 2026-07-28 — **Designed rather than deferred, and moved to the layer forward
  expansion shipped in.** The Decision now says what a reverse relation *is* — a
  slice field carrying the same `expands=` tag, resolved against the target's
  model — rather than only what must be declared about one. Added: the SQL shape
  and the argument that a join cannot express it, the `{items, has_more}`
  envelope and why a bare array is dishonest, a declared cap whose overflow is
  an endpoint that already exists, and a total order under the cap.

  Confidence dropped from Medium to Low in the process, which is the honest
  direction: the record grew a great deal of unbuilt argument about how Postgres
  will behave, and ADR-0025 is the precedent for that kind of claim being wrong
  until a database has ruled on it.

  One earlier claim is corrected rather than kept: "the target table is not
  edited" holds for the schema declaration and not for the generated struct,
  which gains the field the expanded rows land in.

  Chosen over building the TypeScript client ([ADR-0028](0028-typescript-client.md))
  next, on the grounds that this stays inside a toolchain `pgtest` can already
  judge against a real database, while that one is Low confidence with no
  consumer to validate it and puts Node in the CI of a stdlib-only Go module.
- 2026-07-28 — **Built, the same day it was designed**, which is the order this
  record's own preamble asks for and the reverse of how forward expansion
  happened. Status Exploring → Working, confidence Low → Medium.

  What the design got right, checked rather than argued: the subquery composes
  (two collections in one statement produce two `SubPlan`s and no `GROUP BY`),
  the cap and `has_more` behave over HTTP, `Hidden` holds over collected rows,
  a collection does not change the page's row count or its `?count=exact`, and a
  cycle — tasks expand their list, lists collect their tasks — does not recurse,
  because a relation's target still resolves lazily.

  What the database decided: NULLs lead a descending order. Asserted, not
  worked around.

  What building it added to the design: the capability asymmetry above, and two
  lint rules rather than one — an unindexed foreign key costs a scan per row of
  the page here rather than one per statement, and a cap whose overflow cannot
  be filtered is a dead end.

  The trigger under **What would change our mind** is now answerable with an
  example rather than an argument: `example/tasks` expands a list's tasks in one
  request, capped at twenty and ordered by position, where the same screen was
  previously an N+1 the client had to write. `example/blog` collects an org's
  authors, which is the fixture the reverse-direction `Hidden` test needs.

  Still open, and deliberately: nesting, a per-request order or cap, and any
  evidence about a collection over a large child table.
