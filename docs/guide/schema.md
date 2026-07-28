# Schema

A schema is ordinary Go values. That is what lets one declaration be the source
of truth for migrations, models, REST handlers and the OpenAPI document — there
is no separate schema language to keep in sync, and no reflection over a
database at startup.

```go
package blogschema

import "github.com/jryannel/sqlb/schema"

var Post = schema.Table("posts",
    schema.UUIDv7("id").PrimaryKey(),
    schema.Ref("author", Author).OnDelete(schema.Restrict),
    schema.Text("title").Searchable().Sortable(),
    schema.Enum("status", "draft", "review", "published").
        Default(schema.Value("draft")).
        Filterable().
        Sortable(),
    schema.Timestamps(),
    schema.SoftDelete(),
).
    Index("author_id").
    Check("published_posts_have_a_date", "status <> 'published' OR published_at IS NOT NULL").
    Describe("A blog post.").
    Expose(schema.REST{Ops: schema.CRUD | schema.OpList, MaxPageSize: 100})
```

`schema.Table` registers into the default registry as a side effect of
declaration, which is why a codegen program only has to import the package.

## Column types

| Constructor | SQL type | Go type |
|---|---|---|
| `Text(name)` | `text` | `string` |
| `Varchar(name, n)` | `varchar(n)` | `string` |
| `Int(name)` | `int` | `int32` |
| `BigInt(name)` | `bigint` | `int64` |
| `Float(name)` / `Numeric(name)` | `float` / `numeric` | `float64` |
| `Bool(name)` | `bool` | `bool` |
| `UUID(name)` | `uuid` | `string` |
| `Timestamp(name)` | `timestamptz` | `time.Time` |
| `Date(name)` / `Time(name)` | `date` / `time` | `time.Time` |
| `JSON(name)` | `jsonb` | `json.RawMessage` |
| `Bytes(name)` | `bytea` | `[]byte` |
| `Enum(name, values...)` | `text` + a check constraint | a named string type |

Two shorthands cover the conventional cases. `UUIDv7(name)` is a UUID column
defaulting to a generated, time-ordered v7 value — the usual primary key.
`Enum` emits a Go string type with one constant per value, so
`blog.PostStatusPublished` exists and a typo does not compile.

`Nullable()` allows SQL NULL and makes codegen emit the Go field as a pointer.

### Groups

`Timestamps()` and `SoftDelete()` insert several columns as a unit:

```go
schema.Timestamps()   // created_at, updated_at — both default now(), read-only, sortable
schema.SoftDelete()   // deleted_at — nullable, read-only
```

Factor your own recurring column sets the same way by returning a
`schema.Group`.

## Capabilities

This is the part that matters most. Every capability is opt-in per column, and a
column that does not declare one **cannot be reached through it** — not by a
filter, not by a sort, not by a projection. The failure is a 400 naming the
columns that would have worked, never a leak and never a silently ignored
parameter.

| Method | Allows |
|---|---|
| `Filterable()` | Use in a REST filter expression: `?status=eq.draft` |
| `Sortable()` | Appear in `?sort` |
| `Searchable()` | Inclusion in the `?search` fan-out (implies `Filterable`) |
| `Expandable()` | A reference resolved inline via `?expand` (references only) |

And three that restrict rather than permit:

| Method | Effect |
|---|---|
| `ReadOnly()` | Never settable through REST — the database or a hook owns it |
| `Immutable()` | Settable at create, rejected on update |
| `Hidden()` | Never serialised into a REST response, and unusable as a filter |

`Hidden` is the one to reach for on a password hash. It is absent from the
OpenAPI schema, from the filter vocabulary, and from the allow-list in a
rejection message — so it cannot be recovered by probing, which a merely
unreadable-but-filterable column can.

Go code going through the query engine directly is trusted and bypasses
`ReadOnly` and `Immutable`; they are enforced at the REST boundary. `Hidden` is
enforced at the projection, so `filter.Apply` cannot select one even by mistake.

