# ADR-0031: The Dart client keeps the vocabulary and gives up the narrowing

- **Status:** Working — one file emitted into `example/tasks/mobile`, gated by
  `mise run test-dart`
- **Confidence:** Medium on the vocabulary, which is
  [ADR-0028](0028-typescript-client.md)'s design in a second language and is
  asserted the same way — `lib/refusals.dart` marks sixteen illegal requests
  with `// ignore:` and the `unnecessary_ignore` lint fails the build if one
  stops being needed. Low on the row view and on the pager, because no Flutter
  application has yet lived with either
- **Decided:** 2026-07-29
- **Last reviewed:** 2026-07-29

## Context

[ADR-0028](0028-typescript-client.md) generates a TypeScript client from the
registry rather than from the emitted OpenAPI document, because the document is
lossy exactly where the value is: `?status=eq.published` documents as
`array<string>` with the operator vocabulary in prose. That argument is about
the schema and the wire, not about TypeScript, so it transfers to any typed
client. A Flutter application is the second consumer to ask for one.

Three things do not transfer, and they are what this record is for.

**Dart has no structural types.** TypeScript narrows a response by `select`
with `Pick<Task, S>` and widens it by `expand` with a conditional type. Dart has
no mapped types, no literal-union generics and no conditional types, so
`select: ['title']` cannot produce a type that has `title` and does not have
`status`. ADR-0028 already names this case: *"if narrowing the response by
`select` and `expand` needs enough generic machinery that its type errors become
unreadable, drop the narrowing."* Here it is not unreadable, it is unwritable.

But dropping the narrowing is not free, and this is the sharp part. In
TypeScript a projected row that is typed as the full row yields `undefined` and
carries on. In Dart, a class with `required String title` cannot be constructed
from a response that omitted `title` at all, so a strict data class and `select`
are mutually exclusive: either the constructor throws on every projection, or
every column is nullable and the type stops saying which ones the schema
guarantees.

**Dart has no implicit deserialisation.** `JSON.parse` hands JavaScript an
object whose properties are already the wire's, which is what makes ADR-0028's
snake_case decision free — camel-casing there would need a runtime mapping layer
and nothing else would. Dart has to walk the map and read each key by name
whatever the member is called, so the mapping is not a layer, it is the string
literal already being written. Meanwhile snake_case members would fail
`non_constant_identifier_names` in every file that touched them, and a client a
project has to exclude from analysis is a client it will not read.

**Dart has no keyed query cache.** TanStack Query is why ADR-0028 emits a key
factory: the change feed ([ADR-0012](0012-change-feed-outbox.md)) delivers a
table and a row key, and something has to turn those into keys to invalidate.
Riverpod, BLoC and the rest have no such registry — invalidation is a provider
handle or a stream, both of which are the application's. A key factory here
would be vocabulary with no consumer.

What *does* transfer, and gains weight on a phone: a list on a small screen
loads as it is scrolled, `next_cursor` is on every list response that has a
page after it ([ADR-0027](0027-keyset-pagination.md)), and hand-written clients
reconstruct that walk from `has_more` and an offset counter. The reference
application this was designed against does exactly that, in four places.

## Decision

Emit one Dart file from the same registry the other emitters read, into the
repository that consumes it, under `DartDir`.

The vocabulary is ADR-0028's, unchanged in substance: `where` admits only
filterable columns with the operator set narrowed by column type; `sort` is the
sortable columns and their descending forms; `select` and `expand` are closed
sets; hidden columns have no spelling anywhere; and the transport is injected.

Four things differ, each because of something above.

### Members are camelCase, and the wire spelling sits beside them

`org_id` becomes `orgId`, reading `_str('org_id')`. This is the one place this
record contradicts ADR-0028, and the reason is that its argument does not hold
here: the mapping costs a string constant, not a runtime layer, because a Dart
client decodes explicitly either way.

### A row is a view over the response, not a copy of it

