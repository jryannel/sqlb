# Releases

Every tagged release, newest first.

Each entry is the annotated tag's own message, marked up for the web — `git show
v0.5.0` prints the original. That is the arrangement rather than a hand-kept
changelog because a tag message cannot go stale: it is written where the release
is made and is immutable once pushed, so the only way this page can be wrong is
by missing a version, which is one failure rather than five.

[Compatibility](compatibility.md) says what is frozen and what is expected to
move. Semantic versioning applies from `v1.0.0`; until then a minor bump may
break a surface listed there, and the break is described here with the
mechanical edit that fixes it. [The road to 1.0](release-1.0.md) says what has
to be true before that promise becomes permanent.

## v0.7.0

2026-08-01 · [tag](https://github.com/jryannel/sqlb/releases/tag/v0.7.0)

One break, and it is the one this library most needed to make before anyone
depended on it: **there is no default hook registry**
([ADR-0047](adr/0047-no-default-hook-registry.md)).

Hooks are the rules that confine what a query may see, so they were also the
one surface where ambient state could decide a tenant boundary. `On[T]()`
registered into a package-level default, `New(exec)` handed every handle that
same default, and `OnIn[T](r)` — the form that says where the rules land —
carried the longer name. [compatibility.md](compatibility.md) had listed the
hazard under *Will move* for two releases, in the words it turned out to
deserve: which registry a statement uses is decided by the dynamic type of the
executor passed to it.

What made it a release rather than a note is that it cost an adopter a tenant
boundary. Moving an application onto a per-application registry left one module
still calling `On[T]()`, so that module's rules were no longer on the handle it
queried through — and it still compiled, still mounted, and still answered,
with every tenant's rows in the response. Both spellings were valid, and the
wrong one was shorter.

So the default is gone. `sqlb.New` gives each handle an empty registry of its
own, `On[T](r)` is the only registration form, `rest.PublishChanges[T](r, p)`
takes the registry too, and an `Executor` that is not a `*sqlb.DB` resolves to
a registry nothing can register into — a statement against a bare pool is
unconfined, and says so.

**The mechanical edit**, and every one of them is a compile error rather than a
behaviour change, which is the point: the failure this prevents is silent, so
its migration must not be.

```
On[T]()               →  On[T](reg)
OnIn[T](reg)          →  On[T](reg)
PublishChangesIn[T]   →  PublishChanges[T]
sqlb.New(pool)        →  sqlb.New(pool).WithHooks(reg)
```

There is no shim, because a shim is the ambient registry under a new name.

What goes with the default is its bookkeeping. `Hooks.Reset` survives with its
reason rewritten — a test gets isolation from `NewRegistry`, which cannot be
forgotten in a teardown — and the `sync.Once` guards, `t.Cleanup(Reset)` pairs
and "has anything registered yet?" checks that existed to manage a registry
nobody asked for are deleted. The examples had all left already, which is the
tell: `example/tasks` built its own registry and said why, and the fx kit never
used the default at all.

**What this does not fix**, named in the record rather than discovered later: a
registry nothing attaches. `On[T](reg)` compiles whether or not any handle
carries `reg`, so hooks can still be registered where nothing runs them. That
is strictly narrower — the registry and the handle are usually adjacent
expressions rather than action at a distance — and the case that matters is
still caught at the mount, because a model declaring `Scoped` is refused when
the handle's registry has no hook for it
([ADR-0030](adr/0030-declared-scope-is-required.md)).

## v0.6.0

2026-08-01 · [tag](https://github.com/jryannel/sqlb/releases/tag/v0.6.0)

The release an issue tracker wrote. Twenty issues were filed against the request
path on 31 July from an external review and an adoption port; this closes the
last of them, plus six more raised the day after by an adoption that got far
enough to find sharper ones. The theme is not a feature — it is that most of
what a real consumer hit was a default that was right for the schema and wrong
for the reader.

Three breaks, and the first is the one to read.

**A computed column is opt-in.** It is declared on the model, and the model is
shared, so projecting every declared one charged every reader for the most
expensive one — three correlated subqueries attached to an existence check by
id — and a column declaring `Needs` made that check *fail*, demanding a `viewer`
bind from a query with no business supplying one. Nothing projects a computed
column now unless it asks:

```go
sqlb.Query[Project]().WithComputed("total_tasks", "is_starred")
rest.Options{Computed: []string{"total_tasks", "is_starred"}}
```

The mechanical edit is `WithComputed` on a hand-written query and `Computed` on
a hand-written mount. A *generated* resource opts into its table's own computed
columns, so generated endpoints answer exactly as they did; what changes is
everything else reading the same model, which is where the bug was. For a
resource it is a boundary rather than a projection setting — a column the
resource does not select is not filterable, sortable or nameable in `?select`
there either, because a filter on a correlated subquery costs what the
projection would have. The obligation moved with it: `rest.Resource` used to
refuse any mount whose *model* declared a `Needs` column, and now asks only of
the resources that render one.

**The generated Go client is its own package.** A program that wanted the typed
client took [spf13/cobra](https://github.com/spf13/cobra) and a whole command
tree with it, so a sync job made one HTTP request at the cost of a command-line
framework. `cli/client/client_gen.go` now carries `Client`, `Request`,
`Transport`, `Do` and `Run` against the standard library and nothing else, and
`cli/cli_gen.go` is the cobra tree importing it. Regenerate, then the edit is in
the four-line main: `&cli.Client{…}` becomes `&client.Client{…}` from the new
package. `ClientDir` emits the client with no command tree at all, which is the
server-to-server case; `CLIDir` emits both and defaults the client into a
`client/` subdirectory, so a project that set only `CLIDir` keeps working.

**A nil member of `OneOf` widens the set.** `IN (NULL)` is never true, so a set
assembled from nullable values came back quietly narrower than the caller wrote;
the nil member now contributes `IS NULL` instead. A set with no nil in it
renders byte-identical, and generated endpoints never reach it — this is
hand-written Go. `NotOneOf` is deliberately unchanged, and now says why on
itself.

One thing that is not a source break and will still stop a build: every
generated struct tag gained the column's logical type, so `sqlb generate` has to
run before `sqlb check` passes. That tag is what fixed the expansion bug below.

Worth stating rather than leaving to be noticed: none of the three was listed
under *Will move*. [compatibility.md](compatibility.md) says a minor bump may
break a surface listed there and that each break is described here with its
mechanical edit — half of that promise is kept above and half is not, because
all three came out of consumer reports rather than a plan. The document now
records them where the announcement should have been, which is the correction
available after the fact.

What landed, beyond the breaks:

- **A change feed, as a transport.** `rest.Events` mounts an SSE stream through
  huma's sse package, so it lands in the OpenAPI document typed rather than as
  untyped text; `rest.Broker` is the in-process source behind a `rest.Source`
  seam the outbox implements later. A subscriber receives `{table, key, op}` and
  refetches, because a payload built outside the subscriber's context would skip
  the resource's `BeforeQuery` scope and hand one tenant's rows to another.
  Correct on one replica and quietly wrong on two, which is the first thing its
  doc comment says. [ADR-0045](adr/0045-the-stream-is-a-seam.md).
- **The filter tree gained `not`, and containment gained its negation.** `nhas`,
  `nhasany`, `nhasall` and `nhasdoc` exist because the URL grammar conjoins by
  design and has nowhere to put a `not` — shipping only the tree would have left
  the two frontends compiling different vocabularies, which is the one thing
  [ADR-0003](adr/0003-one-ast-two-producers.md) claims they do not. A negation
  is not a complement: each compiles to `NOT (…)`, so a NULL column matches
  neither `has` nor `nhas`, exactly as `nin` already behaved.
- **`ON CONFLICT DO UPDATE` assigns an expression.** An upsert could only copy
  the proposed row, so `updated_at = now()` had to come from the application
  clock and a counter could not be written at all. `OnConflictSet` takes any
  expression, and a column reference inside one has to say which row it means —
  `Excluded` or `Current` — because `count = count + 1` reads like an
  accumulation whichever side SQL silently picks.
- **Where NULLs sort is declared on the column.** Postgres's default is not one
  placement but two, `NULLS LAST` ascending and `NULLS FIRST` descending, so a
  feed ordered by a column that is NULL until a row is published lifted every
  draft to the top. `Sortable(schema.NullsLast)` fixes it once, in both
  directions, for every caller including the generated clients — which need no
  new syntax for it. The cursor carries the declared placement, so cursors
  issued before this release still decode and one issued under a since-changed
  declaration is refused rather than mispaged.
- **An expanded row is the same shape as a direct one.** Expanding a relation
  whose target had a `date` column answered 500: `json_build_object` serialises
  a date as `"2026-07-01"` and the Go field is a `time.Time`, which parses
  strictly as RFC 3339. Cast to UTC midnight now — `::timestamp AT TIME ZONE
  'UTC'`, not `::timestamptz`, which resolves through the session zone and loses
  a day east of UTC.
- **`?search` can reach past the row.** A `Searchable` computed column of text
  type is now legal, so a chat named in the UI by its participants — a direct
  message has no name of its own — is findable by a participant's name. The
  refusal that blocked it gave a reason about type that the "Searchable requires
  a text column" rule already made; what it actually cost was the only way to
  search across a relation. The cost objection is answered by the opt-in above.
- **An adoption's declarations.** `numeric(p, s)`, index column ordering, a
  foreign-key cycle broken with an `ExternalRef` instead of dropped, and a
  self-referential FK that no longer reads as permanent drift — the four things
  that made a drift gate against a live database un-buildable.
- **The request path's bounds.** `?page=`/`?offset=` are capped and no longer
  overflow; a repeated single-valued parameter is refused rather than silently
  dropped; `POST` and `PATCH` reject unknown query parameters like every other
  operation; a multi-row insert decides default-omission per row rather than per
  statement.
- **`sqlbfx`**, an fx module over the same handles, and a principal seam so a
  core-style app takes `Handles()` only.

What it cost. `FromGo` is **cut** rather than pending:
[ADR-0041](adr/0041-computed-fields.md) wrote the condition — "if the first two
applications express everything in SQL" — and both did, so the record says so
and closes [#17](https://github.com/jryannel/sqlb/issues/17) with the evidence
rather than leaving a fourth tier in the tracker. The change feed is correct on
one replica and loses a publication if the process dies between `COMMIT` and the
fan-out; both are stated where a reader meets them rather than in a footnote.
And a `time` column has the same expansion defect a `date` column had, unfixed
on purpose: nothing round-trips one, so casting it would have been a guess.

## v0.5.0

2026-07-31 · [tag](https://github.com/jryannel/sqlb/releases/tag/v0.5.0)

Three things the adoption review ranked, built: a computed column, a declared
action, and the exit. Between them they answer the two objections that were not
about missing features — that one derived value pushed an entity off the
generated path entirely, and that sqlb owns too much to be reversible.

One break, and it is a rename. `schema.Action` is no longer the foreign-key
referential type; that noun went to the domain verb below, and the type is
`schema.RefAction`. The constants every call site actually writes —
`schema.Cascade`, `schema.SetNull` and the rest — are unchanged, so a schema
breaks only if it named the type, and the mechanical edit is `schema.Action` →
`schema.RefAction` in a foreign-key position. [compatibility.md](compatibility.md)
announced it under *Will move* and now records that it landed.

**A computed column is an expression.**
[ADR-0041](adr/0041-computed-fields.md), three of its four tiers:

```go
schema.Computed("is_overdue", schema.TypeBool,
    schema.FromSQL("due_date < current_date AND open_tasks > 0")).
    Filterable()
