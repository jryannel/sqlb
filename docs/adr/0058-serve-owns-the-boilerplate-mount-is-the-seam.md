# ADR-0058: `rest.Serve` owns the boilerplate every server repeats, and `mount` is where an application's opinions begin

- **Status:** Exploring — built and live-tested in `example/tasks2`
  (`rest.Serve`, `sqlb init`), not used by a second application
- **Confidence:** Medium — the mechanism is a mechanical extraction (measured
  byte-identical server behaviour before and after in `tasks2`), so the risk
  is not whether it works but whether the seam is drawn in the right place
- **Decided:** 2026-08-14
- **Last reviewed:** 2026-08-14

## Context

Every sqlb server's `main.go` opened a pool, pinged it, ran migrations,
started an `http.Server`, and shut it down gracefully on `SIGINT`/`SIGTERM` —
identically, because none of it depends on the schema. `example/tasks2`
measured the split: of ~134 lines in `run()`, only the resource-mounting
half — which tables, which `huma.Group`, which middleware — was specific to
the application.

[ADR-0044](0044-the-container-is-an-adapter.md) already answered the general
form of this question for the fx container case: publish only what needs no
opinion attached (a context contract, stdlib only), and keep the opinionated
glue (which router, which migration runner) as a copy-paste example, never an
import. This record applies the same rule to the plain, no-DI-container path
most sqlb applications actually take — `rest.NewServer` already draws that
line for the HTTP layer (net/http, no third-party router); `rest.Serve` draws
it for everything around the HTTP layer.

Two things surfaced while building it, worth recording because they are not
what `rest.Serve` had to build:

- **`huma.NewGroup(api)`** — no prefix, since `Options.Path` is already
  absolute — is the actual primitive for per-resource middleware (auth on
  writes, not reads). It already exists in `huma`; nothing here needed
  inventing.
- **Generated `Register` bundles every exposed table onto one shared `api`**,
  with no way to route a subset through a group. `tasks2`'s `mount` hand-
  reconstructs `Task`'s `rest.Resource`/`CollectionAction` calls instead of
  calling `Register`, solely so `Task`'s routes can go through a group and
  `List`'s do not. That is a codegen gap this record does not close.

## Decision

**`rest.Serve(ctx, ServeConfig, mount)`** opens the pool, pings it, runs
`ServeConfig.Migrate` if set, builds a `*Server`, calls
`mount(*Server, sqlb.Executor) error`, and then serves until `ctx` is
cancelled, shutting down gracefully either way.

**`Migrate` is a caller-supplied `func(ctx, *pgxpool.Pool) error`, not a
migration runner `rest` owns.** Owning one — goose, atlas, anything — would
be exactly the opinion ADR-0044 says stays out of a published import; a
project that migrates as a separate deploy step passes `nil` and pays
nothing.

**`mount` is the whole seam.** Which resources, whether they need a
`huma.Group`, what that group's middleware does — none of it is inferable
from a schema value alone, and `Serve` does not try.

**`sqlb init -module <path> [dir]`** applies the identical boundary to
project scaffolding: it writes `go.mod`, a one-table schema, `sqlb.go`, and a
`cmd/server/main.go` built on `rest.Serve` with disk-read (not embedded)
migrations — but does not run `go mod tidy`, `go generate`, or
`sqlb migrate` itself, because each needs something `init` cannot promise
(network resolution, a prior step's output). It was verified against the
real published module on the proxy, not a local `replace`.

## Consequences

**Buys.** `tasks2`'s `main()` went from ~134 lines to 29, and the 29 that
remain are the DSN check and the `Serve` call — no pool code, no
`http.Server`, no signal handling left in application code at all. `sqlb
init` turns into a running CRUD API in five ordinary commands from an empty
directory.

**Costs.** `Serve` fixes the shape of the boilerplate it owns — one pool, one
`*Server`, one `http.Server` on one address. An application that wants two
independent `huma.API`s in one process (a consumer surface and a superadmin
one, each with its own OpenAPI document — the pattern behind
[ADR-0050](0050-reachability-is-a-property-of-the-mount.md)'s Revisiting
status) does not fit inside one `Serve` call and has to open the pool and
wire both servers by hand, which is exactly what `Serve` was supposed to
remove.

## What would change our mind

- A second application's `main.go` needs a shape `Serve` cannot express
  (two servers, two ports, a non-HTTP listener) often enough that the
  single-server assumption is the wrong default rather than the common case.
- `Migrate` staying a caller callback turns out to be false economy — if
  every real project's migrate func is the same ten lines, `rest` not
  shipping a goose adapter is optimizing against a dependency nobody minded.

## Cost of change

**Widening is free** — `ServeConfig` gaining fields is additive, and a
caller that never sets `Migrate` or `ShutdownTimeout` sees no change.
**Narrowing is not tested yet**: nothing depends on today's signature, so the
honest cost of changing it is zero until a second application adopts it.

## Open questions I had to answer myself

- **Whether `Serve` should support more than one `*Server` per call**, for
  the two-API-per-process case ADR-0050's revision surfaced. Not built —
  today that shape means not using `Serve` for the second server. Left open
  because one example is not enough evidence to design a multi-server
  signature against.
- **Whether the codegen gap `mount` works around (`Register` bundling every
  table) belongs in this record or is purely ADR-0050's.** Recorded here
  because `Serve`'s design is where it was felt, not because this record
  proposes to close it.

## Revisions

- 2026-08-14 — Written, after building `rest.Serve` and `sqlb init` and
  measuring the line-count reduction and behavioural equivalence in
  `example/tasks2` against real Postgres.
- 2026-08-14 — **`sqlb init`'s scaffold corrected to actually use
  `rest.Serve`.** It did not: the Decision text above already claimed it did,
  written ahead of the code rather than after it, and the scaffold still had
  its own hand-rolled pool/listen/shutdown at the time. Caught by review, not
  by a test — worth recording as the reason this file's own claims are now
  checked against the code before being trusted. Fixed and re-verified live
  against the real published module (not a local `replace`), same as the
  first pass.
- 2026-08-14 — [ADR-0057](0057-a-read-is-a-query-and-a-row-scoped-write-is-a-mutation.md)
  wired `Query`/`Mutation` into codegen alongside `Action`, which does not
  change anything here — `mount`'s reason for hand-reconstructing `Task`'s
  resource calls (the group) is unaffected — but is worth cross-linking:
  `Register` now grows up to three optional trailing params, and a table
  with none of the three still gets the two-arg form untouched.