`class Task extends Row` wraps the decoded map and reads columns on access. That
is what makes a projection representable at all, and it makes the failure
loud in the right place: a column the request did not return throws
`MissingColumn`, naming the row, the column and the fix, rather than yielding
null or refusing to construct. Absence is checked with `containsKey`, so a
nullable column returned as null and one that was never selected stay different
facts — only one of them is a mistake.

It costs the narrowing that TypeScript gets for free. `select: [title]` followed
by `task.status` compiles and throws. That is worse than a compile error and
better than the alternatives, which are a null that travels, or no `select` at
all on the producer that most needs to send less.

There is a second, smaller reason it is a view. A list of two hundred rows whose
screen reads three columns decodes three columns, and `toJson()` hands back
exactly what arrived, so a local cache stores the response rather than
re-encoding a parse of it. Expansions and the values that need real work
(`DateTime`, enums) are memoised, because a Flutter getter is read once per
frame.

### The framework layer is a cursor pager, not a set of providers

`taskPager(transport)` returns a `CursorPager<Task>` that holds the rows and the
position and nothing about how they are shown, so it drops into a Riverpod
notifier, a BLoC or a `StatefulWidget` without preferring any of them. It
collapses concurrent `loadMore()` calls onto the one already running, because a
scroll listener fires on every frame near the end of a list.

Generating Riverpod providers would be ADR-0028's rejected hooks with a
different name: a framework baked in, and the thing people copy out and edit.
The difference is that TypeScript had `queryOptions` — a composable unit that is
not a hook — and Dart has no equivalent, so the layer stops at the pager.

### `dart format` must be a no-op on the output

The emitter reproduces the formatter's decisions rather than deferring to it: it
knows the 80-column rule and where the tall style breaks an arrow body, a
constructor's named parameters and a map literal. This module has no Dart
toolchain and will not acquire one, so the alternative is a generated file the
formatter rewrites — which puts a project's `dart format --set-exit-if-changed`
gate and its `codegen.Check` staleness gate in direct conflict, with no
resolution that does not involve excluding the file from one of them.

### A table whose singular collides with `dart:core` takes a `Row` suffix

`lists` would give `class List`, and declaring it makes every `List<T>` in the
emitted library mean the wrong thing — a wall of type errors rather than a name
clash. So the row class becomes `ListRow`, while the vocabulary types
(`ListColumn`, `ListWhere`, `ListListParams`) keep the plain name, since none of
them collides. The same rule escapes a column named `class` or `to_json` with a
`Value` suffix, rather than a trailing underscore or a `$`, because those trip
the lowerCamelCase lint the escape exists to satisfy.

## Consequences

**What this buys.** The producer with the least type safety over this API gets
the most from a generated one: a Flutter app filtering a task list is assembling
a query from user input, and every column, operator, sort term and enum value in
it is now checked by the analyser. [ADR-0011](0011-actionable-errors.md)'s
`allowed` list reaches the caller, so a filter UI can offer the alternatives
instead of a dead end. And the cursor walk that four places in the reference
application hand-roll is one function.

**What this costs.** A third toolchain in a repository whose pitch is a
stdlib-only Go module. It is the Dart SDK rather than Flutter — the emitted
client imports nothing, so nothing in `example/tasks/mobile` needs a Flutter
install — but it is still a pinned SDK, a `pub get` in CI and a gate.

**A projection is a runtime error rather than a compile error.** This is the one
real regression against the TypeScript client and it is not recoverable in the
language. It is mitigated, not fixed: the throw names the column and the fix,
and `row.has(column)` exists for code that means to branch on it.

**Format stability is asserted against one formatter version.** `dart format`'s
tall style arrived in 3.7 and changed the indentation of exactly the constructs
this emitter produces. A future style change breaks `mise run test-dart` with a
diff, which is the right failure — visible, in this repository, and fixed in the
emitter rather than in every consumer.

**The escape hatch is emitted from the start**, as `params`, for the reason
ADR-0028 gives: reaching for it is the signal that the typed layer is in the
wrong place, and the signal has to be observable.

**What building it changed.** Three things the plan did not anticipate:

