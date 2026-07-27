# Compatibility

What `v0.1.0` promises, and what it deliberately does not.

sqlb is pre-1.0 and has one author, no tags before this one, and no observed
consumers. That is the honest starting position, and it is the reason this
document exists: an unreleased `main` reads as unknown risk, whereas a tag with
a stated blast radius is something a reader can decide against.

Semantic versioning applies from `v1.0.0`. Until then a minor bump may break a
surface listed under **Will move**, and each break is described in the release
notes with the mechanical edit that fixes it.

## Frozen

These are the surfaces worth freezing early, because they are the ones other
code and other *systems* couple to. Breaking them would invalidate stored data
or deployed clients, not just call sites.

- **`Executor`** — `QueryContext` and `ExecContext`, and nothing more. Every
  wrapper, tracer and pool adapter written against it stays valid. Widening this
  interface would break every implementation at once, so it grows by adding
  optional interfaces that are type-asserted for, never by adding methods.
- **The filter grammar** — the URL syntax (`?status=eq.draft`, `?order=`,
  `?select=`, `?limit=`) and its operator names. This is a wire format: a
  deployed client or an agent driving the API off `sqlb.json` has requests built
  against it. New operators are additive; existing spellings do not change
  meaning.
- **The generated DDL's shape** — `migrate.Diff` output for a given pair of
  schemas may improve, but a migration already written and applied is never
  reinterpreted.

## Will move

Named in advance, so the break is a documented plan rather than a surprise.

- **Hook registration.** `sqlb.On[T]()` reaches a process-global registry
  ([ADR-0008](adr/0008-hooks-as-domain-seam.md)). When the transaction-scoped
  handle lands it becomes a wrapper over a default registry, and hooks gain a
  way to be scoped to a unit of work. Registrations written today keep
  compiling; what changes is that a second registry becomes possible, and
  `Reset`-based test isolation stops being the only option.
- **Terminal call signatures**, when Go 1.27 arrives. `sqlb.Collect[R](ctx, db,
  b)`, `filter.Apply(b, q)` and the `db` threaded through every terminal call
  all gain method forms. The README's *Go 1.27 generic methods* section has the
  table. These are additive — the functions stay.
- **`?expand`.** Declaring it is currently refused at startup rather than
  accepted and ignored. When the joins land, the refusal is removed and the
  capability starts working. Nothing that compiles today stops compiling.

## Not covered

Anything under `introspect`, `migrate`, `codegen` or `pgtest` that is reached
only from a build step or a test. These are tools, not a runtime surface, and
they change with less ceremony.