`ReadOnly` on a column a hook fills is the combination worth understanding,
because it is how a tenant id stays out of a client's reach. The column is
absent from both generated bodies, so no request can name it, and `BeforeCreate`
supplies it from whatever the request authenticated as.
[`example/tasks`](../../example/tasks/taskschema/schema.go) does this on every
`workspace_id` in its schema and explains the alternative it rejected.

The capabilities render into the `sqlb` struct tag that codegen writes onto the
model, which is how the runtime reads them back without importing this package:

```go
schema.Text("email").Unique().Searchable()   // → sqlb:"filter,search"
schema.Text("secret").Hidden()               // → sqlb:"hidden"
```

## References

```go
schema.Ref("author", Author).OnDelete(schema.Restrict).Expandable()
```

`Ref` produces a column named `author_id` and a relation named `author`, typed
to match the target's primary key. The actions are `NoAction`, `Restrict`,
`Cascade`, `SetNull` and `SetDefault`.

Across a module boundary, use `ExternalRef`, which emits the column and an index
to join on but **no foreign key**:

```go
// in the billing module, with no import of the tenants module
schema.ExternalRef("tenant", "tenants.id").Filterable()
```

The two modules stay independently deployable and independently migratable, and
either can move to its own database without dropping a constraint. Referential
integrity becomes the application's job — the trade a module architecture is
already making everywhere else. The target string is free text and is not
resolved, because resolving it would require exactly the dependency this avoids.

An external reference cannot be `Expandable`: expanding it would join a table
this module does not own.

## Indexes and constraints

```go
).
    Index("org_id", "status").                       // composite
    UniqueIndex("org_id", "slug").
    AddIndex(schema.Index{Columns: []string{"body"}, Method: "gin"}).
    Check("name", "status <> 'published' OR published_at IS NOT NULL")
```

`AddIndex` takes a fully specified `Index` for what the shorthands do not cover
— GIN indexes, partial indexes via `Where`. An external reference gets an index
whether or not you asked for one, and it is added to the table's own index list
rather than applied invisibly, so it shows up in `Indexes()`, the manifest and
the generated DDL like any other.

## Modules

A registry is the unit of isolation. Independent modules each declare into their
own, so two of them may both own a table called `events`:

```go
var Billing = schema.NewModule("billing")
var Invoice = Billing.Table("invoices", …)   // → billing_invoices
```

The prefix is applied by the registry rather than written into each declaration,
which is the point: a convention repeated at every call site is one that drifts.
Declarations still use the local name, so moving a table between modules changes
one line. The URL keeps the local name too — a module prefix is a storage
concern, and leaking it into the API would make that move a breaking change.

## Checking your work

Two different questions, two different calls.

**`Validate()` — is this schema well-formed?** Every authoring mistake is
reported at once rather than one per run. Call it from a test, or let codegen
fail.

**`Lint()` — will this schema behave badly in production?** Problems that
compile fine and produce a bad database or a bad API:

```go
for _, d := range reg.Lint() {
    fmt.Println(d)
}
```

```
[warn] unindexed-filter: events.kind: column is filterable but is not the leading column of any index, so filtering on it scans the table
    fix: add .Index("kind") to the table, or drop .Filterable() from the column
[info] list-without-sort: events: list endpoint has no sortable column, so every client gets the same primary-key order and none can ask for another
    fix: mark at least one column .Sortable(), conventionally created_at
[info] no-max-page-size: events: no MaxPageSize, so the package default applies as the hard ceiling
    fix: set MaxPageSize on the REST exposure to a value this table can serve
```

That output is from `ExampleRegistry_Lint` in `schema/example_test.go`, so it is
what the linter actually says. Both are worth running from a test — the loop
here is `go test`, not a CLI.

## Next

- [Queries and hooks](queries-and-hooks.md) — using the models this produces
- [REST](rest.md) — what `Expose` publishes
- [Migrations](migrations.md) — turning a schema edit into DDL
