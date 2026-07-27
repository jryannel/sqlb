# ADR-0016: A guard is not trusted until it has failed on purpose

- **Status:** Working
- **Confidence:** High
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

Three guards written in this repository reported success while checking
nothing. All three were written deliberately, by someone thinking about
correctness at the time, and all three looked right on review:

- `deps-check`, first version: grepped package paths for a dot and matched the
  standard library's own vendored `golang.org/x/net` packages, so its filter
  removed everything and the result was always empty.
- `deps-check`, second version: let `go list -m all` fail to stderr, leaving the
  captured output empty, which read as "no dependencies".
- `bisect-check`: ran under mise's `set -e`, so the first commit — which
  legitimately has no Go packages — killed the script before it printed
  anything. It reported a broken history that was entirely green.

The common shape is that a guard's failure path is exercised far less than its
success path. A test is run every time it passes; a guard's *failing* branch may
never run at all until the day it matters, and by then nobody is watching it
closely enough to notice it said nothing useful.

A guard that cannot fail is worse than no guard, because it reports safety it
never checked. Absent tooling prompts caution; broken tooling prevents it.

## Decision

A guard is not trusted until it has been observed failing on purpose.

Before a check joins `ci`, demonstrate both directions: it passes on a clean
tree, and it fails — with a message that names the problem — on a tree
deliberately broken in the way it exists to catch. Restore the tree afterwards.

Where the broken state is cheap to construct, prefer a test that constructs it,
so the failing branch is exercised on every run rather than once by hand. The
`migrate` tests do this for the destructive-change guard; `codegen.Check` is
covered the same way.

Two smaller rules follow from the specific failures above:

- A command whose own failure would empty the result must have its exit status
  checked. Silence is not evidence of cleanliness.
- Under `set -e`, an expected failure must be guarded by `if`, not by reading
  `$?` afterwards.

## Consequences

**What this buys.** The failing branch of every guard has run at least once,
under conditions someone chose. That is the only evidence that a green CI means
anything.

**What this costs.** Adding a check takes longer, and the demonstration is
manual unless the broken state is cheap enough to construct in a test. Some
guards — the bisect check, anything needing a specific repository shape — are
awkward to fake, so the proof stays a one-off rather than a regression test.

## What would change our mind

- If a fourth guard is found silently passing, the manual demonstration is not
  working and guards need a shared harness that constructs the failure for them.
- If the demonstration step becomes a bottleneck on adding checks, that is worth
  knowing, but the answer is probably to make failures cheaper to construct
  rather than to skip the proof.

## Cost of change

Low. This is a working practice, not a structure: dropping it costs nothing
mechanically and leaves the existing guards in place. What it costs is
confidence, and only gradually — a guard that rots into uselessness is invisible
by construction, which is precisely why the practice exists.

## Alternatives considered

**Trust review.** Rejected on evidence: all three failures survived being
written carefully and read afterwards. The bug is never in the part being
looked at.

**Mutation testing for guards.** The rigorous version — break the tree
automatically and assert every guard notices. Attractive, and probably where
this goes if the manual step proves unreliable. Not built, because three data
points do not yet justify a framework.

## Revisions

- 2026-07-27 — Written, after the third guard in one session was found
  reporting success while checking nothing.
