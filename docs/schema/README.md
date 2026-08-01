# Declaring tables

A schema is ordinary Go values. That is what lets one declaration be the source
of truth for migrations, models, REST handlers, generated clients and the
OpenAPI document — there is no separate schema language to keep in sync, and no
reflection over a database at startup.

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

This page covers the column vocabulary and the table-level constructs.
[Capabilities](capabilities.md) is what each column opts in to, and
[References and relations](references.md) is how tables point at each other.

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

`Nullable()` allows SQL NULL and makes codegen emit the Go field as a pointer —
so a nullable `JSON` column is a `*json.RawMessage`, and a nullable `Timestamp`
a `*time.Time`.

The exceptions are the two types that can already express absence on their own.
A `Bytes` column stays `[]byte` and an `Array()` column stays a slice, because
nil is what a slice *is* when the value is absent; a pointer would add a second
spelling for the same thing. `json.RawMessage` is a slice too, but a document
type rather than a bag of bytes, so it takes the pointer like everything else.

The full table, including which filter operators each type admits, is in the
[column type reference](https://jryannel.github.io/sqlb/reference/column-types/).

### Arrays

`Array()` is a modifier on any of the scalar constructors above, so a `text[]`
is a text column that says so:

```go
schema.Text("labels").Array().Filterable().Default(schema.Value("{}")),
schema.Enum("channels", "web", "email").Array().Nullable(),
```

The Go field is the plain slice — `[]string`, not a wrapper type — and the
generated TypeScript and Dart clients get `string[]` and `List<String>`, which
is precisely what the same column declared as `JSON` could not give them.

The constructor keeps naming the *element*, and that is load-bearing rather than
cosmetic: `?labels=has.urgent` binds one `text`, so the enum's value set and the
varchar's length stay attached to the thing that has them.

Nullability is about the column, not the elements. A NULL column and an empty
array are different values, and the Go side spells them `nil` and `[]string{}`;
there is no way to declare an array whose *elements* may be NULL, because
`{a,NULL,b}` and `NULL` are two absences no generated client could tell a UI
apart.

Three rules, all reported by `schema.Validate`:

- an array column cannot be `Sortable` — the keyset cursor encodes the ordering
  columns, and an array has no spelling in it;
- it cannot be `Searchable` — search is a text operation;
- a `Filterable` one **must** carry a GIN index, or every filter over it is a
  sequential scan that returns the right rows and reports nothing.

```go
).AddIndex(schema.Index{Columns: []string{"labels"}, Method: "gin"})
```

Elements are the scalar types; not `JSON`, not `Bytes`, and one dimension only.
Filtering one is [`has` / `hasany` / `hasall`](../rest/filtering.md#array-columns-take-containment-and-nothing-else).

### Computed columns

A `Computed` column is an expression rather than storage. It emits no DDL — the
table Postgres holds does not have it — and it is a column to everything above
Postgres: it is in the row type, the JSON, the TypeScript and Dart types, the
CLI's columns and the OpenAPI document, and `Filterable` and `Sortable` gate it
exactly as they gate a stored one.

```go
schema.Computed("is_overdue", schema.TypeBool,
    schema.FromSQL("due_date < current_date AND open_tasks > 0")).
    Filterable(),

schema.Computed("total_tasks", schema.TypeInt,
    schema.FromSQL("(SELECT count(*) FROM tasks t WHERE t.project_id = projects.id)")),

schema.Computed("is_starred", schema.TypeBool,
    schema.FromSQL("EXISTS (SELECT 1 FROM stars s "+
        "WHERE s.project_id = projects.id AND s.member_id = ?)")).
    Needs("viewer").Filterable(),
```

The compiler substitutes the expression wherever the column is named, so one
declaration reaches the projection, `?filter=is_overdue.eq.true` and
`?sort=-progress` at once. The projection aliases it back to the column name,
which is what lets the row scan into the field.

**A subquery is projection-only unless you say otherwise.** Writing
`Filterable()` on one is the acknowledgement that a subquery in a `WHERE` runs
once per candidate row.

**`Needs` supplies what the request knows.** Each `?` takes the bind named at the
matching position, and the value arrives with the query:

```go
sqlb.On[Project]().BeforeQuery(func(ctx context.Context, q *sqlb.Builder[Project]) error {
    q.Bind("viewer", memberFrom(ctx))
    return nil
})
```

Like `Scoped`, the declaration writes no value — it obliges a hook. A resource
whose binds nothing supplies does not mount, because an unbound expression would
render `member_id = NULL`, answer `false` for every row forever, and look exactly
like a feature that works. The bind is sent once however many times the
expression appears in the statement.

Four rules, reported by `schema.Validate`:

- a computed column cannot be `Searchable` — `?search` fans out over text columns
  with `ILIKE`, and an expression has no reading there;
- it cannot be `Sortable` if its expression is volatile — one reading `now()` or
  `current_date` is a different value on the next page, and the keyset cursor
  pages on the sort column;
- it cannot be a primary key, unique, defaulted, a reference, indexed or an
  enum — each is a statement about storage;
- `Needs` must name exactly as many binds as the expression takes.

Nothing parses the SQL: `sqlb generate` refuses the four rules above, but a typo
inside the expression reaches Postgres, and `Explain` against a real database is
what catches it early.

Nothing writes one: it is absent from the generated create and update bodies and
from every `INSERT`, and a write's `RETURNING` reads back the bind-free ones so a
`POST` response carries them without a second read. A parameterised one is left
out of `RETURNING` — a write has no viewer to bind — and arrives on the next
read.

An index can never serve one, which is why a trigger-maintained counter or a
`GENERATED ALWAYS AS … STORED` column is still the better answer when the value
allows it; `schema.Lint` says so once per filterable computed column.
[`example/computed`](../../example/computed/) is the five techniques side by
side, and [ADR-0041](../adr/0041-computed-fields.md) is why the tiers are drawn
where they are.

### Groups

`Timestamps()` and `SoftDelete()` insert several columns as a unit:

```go
schema.Timestamps()   // created_at, updated_at — both default now(), read-only, sortable
schema.SoftDelete()   // deleted_at — nullable, read-only
```

Factor your own recurring column sets the same way by returning a
`schema.Group`.

**`SoftDelete` adds a column and stops.** Nothing writes `deleted_at`, nothing
filters it out of reads, and the generated `DELETE` issues a real `DELETE`.
Making it mean something is two pieces of your own — a `BeforeQuery` hook that
adds the predicate, and an endpoint that stamps the column — and the schema
knows to *ask* for the first: a resource over a soft-deleting model does not
mount until a hook confines it. [`example/blog`](../start/first-app.md) is that
pair written out.

## Indexes and constraints

```go
).
    Index("org_id", "status").                       // composite
    UniqueIndex("org_id", "slug").
    IndexNamed("idx_posts_author", "author_id").     // the name the database has
    AddIndex(schema.Index{Columns: []string{"body"}, Method: "gin"}).
    Check("name", "status <> 'published' OR published_at IS NOT NULL")
```

`AddIndex` takes a fully specified `Index` for what the shorthands do not cover
— GIN indexes, partial indexes via `Where`, and per-column sort order via
`Orders`. A partial unique index is often the cleanest way to state a domain
rule the type system cannot:
`UNIQUE (book_id, borrower_id) WHERE returned_at IS NULL` makes borrowing a book
you already have out impossible, and borrowing it again next year ordinary.

`Orders` is what an index backing a specific `ORDER BY` needs, because there the
ordering *is* the index — one built for
`ORDER BY position ASC NULLS FIRST, created_at DESC` serves nothing else:

```go
AddIndex(schema.Index{
    Name:    "idx_tasks_project_position",
    Columns: []string{"project_id", "position", "created_at"},
    Orders: map[string]schema.IndexOrder{
        "position":   {Nulls: schema.NullsFirst},
        "created_at": {Desc: true},
    },
})
```

An absent entry is ascending with Postgres's default null placement, and a
placement that already follows from the direction is dropped when the DDL is
rendered — so a declaration and what `pg_get_indexdef` hands back agree, and two
spellings of the same order do not propose replacing each other.

A partial index's `Where` is hand-written SQL, and Postgres stores it as a parse
tree rather than as text: `latitude IS NOT NULL` comes back as
`(latitude IS NOT NULL)`. `sqlb migrate` puts the declared predicate through the
same normalisation before diffing (`shadow.Normalize`), so you write it the way
you would say it rather than the way Postgres would print it.

`Index` and `UniqueIndex` name the index by convention — `posts_org_id_idx`,
`posts_org_id_slug_uniq`. `IndexNamed` and `UniqueIndexNamed` take the name
instead, which is what describing a database somebody else's tool built needs: a
declared index whose name differs from the live one is a rename, and across a
schema of any size that turns adoption into renaming every index in the
database.

An index name is not always inert, which is the sharper half. Postgres reports a
violated constraint by name, and matching that name is the standard way to tell
one unique violation from another —

```go
pgErr.Code == "23505" && pgErr.ConstraintName == "idx_projects_org_code"
```

— so renaming a unique index turns a handled collision into an unhandled 500
without touching the code that handled it. The generated migration says so when
it proposes one.

An external reference gets an index whether or not you asked for one. It is
resolved when the index set is read, so an index you declare on the same column
replaces it rather than colliding with it, and it shows up in `Indexes()`, the
manifest and the generated DDL like any other.

`Check` is the floor under everything else. A hook is a convention; a check
constraint cannot be bypassed by code that has not been written yet — see
[where domain logic goes](../concepts/domain-logic.md).

## Exposure

`Expose` is what publishes a table over HTTP. Without it, the table is reachable
from Go and has no REST surface at all.

```go
Expose(schema.REST{
    Path:            "/posts",
    Ops:             schema.OpCreate | schema.OpRead | schema.OpUpdate | schema.OpList,
    DefaultPageSize: 20,
    MaxPageSize:     100,
    MaxFilters:      12,
})
```

`schema.CRUD` is create, read, update and delete together; `OpList` is separate
because a table can be readable by id without being listable. Leaving an
operation out means the endpoint does not exist — not that it answers 405.

`MaxPageSize` is a hard ceiling rather than a hint, and `MaxFilters` bounds how
many predicates one request may carry, which bounds the cost of a single query.
Both are worth setting per resource; see [Pagination](../rest/pagination.md).

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

Across a module boundary, use `ExternalRef`; see
[References](references.md#across-a-module-boundary).

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

- [Capabilities](capabilities.md) — what each column lets the outside world do
- [References and relations](references.md) — foreign keys, and both directions
  of one
- [Migrations](../migrations/README.md) — turning a schema edit into DDL
- [Queries](../queries/README.md) — using the models this produces
