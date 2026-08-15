# The way out

sqlb owns more of a project than a query builder does: the schema, the
migrations, the wire format, the generated client and the CLI. That
concentration is the honest objection to adopting it, and it is not answered by
another feature.

It is answered by a door.

```bash
sqlb eject ./schema
```

That writes a package which imports [pgx](https://github.com/jackc/pgx) and the
standard library and nothing else. No sqlb, no huma, no router. Deleting sqlb
from your `go.mod` afterwards is a supported end state rather than a
hypothetical one.

| File | What it is |
|---|---|
| `schema.sql` | The whole schema as DDL — the same statements a first migration would carry. |
| `models.go` | The row structs, with the `sqlb` tags removed. |
| `store.go` | One function per statement, with the SQL written out. |
| `support.go` | Query-string parsing, `WHERE` assembly, JSON writing. The only file that is the same in every project. |
| `handlers.go` | `net/http` handlers, one per exposed operation. |
| `README.md` | What came out, and what did not. Generated with the code. |

## What comes out whole

CRUD and list, at the same paths, with the same status codes and the same JSON
envelope. The filter operators that are one SQL fragment each — `eq`, `ne`,
`lt`, `lte`, `gt`, `gte`, `in`, `nin`, `isnull`, `notnull`, `between`, `like`,
`ilike`, `contains`, `startswith`, `endswith` — plus the bare `?column=value`
shorthand. `?sort`, `?search`, `?page`/`?per_page`, `?count=exact`, the ceilings
the schema declared, and the RFC 9457 error shape with its `allowed` lists.

Two properties come with it, because they are properties rather than
conveniences:

- **Capabilities stay opt-in.** A column that never declared `Filterable` is not
  filterable in the exit either, and a `Hidden` one has no spelling at all. A
  column outside the grammar cannot be probed through it, and that has to keep
  being true after the framework is gone.
- **The obligation stays compulsory.** A table that declared `Scoped` or
  `SoftDelete` refuses to register without a `Confine` function
  ([ADR-0030](architecture.md#declared-scope-is-required) with the machinery
  removed), and a scoped table with a create endpoint refuses without an
  `Assign` one.

```go
if err := ejected.Register(mux, pool, ejected.Options{
    Posts: ejected.PostsHooks{
        Confine: func(r *http.Request) ([]ejected.Condition, error) {
            return []ejected.Condition{
                {Column: "org_id", Op: ejected.OpEq, Value: orgFrom(r)},
                {Column: "deleted_at", Op: ejected.OpIsNull},
            }, nil
        },
    },
}); err != nil {
    log.Fatal(err)   // the same refusal rest.Resource gave you, at the same moment
}
```

## What does not, and how you find out

Keyset cursors, `?select`, `?expand`, the JSON filter tree, and the array and
document operators. Each of them is the engine rather than the surface, and
reproducing them would mean emitting a copy of sqlb — which is not an exit, it
is a fork with a different import path.

A request that uses one is **refused by name**:

```json
{
  "status": 400,
  "detail": "?expand is not served here",
  "errors": [{
    "message": "relation expansion did not come out with the exit; fetch the related row from its own endpoint",
    "location": "query.expand"
  }]
}
```

That is deliberate. A 200 with a field quietly missing is the failure mode that
would make an exit worse than no exit at all.

The full list, including the notes that apply to *your* schema — a computed
column that took a per-request bind, a filterable array column, a pgvector
column — is in the emitted `README.md`, written at the same time as the code it
describes.

## The exit is tested

`example/blog/ejected` is a committed one. `pgtest/eject_test.go` stands it up
beside the generated resources it came from, points both at the same database,
and sends both the same requests — comparing the response bodies byte for byte,
with exactly two known differences subtracted: huma's `$schema` link, and
`next_cursor`, which is the paging that did not come out.

```bash
mise run eject-check     # the committed exit still matches the schema
mise run test-pg         # the exit still answers what the generated resource answers
```

Keep `sqlb eject -check` in your own CI for as long as you want the door kept
oiled — and delete that gate on the day you walk through it, because from then
on the code is yours to edit and drift is the point.

## What it is not

It is not a migration path, and not a dual-run mode. Nothing keeps the ejected
package and the sqlb resources in step after you take it; the emitted code
assumes it will be edited. [ADR-0042](architecture.md#the-exit-is-generated) records
why the line is drawn there, and what would move it.
