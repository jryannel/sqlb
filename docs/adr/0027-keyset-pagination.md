# ADR-0027: A page is a position, not a distance

- **Status:** Working — forward cursors are built, and a real Postgres walks 45
  paged result sets to exhaustion on every `mise run ci`
- **Confidence:** High on the boundary predicate, which is decided by Postgres
  rather than by assertion; Medium on the wire shape, since no client has yet
  had to hold a cursor across a sort change
- **Decided:** 2026-07-28
- **Last reviewed:** 2026-07-28

## Context

`LIMIT n OFFSET k` had been the only paging this project offered, and it is the
wrong primitive for the surface the project exists to serve.

It has two defects, and they are the same defect. `OFFSET k` asks Postgres to
produce `k + n` rows and throw `k` of them away, so page 500 costs five hundred
times page 1 — on the endpoint whose whole purpose is walking a filtered set.
And because a page is addressed by its *distance* from the start, a row inserted
while a client pages shifts every later page by one: the client sees a row twice
or never sees it at all. Nothing is wrong with the query, the client or the
insert. The duplicate is what "distance from the start" means when the start can
move.

Both are fixed by naming the page boundary instead of counting to it. The
awkward part is that naming it correctly is harder than it looks, and three
things had to be decided before any of it worked.

### The ordering was not a total order, and never had been

A boundary can only be named if no two rows can tie on every ordering term.
`ORDER BY status` over a table with four statuses does not distinguish the rows
inside a status, so "the rows after this one" is not a question the database can
answer — and the same ambiguity was already making *offset* paging skip and
repeat rows. `schema.Lint` has reported this as `list-without-sort` since the
lint package existed, which is to say the project knew about the defect and
shipped a warning about it.

So the tiebreaker is not a cost of cursors. It is a bug fix that cursors forced.
Every list `filter.Apply` builds now ends with the primary key, whether it is
paged by cursor or by offset, and `list-without-sort` describes a state the REST
layer can no longer be in.

### NULLs do not compare, and the ordering does not say so

`ORDER BY published_at` puts NULLs somewhere — last for `ASC`, first for `DESC`,
which is Postgres's default and is not a single placement but a function of the
direction. A boundary of `published_at > $1` is therefore wrong twice over: it
drops every NULL row when NULLs sort after the boundary, and `$1` being NULL
makes the whole comparison NULL, which silently truncates the walk. Neither
failure produces an error. Both produce a short answer.

### The obvious predicate defeats the point

The general form of "strictly after" is the lexicographic expansion — greater on
the first term, or equal there and greater on the second, and so on. It is
always correct. It is also an `OR` of conjunctions, which Postgres typically
answers with a bitmap of several index scans rather than one seek, and a seek is
the entire reason to page this way.

## Decision

**A cursor names a position in an ordering, and the ordering is always total.**

`Builder.Stable()` appends the primary key unless the ordering already contains
it, taking the direction of the last existing term. `filter.Apply` calls it on
every list, so deterministic paging is not opt-in. `After(cursor)` calls it too,
including for an empty cursor — the *first* page is the one whose ordering has
to be total, because its last row is what becomes the cursor.

**The boundary compiles two ways, and the fast one is used when it is exactly
equivalent.** When every term runs the same direction and no ordering column is
nullable, the boundary is a row comparison — `(view_count, id) < ($1, $2)` —
which Postgres turns into a single index condition on a matching index.
Otherwise it is the lexicographic expansion, with NULL handled per term: a real
boundary under NULLS-after includes the NULL rows, a NULL boundary under
NULLS-after has nothing following it at all, and a NULL boundary under
NULLS-before is followed by every real value.

The row-comparison path is disqualified by a *nullable column*, not by a null
*value*. The values being compared are in the rows, not only in the cursor.

**The cursor is opaque by convention, not by encryption.** It is base64url of
JSON: the ordering terms, their directions, and the boundary row's value for
each, encoded as the same JSON the API emits for that column. Nothing in it is
secret — a client that decodes one learns what it could read off the response.
What makes it safe to accept is that `After` checks the terms against the
ordering the request actually asked for, so an edited cursor can only move the
boundary along a column the caller was already permitted to sort by. That is
something `?view_count=lt.100` would have done anyway.

**The boundary is not a filter.** It is held on the builder separately from
`Where`, so `countSQL` drops it exactly as it drops `LIMIT`. `?count=exact`
answers how large the result set is, not how much of it is left.

**Columns only, and an expression ordering is refused by name.** `keysetTerms`
rejects any `ORDER BY` term that is not a column — *"cursor pagination orders by
columns only, and this query orders by an expression; order by a column, or page
with Limit and Offset"* — because the boundary is read off the returned row, and
a computed term is not on the row. It is a programming error rather than a bad
request: the ordering was assembled by the caller, not sent by a client.

That is also the answer for a distance-ordered vector search
([ADR-0026](0026-vectors-declare-their-index.md)), and it is right for a stronger
reason than the value being unreadable. An ANN index returns *approximate*
neighbours, so the ordering it produces is not a total order over the table at
all, and a boundary named in it could skip or repeat rows however carefully the
distance were encoded. Search being its own operation in that record is not only
about the shape of the API — this is one of the things that makes it one, rather
than a list that happens to be sorted differently.

