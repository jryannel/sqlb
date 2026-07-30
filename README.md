# sqlb

[![Go Reference](https://pkg.go.dev/badge/github.com/jryannel/sqlb.svg)](https://pkg.go.dev/github.com/jryannel/sqlb)
[![CI](https://github.com/jryannel/sqlb/actions/workflows/ci.yml/badge.svg)](https://github.com/jryannel/sqlb/actions/workflows/ci.yml)
[![Go Version](https://img.shields.io/github/go-mod/go-version/jryannel/sqlb)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

A schema-first data layer for Go and Postgres: declare your tables once, get
typed composable queries, a validated REST filter grammar, and domain hooks —
without hand-writing the HTTP-to-SQL layer for every dynamic view.

**[Documentation](https://jryannel.github.io/sqlb/)** ·
[Quickstart](https://jryannel.github.io/sqlb/start/quickstart/) ·
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
- **Paging that survives a write.** `?cursor=` names the position of the last
  row rather than counting to it, so page 500 costs what page 1 costs and a
  concurrent insert cannot make a client read a row twice. Every list response
  carries the cursor for the next page, so adopting it needs no flag.
- **Nothing runs unasked.** `SQL()` renders text and args without executing.
  `Explain` plans against the live schema without running it, so it also fails
  on the migration that was written and never applied — which a compile-time
  column check cannot. `Diff` returns migration changes as values; your runner
  applies them.
- **The clients are generated from the schema too.** A TypeScript client,
  emitted into the repository that consumes it, where `where` admits only
  filterable columns with the operators their type accepts, `select` narrows the
  response type, and a hidden column has no spelling at all. The OpenAPI
  document cannot say any of that — `?status=eq.published` documents as
  `array<string>` — so it is generated from the model instead
  ([guide](https://jryannel.github.io/sqlb/typescript/)). The same vocabulary
  reaches a Flutter app as Dart — plus the cursor pager an infinite-scrolling
  list needs, which is the piece a mobile client otherwise rebuilds out of
  `has_more` and an offset counter
  ([guide](https://jryannel.github.io/sqlb/dart/)) — and a shell as a
  [cobra](https://github.com/spf13/cobra) command tree: one flag per filterable column, its operators in the usage string, so
  `--help` states what a resource accepts without a request — which is the form
  the guarantee has to take for a caller with no compile step, such as an agent
  ([guide](https://jryannel.github.io/sqlb/cli/)).
- **One dependency, and it is the one you already have.** The engine is written
  on [pgx](https://github.com/jackc/pgx) and takes nothing else; a CI gate fails
  on anything that is not pgx or something pgx itself pulls in. That is a
  deliberate reversal — sqlb used to depend on the standard library alone, and
  [ADR-0040](docs/adr/0040-the-driver-is-a-dependency.md) says what it bought:
  sqlb writes join a `pgx.Tx` your own code opened, arrays need no codec, and
  pgvector's binary format is reachable. Only the REST adapter pulls in
  [Huma](https://huma.rocks), and only if you use it. The generated TypeScript,
  the generated Dart and the generated CLI are separate toolchains and separate
  opt-ins; the emitters produce text, so `codegen` itself takes nothing.

## Install

```bash
go get github.com/jryannel/sqlb
```

Go 1.25 or newer, and Postgres.
[Quickstart](https://jryannel.github.io/sqlb/start/quickstart/) goes
from here to a running server.

The generator is a command, and the loop is one line each way:

```bash
go install github.com/jryannel/sqlb/cmd/sqlb@latest

sqlb generate ./schema                # models, typed columns, REST bodies, manifest, clients
sqlb check ./schema                   # the CI drift gate: writes nothing, fails if stale
sqlb migrate -name adds_slug ./schema # the migration that closes the gap
```

The argument is the package that declares your schema, and the package says what
to emit and where by exporting one function. Because the schema is Go, `sqlb`
compiles a driver against your module to read it — see
[ADR-0032](docs/adr/0032-sqlb-command.md) for why that is forced and what it
costs.

`generate` and `check` need no database. `migrate` works out the current schema
by replaying your committed migrations into a scratch Postgres, because reading
a live one tells you what the database looks like rather than whether the
migrations produce it — so it needs an empty database, except for the very first
migration, which diffs against nothing.

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

Not built yet, in the order they matter: a durable change feed, and an MCP
server over the manifest.
[Vision](https://jryannel.github.io/sqlb/project/vision/) has the detail.

## Documentation

| | |
|---|---|
| [Start here](https://jryannel.github.io/sqlb/start/) | Overview, quickstart, a worked first app, structs-first adoption |
| [Concepts](https://jryannel.github.io/sqlb/concepts/) | The five ideas the rest of it rests on |
| [Schema](https://jryannel.github.io/sqlb/schema/) · [Queries](https://jryannel.github.io/sqlb/queries/) · [REST](https://jryannel.github.io/sqlb/rest/) · [TypeScript](https://jryannel.github.io/sqlb/typescript/) · [Dart](https://jryannel.github.io/sqlb/dart/) · [CLI](https://jryannel.github.io/sqlb/cli/) · [Migrations](https://jryannel.github.io/sqlb/migrations/) | One section per surface |
| [Examples](https://jryannel.github.io/sqlb/examples/) | Six worked applications, and what each one proves |
| [Reference](https://jryannel.github.io/sqlb/reference/) | Filter operators, column types, capabilities, codegen options, CLI, rejections |
| [Architecture](https://jryannel.github.io/sqlb/project/architecture/) | How the pieces fit, the request path, where safety lives |
| [Decision records](https://jryannel.github.io/sqlb/adr/) | What was decided, why, and what would change our mind |
| [`example/blog`](example/blog/) | A worked schema and everything codegen emits from it |
| [`example/tasks`](example/tasks/) | A multi-tenant task manager: auth, migrations, a runnable server, and a generated TypeScript client, Dart client and CLI |
| [`example/fxapp`](example/fxapp/) | The same pieces assembled by uber-go/fx: hooks arriving through a value group, and a resource that refuses to mount without them |

## Development

```bash
mise run test    # the inner loop; no Docker or Postgres needed
mise run ci      # the full gate, same as .github/workflows/ci.yml
mise tasks       # everything else
```

Tool versions are pinned in `mise.toml`, so a green run locally and a green run
in CI use the same Go and the same linter. The engine's tests run against an
in-memory executor rather than a database, which keeps the inner loop fast;
`test-pg` answers what that cannot — whether the generated SQL is *valid* rather
than merely expected — and is part of `ci`.

[CONTRIBUTING.md](CONTRIBUTING.md) has what a change is expected to carry, and
where to argue with a decision record rather than around it.

## License

MIT — see [LICENSE](LICENSE).
