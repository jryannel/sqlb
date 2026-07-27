# ADR-0018: Repository tooling operates on the files git tracks

- **Status:** Working
- **Confidence:** Medium
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

Parallel agent sessions check this repository out into `.claude/worktrees/`,
inside the repository directory, ignored via `.git/info/exclude`. Git therefore
does not see them — but a tool that walks the filesystem does, because none of
them read git's ignore rules.

`fmt-check` ran `gofmt -l .`. On 2026-07-27 it failed the gate for this
repository naming
`.claude/worktrees/rest-api-huma-chi-7c2b3e/rest/list.go` — a file belonging to
an unrelated session, while every tracked file was clean. The failure is
unattributable from where it appears: the person reading it has no edit that
explains it, and the named file does not exist in their working set.

The part that made this hard to see coming is that the repository already had
*two* definitions of "the tree" and only one of them was broken. Every other
gate reaches the code through the go tool's `./...`, which skips directories
whose name begins with a dot, or through `git archive`. So `vet`, `lint`,
`test-race`, `generate-check` and `bisect-check` were all immune — verified, and
verified in the weak configuration, with the sibling checkout's own `go.mod`
removed, so the result rests on the dot-prefix rule rather than on the module
boundary a real worktree incidentally also provides. That immunity is a
property of the go tool, not a decision this repository made, and it silently
covered for the absence of a rule until a check was written that did not go
through the go tool.

`fmt` was the same defect in a worse form. `gofmt -w .` does not merely report
on what it walks, it rewrites it, so running the repository's own formatter
would have reformatted a neighbouring session's checkout underneath whoever was
working in it.

## Decision

Tooling that operates on "the repository" takes its file list from
`git ls-files`, never from a filesystem walk.

What git tracks is the definition of this repository. Anything else in the
directory belongs to someone else — another session's checkout, a build
artefact, a scratch file — and is neither ours to check nor ours to rewrite.

The go tool's `./...` is an accepted equivalent where it applies, since it is
bounded by the module and skips dot-directories. But that is incidental
immunity, so a new check that relies on it demonstrates it rather than assuming
it, per [ADR-0016](0016-guards-proven-both-ways.md).

Scoping a check this way introduces two new failure modes that must be handled
explicitly, both instances of the rule in ADR-0016 that silence is not evidence
of cleanliness: an empty file list checks nothing and must fail, and the
underlying command's own failure empties the result and must be caught rather
than read as clean.

## Consequences

**What this buys.** The gate's idea of the repository matches git's, which is
the only definition it shares with CI. A failure names a file the reader can
act on. Nothing writes outside the tracked set, so the formatter cannot damage
a neighbouring worktree. And the rule holds regardless of where sessions put
their checkouts or what lands in the exclude file next.

**What this costs.** A file that has not been `git add`ed is outside the set, so
`mise run fmt` will not format it and any future tracked-files *check* would not
see it either. That is the price of using git's answer rather than the
filesystem's, and it is not theoretical: while this record was being written, an
untracked `introspect/` directory in the main worktree held a misformatted
`types.go`, which `lint` reported and a tracked-files scan passed over.

Note that exposure runs the *opposite* way to the failure this record exists to
fix — that file was inside the module and outside the index, where the original
was inside the filesystem walk and outside the module. The two together are the
argument for the rule rather than against it: neither the filesystem nor the
index is automatically the right set, so a tool's idea of the tree has to be
chosen deliberately and stated. It is also the reason formatting is gated by
`lint` and not by a tracked-files check; see Alternatives.

A tracked file deleted from the working tree makes the underlying command error,
failing loudly rather than passing on an empty list. That is the intended
direction, but it is a failure mode that did not exist before.

Each such task grows from one line to several, because the empty-list and
command-failure branches have to be written and exercised.

## What would change our mind

- **If a tracked Go file appears that `./...` does not cover** — a `testdata/`
  fixture, a `//go:build ignore` helper — its formatting is checked by nothing
  at all, since `lint` is now the only formatting gate. That is the condition
  that brings a tracked-files check back. Today all 59 tracked Go files sit in
  packages `go list ./...` reports, so lint's set is a strict superset; that was
  measured, not assumed, and it needs re-measuring whenever the package layout
  moves — the count was 47 a few hours before this record was committed.
- **If the `gofmt` formatter is ever disabled in `.golangci.yml`**, formatting
  silently stops being checked. There is no longer a second gate to notice. The
  file says so at the point of the edit, which is the cheapest place to put the
  warning, but this is the accepted cost of collapsing to one gate.
- **If worktrees move outside the repository directory**, the original
  motivation disappears. The rule should stay anyway: matching git's definition
  is what makes the local gate and CI agree, and that was true before worktrees
  existed.

## Cost of change

Low to reverse, and asymmetric in an unusual way. Putting a task back to a
filesystem walk is a one-line edit with no migration behind it.

The asymmetry is in the failure it reintroduces, not in the work. A gate that
walks the filesystem fails in *someone else's* session, naming a file the
reader has never touched — so the cost is not paid by whoever makes the change,
and lands on the person least equipped to attribute it. Cheap to undo; expensive
to diagnose having undone.

## Alternatives considered

**Prune the walk** — `find . -path ./.claude -prune -o -name '*.go' -print`.
Rejected because it hard-codes one directory name and gets the next one wrong:
any future ignored directory holding Go files repeats the bug exactly. Git
already maintains the answer; restating it by hand is how the two drift.

**Move the worktrees outside the repository.** Genuinely close, and it removes
the whole class of problem rather than this instance of it. Rejected because it
is not this repository's call — the path is chosen by the agent tooling, and a
repository that is only correct under one harness configuration is worse than
one that is correct regardless. `.git/info/exclude` already commits to them
living inside.

**Keep `fmt-check` alongside `lint`.** This is what the first version of this
record decided, and it was reversed the same day. The argument for keeping it
was that it runs in seconds where a full lint run does not, that its failure
names the fix rather than printing a diff, and that it does not depend on the
lint config keeping the `gofmt` formatter enabled.

The first two are ergonomics, not gating, and the third is an argument for
noting that the formatter is load-bearing — which `.golangci.yml` now does —
rather than for a second gate. What settled it is that `fmt-check` was not
merely redundant but strictly narrower: it saw tracked files, `lint` sees every
file in a package, and the difference is real rather than theoretical, since an
untracked misformatted file was sitting in the tree at the time and only `lint`
reported it. Two gates where the smaller one is a subset of the larger is not
redundancy worth paying for; it is a second thing to keep in step.

Formatting is therefore checked by the `gofmt` formatter inside `golangci-lint`
and nowhere else. That it fails on a formatting-only defect — a whitespace
misindent with no other finding in the file — was demonstrated before the gate
was removed, not assumed, per [ADR-0016](0016-guards-proven-both-ways.md).
`mise run fmt` remains, as the fix rather than the check.

## Revisions

- 2026-07-27 — `fmt-check` removed; formatting is now checked only by the
  `gofmt` formatter inside `golangci-lint`, which was demonstrated to fail on a
  formatting-only defect first. The record had argued for keeping it hours
  earlier, on grounds that turned out to be ergonomics rather than gating. The
  scoping rule itself is unchanged and still applies to `fmt`, which rewrites
  what it walks and so is the task where the original bug did real damage.
- 2026-07-27 — Written, after `gofmt -l .` walked into a parallel agent
  worktree and failed the gate naming a file from another session.
