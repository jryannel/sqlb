# ADR-0029: The CLI is generated too, and its help text is the type system

- **Status:** Working — emitted into `example/tasks/cli`, built and exercised by
  `mise run test-cli`, which is part of the gate
- **Confidence:** Medium on the shape — the flag vocabulary, the presence rule
  and the cursor walk are asserted against an httptest server; Low on `--all` and
  the enum completions, because no operator has lived with either
- **Decided:** 2026-07-28
- **Last reviewed:** 2026-07-28

## Context

[ADR-0028](0028-typescript-client.md)'s argument reaches a second consumer. A
growing share of traffic against an application's own API is an agent working in
a shell, which has to discover what a resource accepts before asking for
anything. Against this API `curl` is a poor fit for one reason: the grammar is
compositional and capability-gated, so the set of legal requests is not visible
from the endpoint. `GET /tasks?priority=gte.2` either works or returns a 400
naming the filterable columns, and the only way to find out is to send it.

[ADR-0011](0011-actionable-errors.md) makes that 400 recoverable, which is most
of a fix — but it is delivered after a failed request, and the caller who most
needs it is the one for whom a round trip is a turn. A generic OpenAPI-driven CLI
(`restish`, `httpie`) inherits the same loss the TypeScript case has: the filter
grammar arrives as free text.

What differs from the TypeScript case is where the vocabulary can land. A CLI has
no compile step, so nothing can refuse an illegal request. What it has instead is
`--help`, which is read *before* the request rather than after it.

## Decision

Emit a cobra command tree from the same registry the other emitters read, into
the consuming repository, as one self-contained package.

- **The commands are the exposed operations.** One command per exposed table, one
  subcommand per declared operation, so a 405 is unreachable rather than merely
  documented.
- **The flags are the capability vocabulary.** One `--column` flag per filterable
  column, taking the wire spelling of a condition (`--status eq.todo`) and
  repeatable, because repeating a parameter is what conjoins conditions. `--sort`,
  `--select` and `--expand` complete from what opted in. `--help` is therefore an
  exact statement of what the resource accepts, printed without a request — which
  is [ADR-0009](0009-typed-column-facade.md)'s property in the only form a shell
  can carry it: not a refusal, but a disclosure.
- **The operator set is narrowed by column type**, exactly as `rest` narrows the
  documentation and `filter` narrows the parse.
- **Presence is read from the flag, not the value.** A patch sends only the
  columns whose flags were passed, because a flag left out and a flag set to `""`
  must write different SQL. Setting a column back to NULL gets `--set-null`,
  checked locally against the nullable columns so the refusal names them.
- **The transport is a field.** A `Transport` field replaces the built-in
  implementation entirely — [ADR-0007](0007-generated-rest-handlers.md)'s seam
  argument in the third language it has come up in.
- **`--all` walks by cursor** ([ADR-0027](0027-keyset-pagination.md)), not by
  counting pages. A shell loop over `?page=` re-reads rows when the table is
  written underneath it, and the walks long enough to matter are exactly the ones
  during which that happens.
- **Output is JSON on stdout, errors on stderr**, written through unchanged so
  nothing sits between the server's answer and `jq`. A rejection renders the
  problem document with its `allowed` list intact.

Explicitly not: interactive prompts, a config file, an output-formatting
language, credential storage, or a published binary.

**One package, not two.** Both halves need cobra, and splitting would make the
emitted import set depend on which operations a schema exposes — `go/format`
catches a parse error and not an unused import, so that failure would surface at
the consumer's build.

**It speaks HTTP, not SQL.** The generated CLI does not import sqlb and holds no
database credential. A direct-to-Postgres CLI would bypass what the HTTP layer
enforces per request — the JWT claims a `BeforeQuery` hook reads, the rate limit,
the audit log — while looking from outside like the same command.

## Consequences

**Buys.** The vocabulary a resource accepts is readable without a request, by a
human or an agent, and cannot drift from what the server enforces because both
come from one declaration. A column that gains `Filterable()` gains its flag at
the next `go generate`. Hidden columns have no spelling at all. The cursor walk
and the URL encoding — the two things a shell script gets subtly wrong — are
written once and tested.

**Costs.** A cobra dependency in the consuming module. The help output is long,
and a wide table produces a screen of flags; there is no way to shorten it
without withholding what is being bought. Per-column flags scale with the schema
rather than the API surface — a six-table schema emits around 1,800 lines.

And the CLI covers exactly the generated CRUD. `example/tasks` has hand-written
`register` and `login` endpoints and the CLI has no command for either, so the
first thing an operator needs — a token — is the one thing it cannot get them.

**What building it changed.** Binding a flag writes its default into the
variable, so registering persistent flags overwrote a `Client` the caller had
configured in Go; feeding each field's value in as the flag's default is also the
precedence a reader expects. And flag names are kebab-case as cobra expects,
while a caller reading a column out of `sqlb.json` has the snake_case one — a
normalisation function accepts both.

## What would change our mind

- Operators reach for `curl` anyway — the flags are not buying discovery, and the
  tool should shrink to one generic `request` command that handles auth and paging.
- Per-column flags make `--help` unreadable on a wide table, beyond about thirty
  filterable columns — then the vocabulary belongs in a `describe` subcommand
  with a single `--filter col.op.value` for the requests.
- An agent wants the manifest rather than the help text — the emitter is aimed at
  the wrong reader, and the right artefact is an MCP server with the CLI as the
  human's half.
- Commands get wrapped in shell functions to add a flag or reshape output — that
  is copy-out-and-edit in shell form, and the seam is wrong.
- Hand-written endpoints dominate a real application — then the generated tree
  should mount under a hand-written root, and `New` should return a command to
  attach rather than a program.

## Cost of change

Cheap in the emitter and in the tree — regeneration reaches every command, and
`go build` names what stops compiling. The asymmetry is in the command line
itself, which is an interface with users.

**Adding is free.** **Renaming is not**: flag and command names end up in
scripts, shell history and whatever an agent has learned, none of which this
repository can fix. The wire-shaped `operator.value` is the spelling to keep
longest, because it is the same string the manifest and the errors use. **The
output shape is the most expensive**, because it is what gets piped — `--all`
returning a page rather than a bare array is part of that contract, chosen so a
walk and a single page read by the same expression.

## Revisions

- 2026-07-28 — Written, after building it.
- 2026-07-30 — Condensed.
