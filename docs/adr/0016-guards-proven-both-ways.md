# ADR-0016: A guard is not trusted until it has failed on purpose

- **Status:** Working
- **Confidence:** High
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

Three guards in this repository reported success while checking nothing. All
three were written deliberately and looked right on review:

- `deps-check` v1 grepped package paths for a dot, matched the standard library's
  own vendored `golang.org/x/net`, and filtered everything away.
- `deps-check` v2 let `go list -m all` fail to stderr, so empty output read as
  "no dependencies".
- `bisect-check` ran under `set -e`, so the first commit — legitimately without
  Go packages — killed the script before it printed anything.

A guard's failure path is exercised far less than its success path, so it may
never run until the day it matters. A guard that cannot fail is worse than no
guard: absent tooling prompts caution, broken tooling prevents it.

## Decision

A guard is not trusted until it has been observed failing on purpose. Before a
check joins `ci`, demonstrate both directions: it passes on a clean tree, and it
fails — with a message naming the problem — on a tree deliberately broken in the
way it exists to catch.

Where the broken state is cheap to construct, prefer a test that constructs it,
so the failing branch runs every time. The `migrate` tests do this for the
destructive-change guard; `codegen.Check` is covered the same way.

Two rules follow from the specific failures above:

- A command whose own failure would empty the result must have its exit status
  checked. Silence is not evidence of cleanliness.
- Under `set -e`, an expected failure must be guarded by `if`, not by reading
  `$?` afterwards.

## Consequences

**Buys.** The failing branch of every guard has run at least once under
conditions someone chose. That is the only evidence a green CI means anything.

**Costs.** Adding a check takes longer, and the demonstration is manual unless
the broken state is cheap to construct. Some guards — the bisect check, anything
needing a specific repository shape — stay a one-off proof rather than a
regression test.

## What would change our mind

- A fourth guard is found silently passing — the manual demonstration is not
  working, and guards need a shared harness that constructs the failure for them.
- The demonstration becomes a bottleneck on adding checks — make failures cheaper
  to construct rather than skip the proof.

## Cost of change

Low mechanically — it is a practice, not a structure. What dropping it costs is
confidence, and only gradually: a guard that rots into uselessness is invisible
by construction, which is why the practice exists.

## Revisions

- 2026-07-27 — Written, after the third guard in one session was found reporting
  success while checking nothing.
- 2026-07-30 — Condensed.
