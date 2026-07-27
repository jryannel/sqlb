# Guide

Task-shaped documentation: what to do, in the order you do it. The
[README](../../README.md) explains what sqlb is and why it is shaped the way it
is; this explains how to use it.

| Page | Covers |
|---|---|
| [Getting started](getting-started.md) | Install, declare a schema, generate, run your first query |
| [Schema](schema.md) | Columns, capabilities, references, indexes, validation and linting |
| [Queries and hooks](queries-and-hooks.md) | Building queries, mutations, transactions, and where domain logic goes |
| [REST](rest.md) | Mounting resources, the filter grammar, OpenAPI, rejections |
| [Migrations](migrations.md) | Diffing a schema change, rendering files, adopting an existing database |

Every Go snippet on these pages is also a compiled `Example` function in the
package it documents, so it is verified by `go test ./...` rather than by
having been correct on the day it was written. Where a page shows output, that
is the real output. The examples are the canonical form; if a page and an
example ever disagree, the example is right.

## Related

- **[pkg.go.dev](https://pkg.go.dev/github.com/jryannel/sqlb)** — the API
  reference, with those examples attached to the symbols they document.
- **[`example/blog/`](../../example/blog/)** — a worked schema, everything
  codegen emits from it, and an assembled server. It is a real test suite, so it
  cannot drift from the code.
- **[docs/adr/](../adr/)** — why each decision was made, and what would change
  it. Read these when a design choice looks wrong; most of them have already
  been argued.
