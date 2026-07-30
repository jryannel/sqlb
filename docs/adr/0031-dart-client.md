# ADR-0031: The Dart client keeps the vocabulary and gives up the narrowing

- **Status:** Working — one file emitted into `example/tasks/mobile`, gated by
  `mise run test-dart`
- **Confidence:** Medium on the vocabulary, asserted as in
  [ADR-0028](0028-typescript-client.md) — `lib/refusals.dart` marks sixteen
  illegal requests with `// ignore:`, and `unnecessary_ignore` fails the build if
  one stops being needed. Low on the row view and the pager, since no Flutter
  application has lived with either
- **Decided:** 2026-07-29
- **Last reviewed:** 2026-07-29

## Context

ADR-0028's argument — generate from the registry, not the lossy OpenAPI document
— is about the schema and the wire, so it transfers to any typed client. Three
things do not transfer:

**Dart has no structural types.** No mapped types, no literal-union generics, no
conditional types, so `select: ['title']` cannot produce a type that has `title`
and not `status`. ADR-0028 anticipated dropping the narrowing if it got
unreadable; here it is unwritable. And dropping it is not free: in TypeScript a
projected row typed as the full row yields `undefined` and carries on, while in
Dart a class with `required String title` cannot be constructed from a response
that omitted it. A strict data class and `select` are mutually exclusive.

**Dart has no implicit deserialisation.** It must walk the map and read each key
by name whatever the member is called, so the mapping is not a layer — it is the
string literal already being written. Meanwhile snake_case members fail
`non_constant_identifier_names`, and a client a project must exclude from
analysis is a client it will not read.

**Dart has no keyed query cache.** Riverpod and BLoC have no registry to
invalidate against, so a key factory would be vocabulary with no consumer.

What does transfer, and gains weight on a phone: a list loads as it is scrolled,
`next_cursor` is on every list response with a page after it
([ADR-0027](0027-keyset-pagination.md)), and the reference application
reconstructs that walk from `has_more` and an offset in four places.

## Decision

Emit one Dart file from the same registry the other emitters read, under
`DartDir`. The vocabulary is ADR-0028's unchanged: `where` admits only filterable
columns with operators narrowed by type, `sort` is the sortable columns and their
descending forms, `select` and `expand` are closed sets, hidden columns have no
spelling, and the transport is injected. Four things differ:

**Members are camelCase, with the wire spelling beside them.** `org_id` becomes
`orgId`, reading `_str('org_id')`. The one place this contradicts ADR-0028, and
only because its argument does not hold here — the mapping costs a string
constant, not a runtime layer.

**A row is a view over the response, not a copy of it.** `class Task extends Row`
wraps the decoded map and reads columns on access, which is what makes a
projection representable at all. A column the request did not return throws
`MissingColumn`, naming the row, the column and the fix, rather than yielding
null. Absence is checked with `containsKey`, so a nullable column returned as
null and one never selected stay different facts. It costs the narrowing:
`select: [title]` then `task.status` compiles and throws — worse than a compile
error, better than a null that travels or no `select` at all. Secondary benefit:
a 200-row list whose screen reads three columns decodes three columns, and
`toJson()` hands back exactly what arrived.

**The framework layer is a cursor pager, not a set of providers.**
`taskPager(transport)` returns a `CursorPager<Task>` holding rows and position
and nothing about how they are shown, so it drops into Riverpod, BLoC or a
`StatefulWidget` without preferring any. It collapses concurrent `loadMore()`
calls, because a scroll listener fires every frame near the end of a list.
Generating Riverpod providers would be ADR-0028's rejected hooks under another
name; Dart has no `queryOptions` equivalent, so the layer stops at the pager.

**`dart format` must be a no-op on the output.** The emitter reproduces the
formatter's decisions rather than deferring to it, because this module has no
Dart toolchain — the alternative puts a project's `dart format
--set-exit-if-changed` gate and its `codegen.Check` staleness gate in direct
conflict.

**A table whose singular collides with `dart:core` takes a `Row` suffix.**
`class List` would make every `List<T>` in the library mean the wrong thing, so
the row class is `ListRow` while the vocabulary types keep the plain name. A
column named `class` takes a `Value` suffix, not a trailing underscore, which
would trip the lint the escape exists to satisfy.

## Consequences

**Buys.** The producer with the least type safety over this API gets the most
from a generated one: a Flutter app filtering a task list assembles its query
from user input, and every column, operator, sort term and enum value is now
checked by the analyser. ADR-0011's `allowed` list reaches the caller, so a
filter UI can offer alternatives instead of a dead end. The cursor walk is one
function.

**Costs.** A third toolchain — the Dart SDK rather than Flutter, but still a
pinned SDK, a `pub get` in CI and a gate.

**A projection is a runtime error rather than a compile error.** The one real
regression against the TypeScript client, unrecoverable in the language and
mitigated rather than fixed: the throw names the column and the fix, and
`row.has(column)` exists for code that means to branch on it.

**Format stability is asserted against one formatter version.** A future style
change breaks `mise run test-dart` with a diff, which is the right failure —
visible here, and fixed in the emitter rather than in every consumer.

**What building it changed.** The formatter drove the shape of the runtime, not
just its layout: readers became instance methods on `Row` so a getter fits on one
line, and the result reads better than the version written for the emitter's
convenience. Dart's `unnecessary_ignore` turned out to be `@ts-expect-error`
exactly, so the refusals file needs no bespoke checker and the guard fails when
it stops guarding ([ADR-0016](0016-guards-proven-both-ways.md)). And the pager
takes no cancellation token: a walk is many requests and Dio's `CancelToken` is
one-shot, so cancelling one page would poison every page after it.

## What would change our mind

- Applications wrap every generated call in a repository class anyway — the free
  functions are the wrong unit, the same signal ADR-0028 watches for.
- `MissingColumn` is hit in production rather than development — `select` is too
  sharp a tool for this language and belongs in `params`.
- A Flutter app finds the row view awkward where a `freezed`-style value class
  would not be, most likely `copyWith` in an optimistic update — then plain data
  classes with a strict constructor win.
- Reproducing `dart format` becomes churn across SDK versions — document the file
  as formatter-exempt, the way `*.g.dart` already is by convention.
- A second Dart consumer needs the client without running the generator — the
  no-package decision reopens.
- ADR-0012 lands and applications want more than `TableName` to route an event —
  the key-factory question reopens with an actual consumer to design against.

## Cost of change

Cheap: one consumer, in this repository, regenerating from the schema, with
`dart analyze` naming what stops compiling. The expensive thing is the row view,
in one direction only — moving to constructor-built data classes removes `select`
from the client, which is a capability, not a spelling. Moving the other way is
free. camelCase is cheap both ways, unlike snake_case in ADR-0028, because the
wire spelling is already a string constant in one place per column.

## Revisions

- 2026-07-29 — Written and built in one pass, against a Flutter application read
  for the purpose: Dio with an auth interceptor, Riverpod, `freezed` models, and
  four hand-rolled offset walks. All three departures from ADR-0028 come from
  that reading rather than from the language in the abstract.
- 2026-07-30 — Condensed.
