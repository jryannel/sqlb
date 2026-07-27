# Review — adoption readiness, and the sqlc question  (2026-07-27)

The question asked was *"would you use sqlb over sqlc, and if not what is
missing?"* This records the answer and the work that would change it.

Read it as a snapshot of one reviewer's judgement at `ec55b85`, not as a
verdict. Where a finding names a file and line, the claim was checked against
the code; where it is a judgement call, it says so.

## Verdict

**Not yet, and "over sqlc" is the wrong axis.** They do not compete for the same
work, and [vision.md:91](vision.md) already says so — sqlb "should stay useful
alongside sqlc rather than demanding all of a codebase." That is the correct
position, and the review does not argue with it.

The narrower comparison is the honest one:

| Work | Choose | Why |
|---|---|---|
| Static queries, typed at compile time | **sqlc** | Its whole guarantee; sqlb's is weaker by design ([ADR-0009](adr/0009-typed-column-facade.md)) |
| Reporting, recursive CTEs, window functions | **sqlc** | sqlb sends these to `Raw`, which is declared a non-goal, not a gap |
| A filter/sort/search list endpoint | **sqlb** | sqlc structurally cannot express a conditional `WHERE`; this is the reason sqlb exists |
| Multi-tenant scoping across every read | **sqlb** | `BeforeQuery` is genuinely load-bearing and has no sqlc equivalent |

So the interesting question is not *which one*, it is *what stops sqlb being
used for the third and fourth rows today*. Two things: its age, and a
transaction story that has not been built.

**Age dominates everything below.** 32 commits, all dated 2026-07-27, no tags,
no released version, one author, no observed consumers. Nothing in this document
outweighs that, and no amount of feature work substitutes for elapsed time under
real traffic. Findings 1–3 are what would be worth fixing *while* that time
passes.

## What holds up

Stated because it calibrates the rest, not as praise.

- **One AST, two producers** ([ADR-0003](adr/0003-one-ast-two-producers.md)) is
  the real idea here, and it is intact: the URL grammar and Go code compile to
  the same predicates, so `BeforeQuery` constrains both. Nothing else in the Go
  ecosystem does this.
- **Capabilities opt-in** ([ADR-0006](adr/0006-capabilities-are-opt-in.md)) is
  the right answer to the PostgREST failure mode, and it is enforced in the
  places that matter — a `Hidden` column is absent from the OpenAPI schema, the
  filter vocabulary and the rejection allow-list alike.
- **The engine depends on the standard library alone**, and `deps-check`
  enforces it with an understanding of why `go list -deps` would otherwise lie
  ([ADR-0015](adr/0015-module-isolation.md)). That is a real constraint held
  properly.
- `mise run test` is green across all nine packages, and `pgtest` proves the
  DDL against a live Postgres *and* through PgBouncer in transaction pooling
  ([ADR-0019](adr/0019-pgbouncer-in-the-path.md)).

## Findings

### 1. There is no transaction story above `Executor` — **FIXED 2026-07-27**

> Resolved after this review was written, and along the lines it recommended:
> `sqlb.DB` carries an executor and a hook registry, `WithTx` runs a unit of
> work with rollback on error and on panic, and `TxFrom(ctx)` lets a hook read
> what its own transaction has written. One thing came out better than the
> recommendation predicted — because `*DB` satisfies `Executor`, no call site
> changed and `rest.Resource` needed no `*DB` parameter at all.
> [ADR-0020](adr/0020-transaction-scoped-handle.md) records the decision and
> what it costs. The finding is kept below as the argument that motivated it.

`Executor` ([exec.go:17](../exec.go)) is `QueryContext` + `ExecContext`, which
`*sql.Tx` satisfies, so the plumbing works. Nothing sits on top of it. There is
no `WithTx`, no way to run a multi-statement unit as one, and — the sharper
problem — the hook registry is a package-level `sync.Map`
([hooks.go:39](../hooks.go)), so hooks cannot carry per-transaction state. A
`BeforeQuery` that needs to see uncommitted rows from earlier in the same unit
of work has no way to know it is in one.

The README defers this to Go 1.27, on the grounds that `db.Query[T]()` needs
generic methods on concrete types. That reasoning is right about the
*ergonomics* and wrong about the *scoping*: the object graph does not need
generic methods, only the call syntax does.

**Recommendation.** Build the handle now with package-level generic functions,
and let 1.27 add the methods additively exactly as the README's table predicts:

```go
type DB struct { exec Executor; hooks *Registry }

func New(exec Executor) *DB
func (d *DB) WithTx(ctx context.Context, fn func(*DB) error) error
func QueryIn[T any](d *DB) *Builder[T]     // becomes d.Query[T]() in 1.27
```

