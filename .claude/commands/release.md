---
description: Cut a release — the releases page, then an annotated tag whose message is the notes, then the GitHub release carrying the same text
argument-hint: <version, e.g. v0.11.0 — omit to be told what the next one should be>
disable-model-invocation: true
allowed-tools: Read, Write, Edit, Glob, Grep, Bash(git log:*), Bash(git show:*), Bash(git tag:*), Bash(git rev-list:*), Bash(git rev-parse:*), Bash(git status:*), Bash(git diff:*), Bash(git fetch:*), Bash(git push:*), Bash(gh run list:*), Bash(gh release create:*), Bash(gh release list:*), Bash(gh issue list:*), Bash(gh issue view:*), Bash(mise run site-check)
---

Cut release `$ARGUMENTS`.

A release here is three artifacts that must carry the same text: an entry on
[`docs/releases.md`](../../docs/releases.md), an **annotated tag whose message
*is* the notes**, and a GitHub release. `CLAUDE.md` states the constraint — both
workflows green on the commit being tagged — and the ordering below is what
makes that achievable rather than lucky.

## The ordering, and why it is not arbitrary

**The releases-page commit is the commit you tag.** `pages` triggers only on
pushes to `main` touching `site/**`, `docs/**` or its own workflow file, so a
commit that changes no published path has no `pages` run at all — and a tag on
such a commit can never show the green you are claiming. v0.8.0 and v0.10.0 tag
`docs: … on the releases page`; that is the shape.

So: write the page, land it, let both workflows finish on that commit, tag
*that* commit. Not the feature commit, not `HEAD` at the moment you happen to
be ready.

## Steps

1. **Establish where you are.** On `main`, clean tree, in sync with origin:

   ```bash
   git fetch origin --tags && git status --short && git log --oneline origin/main -1
   ```

   If the tree is dirty or `main` has moved, stop and say so. A release from a
   tree you have not seen is the failure this whole command exists to prevent.

2. **Settle the version.** Highest existing tag plus one minor, unless the
   changes say otherwise:

   ```bash
   git tag --sort=-v:refname | head -3
   ```

   Pre-1.0, a minor may break a surface listed under *Will move* in
   [`docs/compatibility.md`](../../docs/compatibility.md). **Read that file
   before deciding.** If a break is not listed there, the break is the problem,
   not the version number — stop and raise it. `docs/release-1.0.md` says what
   has to be true before the promise becomes permanent.

3. **Gather the material, in this order** — the last of the three is the one
   that carries reasoning nothing else does:

   ```bash
   git log --oneline $(git describe --tags --abbrev=0)..HEAD
   gh issue list --state closed --limit 40 --json number,title,closedAt
   git log $(git describe --tags --abbrev=0)..HEAD -- docs/adr/
   ```

   Read the **commit bodies**, not just the subjects. `CLAUDE.md`: the body says
   what was wrong, what was decided, and what was deliberately not done, and
   `git log` is the design record ADRs summarise. Notes written from subjects
   alone read as a changelog, which is the thing this format is not.

4. **Draft the notes.** One text, used three times. The house shape, from the
   tags already written:

   - **Open with the claim the release makes** — what became true, in the
     repo's voice. `git show v0.10.0` is the reference.
   - **Every break gets `BREAKING:` and a mechanical edit.** Name the failure a
     reader would actually have seen — the error text, where it surfaces, why
     the gates were green anyway — then the edit that fixes it and which
     direction is safe. A break without its edit is an incident.
   - **Say what was deliberately not done,** and let a rejected alternative keep
     its objection.
   - **Do not invent a theme.** If the release is four unrelated fixes, it is
     four unrelated fixes. Inventing a rhyme is the failure mode with the worst
     ratio of plausibility to truth.

5. **Show me the draft and stop.** Do not tag, push or publish anything until I
   have read it. This is the only judgement step in the command and it is the
   one worth interrupting for.

6. **Write the releases page.** New section at the top of `docs/releases.md`,
   newest first: `## <version>`, the date, a `[tag]` link, then the notes marked
   up for the web — the same text, with code spans and links to
   `compatibility.md#will-move` where a break is named. Then:

   ```bash
   mise run site-check
   ```

   Needs no `npm install`, and catches the dead link whose target moved. A
   release that breaks the site is a release that breaks the page describing it.

7. **Land it and wait.** Commit as `docs: <version> on the releases page`, land
   it on `main` (`/git-ship`), and **wait for both workflows on the merged
   commit**:

   ```bash
   gh run list --commit $(git rev-parse origin/main)
   ```

   `gh pr checks` is half the answer here and always will be — `pages` does not
   run on pull requests. Both must read `success` before step 8.

8. **Tag that commit, and push the tag.**

   ```bash
   git tag -a <version> $(git rev-parse origin/main) -F <notes-file>
   git push origin <version>
   ```

   The tag message is the notes, plain text, no markup. It is immutable once
   pushed, which is the whole reason the releases page quotes it rather than
   maintaining its own changelog.

   The release gate hook checks both workflows here and will refuse if either is
   red, absent, or unfinished. If it refuses, it is right — read the reason
   rather than reaching for `[skip-release-gate]`.

9. **Publish the GitHub release** with the same text:

   ```bash
   gh release create <version> --title <version> --notes-file <notes-file>
   ```

10. **Report:** the version, the tagged SHA, both run URLs, the release URL, and
    anything you had to decide yourself under
    `## Open questions I had to answer myself`.

## What stops a release

Stop and ask rather than working around any of these:

- A break not listed under *Will move* in `compatibility.md`.
- `mise run site-check` red — the page describing the release cannot publish.
- Either workflow red, missing or still running on the commit to be tagged.
- A commit body you could not find a reason in. Notes that reconstruct a
  plausible reason are worse than notes with a gap, because they read as
  settled and get quoted back.

## Not this command's job

Backfilling a missing GitHub release for an already-pushed tag, and re-cutting a
release that went out wrong. Both are recoveries with their own hazards — a tag
is immutable once anyone has fetched it — and neither is worth automating from
one occurrence.
