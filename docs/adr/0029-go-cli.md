# ADR-0029: The CLI is generated too, and its help text is the type system

- **Status:** Working — emitted into `example/tasks/cli`, built and exercised by
  `mise run test-cli`, which is part of the gate
- **Confidence:** Medium on the shape — the flag vocabulary, the presence rule
  and the cursor walk are asserted against an httptest server rather than
  assumed; Low on `--all` and on the enum completions, because no operator has
  yet lived with either
- **Decided:** 2026-07-28
- **Last reviewed:** 2026-07-28

## Context

[ADR-0028](0028-typescript-client.md) generates a TypeScript client because the
OpenAPI document is lossy exactly where the value is: `?status=eq.published`
documents as `array<string>` with the operator vocabulary in prose, so a generic
generator emits `status?: string[]` and `status=bogus.x` compiles.

The same argument reaches a second consumer, and it arrives with the question
answered differently. A growing share of the traffic against an application's
own API is not a browser and not a service — it is an agent, working in a shell,
that has to discover what a resource accepts before it can ask for anything. The
tool it reaches for is `curl`, and against this API `curl` is a poor fit for one
specific reason: the grammar is compositional and capability-gated, so the set
of legal requests is not visible from the endpoint. `GET /tasks?priority=gte.2`
either works or returns a 400 naming the filterable columns, and the only way to
find out is to send it.

[ADR-0011](0011-actionable-errors.md) makes that 400 recoverable — it names what
would have been accepted — and that is genuinely most of a fix. But it is a fix
delivered after a failed request, and the caller who most needs it is the one for
whom a round trip is a turn.

The third-party option is a generic REST CLI (`restish`, `httpie`) pointed at
the OpenAPI document. These are good tools, and `restish` in particular reads a
document and offers per-operation commands. They inherit the same loss the
TypeScript case has: the parameters they can offer are the ones the document can
describe, so the filter grammar arrives as free text and the operator set as
prose in a description.

What is different from the TypeScript case is where the vocabulary can land. A
CLI has no compile step, so there is nothing to refuse an illegal request at.
What it has instead is `--help`, which is read before the request rather than
after it.

## Decision

Emit a cobra command tree from the same registry the other emitters read, into
the consuming repository, as one self-contained package.

**The commands are the exposed operations.** One command per exposed table, one
subcommand per operation it declares — `tasks list`, `tasks get <id>`,
`tasks create`, `tasks update <id>`, `tasks delete <id>`. An operation the
resource does not serve has no subcommand, so a 405 is unreachable rather than
merely documented.

**The flags are the capability vocabulary.** One `--column` flag per *filterable*
column, taking the wire spelling of a condition — `--status eq.todo`, or a bare
value for equality — and repeatable, because repeating a parameter is what
conjoins conditions. `--sort`, `--select` and `--expand` complete from the
columns and relations that opted in. A column that declared no capability has no
flag that reaches it, and `--help` is therefore an exact statement of what the
resource accepts, printed without a request.

This is [ADR-0009](0009-typed-column-facade.md)'s property in the only form a
shell can carry it: not a refusal, but a disclosure.

**The operator set is narrowed by column type**, exactly as `rest` narrows the
documentation and `filter` narrows the parse. The null tests appear only on a
nullable column, the pattern operators only on a text one. Offering either
where it does not apply would document a request that parsing rejects.

**Presence is read from the flag, not the value.** A patch sends only the
columns whose flags were passed, because a flag left out and a flag set to `""`
must write different SQL. That is the same distinction the generated patch body
keeps a presence map for, and `cobra.Flags().Changed` is where a CLI keeps it.
The case a flag cannot express at all — setting a column back to NULL — gets
`--set-null`, whose argument is checked against the nullable columns locally so
the refusal names them.

**The transport is a field.** `Client` carries base URL, token, timeout and an
HTTP client, and a `Transport` field replaces the built-in implementation
entirely. Auth, retry and what a 401 does are not derivable from a schema —
[ADR-0007](0007-generated-rest-handlers.md)'s seam argument, in the third
language it has come up in.

**`--all` walks by cursor.** A collection is exhausted by following
`next_cursor` ([ADR-0027](0027-keyset-pagination.md)), not by counting pages, and
the walk is emitted rather than left to the caller. This is the one operation
where the difference is not academic: a shell loop over `?page=` re-reads rows
when the table is written to underneath it, and the walks that take long enough
to matter are exactly the ones during which that happens.

**Output is JSON on stdout, errors on stderr.** The response body is written
through unchanged, so nothing sits between the server's answer and `jq`. A
rejection is rendered from the problem document with its `allowed` list intact,
which is [ADR-0011](0011-actionable-errors.md) reaching the last consumer in the
chain.

And explicitly not: interactive prompts, a config file, an output-formatting
language, credential storage, or a published binary.

### One package, not two

The TypeScript emitter splits client from queries because the two halves differ
in what they depend on. Both halves here would need cobra, and splitting
invariant runtime from per-table commands would make the emitted import set
depend on which operations a schema happens to expose. `go/format` catches a
parse error and not an unused import, so that failure would surface at the
consumer's build rather than in this repository — the class of bug the gofmt
pass exists to prevent.

### It speaks HTTP, not SQL

The generated CLI does not import sqlb and holds no database credential. A
direct-to-Postgres CLI is a defensible tool and a different one: it would bypass
whatever the HTTP layer enforces per request — the JWT claims a `BeforeQuery`
hook reads, the rate limit, the audit log — while looking from the outside like
the same command. The tenant boundary in `example/tasks` is enforced by a hook
reading claims that arrive from HTTP middleware, so a CLI that skipped the
server would be scoped to nothing.