`QueryIn[T](tx)` is uglier than `tx.Query[T]()`. It is the same object graph,
and it unblocks findings 2 and the `rest` layer's participation in a caller's
transaction, three years before the toolchain is safe to require.

**Cost of change.** Moderate but bounded. `sqlb.On[T]()` becomes a wrapper over
a process-default registry, so no existing call site breaks; `rest.Resource`
grows a `*DB` where it takes an `Executor` today. Deferring costs more later —
every month of adoption adds call sites that assume the global.

### 2. `AfterCreate` runs inside the transaction, and there is no `AfterCommit` — **FIXED 2026-07-27**

> Resolved, and close to the thirty lines this predicted. `WithTx` holds the
> callback list and drains it after `Commit` returns nil; `sqlb.AfterCommit(ctx,
> fn)` is the form a hook uses. Two things this finding did not anticipate:
> registering outside a transaction is *refused* rather than run immediately,
> because under autocommit the callback's timing would depend on which hook
> registered it; and `ErrAfterCommit` distinguishes a failed side effect from a
> failed unit of work, since retrying a durable write would double it.
> [ADR-0012](adr/0012-change-feed-outbox.md) now says why this is not the change
> feed.

[hooks.go:79](../hooks.go) documents that `AfterCreate` "runs inside the
caller's transaction, so returning an error rolls the [operation] back." That is
correct for validation and wrong for the single most common use of an
after-hook: publish an event, enqueue a job, invalidate a cache — all of which
must not fire if the transaction aborts, and all of which currently have
nowhere to go.

The full hook set is `BeforeQuery`, `Before`/`AfterCreate`,
`Before`/`AfterUpdate`, `Before`/`AfterDelete`. No commit-scoped hook exists.

[ADR-0012](adr/0012-change-feed-outbox.md)'s outbox is the eventual answer, but
it is item 4 on the roadmap and this is needed before item 1 ships.

**Recommendation.** Once finding 1 lands, `WithTx` holds a deferred callback
list and drains it after `Commit` returns nil. Roughly thirty lines, and it does
not prejudge the outbox design — the outbox becomes one registered
`AfterCommit` among others rather than the only way to observe a write.

**Cost of change.** Low, and lower now than after the outbox exists, because
retrofitting `AfterCommit` around a shipped outbox means deciding whether the
outbox is a hook or a peer of hooks. Decide it while only one of them exists.

### 3. `?expand` is accepted, advertised, and silently does nothing — **FIXED 2026-07-27**

> Resolved after this review was written. `rest.Resource` now rejects a
> non-empty `Options.Expandable` at startup, `filter.Apply` fails the builder
> rather than dropping a parsed `?expand`, and the manifest reports neither the
> capability nor the relation names. The finding is kept below as the record of
> what was wrong, since the same trap returns the moment expansion is
> implemented halfway. **When the joins land, the three guards and the inverted
> assertion in `schema/lint_test.go` are the checklist of what to turn back on.**

This is the one finding a user would hit as a bug rather than a limitation.

- `schema.Ref(…).Expandable()` is declared in the shipped example
  ([example/blog/blogschema/schema.go:30](../example/blog/blogschema/schema.go)).
- `schema.Validate` checks it is on a `Ref` and module-local
  ([schema/registry.go:199](../schema/registry.go)); `schema.Lint` checks the
  foreign key is indexed ([schema/lint.go:125](../schema/lint.go)). Neither says
  the join does not exist.
- `BuildManifest` advertises `expand` in the operator vocabulary and lists the
  relation names ([schema/manifest.go:157](../schema/manifest.go),
  [manifest.go:220](../schema/manifest.go)).
- `rest` puts it in the OpenAPI document
  ([rest/params.go:97](../rest/params.go)).
- The parser accepts it and writes `Query.Expand`
  ([filter/filter.go:202](../filter/filter.go)).
- **`filter.Apply` never reads `Query.Expand`**
  ([filter/filter.go:119](../filter/filter.go)). It is written in exactly one
  place and read in none.

So the request succeeds, returns 200, and the relation is simply absent. The
README's mitigation — "`rest.Options.Expandable` should stay empty" — is a
convention, not a mechanism, and it is contradicted by the manifest, which
publishes the relation names to anything reading it. An agent driving the API
off `sqlb.json` will generate requests that quietly return less than they ask
for.

