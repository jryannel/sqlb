# ADR-0046: A negation is SQL's, and the alternative is a second vocabulary rather than a redefinition

- **Status:** Working — this records a decision the code already makes, written
  because it has been re-proposed twice and the proposal reached for the wrong
  alternative both times
- **Confidence:** High that the three options are the three, and high that the
  middle one is ruled out structurally rather than by taste — the seam argument
  below does not depend on which semantics one prefers. High that the second
  option is foreclosed for existing spellings, because
  [compatibility.md](../compatibility.md) already promises it in writing. Medium
  on the sequencing of the fourth path — an additive second vocabulary is on the
  1.1 list and nothing has been built, so its cost is estimated rather than paid
- **Decided:** 2026-08-01
- **Last reviewed:** 2026-08-01 — written

## Context

**The complaint is always the same and it is always correct.** `?labels=nhas.urgent`
does not return rows whose `labels` is NULL. Neither does `?status=neq.draft`
return rows whose `status` is NULL, and neither does the JSON tree's
`{"op":"not", …}`. A caller who reads a filter language as set operations expects
the complement of a set to be everything else; SQL gives them three-valued logic,
where a row that is unknown on both sides of a negation falls out of both.

Every negation in the surface behaves this way, and there are two families:

| Family | Spelling | Where | Compiles to |
|---|---|---|---|
| leaf complements | `ne`, `neq`, `nin` | `filter/filter.go:576`, `:578` | `<>`, `NOT IN` (`expr.go:245`, `:311`) |
| leaf complements, containment | `nhas`, `nhasany`, `nhasall`, `nhasdoc` | `filter/filter.go:608` | `NOT (…)` around the containment operator |
| group negation | `{"op":"not"}` in the JSON tree | `filter/json.go:215` | `sqlb.Not` → `Unary{Op: "NOT"}` (`expr.go:163`) |

