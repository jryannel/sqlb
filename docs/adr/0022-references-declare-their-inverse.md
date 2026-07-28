# ADR-0022: A reference declares the name its target knows it by

- **Status:** Exploring — and narrower than when written: forward expansion
  shipped without any of this ([ADR-0025](0025-expansion-is-one-statement.md)),
  so what is open is the reverse direction alone
- **Confidence:** Medium — the gap is demonstrated and narrow; the shape of the
  fix is one candidate among several, and the layer it would live in has moved
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

**The layer above is now known to be wrong, and is left as written.** The
sketch puts the declaration in the schema DSL, because that is where `Ref` lives
and where this record was reasoning. Forward expansion has since shipped one
layer down: an `expand` capability on the foreign key column plus a
`sqlb:"expands=<column>"` field beside it, read by reflection, with the DSL and
codegen emitting that pair rather than being the mechanism. A reverse
declaration would follow the same route — otherwise `Describe` users, who have
no DSL at all, could never express one. Read `Inverse` above as naming the fact
that must be declared, not the API that would declare it.

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

## What would change our mind

- **If forward expansion turns out to be what people actually ask for.**
  `?expand=author` on a list of posts is the common shape; `?expand=posts` on an
  author is rarer and pages badly. `example/tasks` is the honest test: if
  building its endpoints wants no reverse expansion, this record is solving a
  problem the project does not have, and forward-only expansion ships without it.
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

**Do nothing and ship forward expansion only.** The strongest of the four, and
not exclusive with this record: forward expansion is unblocked today and could
land first. It is listed here because it may turn out to be the whole answer.
**This is what happened** — see the revision below. It has not yet turned out to
be the whole answer, but it has not turned out not to be either, and that is the
open question this record is now waiting on.

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
