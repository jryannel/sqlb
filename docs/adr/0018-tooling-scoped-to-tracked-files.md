# ADR-0018: Repository tooling operates on the files git tracks

- **Status:** Working
- **Confidence:** Medium
- **Decided:** 2026-07-27
- **Last reviewed:** 2026-07-27

## Context

Parallel agent sessions check this repository out into `.claude/worktrees/`,
inside the repository directory and ignored via `.git/info/exclude`. Git does not
see them; a tool that walks the filesystem does.

`fmt-check` ran `gofmt -l .` and failed the gate naming a file belonging to an
unrelated session, while every tracked file was clean — a failure the reader
cannot attribute or act on. `mise run fmt` was the same defect in a worse form:
`gofmt -w .` would have rewritten a neighbouring session's checkout underneath
whoever was working in it.

Every other gate reaches the code through `./...`, which skips dot-directories,
or through `git archive`. That immunity is a property of the go tool, not a
decision this repository made, and it silently covered for the absence of a rule.

## Decision

Tooling that operates on "the repository" takes its file list from
`git ls-files`, never from a filesystem walk. What git tracks is the definition
of this repository; anything else in the directory belongs to someone else.

`./...` is an accepted equivalent where it applies, but that is incidental
immunity, so a check relying on it demonstrates it rather than assuming it
([ADR-0016](0016-guards-proven-both-ways.md)).

Scoping this way adds two failure modes that must be handled explicitly: an empty
file list checks nothing and must fail, and the underlying command's own failure
empties the result and must be caught rather than read as clean.

Formatting is checked by the `gofmt` formatter inside `golangci-lint` and nowhere
else. `fmt-check` was strictly narrower — tracked files versus every file in a
package — so it was a second thing to keep in step rather than a second gate.
`mise run fmt` remains as the fix, not the check.

## Consequences

**Buys.** The gate's idea of the repository matches git's, which is the only
definition it shares with CI. A failure names a file the reader can act on.
Nothing writes outside the tracked set, so the formatter cannot damage a
neighbouring worktree.

**Costs.** A file that has not been `git add`ed is outside the set. Not
theoretical: an untracked `introspect/types.go` was misformatted while this
record was being written, and only `lint` reported it. That exposure runs the
opposite way to the original bug — inside the module and outside the index, where
the original was inside the walk and outside the module. Neither the filesystem
nor the index is automatically the right set, which is the argument for choosing
deliberately. Each task also grows from one line to several.

## What would change our mind

- A tracked Go file appears that `./...` does not cover — a `testdata/` fixture,
  a `//go:build ignore` helper. Its formatting would be checked by nothing, and
  that brings a tracked-files check back. All 59 tracked Go files sit in packages
  `go list ./...` reports today; that was measured, and needs re-measuring when
  the layout moves.
- The `gofmt` formatter is disabled in `.golangci.yml` — formatting silently
  stops being checked, with no second gate to notice.
- Worktrees move outside the repository directory — the motivation disappears,
  but the rule should stay: matching git's definition is what makes the local
  gate and CI agree.

## Cost of change

Low to reverse — a one-line edit. The asymmetry is in the failure it
reintroduces: a filesystem walk fails in *someone else's* session, naming a file
the reader has never touched. Cheap to undo; expensive to diagnose having undone.

## Revisions

- 2026-07-27 — Written, after `gofmt -l .` walked into a parallel agent worktree
  and failed the gate naming a file from another session.
- 2026-07-27 — `fmt-check` removed in favour of lint's `gofmt` formatter, after
  demonstrating it fails on a formatting-only defect. The scoping rule is
  unchanged and still applies to `fmt`, which rewrites what it walks.
- 2026-07-30 — Condensed.
