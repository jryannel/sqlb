# Using your own structs

The schema DSL and code generation are both optional. If you already have model
structs — from another generator, or written by hand — point sqlb at them and
describe them at startup. Nothing in the query builder, the REST layer or the
hooks requires the DSL.

```go
type Invoice struct {
    ID         string    `json:"id"`
    CustomerID string    `json:"customerId"`
    AmountDue  int64     `json:"amountDue"`
    Memo       string    `json:"-"`
    CreatedAt  time.Time `json:"createdAt"`
}

func init() {
    sqlb.Describe[Invoice]().
        PrimaryKey("id").
        Defaulted("id").
        Timestamps("created_at").
        Filterable("customer_id", "amount_due").
        Sortable("amount_due", "created_at").
        Hidden("memo")
}
```

Column names are derived from field names (`CustomerID` → `customer_id`), so the
query builder works with no metadata at all. What `Describe` adds is what
reflection cannot infer:

- **which column is the key**, so `One`, `After` and the item endpoints have
  something to address a row by;
- **which columns have database defaults** — without this, an insert writes `""`
  over your generated uuid;
- **which capabilities are open**, which is the whole safety model. The default
  is that nothing is filterable, sortable or searchable, because a default of
  "everything" would make adopting sqlb a way to widen an API by accident.

Naming a column that does not exist panics at startup and lists the ones that
do.

Call it from `init`, and it panics if you do not. Describing late is wrong
because a query built before the description does not carry it and one built
after does — not because it corrupts anything: each call publishes a fresh copy
of the model, so a query already in flight keeps the one it started with.

## Relations

`?expand` works here too. A relation is a field the expanded row lands in, plus
the column it joins on:

```go
type Invoice struct {
    CustomerID string
    Customer   *Customer `db:"-"`   // not a column
}

sqlb.Describe[Invoice]().Relation("Customer", "customer_id")
```

One call, because there is one fact. Declaring the relation is what makes
`customer_id` expandable — with struct tags the two halves are written
separately and can disagree, which is why the tagged form is checked and this
one has nothing to check.

## What you give up

Everything downstream of the declaration, because there is no declaration to
generate from:

| | Schema-first | Structs-first |
|---|---|---|
| Typed column facade (`PostCols`) | generated | use `sqlb.F("column")` |
| Request bodies (`PostCreate`, `PostPatch`) | generated | write them, or use `rest.None[T]` |
| Migrations from a schema diff | yes | your existing runner owns the DDL |
| TypeScript client, Dart client, Go CLI | generated | not available |
| REST resources | one generated `Register` call | one `rest.Resource[T, C, U]` call per table |

The query builder, the filter grammar, the capabilities, the hooks and the
pagination are identical. What moves is who writes the boilerplate around them.

You can also start here and move: `Describe` and the DSL declare the same
metadata by two routes, so adopting the DSL later is a schema file plus a
codegen program, not a rewrite.

## Alongside sqlc

This is the common case, and it has its own page.
[Using sqlb with sqlc](../with-sqlc.md) covers who owns the schema, which
queries go where, and how both land on one unit of work — one `pgx.Tx` satisfies
`sqlb.Executor` and a pgx-generated `DBTX` at once, so a sqlc `Queries` and a
sqlb builder can share a transaction in either direction.

The short version: sqlc is the right tool for a query you can name, and this is
the right tool for a view whose shape depends on what the user did. A project
can want both, and stock sqlc output needs no editing to be described.

## Next

- [Queries](../queries/README.md) — the builder these structs feed
- [Mounting resources](../rest/README.md) — `rest.Resource` written out by hand
- [Capabilities](../concepts/capabilities.md) — what `Filterable` and friends
  actually promise
