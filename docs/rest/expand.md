# Expanding relations

`schema.Ref("list", List).Expandable()` makes a reference reachable inline, on
the collection and on a single row alike:

```
GET /tasks?expand=list
GET /tasks/{id}?expand=list
```

```json
{
  "id": "01937...",
  "list_id": "01936...",
  "list": { "id": "01936...", "name": "Backlog", "color": "#6b7280" }
}
```

The key stays where it was — expansion adds the row, it does not replace the
reference — and the relation is named `list`, not `list_id`: the parameter names
the relation, the column keeps its own name.

It is one statement, a `LEFT JOIN` and a `json_build_object` over the target's
columns. Not two queries: the batched `WHERE id IN (…)` alternative runs at a
later snapshot, so a row can vanish between the two and a caller gets a null
expansion for a reference the database still holds.

`Hidden` survives the join. The target's columns are listed explicitly rather
than taken with `row_to_json`, so a hidden column of the expanded table is as
absent from an expansion as it is from the table's own responses — otherwise
`?expand` would be a way to read a column the resource refuses to serve.

Codegen wires all of it: the relation field on the model, and the resource's
`Expandable` list. Nothing here is hand-written.

## The other direction

A reference can also be expanded backwards — a list and the tasks that point at
it — and that is declared on the same line, because the referencing table is the
one that already owns the column:

```go
schema.Ref("list", List).Filterable().Expandable().
    Inverse("tasks").
    InverseExpandable(schema.ExpandOrder("position"), schema.ExpandLimit(20))
```

Read as: a task has a list; a list has tasks; both may be expanded. The name has
to be declared rather than derived, because two references to one table — an
author's posts and the posts an author reviewed — would derive the same name for
different sets. Absent `Inverse` there is no reverse relation, which is normal.

```
GET /lists?expand=tasks
```

```json
{
  "id": "01936...",
  "name": "Backlog",
  "tasks": {
    "items": [{ "id": "01937...", "title": "Write the migration" }],
    "has_more": false
  }
}
```

The value is an envelope, not a bare array, and the reason is `has_more`. A
collection is capped — 20 above, 50 by default — because an uncapped one makes
one response's size a function of data nobody bounded, and an array that was
silently truncated is a wrong answer rather than a short one. Past the cap the
caller follows the child's own endpoint, filtered by the same key:

```
GET /tasks?list_id=eq.{id}&sort=position&page=2
```

which is why that column wants to be `Filterable` — `schema.Lint` says so when
it is not, along with reporting an unindexed foreign key, which matters more
here than in the forward direction.

Under the covers this is one correlated subquery per relation rather than a
join: joining a collection would multiply the base rows, so the page's row count
would depend on the data, and two expanded collections would multiply each
other. It is still one statement, so the snapshot argument above is unchanged,
and `Hidden` holds over the collected rows exactly as it holds over a joined one.

Ordering is declared and always total — the child's primary key is appended as a
tiebreaker — because under a cap the order does not merely arrange the result,
it decides which children the caller never sees.

## What is refused, and what is free

A relation the schema did not mark expandable is refused with the list of the
ones that would have worked, and an unexpanded request pays for no join at all.
Both endpoints produce the same rejection, because both go through the same
parser rather than through two hand-written checks.

`?expand` is the item endpoint's only query parameter, and it is absent on a
resource that declares no relation — asking for it there is an unknown
parameter, not a silently ignored one. `POST` and `PATCH` return the row they
wrote without expansions; fetch the relation with a `GET` if you need it.

Expansion resolves **one level**. A relation expands to its row; that row's own
relations do not expand in turn, and there is no `?expand=list.workspace`. One
level is a join per relation and a bounded statement; nesting is where a depth
limit and a cost model have to be argued for, and neither has been.

## Hooks do not follow the join

A `BeforeQuery` hook registered on the target model does not run for an
expansion — the target arrives as a joined subexpression, not as a query of its
own. Where a hook enforces a boundary the expansion has to respect, **the schema
has to enforce it too**: `example/tasks` keeps a task and its list in the same
workspace with a composite foreign key, not with the hook.

This is the sharpest edge on this page, and it is sharper than it looks. A table
that declares `Scoped` has been *proved* to have a confining hook before it
mounts — so an author who declares it, satisfies the check and sees the resource
mount has more reason than before to believe the rows are confined everywhere.
An expansion joins that table from a parent, and no handler for it runs, so the
hook the check proved exists is precisely the one the join does not call.

**What confines an expansion is the foreign key, not the declaration.** A
composite key carrying the scoping column makes a cross-tenant reference
unrepresentable; a plain single-column key does not, and nothing at mount time
will say so ([ADR-0030](../adr/0030-declared-scope-is-required.md#consequences)).

[ADR-0025](../adr/0025-expansion-is-one-statement.md) records why it is one
statement, why the columns are listed rather than taken wholesale, and what
would make either worth revisiting.

## Next

- [Rejections](errors.md) — what a non-expandable relation says
- [References and relations](../schema/references.md) — declaring both directions