- The formatter drove the shape of the runtime, not just its layout. Readers
  became instance methods on `Row` (`_str('title')` rather than
  `_str(_json, 'Task', 'title')`) so that a getter fits on one line, and the
  result is more readable than the version written for the emitter's
  convenience.
- Dart's `unnecessary_ignore` lint turned out to be `@ts-expect-error` exactly:
  an `// ignore:` for a diagnostic that is no longer produced is itself
  reported. `lib/refusals.dart` therefore needs no bespoke checker, and the
  guard fails when it stops guarding — the property ADR-0016 asks of a guard.
- The pager takes no cancellation token, unlike the single-request functions. A
  walk is many requests and Dio's `CancelToken` is one-shot, so cancelling one
  page would poison every page after it.

## What would change our mind

- If applications end up wrapping every generated call in a repository class
  anyway, the free functions are the wrong unit and the emitter should produce
  the repository — the same signal ADR-0028 watches for with `queryOptions`.
- If `MissingColumn` is hit in production rather than in development, `select`
  is too sharp a tool for this language and should be dropped from the typed
  layer, leaving it to `params`.
- If a Flutter application finds the row view awkward where a `freezed`-style
  value class would not be — `copyWith` in an optimistic update is the likeliest
  place — then the projection is worth less than the ergonomics and the emitter
  should produce plain data classes with a strict constructor.
- If reproducing `dart format` becomes a source of churn across SDK versions,
  give up on it and document the generated file as formatter-exempt, the way
  `*.g.dart` already is by convention.
- If a second Dart consumer needs the client without running the generator, the
  no-package decision is the one to reopen — the same one ADR-0028 flags as most
  likely to be asked for, and for the same reason.
- If [ADR-0012](0012-change-feed-outbox.md) lands and applications want more
  than `TableName` to route an event, the key-factory question reopens with an
  actual consumer to design against.

## Cost of change

Cheap. One consumer exists, it is in this repository, and it regenerates from
the schema, so a change to the emitter reaches every call site in the run that
produced them and `dart analyze` names the ones that stop compiling.

The expensive thing to change later is the row view, and it is expensive in one
direction only. Moving from a JSON-backed view to constructor-built data classes
is a breaking rename of nothing — the getters keep their names — but it removes
`select` from the client, which is a capability, not a spelling. Moving the
other way is free.

camelCase is cheap in both directions here, unlike snake_case in ADR-0028: the
wire spelling is already a string constant in one place per column, so changing
the convention is a change to `dartMember` and a regeneration.

## Alternatives considered

**`freezed` and `json_serializable` data classes.** The idiomatic Flutter
answer, and what the reference application uses for its hand-written models. It
loses on three counts: it makes `build_runner` a dependency of consuming the
schema, generated code that generates code is a build-order problem nobody
enjoys, and a strict constructor cannot represent a projection. Worth revisiting
if `select` turns out not to earn its keep.

**Every column nullable, with no projection error.** The smallest change that
makes projections work with data classes. Rejected because it throws away what
the schema knows: a column the database declares NOT NULL would be typed `String?`
in every screen that reads it, and the null check would be noise everywhere to
accommodate a case only `select` creates.

**Generate Riverpod providers.** Rejected above. It is ADR-0028's hooks
argument, and Riverpod's own code generation already occupies that space in a
consuming app.

**`openapi-generator` against the emitted document.** The same alternative
ADR-0028 weighs, with the same answer and one addition: its Dart output is
`dio` + `built_value`, which is two dependencies and a `build_runner` step to
get types that still cannot express the operator vocabulary.

**Publish a pub package.** Rejected for version skew, exactly as ADR-0028
rejects an npm package. A client generated against the server it talks to cannot
be a version behind it.

## Revisions

- 2026-07-29 — Written and built in one pass, against a Flutter application read
  for the purpose: Dio with an auth interceptor, Riverpod, `freezed` models, and
  four hand-rolled offset walks over a list endpoint. The three departures from
  [ADR-0028](0028-typescript-client.md) — camelCase members, a row view instead
  of a data class, and a pager instead of a query-key factory — all come from
  that reading rather than from the language in the abstract.
