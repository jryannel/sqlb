#!/usr/bin/env bash
# PreToolUse hook — refuse to tag, push a tag, or publish a release while either
# workflow is red on the commit being released.
#
# Why a hook and not a sentence in CLAUDE.md: the sentence is already there, in
# bold, and it did not hold. `ci` and `pages` are separate workflows and `pages`
# runs only on pushes to main, so a pull request shows one of the two — which is
# why `gh pr checks` reads green on a commit whose site build is broken. v0.7.0
# was tagged that way and v0.8.0 nearly was. Twice is the threshold: a rule
# someone has to remember is a rule that drifts, so this either opens or does not.
#
# What it checks, on the commit the tag points at:
#   - `ci`    must have run and succeeded. No run means nothing was verified.
#   - `pages` must have succeeded if it ran. If it did not run — the commit
#             touched no publishable path — the latest `pages` run on main must
#             be green, because that is what the release's docs links resolve to.
#
# Deliberately not checked: whether the version is the right one, whether the tag
# message reads well, whether the release notes match. Those are judgement, and
# judgement belongs in /release where it can be argued with.
#
# Escape hatches, because a gate you cannot bypass gets switched off entirely:
#   - put [skip-release-gate] anywhere in the command
#   - export SQLB_SKIP_RELEASE_GATE=1
#
# Infrastructure failure (no gh, not authenticated, API down) asks rather than
# denies. Being unable to see the runs is not the same as the runs being red,
# and a gate that blocks when the network is down teaches people to disable it.

set -uo pipefail

input=$(cat)
command=$(printf '%s' "$input" | jq -r '.tool_input.command // ""')
cwd=$(printf '%s' "$input" | jq -r '.cwd // "."')
cd "$cwd" 2>/dev/null || exit 0

[ "${SQLB_SKIP_RELEASE_GATE:-}" = "1" ] && exit 0
case "$command" in *"[skip-release-gate]"*) exit 0 ;; esac

decide() { # decide <allow|deny|ask> <reason>
  jq -n --arg d "$1" --arg r "$2" '{
    hookSpecificOutput: {
      hookEventName: "PreToolUse",
      permissionDecision: $d,
      permissionDecisionReason: $r
    }
  }'
  exit 0
}
deny() { decide deny "$1"; }
ask()  { decide ask  "$1"; }

# ---------------------------------------------------------------------------
# Is this a release action, and which commit does it release?
#
# The matcher in settings.json is the tool, not the command, so this is where a
# non-release Bash call gets waved through. Silence is the common path.
# ---------------------------------------------------------------------------

is_release_action=false
case "$command" in
  *"gh release create"*)                      is_release_action=true ;;
  *"git tag "*)
    # Reading a tag is not making one. -d/-l/--list/-v are inspection.
    case "$command" in
      *" -d "*|*" --delete "*|*" -l "*|*" --list "*|*" -v "*|*" --verify "*) : ;;
      *"git tag -a"*|*"git tag -s"*|*"git tag -m"*|*"git tag v"*) is_release_action=true ;;
    esac ;;
  *"git push"*)
    case "$command" in
      *" --tags"*|*" --follow-tags"*|*[[:space:]]v[0-9]*) is_release_action=true ;;
    esac ;;
esac
[ "$is_release_action" = true ] || exit 0

# The first vX.Y.Z-shaped token in the command is the version, if there is one.
tag=$(printf '%s' "$command" | grep -oE '\bv[0-9]+\.[0-9]+\.[0-9]+[A-Za-z0-9.+-]*' | head -1)

sha=""

# 1. An existing tag names its own commit, which is the reliable case: pushing a
#    tag, or publishing a release for one already made.
if [ -n "$tag" ] && git rev-parse -q --verify "refs/tags/$tag" >/dev/null 2>&1; then
  sha=$(git rev-list -n1 "$tag" 2>/dev/null)
fi

# 2. `gh release create --target <ref>`.
if [ -z "$sha" ]; then
  target=$(printf '%s' "$command" | sed -n 's/.*--target[ =]\([^ ]*\).*/\1/p' | head -1)
  [ -n "$target" ] && sha=$(git rev-parse -q --verify "${target}^{commit}" 2>/dev/null)
fi

# 3. `git tag -a <version> <commit>` — the commit is positional, and defaulting
#    to HEAD here is wrong rather than merely imprecise: /release tags
#    `origin/main` explicitly, and a worktree's HEAD is some other branch. The
#    gate would then verify a commit nobody is releasing and wave the real one
#    through. Skip flags and the arguments that belong to them, then take the
#    first positional that resolves to a commit and is not the tag itself.
if [ -z "$sha" ]; then
  set -f                       # no globbing while word-splitting the command
  # shellcheck disable=SC2086
  set -- $command
  skip_next=false
  for tok in "$@"; do
    if [ "$skip_next" = true ]; then skip_next=false; continue; fi
    case "$tok" in
      -m|-F|-u|--file|--message|--local-user|--notes-file|--title|-t|--target)
        skip_next=true; continue ;;
      -*|git|tag|push|release|create) continue ;;
      "$tag") continue ;;
    esac
    if cand=$(git rev-parse -q --verify "${tok}^{commit}" 2>/dev/null); then
      sha=$cand; break
    fi
  done
  set +f
