# ADR-0044: The container is an adapter, and the adapter is glue to copy

- **Status:** Working — `example/fxapp/fxkit` is the glue, `sqlb.WithPrincipal`
  is the seam, and nothing about fx is published.
- **Confidence:** High for the split, Medium for where the line falls if a
  second consumer ever appears.
- **Decided:** 2026-07-31
- **Reversed in part:** 2026-08-01 — see Revisions.
- **Last reviewed:** 2026-08-01

## Context

`example/fxapp` answers the question people building on
[uber-go/fx](https://github.com/uber-go/fx) ask first — where do the sqlb
pieces go when a container assembles the application — and it answered it with
roughly four hundred lines of glue that every fx adopter re-writes:
`dbbase` (the pool, the migration runner over a value group, the `Migrated`
token), `sqlbkit` (the hook registry assembled from a value group, the
scoped/unscoped handle pair), and `httpkit` (chi, a Huma API, the server's
lifetime, the middleware and operation groups).

Three things about that glue looked worth more than an example can deliver:

1. **The group names and element types are a de-facto contract with no
   owner.** A module contributes `sqlbkit.HookSet` to `group:"hooks"` — but
   `hooks` is a name the example happened to pick, `HookSet` is a type the
   example happened to define, and a third-party module (an auth module, an
   audit module) has nothing stable to compile against. Pluggability needs the
   contract to be published, not copied.

2. **The boot-refusal guarantee stops at the example's edge.** sqlb's
   distinctive check — a `Scoped` resource refuses to mount without its
   confining hook ([ADR-0030](0030-declared-scope-is-required.md)) — reaches fx
   only because the example's `OperationSet.Register` returns an error. Whether
   a given application's copy of the glue preserves that path is a matter of
   how carefully it was copied.

3. **The seam between "who is calling" and "what confines the query" is a
   per-app convention.** `access` writes a slug into the context under a
   private key; `spaces.Directory.Current` reads it back. Swapping the auth
   mechanism means touching that convention everywhere it is spelled. A
   published contract for the principal is what would make auth modules
   interchangeable.

The engine deliberately knows nothing about containers, and
[ADR-0040](0040-the-driver-is-a-dependency.md)'s dependency stance — pgx and
nothing else, enforced by `deps-check` — rules out fx (which brings `dig` and
`multierr`) ever entering the engine module.

The first decision took all three of those as reasons to publish a module,
`github.com/jryannel/sqlb/sqlbfx`. That was reversed a day later. The three
observations were not wrong; what was wrong was treating them as one problem
with one answer, when reason 3 is about a contract with a real second consumer
and reasons 1 and 2 are about a contract with a hypothetical one.

## Decision

**The assembly is a package of the example: `example/fxapp/fxkit`.** Copy it,
adapt it, own it. It is not published and has no import path anyone outside
this repository should use.

The argument is what the glue actually consists of. Every line of it is an
opinion — chi for the router, humachi for the API, goose for the runner,
`log/slog` for the log — and opinions that load-bearing are the wrong thing to
put behind an import path, because a library can only be taken or refused. An
application on echo, or on golang-migrate, or on a platform layer that already
owns the router, does not want a smaller version of this kit; it wants this
file with four lines changed. Publishing converts an adaptable reference into a
take-it-or-leave-it dependency, and then spends a compatibility surface, a
second `go.mod` and a second release tag doing it.

What publishing buys instead is the thing an example genuinely cannot give:
a type that two *separately-authored* modules can both compile against. That
is real, and it is worth the cost the moment a second author exists. No second
author existed. The reference fx codebase the first decision leaned on
(studio-apps/core, which would have taken `Handles()` alone over its own pool)
will not import it.

**The obligations are written down instead of enforced by an import.** Four
properties, in `fxkit/doc.go` and repeated in its README, each stated as a
decision a copy has to keep rather than a habit it might:

1. A refused mount is a boot failure, naming the module — `OperationSet.Register`
   returns the ADR-0030 error and fx stops the process.
2. `Migrated` is a value, so ordering is a dependency edge and nothing can
   query a table that does not exist yet, in any module-list order.
3. Middleware order is an explicit integer, because fx value groups have no
   defined order.
4. The boot log is deterministic — contributions sorted by module name.

The first is asserted in `TestResourcesRefuseToMountWithoutHooks`, against a
real Postgres, by removing a module and requiring the server not to start.
That test is part of what a copier should take.

**The principal seam moves into the engine.** `sqlb.WithPrincipal(ctx, p)` and
`sqlb.PrincipalFrom[T](ctx)` — a context contract, stdlib only, no change to
ADR-0040's dependency list. Middleware resolves credentials to a principal and
stores it; scoping hooks read it back by type; neither end names the other,
which is what makes an auth mechanism swappable without touching a hook.

This is the half that was published on the right instinct and in the wrong
place. The first decision put it in `sqlbfx` "because that is where its first
consumers are" and listed "a non-fx consumer wants the principal seam" as what
would change our mind. That consumer already existed, in this repository, and
predated the kit: `example/tasks/auth/context.go` had hand-written the same
private-key-plus-typed-accessor pair, with no container anywhere in sight. Two
conventions for one seam is one too many, because a hook written against either
is a hook that cannot move. Both examples now sit on the engine's.

The split the two halves make is the general rule this ADR ends on: **publish
the seams, copy the assembly.** A seam is small, opinion-free and spelled by
application code that nobody regenerates — hooks name the principal directly,
which is exactly why moving it later is expensive and having one of it is
worth a lot. An assembly is large, opinionated and rewritten per deployment;
its value is in being read and adapted, and an import path takes that away.

**Configuration is the application's, still.** `DBConfig` and `HTTPConfig` are
plain structs the application provides — from env, from flags, from wherever —
because ADR-0040 already decided that how the pool is sized and where its DSN
comes from is no part of the library's business. The kit reads no environment
variable and freezes no variable name. The logger is an optional dependency: if
the graph provides a `*slog.Logger` the kit uses it, otherwise `slog.Default()`.

**The five options survive the demotion.** `Pool`, `Migrations`, `Handles`,
`HTTP`, and `Module` as the sum, with `Handles` the one every composition
includes — because the composability was never about being importable. A
codebase whose platform layer owns the pool and the router copies `Handles`
alone and supplies `fx.Supply(fxkit.Migrated{})`, the application asserting
what the kit cannot know. The group names stay prefixed (`fxkit.hooks`, not
`hooks`) for the same scenario: a platform package that already consumes
`group:"migrations"` is exactly what this glue has to sit beside.

## Consequences

**Buys.** No published compatibility surface with no consumers, no second
`go.mod`, no second release tag, and no ordering dance between an engine tag
and a kit tag. The engine's own tests keep resolving exactly what a consumer
of the engine resolves, because there is no workspace widening the build list
to satisfy fx's dependencies. And the glue can now change freely — the example
is versioned with the engine, so a better assembly is a commit rather than a
breaking minor.

**Costs.** Two applications copying `fxkit` define two incompatible `HookSet`
types, and a third-party module cannot target either. That is the pluggability
the first decision was reaching for, and it is genuinely given up. What
replaces it is a checklist and a test, which are weaker — a copy that drops
obligation 1 compiles fine and is silently less safe. The bet is that the
number of people writing a shareable sqlb-fx module in the near term is zero,
and that the checklist is the right instrument until it is not.

**What this is not.** Not a claim that sqlb needs fx — the engine still knows
nothing about containers, `example/tasks` still assembles the same pieces with
a function and an argument, and `deps-check` still proves the engine takes pgx
alone. Not a retreat from the guarantees: the boot refusal, the `Migrated`
edge and the explicit middleware order are all still built and still asserted;
what changed is who owns the code that preserves them. Not codegen — a
generated `store.Module()` waits, as it did before, for these group names to
survive a real consumer.

## What would change our mind

- **A second author writes an sqlb-fx module** — an audit module, an OIDC
  module, anything meant to drop into someone else's module list. That is the
  buyer the contract never had, and it is the signal to publish `fxkit` after
  all. Extracting a module from an example is a mechanical change; the file
  layout here was kept close to what it was as `sqlbfx` for exactly that
  reason.
- **Two copies of `fxkit` drift into the same bug.** If the checklist turns
  out not to survive copying — if an adopter's copy logs the ADR-0030 error
  instead of returning it, and nobody notices for a release — then obligations
  written in prose were the wrong instrument and a type is the right one.
- **A non-fx consumer wants more than the principal seam.** If `Migrated` or
  the ordered-middleware idea shows up in a wire or a hand-wired application,
  the seam/assembly line was drawn in the wrong place and more of it belongs
  in the engine.
- **Codegen wants in.** When two schemas have hand-written the same
  `store.Module()` shape, `codegen.Options.FX` earns its place — and generated
  code needs a stable import path to generate *against*, which would force the
  publish question again on much better evidence than the first decision had.

## Cost of change

Low in both directions, which is the reason it was safe to be wrong once. The
glue is one directory of small files; publishing it later means adding a
`go.mod` and a tag, and un-publishing it (this change) meant a package rename
and a doc rewrite.

The expensive half is the principal seam, and it is now in the place that
makes it cheap to keep: hooks — application code, the part nobody regenerates —
spell `sqlb.PrincipalFrom` directly, so moving it again would cost every
consumer an import rewrite. It has the smallest signature that does the job,
in the module every consumer already imports, which is as settled as it can be
made.

## Revisions

- 2026-07-31 — Written, with `example/fxapp` as the prototype being promoted
  into a published module `sqlbfx`, and its port to the kit as the first proof.
- 2026-08-01 — **Reversed in part.** `sqlbfx` becomes `example/fxapp/fxkit`, a
  package of the example, and the principal seam moves into the engine as
  `sqlb.WithPrincipal` / `sqlb.PrincipalFrom`. Two of this record's own triggers
  fired, one of them immediately: the non-fx consumer of the seam turned out to
  already exist in `example/tasks/auth`, and the third-party module that would
  have bought the published contract turned out not to be coming — studio-apps/core
  will not import the kit. The general rule the two halves make — publish the
  seams, copy the assembly — is the part of this that generalises.
