# ADR-0047: There is no default hook registry, and the short name takes the registry

- **Status:** Working — the default registry is deleted, `On[T](r)` is the only
  registration form, and every example, test and guide in the repository names
  a registry. `mise run ci` is green.
- **Confidence:** High
- **Decided:** 2026-08-01
- **Last reviewed:** 2026-08-01

## Context

Hooks are the rules that confine what a query may see. `BeforeQuery` is how a
tenant boundary stops being something every call site has to remember
([ADR-0008](0008-hooks-as-domain-seam.md)), and `Scoped` turns forgetting
one into a refused mount rather than a leak
([ADR-0030](0030-declared-scope-is-required.md)). They are the most
security-relevant surface this library has.

Until now they were also the surface with ambient state at its centre:

- `sqlb.On[T]()` registered into a package-level `defaultRegistry`.
- `sqlb.New(exec)` handed every handle that same registry.
- `OnIn[T](r)` — the form that names where the rules land — carried the longer
  name, which is a recommendation written the wrong way round.
- `rest.PublishChanges[T](p)` / `rest.PublishChangesIn[T](r, p)` repeated the
  pair exactly.

[compatibility.md](../compatibility.md) already named the hazard under *Will
move*: "which registry a statement uses is decided by the dynamic type of the
executor passed to it, so passing a raw pool where a scoped handle was meant
silently uses the default."

Three things then happened that turned a known rough edge into a decision.

**An adopter's tenant boundary silently switched off.** A codebase moving from
the default registry to a per-application one built from a DI value group left
one module still calling `sqlb.On[T]()`. That module's rules were no longer on
the handle it queried through. It compiled, it mounted, it answered — with
every tenant's rows. Only a test asserting the boundary from outside caught it.
Nothing in the API could have: both spellings were valid, and the one that was
wrong was the shorter, more obvious one.

**The examples had all already left.** `example/tasks` built its own registry
and said why — two servers in one test binary otherwise stacked each other's
predicates. `example/fxapp` did the same through its value group. The default
was what the documentation stepped around, not what it recommended.

**The bookkeeping was visible in the tests.** `Hooks.Reset`, `sync.Once` guards,
`t.Cleanup(func() { On[T]().Reset() })`, and "has anything registered yet?"
checks before mounting — every one of them existed to manage a registry nobody
had asked for.

## Decision

**Delete the default registry.** `New(exec)` gives each handle an empty
`Registry` of its own. Two calls to `New` produce two handles with nothing
between them.

**The short name takes the registry.** `On[T](r)` is the only registration
form; `OnIn` is gone. Likewise `rest.PublishChanges[T](r, p)`, and
`PublishChangesIn` is gone. This is the same argument
[ADR-0044](0044-the-container-is-an-adapter.md) made for `Unscoped` being a
type rather than a flag: the default spelling should be the safe one, and the
thing being controlled should not be reachable by omission.

**A bare Executor resolves to a registry nothing can register into.** An
`Executor` that is not a `*DB` — a raw pool, a borrowed `pgx.Tx` — has no rules.
Saying "no rules" is honest for a handle-less statement, and it is visible at
the call site: the alternative is a handle, and a handle carries its rules.

**`Hooks.Reset` stays, with its reason rewritten.** It no longer exists to undo
process-wide state; a test gets isolation from `NewRegistry`, which cannot be
forgotten in a teardown.

## Consequences

**Every registration is a compile error until it names a registry.** `On[T]()`
fails with "not enough arguments" and `OnIn` with "undefined". That is the
point: the failure this ADR exists to prevent is silent, so its migration must
not be. There is no deprecation window and no shim — a shim would be the
ambient registry under a new name.

**Registering hooks and building a handle stop being independent statements.**
`New(pool)` after a registration no longer picks it up; the registry has to be
named with `WithHooks`. In exchange, "these are the rules in force" becomes a
property of how the handle was assembled rather than of what ran first.

**Bare-executor statements lose rules they used to inherit.** A query issued
against a raw pool while a global registration existed was confined by rules
its call site never mentioned. Those are now unconfined. This reads like a
weakening and is the opposite: that confinement was invisible, unreliable, and
already absent whenever the handle was scoped. Models that must not be read
unconfined declare `Scoped`, and ADR-0030 refuses to mount them without a hook.

**Two applications in one process are independent by construction**, which is
what makes a test binary with two servers in it correct rather than lucky.

**The value-group pattern in `example/fxapp/fxkit` becomes the normal shape**
rather than the careful one, since there is no longer a shorter path that
appears to work. That kit already built its registry this way, which is part of
the evidence: the assembly people are told to copy
([ADR-0044](0044-the-container-is-an-adapter.md)) had never used the default.

**One silent failure survives, in a smaller form: a registry nothing attaches.**
`On[T](reg)` compiles whether or not any handle ever carries `reg`, so hooks
can still be registered into a registry that never runs them. Migrating this
repository hit it three times — a test that built a registry and then mounted
against the bare executor — and each time the compiler was content and a test
caught it.

It is strictly narrower than what it replaces. The old failure was action at a
distance: rules registered correctly, on the registry the library recommended,
silently not applying because a handle somewhere else resolved differently. The
new one is local — the registry and the handle are two expressions usually
within a few lines of each other, and the fix is visible at the point of the
mistake. It is also caught at the mount for the case that matters: a model
declaring `Scoped` is refused when the handle's registry has no hook for it
([ADR-0030](0030-declared-scope-is-required.md)), which is exactly the
tenant-boundary failure this record was written about.

Closing it completely would mean tracking whether a `Registry` was ever handed
to `WithHooks` and complaining if not — a lifetime question a library cannot
answer, since a registry may legitimately be built long before its handle. Left
open deliberately, and named here so it is not rediscovered as a surprise.

## What would change our mind

**An ergonomic cost that shows up in real adoption rather than in a hello-world.**
The bet is that naming a registry costs one line at startup and nothing
thereafter. If adopters end up threading a `*Registry` through call stacks that
have no other reason to know about one, that is a signal the seam is in the
wrong place — though the answer would be a better way to reach the handle, not
a return of ambient state.

**Evidence that bare-executor statements were load-bearing.** If real code
depended on a raw pool inheriting global rules — rather than that being the
silent failure described above — the fix would be to make a bare executor
*refuse* a model that declares `Scoped`, not to restore the default.

## Cost of change

**Reversing this is cheap in code and expensive in trust.** Restoring a default
registry is a package-level variable and a one-argument overload; a few hours.
What it would cost is the property that makes the current design worth
anything: that the rules in force on a handle can be read off the code that
built it. Anyone who has since deleted a `Reset` or a `sync.Once` would get the
leak back without a compile error, which is the same silent failure in the
opposite direction.

**Having done it late is the real bill.** This broke every registration call
site in one edit. Done before `v0.1.0` it would have broken none. The cost of
the delay was paid by an adopter whose tenant boundary switched off silently,
which is the most expensive way to learn that a default was wrong.

## Alternatives considered

**Keep the default, invert only the names.** `On[T](r)` for the safe form,
`OnDefault[T]()` for the ambient one. Rejected: the failure mode is not that
the ambient form is easy to type, it is that ambient state exists at all. A
handle could still acquire rules nothing attached to it.

**Deprecate with a shim that panics on second use.** Rejected: a registry that
works once and then panics is a third behaviour to reason about, and the
adopter this was written for would have hit the panic in production rather than
at compile time.

**Leave it, and document harder.** Rejected on evidence. It *was* documented —
in compatibility.md, under a heading that said it would move — and the boundary
still switched off silently in a codebase whose author had read that page.