```

One interception point, as the record's trace predicted: every consumer already
resolves through a `*ColumnInfo` and renders through the compiler's column, so
substituting the expression there puts the value in the projection, the `WHERE`
and the `ORDER BY` at once. The parameterised tier takes
[ADR-0030](adr/0030-declared-scope-is-required.md)'s shape — `Needs("viewer")`
declares the bind, a `BeforeQuery` hook supplies it through `Builder.Bind`, and
`rest.Resource` refuses to mount when nothing does. Without that refusal an
unbound expression renders `member_id = NULL`, returns false for every row
forever, and looks exactly like a feature that works. No DDL in either
direction, so converting a stored column into a computed one proposes the drop.
`FromGo` is **cut**, not pending — ADR-0041 set the condition "if the first two
applications express everything in SQL", and both did. Nothing in the tree
reaches for it and nothing outside it did either.

**A declared action generates the envelope, and the verb stays plain Go.**
[ADR-0043](adr/0043-declared-actions.md), against the 26 item verbs and ~20
collection verbs the evaluated application had, and the ~30 lines of identical
envelope written four times over before any domain logic:

```go
Task.Action(schema.Action{
    Name:   "complete",
    Body:   schema.Body(schema.Text("note").Nullable()),
    Writes: []string{"status", "completed_at"},
})
```

serves `POST /tasks/{id}/complete` and asks `Register` for one func. The id, the
scoped fetch, the 404, the body, the transaction, the row lock, the write set
and the response are generated; the transition is not. `Writes` is enforced
rather than documented — exactly those columns, off the row the verb mutated —
and it is what makes the fetch take `FOR UPDATE`, since every one of these is a
read-modify-write across a round trip. The verb reaches the TypeScript, Dart and
CLI emitters, `sqlb.json` and the `sqlb impact` diff, where removing one is
breaking and adding one is additive. There is no `Method` field: every legal
value was `POST`. And the hole is named rather than papered over — a collection
action fetches nothing, so it obliges no hook, and that is two in five of the
measured verbs.

**`sqlb eject` writes [the way out](eject.md).**
[ADR-0042](adr/0042-the-exit-is-generated.md), and the answer to the objection a
pre-1.0 library with no consumers cannot answer with a promise: sqlc and chi are
cheap to reverse because they own almost nothing, while sqlb owns the schema,
the migrations, the wire format, the client and the CLI. `sqlb eject ./schema`
generates a package that imports pgx and the standard library and nothing else —
the DDL, the row structs without their `sqlb` tags, one function per statement
with the SQL written out, `net/http` handlers, and a README saying what came out
and what did not. Deleting sqlb from `go.mod` afterwards is a supported end
state.

The fidelity line is between the surface and the engine. Out whole: CRUD and
list at the same paths with the same status codes and the same envelope, every
filter operator that is one SQL fragment, `?sort`, `?search`, `?page`,
`?per_page`, `?count=exact`, the declared ceilings and the RFC 9457 error shape.
Not out, and refused with a 400 that says so rather than ignored: keyset
cursors, `?select`, `?expand`, the JSON filter tree, and the array and document
operators — reproducing those would mean emitting a copy of sqlb, which is a
fork with a different import path rather than an exit. Two properties survive
the loss of the machinery they were implemented in: capabilities stay opt-in, so
a column that never declared `Filterable` is not filterable in the exit and a
`Hidden` one has no spelling at all, and
[ADR-0030](adr/0030-declared-scope-is-required.md)'s obligation stays compulsory.
The load-bearing half is `pgtest/eject_test.go`, which stands the committed exit
beside the generated resources it came from, points both at one database, sends
both the same requests and compares the bodies byte for byte.

**Adopting an existing database** is where the rest of the work went. Each of
these made a schema-vs-database gate propose migrations nobody asked for, which
is the failure that teaches people to stop reading the gate:

- `IndexNamed` and `UniqueIndexNamed` declare an index under the name the
  database already gave it. The name is not inert — Postgres reports a violated
  constraint by name, so renaming a unique index turns a handled collision into
  an unhandled 500 without touching the code that handled it. The generated
  migration says so now.
- `ExternalRef(...).Enforced()` emits a real `FOREIGN KEY` against a table this
  schema has not declared, which is the thing an incremental adoption always has
  to say and had no spelling for. What it gives up is what
  [ADR-0015](adr/0015-module-isolation.md) bought by refusing the constraint: two
  modules joined this way can no longer be migrated independently, so it is
  opt-in and unenforced stays the default. `introspect` imports foreign keys this
  way, which is what stops a gate proposing `DROP CONSTRAINT` forever.
- A `jsonb` default is compared as a document, so `'{"a":1,"b":2}'::jsonb` and
  `'{"b": 2, "a": 1}'` are one default — which is what Postgres thinks too, since
  `jsonb` stores a parsed value rather than the text it arrived as. Only for
  `jsonb`; on a text column those are two strings, and the test says so.

**The round trip is a fixpoint**, asserted rather than assumed. `introspect`,
`RenderSchema` and `Diff` were each well tested and nothing checked that they
agreed with one another about one schema, which is why none of their own tests
could see the three disagreements that fell out. `RenderSchema` could not write a
vector column at all, so a 69-table database could not be turned into 69
declarations to review on account of one column; an index lost its operator class
and storage parameters, which for pgvector Postgres rejects outright, since the
class selects the distance function and there is no default; and an enum's
`CHECK` lost its name, so every later diff proposed dropping and re-adding it.
The gate applies an awkward schema, reads it, renders it back to source that must
compile, rebuilds a second database from what was read, and compares the two
through `pg_catalog` — databases rather than registries, because two registries
agree about everything they both dropped.

**A family of codegen import bugs**, in both directions and all with one cause:
`format.Source` parses without type-checking, so an import that is named but
missing, or present but unused, is valid Go source that fails only at the
consumer's compiler. Three were `jsonb`-shaped and the rest were found by
auditing the whole set — a read-only resource importing `time`, a table whose
patchable columns are all nullable importing `errors`, a schema whose only uuid
column is a primary key importing `google/uuid`, a hidden timestamp named by the
typed update with nothing importing `time`, and a nullable vector matched against
a hand-maintained list of type spellings that was one short. Beside them, a
nullable `jsonb` create body assigning a pointer into a non-pointer field. The
general guard is `TestGeneratedGoCompiles`: eight schema shapes generated into a
scratch package and handed to one `go build`, so the compiler decides rather than
a substring assertion naming the mistake in advance.

## v0.4.0

2026-07-30 · [tag](https://github.com/jryannel/sqlb/releases/tag/v0.4.0)

The release [ADR-0040](adr/0040-the-driver-is-a-dependency.md) was announced for.
`v0.3.0` said the driver question had been decided and that nothing of it was
built; this is it built. sqlb depends on pgx v5, `database/sql` is not the
contract, and `Executor` — Frozen in [compatibility.md](compatibility.md) — broke
on purpose, before the tag that would have made the same work a major version and
a hand migration for everybody.

The mechanical edit is at the seam: pass a `*pgxpool.Pool` where a `*sql.DB` used
to go. `*pgx.Conn` and `pgx.Tx` satisfy `Executor` as they stand, and the last of
those is what this was for — sqlb writes now join a transaction the application
opened itself, which two handles over one pool could never do. `sqlb.New(tx)`
knows it is inside one and deliberately does not take the boundary over, so
`AfterCommit` refuses there rather than queueing callbacks behind a commit sqlb
will never perform. Something that still wants a `*sql.DB` — goose, sqlc — gets
one from `stdlib.OpenDBFromPool` over the same pool, and the examples are written
that way because that is the shape a real adopter lands on.

Two surfaces disappear with it. `sqlb.EncodeArray` is gone with nothing in its
place, and the 449-line array-literal codec behind it: a `[]string` binds as
`text[]` and scans back from one because pgx does that. `SetErrorClassifier`
stays, but the case it was written for is now the default — `ConstraintError`
carries the constraint name, table, column and detail read off `*pgconn.PgError`,
with nothing registered.

The second break is smaller and shows up on the next regeneration. A nullable
`jsonb` column's model field is `*json.RawMessage`: it was the one column whose
generated type did not say it could be NULL. It was also unreadable through
`database/sql`, which is how it was found in a real port — that half no longer
reproduces, because taking pgx replaced the executor that had the gap.
Regenerate, and the compiler names the call sites.

Additive, and new:

- A `jsonb` column is filterable. `?metadata=hasdoc.{"lang":"de"}` compiles to
  `@>`, subset containment rather than equality, so a document carrying more keys
  than the filter named still matches. Not spelled `contains`, for the third time
  and for the same reason: that name is the text substring operator, and one name
  dispatched on column type is the ambiguity the generated clients exist to
  remove. A document column takes `hasdoc`, `isnull` and `notnull` and nothing
  else — there is no bare-value shorthand, and the ordering and pattern operators
  would answer rather than refuse, which is worse.
- A vector column. `schema.Vector("embedding", dim)` stores a pgvector embedding,
  `sqlb.Near` yields the score, the ordering and an `AtLeast` threshold from one
  call rather than three that must agree, and `RegisterVectorType` puts the binary
  codec on the connection — a pgx API with no `database/sql` spelling, and one of
  ADR-0040's arguments. The column is `Hidden` and not optionally so. There is no
  index kind and no REST search operation: a similarity search is an exact scan
  over the rows a filter already selected.
  [ADR-0026](adr/0026-vectors-declare-their-index.md) stages the index as a second
  decision and stays *Exploring*.

Fixed, most of them found by adopting sqlb over something that already existed. A
`VARCHAR(n)` default round-tripped as an expression, so `Diff` proposed the same
`ALTER` on every run and the drift gate stayed red for a reason that was not real.
A schema package under `internal/` could not be read by the generator.
`attgenerated` was misread after the flip, so every column of every imported
database looked generated. A rejected write arrived as "none of the result columns
map to T". And `sqlb generate`'s scratch directory survived an interrupted
compile, into somebody's `git add -A`.

The rest is evidence rather than surface. ADR-0026's physical claims about
pgvector are measured now instead of read out of documentation, and a fourth was
added: the planner may decline the ANN index, which makes the silent under-return
conditional on statistics nobody watches.
[ADR-0041](adr/0041-computed-fields.md) decides computed fields, including the
per-viewer tier a static SQL string cannot express, and builds none of it.
`example/recipes` is 86 Go example functions, one point each, whose printed output
is compared on every `go test` — so a recipe describing an API that changed fails
the build instead of misleading the next reader.

See [the driver](compatibility.md#the-driver), which says what the break bought
and what it cost, in both directions.

## v0.3.0

2026-07-30 · [tag](https://github.com/jryannel/sqlb/releases/tag/v0.3.0)

No API change. What this release carries is a decision, the seam that makes it
buildable, and the test coverage that was holding it up.

[ADR-0040](adr/0040-the-driver-is-a-dependency.md) decides that the engine will
depend on pgx and that `database/sql` stops being the contract — a break to
`Executor` that lands before 1.0 or not at all. Nothing of it is built yet. Read
[the driver](compatibility.md#the-driver) before pinning: the interface every
terminal call takes is going to change, and this is the release that says so in
advance rather than the one that does it.

The enabling refactor is here: the scanners read an internal `rowSource`
interface instead of `*sql.Rows`, which is correct under either answer and turns
the eventual migration into an adapter rather than a rewrite of scan and mutate.

`pgtype` values — `pgtype.Date`, `pgtype.Timestamptz`, `pgtype.UUID` — are now
covered in both directions including NULLs, with compile-time assertions that
fail the build if a pgx release ever drops `sql.Scanner` or `driver.Valuer`. That
path is load-bearing for adopting sqlb over existing sqlc structs and was
previously tested only with `sql.NullTime`.

The `go` directive drops from 1.25.7 to 1.25.0. It was patch-pinned by `go mod
init` rather than by any requirement, and pinning it forced every consumer onto
that exact toolchain.

Also: a documentation pass that closed the open ends across the ADRs and the six
review reports, and a nested `rest` module that was built and reverted within the
day — huma stays the default HTTP path, in the same module.
[ADR-0007](adr/0007-generated-rest-handlers.md) records why.

## v0.2.0

2026-07-30 · [tag](https://github.com/jryannel/sqlb/releases/tag/v0.2.0)

The first release with a transaction handle, and the first that a consuming
application can depend on without a local `replace`.

`v0.1.0` predates `db.go` entirely: `sqlb.DB` and `sqlb.New` — the handle every
data layer takes — landed after it, along with array columns, codegen type
overrides, a JSON filter tree, schema-impact diffing, and a fix for an expansion
carrying its target's scope onto the join.

Cut for the studio-apps port, which could not compile against `v0.1.0`.

## v0.1.0

2026-07-27 · [tag](https://github.com/jryannel/sqlb/releases/tag/v0.1.0)

The first tagged release.

Pre-1.0. Semantic versioning applies from `v1.0.0`; until then a minor bump may
break a surface listed as moving, and the release notes carry the mechanical edit
that fixes it.

Frozen, because other systems couple to them:

- `Executor` — `QueryContext` and `ExecContext`, nothing more
- The filter grammar — the URL syntax and its operator names
- Already-applied migrations are never reinterpreted

Expected to move, named in advance:

- Hook registration, when the transaction-scoped handle scopes the registry that
  `sqlb.On[T]()` reaches today
- Terminal call signatures, when Go 1.27 allows methods to declare type
  parameters — additive, the functions stay
- `?expand`, which is refused at startup until the joins land

See [compatibility.md](compatibility.md).
