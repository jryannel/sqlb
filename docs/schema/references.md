# References and relations

```go
schema.Ref("author", Author).OnDelete(schema.Restrict).Expandable()
```

`Ref` produces a column named `author_id` and a relation named `author`, typed
to match the target's primary key. The actions are `NoAction`, `Restrict`,
`Cascade`, `SetNull` and `SetDefault`.

The column and the relation are two different names on purpose: the column keeps
its own name, and `?expand=author` names the relation. Expansion adds the row,
it does not replace the reference.

## Choosing the delete action

`Restrict` is the right default for a reference that is a *record that something
happened* — a loan is the record that a physical object left the building, so
deleting the book would either fail on the foreign key or destroy the history.
`Cascade` is right for a reference that is ownership: a tenant's rows go when the
tenant does.

`ON DELETE RESTRICT` on both sides of a join table is also a compensating
control in an application with no authentication: the registry cannot be erased
by removing what it points at.

## Both directions of one reference

`Inverse` names the relation as the *target* knows it, and it is declared on the
referencing side because that is where the column and the constraint already
are:

```go
schema.Ref("list", List).Filterable().Expandable().
    Inverse("tasks").
    InverseExpandable(schema.ExpandOrder("position"), schema.ExpandLimit(20))
```

Read as: a task has a list; a list has tasks; both may be expanded.

The two exposures are separate decisions about two endpoints — `?expand=list` on
a task and `?expand=tasks` on a list — and neither implies the other. Absent
`Inverse` there is no reverse relation, which is normal.

**The name has to be declared rather than derived**, because two references from
one table to another — an author's posts and the posts an author reviewed —
would derive the same name for different sets of rows.

The target's generated struct gains a field for the collected rows; its
declaration is untouched.

Two things follow, and both are covered in
[Expanding relations](../rest/expand.md): a reverse expansion is **capped** and
returns an envelope with `has_more` rather than a bare array, and the ordering
is **declared and always total**, because under a cap the order does not merely
arrange the result — it decides which children the caller never sees.

Past the cap, the caller follows the child's own endpoint filtered by the same
key:

```
GET /tasks?list_id=eq.{id}&sort=position&page=2
```

which is why that column wants to be `Filterable`. `schema.Lint` says so when it
is not, along with reporting an unindexed foreign key, which matters more here
than in the forward direction.

## Across a module boundary

`ExternalRef` emits the column and an index to join on, but **no foreign key**:

```go
// in the billing module, with no import of the tenants module
schema.ExternalRef("tenant", "tenants.id").Filterable()
```

The two modules stay independently deployable and independently migratable, and
either can move to its own database without dropping a constraint. Referential
integrity becomes the application's job — the trade a module architecture is
already making everywhere else. The target string is free text and is not
resolved, because resolving it would require exactly the dependency this avoids.

An external reference cannot be `Expandable`, and cannot declare an `Inverse`
either: expanding it in either direction would reach a table this module does
not own, and nothing about the other side is resolvable.

## What a reference cannot express

Two limits worth knowing before you meet them.

**A composite foreign key is not expressible through `Ref`.** Where a boundary
has to hold across an expansion — a task and its list belonging to the same
workspace — a hook cannot enforce it, because [hooks do not follow the
join](../rest/expand.md#hooks-do-not-follow-the-join). `example/tasks` writes
that pair as hand-written `migrate.Change` values, which is the documented
escape hatch and not a fork.

**Expansion resolves one level.** A relation expands to its row; that row's own
relations do not expand in turn, and there is no `?expand=list.workspace`. One
level is a join per relation and a bounded statement; nesting is where a depth
limit and a cost model have to be argued for
([ADR-0025](../adr/0025-expansion-is-one-statement.md)).

## Next

- [Expanding relations](../rest/expand.md) — what these look like on the wire
- [Migrations](../migrations/README.md) — the DDL a reference produces
- [ADR-0022](../adr/0022-references-declare-their-inverse.md) — why the inverse
  is declared rather than derived
