# Capabilities

Every capability is opt-in per column, and a column that does not declare one
**cannot be reached through it** — not by a filter, not by a sort, not by a
projection. The failure is a 400 naming the columns that would have worked,
never a leak and never a silently ignored parameter.

[Capabilities](../concepts/capabilities.md) in Concepts is the model; this is
how to declare them and which combinations are worth knowing. The full method
list is in the
[capability reference](https://jryannel.github.io/sqlb/reference/capabilities/).

## The vocabulary

```go
schema.Text("title").Searchable().Sortable()
schema.Enum("status", "draft", "published").Filterable().Sortable()
schema.BigInt("view_count").Filterable().Sortable().ReadOnly()
schema.Text("password_hash").Hidden()
```

Four permit:

| Method | Allows |
|---|---|
| `Filterable()` | Use in a REST filter expression: `?status=eq.draft` |
| `Sortable()` | Appear in `?sort` |
| `Searchable()` | Inclusion in the `?search` fan-out (implies `Filterable`) |
| `Expandable()` | A reference resolved inline via `?expand` (references only) |

Three restrict:

| Method | Effect |
|---|---|
| `ReadOnly()` | Never settable through REST — the database or a hook owns it |
| `Immutable()` | Settable at create, rejected on update |
| `Hidden()` | Never serialised into a REST response, and unusable as a filter |

Go code going through the query engine directly is trusted and bypasses
`ReadOnly` and `Immutable`; they are enforced at the REST boundary. `Hidden` is
enforced at the projection, so `filter.Apply` cannot select one even by mistake.

The capabilities render into the `sqlb` struct tag that codegen writes onto the
model, which is how the runtime reads them back without importing this package:

```go
schema.Text("email").Unique().Searchable()   // → sqlb:"filter,search"
schema.Text("secret").Hidden()               // → sqlb:"hidden"
```

## Choosing between them

**`Filterable` and `Searchable` are different decisions.** Filterable is exact
match and comparison; searchable joins the `?search=` substring fan-out. An
email column that is filterable and deliberately *not* searchable answers "find
my own record" and refuses to answer "who here uses example.com" — not by
rejecting the request, but because the search never sees the column. On a table
anyone can read, a substring match over addresses is an address-harvesting
endpoint.

**`Sortable` costs an index.** `schema.Lint` reports a filterable or sortable
column that is not the leading column of any index, because filtering on it
scans the table. Sorting also wants a *composite* index with the primary key
appended — `(created_at DESC, id DESC)` — because every list is ordered
deterministically and the tiebreaker is part of the ordering.

**`Hidden` is for a secret, and it is stronger than unreadable.** A hidden
column is absent from the OpenAPI schema, from the filter vocabulary, and from
the `allowed` list in a rejection message, so its existence cannot be probed.
`Hidden` plus `Filterable` is a validation error rather than a combination you
can write, because a filterable secret can be recovered a character at a time.

## `ReadOnly` plus a hook

This is the combination worth understanding, because it is how a tenant id stays
out of a client's reach:

```go
schema.Ref("org", Org).Filterable().ReadOnly().Scoped()
```

The column is absent from both generated request bodies, so no request can name
it, and `BeforeCreate` supplies it from whatever the request authenticated as.
Both halves are live: the handler clears every read-only field before inserting,
so a hand-written `Row()` cannot set one, and a hook still can.

[`example/tasks`](../../example/tasks/taskschema/schema.go) does this on every
`workspace_id` in its schema and explains the alternative it rejected.

## `Scoped`, so the missing hook is caught

A `BeforeQuery` hook cannot be forgotten at a call site. It can be forgotten
*entirely*, and an unscoped model then serves every tenant's rows with a 200
next to them. So the table declares what it expects:

```go
schema.Ref("org", Org).Filterable().ReadOnly().Scoped()
```

`Scoped` writes no predicate — it is inert in exactly the way `SoftDelete`'s
column is. What it does is oblige the resource: `rest.Resource` refuses to mount
a model whose declarations no hook satisfies, and names every missing
registration at once.

The obligation follows the operations, because a `BeforeQuery` hook says nothing
about what a request can overwrite by id — an exposed update needs
`BeforeUpdate`, a delete needs `BeforeDelete`, and a create needs
`BeforeCreate` to supply the tenant column that `ReadOnly` kept out of the
request body.

The check proves a hook *exists*, not that it is right. That is worth knowing
before relying on it, and it catches the case that actually happens: the table
somebody added last week ([ADR-0030](../adr/0030-declared-scope-is-required.md)).

## One table, two surfaces

Every capability above is a property of the *column*, and a column belongs to a
model, and a table has one model. That is the right shape for almost everything
— and it is the wrong shape for the case most applications with an admin panel
have: a public surface and a privileged surface over the same table, differing
in which columns each may see.

`Hidden()` cannot say it. A column hidden for the storefront is hidden for the
admin panel, which is the surface that exists to read it. `Expose` cannot say
it either: a table carries one, and a second call replaces the first rather than
adding a resource.

What can say it is the **mount**. `rest.Options.Columns` narrows one resource to
the columns it names, the way `rest.Options.Computed` narrows it to the derived
columns it is willing to pay for
([#148](https://github.com/jryannel/sqlb/issues/148)):

```go
// The generated one, over every column the schema declares.
if err := catalog.Register(api, db); err != nil { … }

// And a public one beside it, over the same generated model.
err := rest.Resource[catalog.Product, rest.None[catalog.Product], rest.None[catalog.Product]](
    api, db, rest.Options{
        Path:    "/storefront/products",
        Name:    "storefront-product",
        Ops:     rest.OpList | rest.OpRead,
        Columns: []string{"id", "title", "handle", "status", "price_minor"},
    })
```

A column not listed is not reachable from that resource: absent from the
response, absent from the `SELECT` the database sees, not filterable, not
sortable, not searched, not nameable in `?select`, and — the part that matters
for a surface narrowed to conceal something — not named in the list a rejection
offers back.

**What you give up is the second resource's generated half.** The models, the
typed column facade, the manifest and the drift gate all still cover it, because
there is still one model; the mount, and any client for it, are hand-written.
Two further things stay wide, because they come from a Go type rather than from
the mount: the response schema in the OpenAPI document is the model's, and the
create and update body types are whatever you pass for `C` and `U`. A public
surface is usually read-only, which is why `Ops` above names only two — and if
it is not, give it body types of its own.

The alternative — a second `Describe`d struct over the same table — is stronger
in one respect, since a model with no field for a column has no code path that
can return it, and gives up all four of the generated halves. See
[structs-first](../start/structs-first.md) for that table.

## Next

- [References and relations](references.md) — `Expandable` and its inverse
- [Hooks](../queries/hooks.md) — the registrations `Scoped` obliges
- [Rejections](../rest/errors.md) — what a refused capability looks like on the
  wire
