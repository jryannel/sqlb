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

- ~~**Hook registration.**~~ Landed after `v0.1.0`, and it broke nothing:
  `sqlb.On[T]()` is now a wrapper over a process-default `Registry`, and
  `OnIn[T](r)` reaches a scoped one
  ([ADR-0020](adr/0020-transaction-scoped-handle.md)). Registrations written
  against `v0.1.0` compile and behave identically. The one behavioural subtlety
  worth knowing: which registry a statement uses is decided by the *dynamic
  type* of the executor passed to it, so passing a raw `*sql.DB` where a scoped
  `*sqlb.DB` was meant silently uses the default.
- **Terminal call signatures**, when Go 1.27 arrives. `sqlb.Collect[R](ctx, db,
  b)`, `filter.Apply(b, q)` and the `db` threaded through every terminal call
  all gain method forms. The README's *Go 1.27 generic methods* section has the
  table. These are additive — the functions stay.
- **Nested `?expand`.** One level resolves today. If nesting lands it arrives as
  a longer name — `?expand=list.workspace` — under a depth limit, so nothing a
  request can send today changes meaning.

## Not covered

Anything under `introspect`, `migrate`, `codegen` or `pgtest` that is reached
only from a build step or a test. These are tools, not a runtime surface, and
they change with less ceremony.