`Stable()` will still append the primary key to such an ordering, since it only
checks whether the key is already among the terms. That is harmless for
correctness and is a plan question rather than a semantic one: whether a trailing
term costs an ANN index its ordering is exactly the kind of claim ADR-0026 says
to settle with `EXPLAIN` rather than assume, and there is no vector code to
assert it against yet.

**Forward only.** There is no `Before` and no `?before=`.

## Consequences

**What this buys.** Paging costs the same at any depth, and a concurrent insert
lands either behind the cursor — already read, not re-read — or ahead of it,
read later, once. `next_cursor` is on every list response that has a next page,
including responses to requests that paged by offset, so a client adopts cursors
without a flag and without a way to obtain the first one. And every list, cursor
or not, is now deterministically ordered, which closes a defect that predates
this work.

**What this costs.** Three things, and the first is the one to watch.

*The tiebreaker can cost a sort.* `ORDER BY status` over an index on `(status)`
could stream; `ORDER BY status, id` cannot, and Postgres will sort. The
correctness argument is unambiguous and the performance one is not: some list
endpoints got slower to stop being wrong. The fix is a composite index, which is
the same index cursor paging wants anyway, and `unindexed-sort` now names it —
`(sort_column, id)` rather than `(sort_column)` — so the diagnostic suggests the
index the query actually runs.

*Two lint diagnostics changed meaning.* `list-without-sort` was a warning that
paging "may repeat or skip rows", which is no longer reachable through the REST
layer, so it is now an info about clients having no way to choose an order.
Downgrading a warning is worth noticing: the underlying defect did not become
tolerable, it became impossible.

*Two predicate forms is two code paths*, and the boundary between them is a
correctness argument about NULLs rather than a preference. A future change that
widens the fast path is a change to that argument.

*A cursor is coupled to its sort.* Changing `?sort=` and keeping the cursor is a
400. That is the correct answer — the cursor names a position in an ordering
that no longer exists — but it is a failure mode offset paging does not have,
and the error has to say so in a way a client can act on. It names both
orderings and tells the caller to drop the cursor.

## What would change our mind

**On forward-only:** a client that needs a back button and cannot hold the
cursors it has already been given. Infinite scroll and export walks do not need
one; a numbered page control does, and it wants offset paging anyway, which is
still there. If `?before=` does land, it is additive — no request valid today
changes meaning.

**On opacity:** a cursor that has to survive being logged, or one that must not
disclose the boundary value. Neither is true today, and signing would buy
nothing against the tampering that is actually available — the ordering check
already bounds it to columns the caller could sort by.

**On the tiebreaker being unconditional:** if a real deployment finds a list
endpoint where the appended key forces a sort that materially hurts and no
composite index is acceptable, the answer is a lint diagnostic naming the index
to add, not making determinism optional.

## Cost of change

Asymmetric, and the wire format is the expensive half.

The predicate, the two forms and the NULL handling are internal: `cursor.go` is
one file, the boundary is one builder field, and changing any of it costs a
re-run of the walk in `pgtest/cursor_test.go`, which is what decides whether the
change is correct. Cheap.

The cursor's encoding is not internal. A cursor issued today may be sent back
tomorrow, so changing the payload shape breaks in-flight clients rather than
call sites. It is versionable — the payload is JSON with room for a field — but
the version has to be added before it is needed, not after. **The `?cursor=`
parameter and the `next_cursor` field are wire format under
[compatibility.md](../compatibility.md), and the payload should be treated the
same way even though nothing today reads it.**

Removing the unconditional tiebreaker later would be cheap mechanically and
expensive in meaning: it would return the REST layer to a state `schema.Lint`
warns about.

## Alternatives considered

**Signed or encrypted cursors.** Genuinely close, and rejected on what it would
buy rather than on cost. The threat is a client editing the boundary, and the
ordering check already confines that to columns the caller can sort by — so
signing would defend a boundary that is not a boundary. It also needs a key,
which is state this library does not have and would have to acquire from every
consumer. If a cursor ever carries something a client should not choose — a
tenant, a snapshot id — this decision is reopened, and that is the trigger.

**Offset under the hood, cursor on the wire.** Encode the offset in the cursor
and keep the existing query. It is a smaller change and it is a lie: the
response would advertise a stable position while delivering a shifting one, so
clients would adopt it precisely where it does not work.

**Refusing to page a nullable ordering column.** The first design, and it was
five lines against forty. It was rejected for what it would have done to the
schema: `published_at` is nullable and sortable in the shipped example, so the
rule would have made the most obvious cursor query in the project illegal, and
the actionable error would have had nothing actionable to say. Handling NULLs is
harder to write and easier to use.

**Two queries — read the keys, then read the rows.** Never seriously on the
table for paging, and recorded because [ADR-0025](0025-expansion-is-one-statement.md)
rejected it for expansion on a consistency argument that applies here too.

## Revisions

- 2026-07-28 — Written, when keyset paging landed.
- 2026-07-28 — Recorded the rule's boundary, which the code enforced but no
  record stated: an ordering term that is not a column cannot be cursor-paged.
  Prompted by asking what a distance-ordered search
  ([ADR-0026](0026-vectors-declare-their-index.md)) would inherit from
  `Stable()`. The answer was already correct in `keysetTerms`; what was missing
  was why it is correct rather than a limitation, since an approximate ordering
  is not a total order and a boundary in it would be unsound even if the value
  could be read.