**Recommendation.** Until the joins land, fail loudly at the earliest point that
knows: have `schema.Validate` reject `Expandable` outright, or have
`rest.Resource` return an error when `Options.Expandable` is non-empty, and drop
`expand` from the manifest vocabulary. A capability that can be declared and
does nothing is worse than one that does not exist, because the schema reads as
though the feature is on.

**Cost of change.** Trivial now. It rises once someone has `Expandable` in a
schema and has concluded, reasonably, that it works.

### 4. The compile-time gap is real, and `Explain` is the answer — **document, don't build**

`sqlb.F("titel")` fails at runtime ([expr.go:176](../expr.go)); scanning is
reflective ([exec.go:189](../exec.go)); predicates are deliberately untyped, so
a column from the wrong table still compiles. Every one of those is argued for
in [ADR-0009](adr/0009-typed-column-facade.md) and the arguments are sound. It
is still a strictly weaker guarantee than sqlc's, and a prospective user
comparing the two will notice.

The mitigation already exists and is undersold. `sqlb.Explain` fails with the
database's own complaint without executing, which catches exactly the class the
type system does not — and the README currently files it under "Inspection and
vetting," alongside optional niceties.

**Recommendation.** Promote it from an inspection tool to the documented
practice: a test per resource that `Explain`s each query shape, wired into
`mise run ci`, presented as the thing you do *instead of* compile-time column
checking rather than in addition to it. No new code — this is a documentation
and example change, and `example/blog` is the place to show it.

**Cost of change.** None, and it materially improves the answer to "how is this
safe if the columns are strings."

### 5. Nothing is tagged — **do this first, it is free**

`git tag` is empty. For a private repo that is fine; the moment anyone else is
meant to evaluate it, an unreleased `main` reads as "unknown risk," whereas
`v0.1.0` with an explicit stability statement reads as "pre-1.0, the author says
what will move." The second is a far easier thing to adopt against, and it costs
one commit.

**Recommendation.** Tag `v0.1.0` alongside a short compatibility note: `Executor`
and the filter grammar are the surfaces worth freezing early; hook registration
is the one that will move when finding 1 lands, and saying so in advance turns a
future break into a documented plan.

### 6. Say what pairing with sqlc looks like — **highest-leverage doc**

[vision.md:91](vision.md) states that sqlb should stay useful alongside sqlc.
Nothing shows how, and the three questions a reader will have are all
answerable:

- **Who owns the schema?** sqlb — and `migrate.Write`'s output is what feeds
  sqlc's `schema.sql`, so there is one source of truth rather than two.
- **Can `sqlb.Describe[T]()` be pointed at sqlc-generated structs?** The README
  advertises this as the incremental-adoption path. It is not demonstrated
  anywhere. Prove it in `example/`, against real sqlc output.
- **Which queries go where?** The table at the top of this document is a first
  draft of that answer.

Only the author can make this argument credibly, and it converts sqlb's most
common objection — "why not just use sqlc" — into a compatibility story.

## Summary

| # | Finding | Severity | Effort |
|---|---|---|---|
| 5 | ~~No release tag~~ | Adoption | **Fixed** — `v0.1.0`, with [compatibility.md](compatibility.md) |
| 3 | ~~`?expand` accepted, advertised, inert~~ | Correctness | **Fixed** |
| 4 | Promote `Explain` to documented practice | Perception | Low, docs only |
| 2 | ~~No `AfterCommit`~~ | Blocking | **Fixed** |
| 6 | No sqlc pairing story | Adoption | Low, docs only |
| 1 | ~~No transaction-scoped handle~~ | Blocking | **Fixed** — [ADR-0020](adr/0020-transaction-scoped-handle.md) |

Ordered by leverage per unit of effort, which is not the order they will
naturally get done in — 1 is the one that matters most and 2 depends on it, so
the remaining sequence is 5, 1, 2, then the two documents.

**Where that sequence stands:** 5, 3, 1 and 2 are done, which is both blocking
findings closed. What remains is 4 and 6, and both are documentation.

That does not make the verdict "yes" — see below. The clock is what is left.

## What would change the verdict

Findings 1 and 2 closed, plus six months of someone other than the author
running it against production traffic. At that point the answer becomes: use
sqlb for the CRUD and list surface, keep sqlc for the reporting queries — which
is exactly what [vision.md:91](vision.md) already predicts, and the reason the
original question was framed on the wrong axis.

The design is not what is holding this back. The clock is.

**Half of that condition is now met**, on the same day the review was written,
which should be read as evidence about the code's malleability rather than
about its maturity. Both blocking findings closed cheaply because the design
they landed on was already close to right — but the other half of the condition
has not moved at all, and cannot be made to. Nothing above substitutes for
elapsed time under someone else's traffic.
