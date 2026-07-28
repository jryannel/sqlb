# Paging a whole result set

`Limit` and `Offset` are there, and for walking a whole set they are the wrong
tool: `OFFSET k` makes Postgres produce `k + n` rows and throw `k` away, and a
row inserted mid-walk shifts every later page so a row is read twice or not at
all. `After` names the position instead.

```go
var cursor sqlb.Cursor
for {
    q := sqlb.Query[Post]().
        Where(sqlb.F("status").Eq("published")).
        OrderBy(sqlb.F("created_at").Desc()).
        After(cursor).
        Limit(500)

    batch, err := q.All(ctx, db)
    if err != nil || len(batch) == 0 {
        return err
    }
    process(batch)
    if len(batch) < 500 {
        return nil
    }
    if cursor, err = q.CursorFor(batch[len(batch)-1]); err != nil {
        return err
    }
}
```

An empty cursor means "start at the beginning", so the first pass through the
loop needs no special case.

## Why the ordering has to be total

`After` and `CursorFor` both call `Stable()` first, which appends the primary
key unless the ordering already contains it. That is what makes a page boundary
nameable at all — without it, two rows with the same `created_at` cannot be told
apart, and the boundary between them is ambiguous.

Call `Stable()` yourself if you want the total order without a cursor.

A cursor is only valid for the ordering it was issued under; using one against a
different `OrderBy` fails with an error wrapping `sqlb.ErrBadCursor` that names
both orderings. `Count` ignores the boundary, so it answers how large the result
set is rather than how much of it is left.

## Index it

For the best plans, index the ordering: `(created_at DESC, id DESC)` lets
Postgres answer the boundary as a single index seek. The tiebreaker is part of
the ordering, so an index on the sort column alone is not enough —
`schema.Lint`'s `unindexed-sort` diagnostic suggests the composite, which is
what cursor paging wants anyway.

[ADR-0027](../adr/0027-keyset-pagination.md) has the boundary predicate and how
NULLs in a sortable column are handled.

## The same thing over HTTP

A REST list response carries `next_cursor` on every page that has one, so a
client walking a collection needs no first cursor obtained some other way, and
adopting cursors needs no flag. See [Pagination](../rest/pagination.md).

## Next

- [Mutations and transactions](mutations.md)
- [Pagination](../rest/pagination.md) — the wire form