The group negation reached the JSON tree on 2026-08-01
([#96](https://github.com/jryannel/sqlb/pull/96), closing
[#91](https://github.com/jryannel/sqlb/issues/91)) and has not yet reached the
URL grammar ([#98](https://github.com/jryannel/sqlb/issues/98)). Which means the
decision recorded here is about to apply to a second spelling, and is therefore
worth settling before that spelling exists rather than after.

**The proposal that keeps arriving is `IS NOT TRUE`, and it is the one option
that cannot work.** It is a natural reach: the group `not` is new, it is the
place a set reading is most expected, and `NOT (…)` → `IS NOT TRUE` is a
one-line change at `expr.go:167`. It has been proposed twice — most recently by
the multi-app adoption that filed
[#91](https://github.com/jryannel/sqlb/issues/91), which argued for it and then
withdrew the argument on seeing what follows.

**Nothing in the shipped position is arbitrary, but it is not quite uniform
either, and the exception is instructive.** `OneOf` translates a nil member into
`IN (…) OR col IS NULL` (`expr.go:277`, from
[#71](https://github.com/jryannel/sqlb/issues/71)), which looks like set
semantics smuggled into the positive side. It is not. `IN (NULL)` is never true,
so without the translation a set assembled from nullable values would be
*silently narrower than the caller wrote* — the fix addresses a filter that
matched nothing, not a filter whose row set is a matter of preference.
`NotOneOf` deliberately does not mirror it, and says so at `expr.go:300`,
deferring exactly to this decision. The distinction is the one this record turns
on: repairing a spelling that can never match anything is not the same act as
choosing which rows a well-formed negation returns.

## Decision

**Negation is SQL's negation, everywhere in the surface, and the consequence is
documented at each place a caller meets it.** `not`, `neq`, `nin` and the
containment complements are three-valued. A row that is NULL in the column under
test matches neither the operator nor its complement.

It is written down in three places, deliberately not one: the grammar reference
([docs/rest/filtering.md:57](../rest/filtering.md)), the per-column parameter
description that renders in the OpenAPI document — and only for a nullable
column, so a `NOT NULL` column's description stays short (`rest/params.go:236`)
— and the doc comment on the builder method that has the asymmetry
(`expr.go:300`). The escape hatch is spelled out beside each:

```
?or=(labels.nhas.urgent,labels.isnull)
```

**The middle option — `IS NOT TRUE` for the group `not`, leaving the leaf
complements alone — is rejected structurally.** Not because three-valued logic
is better, but because the two grammars have to agree.

A leaf complement's negation lives *in the operator*: `nhas` is one token, the
caller cannot decorate it, and there is nowhere for a null-inclusive variant of
it to live under the current vocabulary. So `IS NOT TRUE` can only be applied to
the group form. That makes these two requests return different rows:

```
?labels=nhas.urgent                 three-valued: NULL rows excluded
?not=(labels.has.urgent)            set semantics: NULL rows included
```

They are the same logical filter under any reading a caller has. The
disagreement between them has no documentation home — it is not a property of
the operator, not a property of the column, and not a property of the group; it
is a property of *which of two equivalent spellings you happened to pick*. A
caller who starts with the leaf form and later hoists it into a group to negate
a conjunction gets a different result set with no error and no signal. That is
strictly worse than the surprise it was proposed to fix, because the original
surprise is at least stable, stateable, and attached to something a document can
point at.

**The option that would genuinely dissolve the dilemma is set semantics
*everywhere* — and it is foreclosed by a promise already made.** `neq` →
`IS DISTINCT FROM`, `nin` → `NOT IN (…) OR col IS NULL`, the containment
complements likewise, and the group `not` to match. Both grammars agree, the
asymmetry with `OneOf` disappears, and callers get the sets they were reasoning
about. It is arguably the better filter language.

[compatibility.md:36](../compatibility.md) freezes the filter grammar as a wire
format, in terms that decide this: *"New operators are additive; existing
spellings do not change meaning."* The containment complements were added and
frozen on the same day this question was raised
([ADR-0033](0033-array-columns.md), 2026-08-01 revision). Changing what `neq`
returns is not a deprecation a client can be warned about — a deployed client,
or an agent building requests off `sqlb.json`, keeps sending the same string and
starts getting a different answer. So this is not a judgement that set semantics
lose on merit. They lose on having arrived after the promise.

**What is open, and what the next person should reach for, is a second
vocabulary — additive, alongside.** `IsDistinctFrom` and a NULL-inclusive
`NotOneOf` are already on the 1.1 list
([release-1.0.md:261](../release-1.0.md)), where a single-app port ranked them
its #2 ask. A URL spelling would follow the same rule that governs everything
else here: new operators are additive.

This is **not** the middle option wearing a different hat, and the difference is
precisely the seam argument. That argument bites when *one logical filter has
two spellings that disagree*. It does not bite when *two different filters have
two different spellings* — as long as each spelling means the same thing in the
URL grammar and the JSON tree, which is a property the conformance test
[#98](https://github.com/jryannel/sqlb/issues/98) proposes would enforce
mechanically rather than by two parsers happening to agree.

Three things have to hold for that path to be worth taking, and they are the
work, not the operator:

- **each new spelling lands in both grammars at once.** A null-inclusive
  operator that exists only in the JSON tree recreates the seam this record
  rejects, one level up;
- **the names must not be near-misses.** `nin` beside an `nin`-with-a-suffix is
  a trap in a wire format, where the failure mode of picking the wrong one is
  wrong rows rather than a 400. The naming is the expensive part of the design;
- **the rejection message has to name both.** [ADR-0011](0011-actionable-errors.md)
  makes the allowed-operator list part of the error, so a doubled vocabulary
  doubles what a 400 has to teach.

## Consequences

**Buys.** One rule, stated once, true in both grammars and at every level of a
filter tree — which is what makes it documentable at all. The escape hatch is
real, spellable today, and appears next to every statement of the problem. And
the surface stays closed under the promise `compatibility.md` makes, which is
the property that lets a generated client be generated once.

**Costs.** The surprise is real and it lands on the caller, who is usually not
thinking about NULLs when they write a negation. The workaround is verbose, and
it gets worse rather than better as a filter nests: `?or=(a.nhas.x,a.isnull)` is
tolerable, and the same repair inside a negated group is not obviously
expressible at all — see the trigger below, because that is the case that would
outrank the freeze. `NotOneOf`'s asymmetry with `OneOf` stands, and reads as an
inconsistency until the second vocabulary lands to explain it. And a record
whose answer is "the better option is unavailable for compatibility reasons"
invites re-litigation, which is why the third option is written out here in full
rather than summarised — the failure mode this document exists to prevent is
someone reaching for `IS NOT TRUE` without knowing there was a third choice and
why it is not on the table.

## What would change our mind

- **The escape hatch turns out to be unspellable in some position.** The
  `?or=(…, col.isnull)` repair assumes the caller can reach the column and add a
  disjunct. If a case exists where a NULL-inclusive negation cannot be expressed
  at all — most likely inside a negated nested group once
  [#98](https://github.com/jryannel/sqlb/issues/98) lands — that is a
  correctness argument rather than an ergonomic one, and it outranks the wire
  freeze. Worth testing for deliberately when #98 is built, not waiting to be
  reported.
- **The second vocabulary lands and the SQL-faithful operators go unused.** If
  applications reach for the null-inclusive forms every time, the default is
  wrong, and changing a default is what a major version is for. This is the
  trigger that needs usage data rather than an argument.
- **A third proposal arrives for `IS NOT TRUE` on the group alone.** That would
  mean this record failed at its one job. The fix then is the documentation and
  the discoverability of this record, not the semantics.
- **`OneOf`'s nil translation is found to have chosen row sets after all.** It is
  recorded here as repairing a never-matching filter rather than picking
  semantics. If a case shows that reading to be wrong, the shipped position is
  already mixed and the seam argument applies to it.

## Cost of change

Asymmetric, already paid in one direction, and widening.

**Adding operators** is additive and stays cheap indefinitely: nothing existing
moves, no deployed request changes meaning, and the generated clients gain
methods rather than losing them. This is why the 1.1 disposition in
`release-1.0.md` was the right one and remains available.

**Changing what an existing spelling means** is a major version plus a hand
migration for every consumer — and unlike an API break, it cannot be caught by a
compiler on the client side, because the client is a string. `restcompat`
cannot see it either: no parameter is removed and no request is rejected, so the
capability diff is blind to it by construction, the same way it was blind to a
NULLS-placement change ([#88](https://github.com/jryannel/sqlb/issues/88)). A
change nothing can detect and nothing can migrate is the most expensive kind
this project has.

That asymmetry is the whole reason the arguably-better language is the one
foreclosed, and it only grows: every deployed client and every agent holding
`sqlb.json` adds to the bill. The window for reconsidering the *default* closed
at 1.0. The window for adding the second vocabulary never closes.

## Revisions

- 2026-08-01 — Written. Prompted by the multi-app adoption's review of
  [#91](https://github.com/jryannel/sqlb/issues/91)/[#96](https://github.com/jryannel/sqlb/pull/96),
  which proposed `IS NOT TRUE` for the group `not`, then withdrew it on the seam
  argument and identified the third option as the one it had actually been
  reaching for. The record exists because that sequence — reach for the middle,
  discover the seam, discover the freeze — is one a future reader should not
  have to repeat, and because [#98](https://github.com/jryannel/sqlb/issues/98)
  is about to apply the decision to a second spelling.
