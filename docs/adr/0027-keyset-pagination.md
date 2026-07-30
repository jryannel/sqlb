# ADR-0027: A page is a position, not a distance

- **Status:** Working — forward cursors are built, and a real Postgres walks 45
  paged result sets to exhaustion on every `mise run ci`
- **Confidence:** High on the boundary predicate, which Postgres decides rather
  than assertion; Medium on the wire shape, since no client has yet had to hold a
  cursor across a sort change
- **Decided:** 2026-07-28
- **Last reviewed:** 2026-07-28

## Context

`LIMIT n OFFSET k` was the only paging on offer, and it has two defects that are
the same defect. `OFFSET k` makes Postgres produce `k + n` rows and discard `k`,
so page 500 costs five hundred times page 1 — on the endpoint whose purpose is
walking a filtered set. And because a page is addressed by its *distance* from
the start, a row inserted mid-walk shifts every later page: the client sees a row
twice or never. Nothing is wrong with the query or the insert; the duplicate is
what "distance from the start" means when the start can move.

Naming the boundary instead is harder than it looks:

- **The ordering was not a total order.** `ORDER BY status` over four statuses
  cannot answer "the rows after this one" — and the same ambiguity was already
  making *offset* paging skip and repeat. `schema.Lint` had warned about it as
  `list-without-sort` since the lint package existed. The tiebreaker is not a
  cost of cursors; it is a bug fix cursors forced.
- **NULLs do not compare, and the ordering does not say so.** `published_at > $1`
  is wrong twice: it drops every NULL row when NULLs sort after the boundary, and
  a NULL `$1` makes the comparison NULL, silently truncating the walk. Neither
  failure errors; both return a short answer.
- **The obvious predicate defeats the point.** The lexicographic expansion is
  always correct, and it is an `OR` of conjunctions that Postgres answers with a
  bitmap of index scans rather than one seek — and the seek is the whole reason
  to page this way.

## Decision

**A cursor names a position in an ordering, and the ordering is always total.**

`Builder.Stable()` appends the primary key unless the ordering already has it,
taking the direction of the last term. `filter.Apply` calls it on every list, so
deterministic paging is not opt-in. `After(cursor)` calls it too, including for
an empty cursor — the *first* page is the one whose ordering must be total,
because its last row becomes the cursor.

**The boundary compiles two ways, and the fast one is used when it is exactly
equivalent.** With every term in the same direction and no nullable ordering
column, it is a row comparison — `(view_count, id) < ($1, $2)` — which Postgres
turns into a single index condition. Otherwise it is the lexicographic expansion
with NULL handled per term. The fast path is disqualified by a nullable *column*,
not a null *value*: the values compared are in the rows, not only in the cursor.

**The cursor is opaque by convention, not encryption.** base64url of JSON: the
terms, their directions, and the boundary row's value for each. Nothing in it is
secret. What makes it safe to accept is that `After` checks the terms against the
ordering the request asked for, so an edited cursor can only move the boundary
along a column the caller could already sort by.

**The boundary is not a filter** — it is held separately from `Where`, so
`countSQL` drops it as it drops `LIMIT`. `?count=exact` answers how large the
result set is, not how much is left.

**Columns only.** An expression ordering is refused by name, because the boundary
is read off the returned row. That is also the answer for a distance-ordered
vector search ([ADR-0026](0026-vectors-declare-their-index.md)), and for a
stronger reason: an ANN index returns *approximate* neighbours, so its ordering
is not a total order at all and a boundary in it could skip or repeat however
carefully the distance were encoded.

**Forward only.** There is no `Before` and no `?before=`.

## Consequences

**Buys.** Paging costs the same at any depth, and a concurrent insert lands
either behind the cursor or ahead of it, read once. `next_cursor` is on every
list response with a next page, including offset-paged ones, so clients adopt
cursors without a flag. Every list is now deterministically ordered, closing a
defect that predates this work.

**Costs.** *The tiebreaker can cost a sort.* `ORDER BY status` over an index on
`(status)` could stream; `ORDER BY status, id` cannot. Some list endpoints got
slower to stop being wrong. The fix is the composite index cursor paging wants
anyway, and `unindexed-sort` now names `(sort_column, id)`.

*Two lint diagnostics changed meaning.* `list-without-sort` is now an info rather
than a warning — the underlying defect did not become tolerable, it became
impossible.

*Two predicate forms is two code paths*, and the boundary between them is a
correctness argument about NULLs, not a preference.

*A cursor is coupled to its sort.* Changing `?sort=` and keeping the cursor is a
400 — correct, but a failure mode offset paging does not have, so the error names
both orderings and tells the caller to drop the cursor.

## What would change our mind

- **Forward-only:** a client needing a back button that cannot hold the cursors it
  was given. A numbered page control wants offset paging, which is still there.
  `?before=` would be additive.
- **Opacity:** a cursor that must survive logging, or must not disclose the
  boundary. If a cursor ever carries something a client should not choose — a
  tenant, a snapshot id — signing is reopened.
- **The unconditional tiebreaker:** a deployment where the appended key forces a
  materially harmful sort and no composite index is acceptable. The answer is a
  lint diagnostic naming the index, not optional determinism.

## Cost of change

Asymmetric, and the wire format is the expensive half. The predicate, the two
forms and the NULL handling are one file and one builder field, decided by
re-running the walk in `pgtest/cursor_test.go`. The cursor's *encoding* is not
internal: one issued today may come back tomorrow, so changing the payload breaks
in-flight clients. It is versionable, but the version has to be added before it
is needed. `?cursor=` and `next_cursor` are wire format under
[compatibility.md](../compatibility.md), and the payload should be treated the
same way.

## Revisions

- 2026-07-28 — Written, when keyset paging landed.
- 2026-07-28 — Recorded the rule's boundary, which the code enforced but no record
  stated: an ordering term that is not a column cannot be cursor-paged. Prompted
  by asking what a distance-ordered search would inherit from `Stable()`.
- 2026-07-30 — Condensed.
