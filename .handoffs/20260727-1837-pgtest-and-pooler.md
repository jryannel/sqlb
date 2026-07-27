# Handoff — pgtest-and-pooler  (2026-07-27 18:37)

Supersedes [`20260727-1626-migrate-diff-engine.md`](20260727-1626-migrate-diff-engine.md)
for current state. That one is still the better read on the migration diff
engine itself, and its "two mistakes made twice" section has not expired.

## Where
- Repo / worktree: `/Users/jryannel/dev/tmp/sqlb`
- Remote: https://github.com/jryannel/sqlb — **private**
- Branch: `main` @ `8b96428` · **0 unshipped · 0 uncommitted** · CI green
- 33 commits, 71 Go files (~19.8k lines), 19 ADRs, two Go modules

Everything is committed and pushed. No checkpoint was needed.

## Goal

A schema-first data layer for Go and Postgres (see the foundation handoff for
the full statement). This session did two things: **wrote down that PgBouncer is
in the connection path**, and **built the `pgtest` module** that turns claims
about Postgres from prose into tests.

The through-line: *this project's records are only worth their confidence
ratings if something checks them.* ADR-0014 had been sitting at Medium with the
reason stated in the record — its round-trip was manual and the scripts were
gone. That gap is now closed, and closing it immediately found a real bug.

## Done so far

Three commits, each green in isolation (`mise run bisect-check`):

- **`1c6b83d`** — **[ADR-0019](../docs/adr/0019-pgbouncer-in-the-path.md)**, and
  ADR-0012 edited in place. PgBouncer appeared nowhere in the codebase or the
  record, while three ADRs leaned on session-scoped behaviour without saying so.
  ADR-0012 said "woken by `LISTEN/NOTIFY`" and left the important half implied:
  the outbox row *is* the event and the notification carries nothing. That is
  what makes the design survive a pooler.
- **`ec55b85`** — the **`pgtest` module**: 947 lines, 9 tests, ~9s, wired into
  `mise run test-pg`, `mise run ci`, and its own CI job.
- **`8b96428`** — **`migrate.MinPostgres(major)`**, closing a gap the harness
  exposed rather than a test.

### What the tests found

**The two-module split was forced, not tidy.** `deps-check` runs `go list -deps`,
which **cannot see test-only imports** — measured, not assumed: a package whose
`_test.go` imports huma reports 0 third-party deps, and `-deps -test` reports 3.
So pgx in the root module's tests would have left the gate printing "standard
library only" forever. `deps-check` now also pins the root module's direct
requirements by name (`mise.toml:210`), proven to fire by adding pgx to go.mod.

**ADR-0014's predicted CHECK difference is real and is the only one.** Exactly
two changes, dropping and re-adding `views_non_negative`, because Postgres
normalises `CHECK ("views" >= 0)`. The fixpoint holds. The allowance is narrow:
only a drop whose name is re-added as a `CHECK` in the same diff — an unpaired
drop, a lost index or a dropped FK still fails.

**PgBouncer 1.24.1, transaction pooling, pgx v5.10 defaults:**

| Claim | Result |
|---|---|
| Query path works through the pooler | **Yes** — but only because the pooler defaults `max_prepared_statements` to **200** |
| `LISTEN` survives | **No** — and it is *accepted then silently useless*, not refused |
| `NOTIFY` survives | **Yes**, including inside a transaction |

The first is a deployment setting, not a property of pgx: on a pooler older than
1.21, or with `max_prepared_statements = 0`, **every query sqlb issues fails**.
The test asks the running pooler for that number rather than asserting it.

**A real bug, found by the harness rather than a test.** `schema.GenUUIDv7`
emits `uuid_generate_v7()` — the `pg_uuidv7` extension's spelling — so generated
DDL for a UUIDv7 primary key does not apply to a stock Postgres. The harness had
to shim it, which is a harness quietly hiding a finding.

`migrate.MinPostgres(18)` now emits Postgres 18's built-in `uuidv7()`. Two
things had to be right or the fix would have been worse than the gap:

- `renderDefault` has **two callers** — one renders DDL, one *compares* current
  against target. Both resolve against the same version, or adopting the option
  would generate a migration altering the default of every UUIDv7 column.
- `introspect` maps **both spellings** onto the same generator. Without it, a
  schema generated for 18 diffs against itself forever. Verified by removing the
  mapping and watching the fixpoint test fail.

## In progress / not finished

Nothing is half-done or broken. Unbuilt, in priority order:

1. **Rendering an imported registry back as `schema.go` source.** `introspect`
   reads, nothing writes the Go back out, so adoption is still half a loop. This
   is the highest-value next piece.
2. **The shadow database** that replays a migration history instead of reading a
   live schema. ADR-0014 prefers it over live introspection; it validates the
   history and catches drift, and `pgtest` now makes it cheap to build against.
