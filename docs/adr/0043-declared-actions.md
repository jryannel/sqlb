# ADR-0043: A declared action generates the envelope, and the verb stays plain Go

- **Status:** Revisiting — the row-only shape is still what is built and what
  `example/tasks` runs against Postgres, but the *What would change our mind*
  trigger on a computed write response has fired; see the 2026-08-14 revision
- **Confidence:** High that the gap is real and large — the adoption review
  measured verbs at 780 lines against 464 for all of CRUD on the same handlers,
  so the part sqlb does not generate is bigger than the part it does. High that
  *envelope-only* is the line, because [vision.md](../vision.md) names the
  failure mode this feature is most likely to walk into. Medium on the
  declaration's spelling, which is the part most likely to change on first
  contact. Medium on where the obligation lands. Low on collection actions,
  which are the half of the feature the safety argument does not reach. Low on
  the widened response shape decided 2026-08-14 — sketched, not built
- **Decided:** 2026-07-31
- **Last reviewed:** 2026-08-14

## Context

**The generated surface stops exactly where applications spend their code.**
[§13.4 of the adoption review](../review-adoption-existing-app.md) counted 26
`POST /{id}/<verb>` routes and about 20 collection-level verbs in the evaluated
application, and measured the task handlers: 780 lines of verbs against 464 for
create, read, update, delete and list together. Every verb opens the same way —
parse the id, fetch the row under the tenant predicate, 404, decode an optional
body — roughly 30 lines before any domain logic, times ~46 endpoints, written
four times over in the handler, the OpenAPI document, the TypeScript client and
the CLI. [#18](https://github.com/jryannel/sqlb/issues/18) ranks closing it
third of six and calls it the biggest single win.

**And it is the feature most likely to be the reason someone leaves.**
[vision.md](../vision.md) states the failure mode without knowing it is
describing this: if generated handlers get copied out and edited by hand, the
seams are in the wrong place. Domain verbs are where logic is most idiosyncratic
— the place a DSL's expressiveness runs out first and most visibly. A feature
that tries to *express* the transition will be fought, worked around, and
finally ejected, and it will not leave alone the parts that were working. The
distinction between generating the envelope and generating the verb is the whole
of whether this works.

**The seam it needs already exists and is already used.** `BeforeCreate` is a
plain Go func the generated path calls; nobody has proposed a language for it.
An action is that arrangement moved one level out: the framework owns the
request, the transaction and the response, and calls a func the application
wrote.

**The envelope is already in the tree, minus the call.** `registerUpdate`
(`rest/item.go:220`) parses the key through `binding.key`, runs the mutation
inside `write(ctx, w, …)` so that `sqlb.AfterCommit` is reachable
([ADR-0021](0021-hooks-receive-an-event.md)), renders the row through the same
`row[T]` marshaller every other operation uses, and translates errors through
`asHumaError`. An action is that function with a fetch in front and a call in
the middle. This is not a new subsystem; it is a fourth caller of one that
exists.

**Three things in the issue's proposed form cannot be built as written**, and
each one moves the design rather than only its spelling:

```go
schema.Action("complete", schema.POST, "/{id}/complete").
    Body[CompleteTaskInput]().          // 1
    Writes("status", "closed_at").
    Do(completeTask)                    // 2, 3
```

1. **`.Body[T]()` is not valid Go.** Methods cannot take type parameters. The
   body has to arrive some other way regardless of what this record decides, so
   the question "reflected from an application type, or declared?" is forced
   rather than optional.
2. **`Do` cannot live in the declaration.** A schema is a value five emitters
   read and `sqlb.json` serialises, through `schema.RESTManifest`. A func is
   neither readable by a generator nor serialisable, and the schema
   package is linked into `sqlb` the *command* — putting the application's
   domain funcs there makes the driver depend on the application.
3. **`rest` may not import `schema`.** `rest/rest.go:57` says so and `rest.Op`
   exists because of it: nothing on the request path imports the DSL, which is
   what keeps the runtime usable without it. An action declaration crosses that
   line as a value, the way exposure already does.

**Where the safety argument actually pays.** [ADR-0030](0030-declared-scope-is-required.md)
made a declared scope an obligation and closed the "nobody wrote the hook" hole
for CRUD. Hand-written verb handlers are precisely where the org-scoped fetch is
still remembered by hand — the same failure class, on the ~60% of routes that
CRUD does not cover. An action whose fetch is generated extends that closure to
where the risk lives. That is the strongest argument for this feature and it is
not the one the issue leads with.

## Decision

**An action declares its envelope on the table and binds its verb at
registration.** Two halves, in two places, because the two have different
readers: the declaration is read by emitters, and the func is read by the
compiler.

```go
Task.Action(schema.Action{
    Name: "complete",
    Body: schema.Body(
        schema.Text("note").Nullable(),
        schema.Timestamp("completed_at"),
    ),
    Writes: []string{"status", "closed_at"},
})
```

`Path` defaults to `/{id}/` plus the name, which is what nearly every verb
wants. There is no `Method`: an action is a POST, and the alternatives are
either the RPC surface this record declines to become or a spelling of DELETE
that CRUD already has.

`Action` returns `*TableDef` and mirrors `Expose(schema.REST{…})` and
`AddIndex(schema.Index{…})` — the established idiom for "a table says one more
thing about itself".

**The body is declared in the field vocabulary, not reflected from an
application type.** Two reasons, and the second is the load-bearing one. The
value of this feature is that verbs enter the *client* emitters, and those read
the declaration — a body sqlb cannot see produces a TypeScript function typed
`unknown`, which is most of the drift the review measured, unfixed. And
reflecting an application struct inverts the dependency: models are generated
*from* the schema, so a schema reading an application type is a cycle waiting
for the first action on a generated model.

**`Do` is bound at registration.** When any table declares an action, codegen
emits `Register(api huma.API, db sqlb.Executor, actions Actions) error` and an
`Actions` struct with one field per action:

```go
type Actions struct {
    CompleteTask func(context.Context, *Task, CompleteTaskInput) error
}
```

The compiler asks for the field name and the exact signature, so an action added
to the schema is a build error at the call site rather than a route that answers
501. It cannot ask for the value to be non-nil — `Actions{}` compiles — so the
nil field is refused at mount, which is [ADR-0030](0030-declared-scope-is-required.md)'s
shape reused: the compiler first, startup second, and never a request.

**The envelope, in order**, and every step of it is a thing the application
writes by hand today:

1. Parse the id (`binding.key`, so a malformed one is a 400 with the same body
   shape as everywhere else).
2. Fetch the row through `BeforeQuery`, with `ForUpdate` when `Writes` is
   non-empty. The lock is not optional: read-modify-write across a network round
   trip is the classic lost update, and the whole point is that nobody has to
   remember it.
3. 404 when the fetch matches nothing — which, on a scoped table, is also the
   answer for a row belonging to someone else.
4. Decode the declared body.
5. Call `Do(ctx, *T, In) error` **inside** the transaction `write` opened, so a
   verb that touches other tables reaches it through `sqlb.TxFrom` and a hook it
   triggers can register an `AfterCommit`.
6. Persist the declared `Writes` columns from the mutated row.
7. 200 with the row, marshalled by the same `row[T]` as read and update.

**`Writes` is enforced, not documented.** The envelope persists those columns
and no others, so the declaration is something the OpenAPI document and
`sqlb impact` can state. A verb that needs to write outside its own row has the
transaction and can issue the statement; a verb that mutates a column it did not
declare finds it unwritten, which is a bug that shows up on the first test rather
than a silent widening of what a route touches.

**A collection action** — a path with no `{id}` — has no fetch: `func(ctx, In) error`,
204, still inside the transaction.

**Errors are the escape hatch, and they are typed.** A `Do` returning a
`*rest.Problem` is answered with its status, which needs no new mechanism —
`asHumaError` already passes through an error carrying its own status
(`rest/errors_test.go:139`). "Cannot complete an archived task" is a 409 the
verb writes in one line. Any other error is a 500 with the statement stripped,
as everywhere else.

**What is deliberately not declared:** preconditions, guards, state machines, or
any way to say *when* the transition is legal. The moment the DSL can express
"refuse if archived", it is expressing the transition, and vision.md's failure
mode is live. Refusing is what `Do` is for, and a one-line refusal in Go is not
the thing anyone is asking to be freed from.

**An action is part of the REST contract.** `restcompat.Capture` records the
declared actions and `sqlb impact` diffs them ([ADR-0039](0039-a-schema-edit-is-an-api-edit.md)):
removing an action, renaming its path, or adding a required body property are
breaks. `RESTManifest` gains them too, so an agent reading `sqlb.json` sees the
verbs rather than inferring a CRUD-only API.

## Consequences

**Buys.** Route coverage goes from roughly 40% of the evaluated application's
endpoints to roughly 90%, and — the part that matters more — the verbs reach the
TypeScript client, the Dart client, the CLI and the OpenAPI document, which is
where the review measured the drift actually living. About 1,400 lines of
envelope stop being written, and stop being written four times. The scoped fetch
stops being remembered by hand on the majority of routes, extending
[ADR-0030](0030-declared-scope-is-required.md)'s closure to where the exposure
is. And the row lock arrives by construction on every read-modify-write verb,
which is a correctness bug this design removes rather than a convenience it adds.

**Costs.** `Register` grows a parameter the first time a schema declares an
action, so every existing call site breaks — a compile error, the cheap kind, but
it lands in generated code that applications call and it wants a release note.
The schema gains a second kind of thing that is not a column, after
[ADR-0041](0041-computed-fields.md)'s computed fields, and a body declared in the
field vocabulary will want shapes columns do not have: a nested object, an array
of ids, a value that is not a column type at all. `Do` runs inside the
transaction, so a verb that calls a third-party API holds it, and behind
PgBouncer in transaction pooling mode that is occupancy rather than only latency
([ADR-0019](0019-pgbouncer-in-the-path.md)) — the answer is `AfterCommit`, and
the documentation has to say so before someone finds out.

**Where the boundary is not held.** A collection action obliges nothing, because
there is no generated fetch for a hook to constrain. Its body is application Go
with a transaction, which is exactly the position `sqlb.Query[T]()` is already
in ([ADR-0030](0030-declared-scope-is-required.md), *Where the boundary is not
held*). Roughly two in five of the evaluated application's verbs are
collection-level, so a large minority of the feature gets the ergonomics and
none of the safety argument. That is worth stating plainly rather than letting
the ADR-0030 inheritance claim cover the whole feature.

**The obligation count grows again.** `Scoped`, `SoftDelete`, computed `Needs`,
and now a nil `Do`. ADR-0041 already flagged that obligations accumulating faster
than they consolidate become a startup failure nobody reads; this is the fourth,
and it is the one that should trigger the consolidation rather than the one that
gets away with it.

## What would change our mind

- **The first three real actions want a body the field vocabulary cannot
  express.** Then the body is an application type after all, and the client
  emitters take its shape from a JSON Schema the application supplies rather than
  from the DSL. That is a bigger change than it sounds, because it is the
  difference between generating a typed client method and generating one that
  takes `unknown`.
- **`Writes` is declared and then worked around.** If verbs routinely mutate
  columns outside their declared set and issue their own `UPDATE` for the rest,
  the declaration is decoration; either drop it and persist the whole row, or
  make the undeclared mutation an error the tests catch.
- **Collection actions dominate.** If they outnumber item actions in practice,
  the safety argument covers the minority of the feature and this record is
  mostly an ergonomics one — which is fine, but it should say so instead of
  leading with ADR-0030.
- ~~**Actions become a place to put reads.** A `GET` action, or a `POST` that
  returns a computed report, means this is an RPC surface rather than a verb
  surface. That is a different feature with different questions about caching and
  the query key, and it needs its own record.~~ **Half fired.** The
  computed-report half fired, for a write's own response rather than a read —
  see the 2026-08-14 revision. The `GET` half has not: an action is still
  POST-only, and this record still declines to become a general RPC surface.
- **`Do` returning `*rest.Problem` makes applications import `rest` for their
  domain errors.** The escape hatch would have become a coupling, and the answer
  is a status-carrying error interface in a package with no HTTP in it.

## Cost of change

**Adding it is additive.** A schema that declares no action generates exactly
what it generates today, including `Register`'s current signature — which is why
the first action in a project is the only breaking moment and why this is not a
1.0 blocker.

**Removing one is not.** A `POST /{id}/complete` is part of the REST contract
[compatibility.md](../compatibility.md) freezes, and by the time it is worth
removing there is a deployed client with a named method calling it. The
asymmetry is the same one every wire-facing capability has, and it argues for
the same discipline: an action is cheap to add and should be added when an
application asks for it by name, not speculatively per table.

**The body's spelling is the expensive part.** The route is a URL and the
`Actions` field is a compile error, but the JSON body is a wire format from the
first deploy: renaming a property is a break for every client, and adding a
required one is too. If any part of this record deserves to be built narrowly and
widened later, it is the body vocabulary — start with the column types, and let
the first action that needs a nested object be the evidence that decides the
shape.

**Revisit when the third application declares actions**, or on the first one that
wants a body shape the field vocabulary cannot hold, whichever comes first. Those
are the two events that decide whether the declaration is right; anything before
them is this record arguing with itself.

## Revisions

- 2026-07-31 — Written, ahead of the code and at the author's request, against
  [#18](https://github.com/jryannel/sqlb/issues/18) and
  [§13.4](../review-adoption-existing-app.md) of the adoption review. Three
  things the issue's form does not have: `.Body[T]()` is not valid Go, so the
  body question is forced rather than optional; `rest` may not import `schema`,
  so the declaration crosses as a value the way `rest.Op` already does; and `Do`
  cannot live in a declaration that is serialised into `sqlb.json` and read by
  five emitters, which is what moves the binding to registration and gives the
  compiler a job. One thing the issue's framing understates: the row lock. Every
  verb is a read-modify-write, and generating the fetch is the only reason
  `FOR UPDATE` can arrive without anyone remembering it.
- 2026-07-31 — Built, and the record moves to Working. `schema.Action` with
  `Body`/`Writes`, `rest.Action` and `rest.CollectionAction`, a generated
  `Actions` struct and `Register` parameter, the verb in the TypeScript, Dart
  and CLI emitters, actions in `sqlb.json` and in the `sqlb impact` diff, and
  `example/tasks` completing a task against Postgres. Five things the design did
  not have, each found by building it:

  **`schema.Action` was already taken.** By the foreign-key referential action —
  `OnDelete(schema.Cascade)` — which this record's own `Decision` block did not
  notice. It is renamed `RefAction`, on the argument that two meanings of
  "action" in one package outlive everyone who could explain them and that this
  is the side almost nobody writes by name: the constants carry the meaning at
  every call site, and the whole rename touched four non-test files.

  **`Method` is gone, and its absence is the record being narrow on purpose.**
  Every legal value was POST. A `GET` action is the RPC surface *What would
  change our mind* already says needs its own record, and a `DELETE` one is a
  spelling of an operation CRUD has. A field with one legal value documents an
  ambition rather than a decision, and adding it later is additive.

  **The input type is emitted even when the action declares no body.** The
  design implied a body type only where there was a body, which would have made
  declaring the first property a change to the signature of the func the
  application already wrote. Emitting an empty struct costs a type nobody reads
  and makes `Body` genuinely additive. The *operation* still reads no request
  body — `ActionSpec.HasBody` decides that separately — because an empty struct
  registered as a required body would make the commonest verb there is refuse a
  request that carries nothing.

  **The summary cannot be defaulted where it is declared.** "Complete a task"
  needs the singular of the table name, and `schema` deliberately has no
  singulariser: codegen's is allowed to guess because a wrong guess in a Go type
  name is cosmetic, and one baked into an OpenAPI document is not. So the
  default is filled in downstream, and the collection form says "Purge archived
  tasks" rather than inventing an article for an endpoint that addresses no row.

  **`Writes` naming a `ReadOnly` column is correct, and worth saying out loud.**
  `example/tasks` completes a task by writing `status` and `completed_at`, and
  the second is `ReadOnly`. The two do not disagree: `ReadOnly` says no
  *request* may set the column, and the envelope writes it on the server's
  authority from the row the verb mutated — the same standing the `BeforeUpdate`
  hook that owns it today has. The validator refuses a computed column and the
  primary key, and lets this through.

  One thing the design got right that only the example proves: the escape hatch
  is load-bearing. `completeTask` writes a comment row through `sqlb.TxFrom`,
  the `BeforeCreate` hook supplies its `workspace_id`, and a refused completion
  rolls the comment back with it — none of which the declaration mentions.

- 2026-08-05 — **`Touches` added**, because the record's escape hatch turned out
  to be invisible from every surface that describes a route
  ([#149](https://github.com/jryannel/sqlb/issues/149)). This record already
  knew the shape of it — *"`completeTask` writes a comment row through
  `sqlb.TxFrom` … none of which the declaration mentions"* is the last line of
  the 2026-07-31 revision — and read it as a note about an example rather than
  as a gap in the contract.

  What made it one is that `Writes` is *reported* as complete, by three tools,
  with nothing to say a verb can exceed it: `sqlb impact` states it, the OpenAPI
  document carries it, and `--help` prints it. The CLI case is the sharp one,
  because [ADR-0029](0029-go-cli.md)'s argument for the CLI is that `--help`
  answers a caller with no compile step, "such as an agent" — and a declared
  write set of two columns invites exactly the inference that the verb is
  confined to one row. That inference can be wrong by ten tables, and the
  repository already treats a plausible wrong answer as worse than a warning.

  So `Touches []string` names tables beside `Writes`'s columns, and it is
  documentation with no enforcement behind it. Enforcing it would mean tracing
  application code the schema package cannot see; the alternative to an
  unenforced claim was not an enforced one, it was silence. Validation refuses
  only what says nothing — an empty name, a duplicate — and an unknown table is
  accepted, since the cross-module write is the case the field exists for.

  **Not a change to what an action covers.** The envelope stays declared and the
  transition stays in Go; the eleven-table command is still a hand-written
  command layer, and `Writes` naming columns on one row *should* understate it.
  What changed is that the generated documentation can now tell the two apart.

  The lock got the same treatment in prose rather than in code: the envelope's
  `FOR UPDATE` covers the row it fetched, and statements issued through
  `sqlb.TxFrom` take their own, in an order the application owns.
  [docs/rest/actions.md](../rest/actions.md) says so under the `TxFrom` example,
  which is where the reader is at the moment they are handed the transaction.

- 2026-08-14 — **The row-only response is being widened, narrowly, for the
  write side.** [#196](https://github.com/jryannel/sqlb/issues/196) named the
  concrete case this record's own trigger predicted: a quiz-grading action
  (`Lesson.submit`, evidence from a real port) grades a `QuizAttempt` and wants
  to answer `{passed, score}` in the same round trip. It can't — `run`
  (`rest/action.go:268`) always returns `*itemOutput[T]`, and the doc comment
  at `rest/action.go:218-221` says so outright: "answers 200 with the row." The
  issue is explicit that it is evidence for this conversation and not a design
  for it, which is what this entry is for.

  **Decision: open the door, but only as far as the write side needs it.** The
  fired trigger names two things and they carry different risk. A `GET`
  action is still declined — nothing here proposes one, and the caching and
  query-key questions this record raised for a *read* RPC surface are real and
  still unaddressed. A `POST`'s response outgrowing the row is a smaller
  thing: the request was already a write, already not cacheable the way a
  `GET` response is, and the envelope already has the transaction open and the
  mutated row in hand at the moment it marshals the answer. What #196 asks for
  is not "let an action read arbitrary state" — it is "let the answer to the
  write it just did say more than the row." That is a report attached to a
  verb, not an RPC surface.

  [ADR-0057](0057-a-read-is-a-query-and-a-row-scoped-write-is-a-mutation.md),
  decided the same day as this entry, already generalised the sibling case on
  the read side: `rest.Query`'s `Do` returns `(Out, error)` for an arbitrary
  `Out`, not `[]T`, at the `rest` package level — codegen still fixes it to
  `[]T`, which that record names as its own open cost, but the `rest`
  primitive underneath does not assume the response is the model. Widening
  `Action`'s item form the same way — `do` becoming
  `func(ctx, *T, In) (Out, error)` in place of `func(ctx, *T, In) error`, with
  `Out` defaulting to `row[T]` so every action declared today keeps compiling
  and keeps answering exactly what it answers now — is the same move applied
  to the write side, not a new one invented for this issue.

  **What this does not settle**, and is future work rather than this entry's
  job to finish: whether `Out` needs a declaration in the field vocabulary the
  way `Body` does, or stays an application type outside the schema's reach —
  and if it stays outside, this record's own reasoning against that choice for
  `Body` applies again: a shape sqlb cannot see is a TypeScript client method
  typed `unknown`, which is the drift this whole feature exists to close, not
  reopen. Whether `Writes` still persists from the mutated `*T` when `do` also
  returns a separate computed `Out`, or whether the two are now one value.
  Whether a widened action reaches `sqlb.json`, `restcompat`, and the
  TypeScript, Dart and CLI emitters the way `Body` and `Writes` already do, or
  ships Go-only the way `Query` currently does. And whether an action with a
  widened `Out` is still one surface next to CRUD, or the first crack in that
  boundary — this entry's answer is that it is still one surface, because the
  envelope's fetch, lock, transaction and persist step are all unchanged and
  only the final marshal is what `Out` touches, but that answer is worth
  checking again once a second and third action actually reach for it, the
  same test [ADR-0057](0057-a-read-is-a-query-and-a-row-scoped-write-is-a-mutation.md)
  applied to `Mutation` and found wanting.

  Status moves to Revisiting rather than staying Working, because nothing
  built changes today — `example/tasks` still runs the row-only shape against
  Postgres — but the direction is decided and the record should read as under
  active reconsideration rather than settled.
  [#218](https://github.com/jryannel/sqlb/issues/218) tracks the
  implementation; [#196](https://github.com/jryannel/sqlb/issues/196) is
  closed by this revision, since its own stated scope was to be evidence for
  this conversation, and that conversation has now happened.
