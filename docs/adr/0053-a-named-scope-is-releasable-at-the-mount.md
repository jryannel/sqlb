# ADR-0053: A named scope is releasable at the mount, and only a named one

- **Status:** Working — the engine applies it, `rest.Options.Unscoped` selects
  it, and the obligation check refuses a resource that released everything
- **Confidence:** High that the asymmetry is the right shape; Medium on the
  spelling, and on scope names being a registry-wide namespace rather than a
  per-model one
- **Decided:** 2026-08-09
- **Last reviewed:** 2026-08-09

## Context

[ADR-0050](0050-reachability-is-a-property-of-the-mount.md) settled which
*columns* a mount may reach, and closed
[#148](https://github.com/jryannel/sqlb/issues/148). Its own doc comment scoped
the promise honestly: what `Columns` does not do is generate the second
resource, and *"the alternative, a second model over the same table, gives up
all four"* — the model, the typed column facade, the manifest, and the drift
gate.

The public/admin split still had to take that alternative, because the two
surfaces differ in **rows** as well. The storefront reads published, unarchived
products; the admin panel exists to read the drafts. Row visibility is a
`BeforeQuery` hook, hooks are keyed by the model's Go type — `Registry` is a
`reflect.Type → *Hooks[T]` map — and `BeforeQuery` reaches every statement whose
subject is `T`, including the ones an `?expand` issues. So a rule registered to
confine the storefront confines the admin panel too, and the only way out was a
second Go type over the same table, which is exactly the alternative that gives
up all four ([#177](https://github.com/jryannel/sqlb/issues/177)).

That is ADR-0050's problem one level down. `Hidden` was a property of the model
that needed to be a property of the mount; a `BeforeQuery` scope is a property
of the model in the same way.

**The reason this was not simply a missing knob.** The global reach is the
feature. One registration confines the generated handlers, a hand-written facet
endpoint, and every query a checkout issues, and
[ADR-0030](0030-declared-scope-is-required.md) refuses to mount a `Scoped` model
that no hook confines. A `rest.Options.Scope []sqlb.Pred` that a mount simply
omits would give the escape hatch the same ergonomics as the confinement, and
put "the surface that forgot" back on the table. ADR-0030 anticipated exactly
this and declined it: *"an unused escape hatch is the thing most likely to be
reached for reflexively"*. What it was waiting for was a real case, which #177
is.

## Decision

**A rule registered under a name may be released by that name. A rule
registered without one may not, by anybody.**

```go
sqlb.On[Product](reg).Scope("storefront").BeforeQuery(publishedOnly)

storefront := sqlb.New(pool).WithHooks(reg)
admin      := storefront.WithoutScope("storefront")
```

and at a mount, `rest.Options{Unscoped: []string{"storefront"}}`.

Three properties carry the design.

**Naming is what makes a rule negotiable.** `Hooks.BeforeQuery` is unchanged and
absolute — every existing registration in every codebase stays unreleasable, and
the short spelling stays the safe one. The author of a rule decides whether it
can be escaped, by choosing how to spell it, and that decision sits next to the
rule rather than at the mount that would like to be out of it.

**The obligation check runs after the release, not before.** `rest.Resource`
derives the released handle first and checks obligations against the handle it
will actually serve from, so a `Scoped` model whose every confining rule a
resource released has nothing confining it and does not mount. Release one of
two and the other still counts. This is the property that makes the hatch
something other than the flag ADR-0030 declined: it does not get a resource past
the check, it changes which registrations the check can see.

**A scope name is a property of the registry, not of a type.** "A shopper sees
the published catalog" is one rule over products, variants, categories and
collections. Registering it under one name on four models means a handle
releases it once and the release reaches all four, including the models a
request arrives at through `?expand`, whose hooks run requalified onto the join
alias. A per-model namespace would have made the admin handle name the same rule
four times and quietly serve draft rows the day a fifth table joined it.

`BeforeCreate` is deliberately not releasable. It stamps a row on the way in
rather than confining a set, so there is nothing for a reader to be released
from, and a create that skipped it would write a row with no tenant rather than
see more of them.

## Consequences

**Buys.** The public/admin pair is generated on both sides. The admin resource
is a second `rest.Resource` over the same generated model, so the model, the
typed column facade, the manifest and the drift gate cover both halves —
`Columns` for the disclosure difference, `Unscoped` for the row difference. In
the shop that prompted it, that is a hand-written package of ~1,300 lines, ~380
of which reimplement the drift gate by reflection because the hand-written half
does not get one.

**Costs.** A second way to spell a hook, and a reader of a registration now has
to look at whether it is named to know whether some mount is out of it.
`Registry.ScopeNames` and `DB.Released` exist so that the answer is
enumerable rather than a grep. The refusals are startup errors, which is the
right place, but they are two more ways a mount can fail to come up.

**Where the boundary is not held.** `sqlb.Query[T]()` against a released handle
in application code is released, exactly as it is unscoped against a bare pool
today — ADR-0030's "the check is at the REST mount, not on the query path" is
unchanged. `WithoutScope` does not itself refuse an unregistered name, because a
registry may gain registrations after a handle is built; `rest` refuses it,
where the whole registry is known.

## What would change our mind

- **People name every scope**, so that nothing is absolute and the asymmetry
  stops meaning anything. The tell is a codebase where `BeforeQuery` no longer
  appears unnamed. The answer would be a registry-level assertion that a
  declared-`Scoped` model retains at least one unnamed rule, not a relaxation.
- **A release wants to be narrower than a whole rule** — "the admin sees drafts
  but still not archived rows" is today two scopes, and if that decomposition
  turns out to be the common case, the unit is wrong and the next step is
  releasing a named *predicate* rather than a named registration.
- **Scope names collide across modules** in a multi-registry deployment. They
  are registry-wide, so two modules that both say `"tenant"` are one rule for
  release purposes. That is the intended behaviour for one application and the
  wrong one for a library that ships hooks; a prefix convention would be the
  cheap fix, a per-module namespace the expensive one.
- **The manifest should carry it.** A mount that releases a rule is a fact about
  the surface, and today it is visible in Go and in the startup error and
  nowhere a contract diff would see it. If a surface changes what it releases
  between versions, `restcompat` should probably say so.

## Cost of change

**The engine half is cheap to remove** — the registration name is one field on
one struct, and unnamed hooks behave exactly as they did. **`Options.Unscoped`
is awkward to remove once schemas are authored against it**, in the way every
capability is: a resource that relies on a release has no other spelling for
what it does. **The check ordering in `Resource` is the expensive thing to get
wrong rather than to change** — releasing before the obligation check is what
this record is really deciding, and a refactor that moved the release later
would restore the hole silently. It is guarded by a test that has been seen to
fail.

## Revisions

- 2026-08-09 — Written. Prompted by #177, itself the row half of the split
  ADR-0050 closed the column half of. The first version of the guarding test
  passed with the release deliberately hidden from the obligation check, because
  a resource exposing all of CRUD has four obligations and any one of them
  satisfied the assertion; it now exposes one operation at a time.