3. **TypeScript client** from the generated OpenAPI.
4. **`?expand`** — the grammar validates relation names, no join is performed.
   `rest.Options.Expandable` must stay empty.
5. **Change feed** — ADR-0012, nothing built. ADR-0019's carve-outs stay
   untested-by-use until it exists.

## Next steps

1. **Render `schema.go` from an imported registry.** It is what makes adoption a
   complete loop, and `pgtest` can now assert the generated source compiles and
   re-imports identically.
2. When touching the DDL layer at all, **run `mise run test-pg`** — it is the
   only thing that will tell you the SQL is valid rather than merely expected.
3. **Settle the migration file format** before any generated migration is
   applied anywhere real. Still the most expensive thing in the record to change
   later, and still not settled.

## How to verify

```bash
mise run test           # inner loop; no Docker, no Postgres
mise run test-pg        # round trip + pooler; needs Docker, ~9s
mise run ci             # full gate, now includes test-pg
mise run bisect-check   # every commit green in isolation
```

`mise run ci` **now requires Docker**. `mise run test` is unchanged and stays
database-free, deliberately — see `pgtest/doc.go`.

## Open questions / decisions pending

- **ADR-0019 is Medium, not High, and cannot move yet.** The carve-outs are
  observed but *unused*: nothing connects direct because the dispatcher does not
  exist. Only building the change feed moves it. Do not raise it to make the
  table look better.
- **ADR-0014 is still Medium.** The DDL is now proven valid on every push, but
  the migrations have only ever been applied and reversed **by hand**, never on a
  table large enough for the lock hazards to be more than a comment. Automating
  *that* is the next real confidence step, and it is a much bigger job than the
  round trip was — it needs volume, not just a container.
- **Session vs transaction pooling was assumed, not confirmed.** ADR-0019 states
  the assumption in its first paragraph. If the deployment is actually session
  mode, most of that record evaporates.
- **`Unblock` is all-or-nothing** — unchanged from the last handoff.
- **`go.mod` is 1.25.7 while mise pins 1.26.4** — still deliberate, still worth
  a conscious confirm. `pgtest/go.mod` matches at 1.25.7.
- CI still warns that `actions/checkout@v4` and `jdx/mise-action@v2` target the
  deprecated Node 20. Carried from the last handoff; still not breaking anything.

## Key files & pointers

**The new module**
- `pgtest/doc.go` — **read this first.** Why it is a module of its own, and why
  there is deliberately no skip-when-Docker-is-absent path.
- `pgtest/main_test.go:112` — `freshStockDB`, a Postgres exactly as it ships;
  `:146` — `bootstrap`, the `uuid_generate_v7()` shim, and a comment saying
  plainly that the gap it papers over is real.
- `pgtest/pgbouncer_test.go:42` — `startPooler`. **One pooler for the run.** The
  first version started one per test and failed on a Docker API timeout.
- `pgtest/target_test.go:50` — `TestTheDefaultTargetStillNeedsTheExtension`. The
  control that stops a fix-that-fixes-nothing passing silently.
- `pgtest/target_test.go:87` — the PG18 fixpoint. Verified to fail without the
  `introspect` mapping.
- `pgtest/pgbouncer_test.go:230` — the `LISTEN` asymmetry, with both halves
  asserted against the same database so a timeout cannot mean "never sent".

**The version option**
- `migrate/target.go:44` — `MinPostgres`; `:56` — `builtinDefaults`, one entry
  on purpose; `:69` — `resolve`, which only rewrites exact matches.
- `migrate/ddl.go:216` — `renderDefault`, and **the comment about its two
  callers**. Read it before changing anything here.
- `migrate/diff.go:91` — `Diff`, now variadic. The option is here and **not** on
  `Options`, which reaches `Render`/`Write` too late to matter.
- `introspect/types.go:93` — both spellings, one generator.

**Records**
- `docs/adr/0019-pgbouncer-in-the-path.md` — the measured results table.
- `docs/adr/0014-migrations-and-import.md` — still the single most useful thing
  to read first for migrations. Two new revision entries at the end.
- `docs/adr/0016-guards-proven-both-ways.md` — read before adding any CI guard.
  Two guards were proven both ways this session; do the same.
- `mise.toml:210` — the direct-dependency pin that keeps `pgtest` honest.

### Worth not learning the hard way again

- **A harness that shims something is hiding a finding.** The UUIDv7 gap existed
  for the whole project and no test saw it, because the only thing that would
  have noticed was a database without the extension. When you find yourself
  bootstrapping the environment so the code under test works, ask what that says
  about the code.
- **`go list -deps` does not see test imports.** Any guard reasoning about
  dependencies has this hole. It is why `pgtest` is a separate module and not a
  build tag.
- **A function used for both rendering and comparing is a trap.** `renderDefault`
  is now version-aware and used for both; they must resolve identically. The same
  shape already had a warning on it at `migrate/ddl.go` — `rewriteIdents`, safe
  only because its result is compared and never rendered.
