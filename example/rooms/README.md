# example/rooms — two bookings cannot overlap

A room-booking service, and one hard invariant: no two *confirmed* bookings on
the same room may overlap in time. That is not a unique key and not a
`CHECK` — it is a relationship between every pair of rows, which is what an
`EXCLUDE` constraint is for.

## The correction this example leads with

[`docs/special-cases.md`](../../docs/special-cases.md), written 2026-07-30,
records `EXCLUDE USING gist` as **"not expressible"** in sqlb. That is no
longer true. `schema.Exclusion` and `TableDef.AddExclude` are a real, shipped,
tested feature (issue #121) — see the doc comment on `AddExclude` in
[`schema/table.go`](../../schema/table.go), whose own worked example is almost
exactly this scenario. `bookings` in [`roomsschema/schema.go`](roomsschema/schema.go)
declares it directly:

```go
Booking.AddExclude(schema.Exclusion{
    Name:     "bookings_no_double_booking",
    Using:    "gist",
    Elements: `"room_id" WITH =, tstzrange(starts_at, ends_at) WITH &&`,
    Where:    `status = 'confirmed'`,
})
```

This example is the worked demonstration of a shipped feature, not the
discovery of a gap. The `btree_gist` extension the `=` comparison needs is
detected and created automatically by `migrate.Diff` — nothing here writes it
by hand.

## Settles

**That the constraint holds under real contention, not merely that it
compiles.** `TestDoubleBookingUnderContentionIsRefusedExactlyOnce` fires eight
goroutines at the same overlapping slot on the same room; exactly one wins,
and the other seven fail with `*sqlb.ConstraintError{Kind:
sqlb.ConstraintExclusion, Constraint: "bookings_no_double_booking"}` — the
constraint's own name, not a generic "duplicate key" and not a driver panic.
This is the alternative to enforcing the rule in application code, where two
concurrent requests interleave between the check and the insert and both win.

**That the `WHERE` clause narrows correctly.** An overlapping *pending*
booking does not collide with a confirmed one, and two confirmed bookings that
do not overlap in time do not collide either.

## Still open, and worth taking seriously

**The timestamptz day-filter trap is real, and this is exactly where it
ships.** `?starts_at=eq.2026-09-01` against a `timestamptz` column becomes
`starts_at = $1` with a text argument. Postgres infers the parameter as
`timestamptz`, parses the date as midnight, and compares it for equality
against a timestamp that is (almost) never exactly midnight. The result is
**zero rows and no error** — for a booking calendar, that is a "what's on
today" view that silently shows nothing. `TestADayFilterAgainstTimestamptzSilentlyMatchesNothing`
pins this. The only correct spelling is `sqlb.RawPred(`"starts_at"::date =
?::date`, day)`, which leaves the typed builder — the REST filter parser has
no way to reach it. This finding is not new (`pgtest/census_test.go` has the
same test against a different table); it is repeated here because a booking
calendar is the shape that hits it first in practice, not a metering chart.

## Deliberately not

Recurrence rules. Calendar sync. A REST surface — neither `Room` nor
`Booking` declares `schema.REST`, because the question this example answers
is what the schema and the query engine do, not what a generated API looks
like over them.

## Running it

```bash
mise run pg-up
SQLB_TEST_POSTGRES='postgres://sqlb:sqlb@localhost:15432/sqlb?sslmode=disable' go test ./... -v -race
```

Standalone module — `go mod tidy` first if you haven't. `roomsschema/sqlb.go`
is what `go generate ./...` reruns after a schema edit.
