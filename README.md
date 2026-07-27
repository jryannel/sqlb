# sqlb

[![Go Reference](https://pkg.go.dev/badge/github.com/jryannel/sqlb.svg)](https://pkg.go.dev/github.com/jryannel/sqlb)
[![CI](https://github.com/jryannel/sqlb/actions/workflows/ci.yml/badge.svg)](https://github.com/jryannel/sqlb/actions/workflows/ci.yml)
[![Go Version](https://img.shields.io/github/go-mod/go-version/jryannel/sqlb)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

A schema-first data layer for Go and Postgres: declare your tables once, get
typed composable queries, a validated REST filter grammar, and domain hooks —
without hand-writing the HTTP-to-SQL layer for every dynamic view.

**[Documentation](https://jryannel.github.io/sqlb/)** ·
[Getting started](https://jryannel.github.io/sqlb/guide/getting-started/) ·
[API reference](https://pkg.go.dev/github.com/jryannel/sqlb) ·
[Decision records](https://jryannel.github.io/sqlb/adr/)

## Why

Static query generators cannot express *"this WHERE clause exists only when the
user typed something in the search box."* The usual workaround is string
concatenation, which is why the HTTP layer of a filter/sort/search page is
mostly boilerplate.

PostgREST solves that by making the database the API, but there is then nowhere
to put Go domain logic, and the whole schema sits one policy mistake away from
being public.

sqlb takes the middle path. A query is a **value**, so predicates can be added
conditionally:

```go
q := sqlb.Query[Post]().Where(sqlb.F("status").Eq("published"))
if search != "" {
    q = q.Where(sqlb.F("title").Contains(search))
}
posts, err := q.OrderBy(sqlb.F("created_at").Desc()).Limit(50).All(ctx, db)
```

and the REST filter grammar compiles into that **same** predicate AST. One
compiler, one bind-parameter discipline, one set of hooks — two producers.

## What that buys

- **Capabilities are opt-in per column.** `Filterable`, `Sortable`,
  `Searchable`, `Hidden`. A column that does not declare a capability cannot be
  reached through it — ever, and the failure is a 400 naming what *would* have
  been accepted, not a leak. This is the difference between this and exposing
  the database.
- **Hooks are the domain seam.** `BeforeQuery` receives the query itself, so one
  registration constrains every read of a model — including the reads that
  generated REST handlers issue. Tenant scoping stops being something each call
  site has to remember.
- **Nothing runs unasked.** `SQL()` renders text and args without executing.
  `Explain` plans against the live schema without running it, so it also fails
  on the migration that was written and never applied — which a compile-time
  column check cannot. `Diff` returns migration changes as values; your runner
  applies them.
- **No dependencies to inherit.** The engine depends on the standard library
  alone, and a CI gate enforces it. Only the REST adapter pulls in
  [Huma](https://huma.rocks), and only if you use it.

## Install

```bash
go get github.com/jryannel/sqlb
```

Go 1.25 or newer, and Postgres.
[Getting started](https://jryannel.github.io/sqlb/guide/getting-started/) goes
from here to a running server.

The schema DSL and code generation are both optional: `sqlb.Describe[T]()`
layers the same capabilities over structs you already have, including stock
[sqlc](docs/with-sqlc.md) output, without editing them.

## Status

**Pre-1.0, one author, no observed consumers.** That is the honest starting
position, and no amount of feature work substitutes for elapsed time under real
traffic. [Compatibility](https://jryannel.github.io/sqlb/project/compatibility/)
says what `v0.1.0` freezes and which surfaces are expected to move.

What *is* proven, and re-checked on every run rather than asserted: CI applies
the generated DDL to a real Postgres 18, reads it back with `introspect`, and
requires the round trip to be a fixpoint; the query path runs through a real
PgBouncer in transaction pooling, because that is the deployed topology; and the
blog example is generated from its schema, so every behaviour test in it is also
a test of the generator's output.

Postgres only. `LISTEN/NOTIFY`, jsonb aggregation and `RETURNING` are all
load-bearing; multi-dialect support would cost the best features.

Not built yet, in the order they matter: a TypeScript client from the generated
OpenAPI document; `?expand` — the grammar validates relation names, but every
surface refuses the parameter rather than accepting it and answering without it;
a durable change feed; and a command-line entry point.
[Vision](https://jryannel.github.io/sqlb/project/vision/) has the detail.

## Documentation

| | |
|---|---|
| [Guide](https://jryannel.github.io/sqlb/guide/) | Install, schema, queries and hooks, REST, migrations |
| [Architecture](https://jryannel.github.io/sqlb/project/architecture/) | How the pieces fit, the request path, where safety lives |
| [Decision records](https://jryannel.github.io/sqlb/adr/) | What was decided, why, and what would change our mind |
| [`example/blog`](example/blog/) | A worked schema and everything codegen emits from it |
| [`example/tasks`](example/tasks/) | A multi-tenant task manager: auth, migrations, a runnable server |

## Development

```bash
mise run test    # the inner loop; no Docker or Postgres needed
mise run ci      # the full gate, same as .github/workflows/ci.yml
mise tasks       # everything else
```

Tool versions are pinned in `mise.toml`, so a green run locally and a green run
in CI use the same Go and the same linter. The engine's tests run against an
in-memory `database/sql` driver, which keeps the inner loop fast; `test-pg`
answers what that cannot — whether the generated SQL is *valid* rather than
merely expected — and is part of `ci`.

## License

MIT — see [LICENSE](LICENSE).
