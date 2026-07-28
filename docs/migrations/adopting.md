# Adopting a database

## Where "current" comes from

Three sources, in increasing order of how much you should trust them.

**A live database**, via `introspect`:

```go
reg, report, err := introspect.Registry(ctx, db, introspect.Options{})
```

Tells you what the database looks like. It is the obvious source and the worst
one, because it cannot tell you whether the migration history *produces* that
state — a hand-applied hotfix, a migration edited after it ran, or a statement
someone skipped are all invisible, and the next migration gets computed against
a state no file describes.

**The migration history**, via `shadow`:

```go
reg, report, result, err := shadow.Build(ctx, scratchDB, shadow.Options{Dir: "db/migrations"})
```

Replays the checked-in history into an empty database and reads back what it
actually produced. This is a different and stronger claim: *this is the schema
the history builds*. It is the better source for the current side of a diff,
because an edited or skipped migration surfaces instead of being baked into the
next one.

This is not a migration runner. It applies all of them, in order, to an empty
database nobody depends on, and throws away the result. No version table is read
or written, nothing is skipped, and `Down` sections are never executed.

**Drift detection** needs no extra API: it is `migrate.Diff` between the
replayed registry and the live one. An empty result is the claim that the
history and the database agree.

## Adopting an existing database

Two calls, and then you own a schema file:

```go
reg, report, err := introspect.Registry(ctx, db, introspect.Options{})
if !report.Empty() {
    // Constructs the DSL cannot express. Read them: the schema does not
    // describe the database completely until this is empty.
    log.Print(report)
}

src, err := codegen.RenderSchema(reg, codegen.SchemaOptions{Package: "blogschema"})
os.WriteFile("blogschema/schema.go", src, 0o644)
```

`introspect` reports every construct it could not express rather than dropping
it, which is what makes the report worth reading rather than skipping.

Everything imports with **no capabilities and nothing exposed over REST**,
because neither can be read from DDL — widening that is a deliberate edit, which
is the correct default for a surface that decides what the outside world can
reach. Table names are not singularised (`orgs` becomes `var Orgs`), because
guessing wrongly on *status* or *address* costs more than renaming a variable
the compiler checks for you.

Generating a migration and adopting a database are the same machinery pointed in
opposite directions.


## Next

- [Diffing and rendering](README.md)
- [Using your own structs](../start/structs-first.md) — the other half of
  adopting sqlb into a project that already exists
- [ADR-0014](../adr/0014-migrations-and-import.md) — why the history beats
  production as a source of truth