fi

# 4. Nothing named a commit, so the release is of whatever is checked out.
[ -n "$sha" ] || sha=$(git rev-parse -q --verify "HEAD^{commit}" 2>/dev/null)
[ -n "$sha" ] || exit 0

short=$(git rev-parse --short "$sha" 2>/dev/null)
label="${tag:-$short}"

# ---------------------------------------------------------------------------
# What do the runs say?
# ---------------------------------------------------------------------------

command -v gh >/dev/null 2>&1 || ask \
"Cannot verify the release gate: \`gh\` is not on PATH.

Releasing $label means asserting that both \`ci\` and \`pages\` are green on
$short, and that has not been checked. Verify by hand:

    gh run list --commit $sha

or re-run with [skip-release-gate] if you have already confirmed it."

runs=$(gh run list --commit "$sha" --json workflowName,conclusion,status,url 2>&1)
if [ $? -ne 0 ] || ! printf '%s' "$runs" | jq -e . >/dev/null 2>&1; then
  ask "Cannot verify the release gate: \`gh run list\` failed on $short.

$(printf '%s' "$runs" | tail -5)

Releasing $label asserts both workflows are green and that is currently
unverifiable. Check by hand, or re-run with [skip-release-gate]."
fi

if [ "$(printf '%s' "$runs" | jq 'length')" -eq 0 ]; then
  deny "No workflow runs exist for $short, so nothing has verified this commit.

Releasing $label from it would repeat the v0.7.0 failure with less evidence.
The usual cause is that the commit is not on \`origin/main\` yet.

    git push origin main     # then wait for ci and pages

Re-check with: gh run list --commit $sha"
fi

# Newest run per workflow — a re-run supersedes the attempt before it.
verdict() { printf '%s' "$runs" | jq -r --arg w "$1" \
  '[.[] | select(.workflowName==$w)][0] | if . == null then "absent"
   else (.status + "/" + (.conclusion // "pending") + "/" + .url) end'; }

ci=$(verdict ci)
pages=$(verdict pages)

case "$ci" in
  absent) deny "\`ci\` has not run on $short.

\`pages\` alone is not the gate — it says the site builds, not that the library
does. Push the commit and let \`ci\` run before releasing $label." ;;
  completed/success/*) : ;;
  completed/*) deny "\`ci\` is red on $short — ${ci#completed/}

Releasing $label would tag a commit the gate rejected. Fix it, push, and let
\`ci\` go green on the new tip." ;;
  *) deny "\`ci\` is still ${ci%%/*} on $short — ${ci##*/}

A release asserts the commit was verified, and it has not finished being
verified. Wait for it, then re-run." ;;
esac

case "$pages" in
  completed/success/*) : ;;
  completed/*) deny "\`pages\` is red on $short — ${pages#completed/}

This is exactly the case CLAUDE.md warns about: \`pages\` never appears on a
pull request, so \`gh pr checks\` reads green while the documentation site is
broken. v0.7.0 shipped this way.

\`mise run site-check\` reproduces it locally without npm." ;;
  absent)
    # No pages run here means the commit touched no publishable path, so the
    # live site is whatever main last deployed. That is what the release notes
    # will link into, so it is the thing to check.
    last=$(gh run list --workflow pages --branch main --limit 1 \
             --json conclusion,status,url,headSha 2>/dev/null)
    if printf '%s' "$last" | jq -e '.[0]' >/dev/null 2>&1; then
      lc=$(printf '%s' "$last" | jq -r '.[0].conclusion // "pending"')
      lu=$(printf '%s' "$last" | jq -r '.[0].url')
      ls=$(printf '%s' "$last" | jq -r '.[0].headSha[0:7]')
      if [ "$lc" != "success" ]; then
        deny "\`pages\` did not run on $short — this commit changes nothing the
site publishes — but the last \`pages\` run on main is $lc ($ls).

    $lu

The deployed site is what this release's links resolve to, so releasing
$label would point readers at a build that failed. Fix the site, push to
main, and let \`pages\` go green."
      fi
    fi ;;
  *) deny "\`pages\` is still ${pages%%/*} on $short — ${pages##*/}

Wait for the deploy to finish. A cancelled or half-finished \`pages\` run can
leave the site serving a partial build, which is why its concurrency group
never cancels in progress." ;;
esac

exit 0
