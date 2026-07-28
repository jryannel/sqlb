# Locks and rollout

Some statements hold a lock for a time proportional to the size of the table.
Whether that matters depends on how many rows the table holds, which is not in
the schema — so this package will not decide for you. It tells you instead:

```go
if blocking := m.Blocking(); len(blocking) > 0 {
    // refuse, or route to whoever sequences an expand/contract rollout
}
```

`migrate.Unblock` rewrites the ones whose remedy is mechanical:

- A scanning `ADD CONSTRAINT` (a `CHECK` or `FOREIGN KEY`) becomes `ADD … NOT
  VALID` plus a `VALIDATE CONSTRAINT` in a later file, moving the scan under a
  lock writers pass through.
- A `SET NOT NULL` becomes the same pair with the requirement set between them,
  since Postgres accepts a validated check as proof and skips its own scan.
- A `UNIQUE` or `PRIMARY KEY` — which has no `NOT VALID` form, because there is
  no way to build an index without reading every row — becomes `CREATE UNIQUE
  INDEX CONCURRENTLY` plus an `ADD CONSTRAINT … USING INDEX` that adopts it.

A **type change is left alone**. Rewriting a table has no in-place form: the
alternative is a second column, a batched backfill and a cutover, and only you
know what a batch costs or when the cutover can happen.

`Unblock` is opt-in rather than the default, because the sequence is longer,
splits the migration across files, and buys nothing on a table small enough that
the scan is instant — which most tables are.

`migrate.Split` separates changes that cannot share a file. Transaction control
in both goose and golang-migrate is per file, not per statement, so a
`CREATE INDEX CONCURRENTLY` would otherwise disable transactions for every other
change generated alongside it, silently removing their rollback guarantee.


## Next

- [Diffing and rendering](README.md) — producing the changes this rewrites
- [Adopting a database](adopting.md)