## Consequences

**What this buys.** The vocabulary a resource accepts is readable without a
request, by a human or by an agent, and it cannot drift from what the server
enforces because both are generated from the same declaration. A column that
gains `Filterable()` gains its flag at the next `go generate`; one that loses it
loses the flag rather than starting to 400. Hidden columns have no spelling at
all. The cursor walk and the URL encoding — the two things a shell script gets
subtly wrong — are written once and tested.

**What this costs.** A cobra dependency in the consuming module, which the
generated file makes explicit. `example/tasks` now takes it, and the "nothing to
inherit" claim continues to hold only because the engine emits text: `codegen`
itself imports nothing new, and `deps-check` still passes with the engine on the
standard library alone.

The help output is long. A wide table produces a screen of flags, and there is
no way to make that shorter without withholding some of what the resource
accepts, which is the thing being bought.

Per-column flags scale with the schema rather than with the API surface. A
fifty-column table generates fifty filter flags, most of which nobody will type.
The generated file for a six-table schema is around 1,800 lines.

And the CLI covers exactly the generated CRUD. `example/tasks` has hand-written
`register` and `login` endpoints on the same router, and the CLI has no command
for either — so the first thing an operator needs, a token, is the one thing it
cannot get for them. The same asymmetry ADR-0028 records for its client: the
generated slice is a minority of a real API.

**What building it changed.** Two things the design did not anticipate:

- Binding a flag writes its default into the variable, so registering the
  persistent flags overwrote a `Client` the caller had configured in Go. The
  fix — feed each field's value in as the flag's default — is also the
  precedence a reader would expect: flag, then field, then environment, then
  built-in. It was found by a test, and it would have made the injected
  transport unusable while appearing to work.
- Flag names are kebab-case, as cobra expects, but a caller who read a column
  name out of `sqlb.json` or out of a rejection has the snake_case one in hand.
  A normalisation function accepts both, which costs five lines and removes the
  choice.

## What would change our mind

- If operators reach for `curl` against this API anyway, the flags are not
  buying discovery and the tool should shrink to one generic
  `request` command that handles auth and paging.
- If the per-column flags make `--help` unreadable on a wide table — say beyond
  thirty filterable columns — then the vocabulary belongs in a `describe`
  subcommand that prints the capability table, with a single `--filter
  col.op.value` for the requests themselves.
- If an agent driving this turns out to want the manifest rather than the help
  text, the emitter is aimed at the wrong reader: `sqlb.json` already describes
  every capability, and the right artefact is the MCP server
  [vision](../vision.md) item 5 names, with the CLI as the human's half.
- If commands get wrapped in shell functions to add a flag or reshape output,
  that is copy-out-and-edit in its shell form, and the seam is wrong. Revisit
  where the transport and the output sit, do not add flags.
- If the hand-written endpoints of a real application dominate — if `taskctl`
  is mostly `auth login` and the generated CRUD is the minority — then the
  generated tree should mount under a subcommand of a hand-written root rather
  than be the root, and `New` should return a command to attach rather than a
  program.
- If anyone needs the CLI against a database rather than a server, that is a
  different tool and a different record, not an option on this one.

## Cost of change

Cheap in the emitter, and cheap in the tree: the generated CLI is regenerated
from the schema, so a change to the emitter reaches every command in the run
that produced them, and `go build` names anything that stops compiling.

The asymmetry is in the command line itself, which is an interface with users.

**Adding is free.** A new column, a new capability, a new exposed table each add
flags or commands without touching what exists.

**Renaming is not.** Flag and command names end up in scripts, in shell history
and in whatever an agent has learned about the tool, none of which this
repository can see or fix. That makes the spellings chosen here — kebab-case
flags, `--set-null`, `--all`, the wire-shaped `operator.value` — expensive to
revise, and the wire-shaped value is the one to keep longest, because it is the
same string the manifest and the error messages use.

**The output shape is the most expensive of all**, because it is what gets
piped. Anything reading the JSON with `jq` depends on the envelope, and `--all`
returning a page rather than a bare array is part of that contract — chosen so
that a walk and a single page can be read by the same expression.

## Alternatives considered

**`restish` or another OpenAPI-driven CLI.** Genuinely close, and the reason
this record exists rather than a README line. It needs no generator, it is
maintained, and it handles auth profiles better than this does. It loses on the
same point the TypeScript case turns on: what it can offer is bounded by what
the document can describe, so the filter grammar arrives as free text and the
capability set as prose. The 400 is still the discovery mechanism.

**A generic `--filter col.op.value` flag.** Much smaller output, uniform across
tables, and the server rejects anything illegal with the list of what would have
been accepted. It was rejected because `--help` then says nothing about which
columns are filterable, which is the entire thing being bought. It survives as
the fallback if per-column flags stop scaling — see "What would change our mind".

**Generating a Go API client, with the CLI as a thin shell over it.** The
layering ADR-0028 uses, and it would give Go callers a typed client for free.
Rejected for now because the row types already exist in `models_gen.go`, and a
client that used them would need the module's import path — a new required
option, and one nothing else in `codegen` needs. The CLI builds request bodies
as maps instead, which is not worse for a tool whose input is strings from a
shell. If a Go client is wanted later, it is a second emitter and this one
becomes its shell.

**Publishing a binary.** Same answer as ADR-0028 gives npm: a CLI generated
against the server it talks to cannot be a version behind it, and a published
one can.

## Revisions

- 2026-07-28 — Written, after building it.
