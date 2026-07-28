# Feedback — building an application on sqlb  (2026-07-28)

A build report from writing a public lending library on sqlb: four tables, a
searchable catalogue, a borrowing registry, server-rendered pages beside the
generated REST API, and one hard invariant — the number of copies on the shelf
must never drift from the loans that are open.

Read it as a snapshot of one build at `e961870` (with local modifications to
`builder.go` and `codegen/`), not as a verdict. Where a finding names a file or
a behaviour, it was checked against the code or against a running Postgres 18;
where it is a judgement call about ergonomics, it says so.

**Scope.** The path exercised was schema → codegen → hooks → REST → a hand-written
web layer, verified by 15 tests under `-race` against a real Postgres. Nothing
here touches `Explain`, `Diff` against an existing database, `shadow`,
PgBouncer, or the sqlc adoption path — no opinion is offered on those.

## Verdict

**The parts that have been thought hardest about are the parts that made the
application good, and they held up under a test designed to break them.** The
friction was all at the edges. Of the findings below, exactly one is a real gap
rather than a rough edge: unclassified constraint errors.

## What worked

**The example comments are the documentation.** The schema was written mostly by
reading `example/tasks/taskschema/schema.go`. The note that `ReadOnly` is "the
single most load-bearing word in the file" is what taught the pattern the
application's central column then used — keep `available_copies` out of both
generated bodies, so no request can make it disagree with the open loans. That
comment did more than an API reference would have.

**Transactional generated writes are the feature that made the application
possible.** Because `rest` wraps a generated write and the hook context carries
the transaction, the copy-count rule lives on the model rather than in a bespoke
borrow endpoint. This is the same upgrade the tasks README describes for
comments, and it generalises: an HTML form and `POST /api/loans` run identical
hooks with neither knowing the other exists. That was verified, not assumed.

**Hooks survived a contention test.** Twenty concurrent borrows of a one-copy
book produce exactly one 201, nineteen 409s, a count of zero, and exactly one
loan row — the nineteen refused transactions take their already-inserted rows
with them. The claim that one registration constrains every write is the claim
the application depends on, and it is true.

**Capabilities generalise further than the docs advertise.** `Filterable` and
deliberately not `Searchable` on `borrowers.email` is an *information-disclosure*
control, not an API-surface one: exact match answers "find my own record", while
a substring match on a public table is an address-harvesting endpoint. The docs
frame capabilities as being about what the API exposes. They are also about what
a public endpoint can be made to leak, and that is worth saying explicitly.

**`schema.Index{Where: ...}`.** A partial unique index — one open loan per book
per borrower — was assumed to need a hand-written migration. It did not.

**Zero-valued defaults are omitted from inserts.** This made a `BeforeCreate`
hook that copies `total_copies` into `available_copies` fall out correctly in
the zero case without a special case: both stay zero, both are omitted, both
land on the column default. Genuinely elegant — see the caveat in finding 3.

## Findings, in order of value

### 1. Constraint violations reach the caller as 500 — the one real gap

A second open loan on the same book by the same borrower returned **500** until
a pre-read was added to `BeforeCreate`. The partial unique index raises a
Postgres error `rest` cannot classify, so the caller's own mistake arrives as a
server error.

This shape is already conceded in `example/tasks/README.md`: *"The constraint
raises a Postgres error rest cannot classify, so a client naming a task in
another workspace would get a 500 for what is squarely its own mistake."* Both
examples work around it the same way — read first, so the constraint is only the
backstop.

The workaround is worse outside the REST layer. The web layer ended up with:

```go
if strings.Contains(err.Error(), "loans_one_open_per_book_per_borrower") {
    return "you already have a copy of that book out"
}
```

String-matching an index name is fragile and survives no rename.

**Suggestion.** Map SQLSTATE 23505, 23503 and 23514 to typed errors carrying the
constraint name — `sqlb.ErrUnique{Constraint}`, `ErrForeignKey`, `ErrCheck`.
`rest` could then answer 409 by default, and an application could match on a
value rather than a substring. Every application with a unique index meets this
on the first day.

### 2. `schema.SoftDelete` costs the same boilerplate in every example

Declaring a soft delete obliges the author to remember four separate things:

1. a `BeforeQuery` predicate filtering `deleted_at`;
2. a `BeforeUpdate` predicate — otherwise a withdrawn row stays patchable while
   reading as 404. This one was nearly missed;
3. omitting `OpDelete` from the exposure;
4. a hand-written `DELETE`-as-`UPDATE` route.

Both examples in the repository do all four, identically. The doc comment
defends this as settled behaviour rather than a missing feature, and the honesty
is right — but when two of two examples need the same four-part workaround, and
forgetting any one of them is a silent leak rather than a loud failure, the
ergonomics argument has probably won.

**Suggestion.** A `sqlb.SoftDeleted[T](reg)` helper registering both predicates
costs nothing conceptually and removes the two failure modes that are invisible
when wrong. Judgement call: whether `rest` should also serve `DELETE` as an
update when the model declares a soft delete is a larger question, and the
current refusal to guess is defensible.

### 3. Correctness-critical semantics live only in the source

Three questions came up whose answers decide whether the application is correct,
and none is answerable from a signature:

- Does `InsertRows(...).Exec()` fire `BeforeCreate` and `AfterCreate`?
  (Yes — established by reading `mutate.go`.)
- Does an insert omit a zero-valued column that has a default?
  (Yes — established by reading `Insert.columns()`. The behaviour is good; it
  had to be verified before the hook in finding 5 could be trusted.)
- Does `WithTx` propagate hooks, and does `TxFrom` resolve inside them?
  (Yes — established by reading `db.go`.)

**Suggestion.** One page — "what fires when, and inside which transaction" —
covers all three. These are not discoverability nits; each one changes whether
an invariant holds.

### 4. Two lint rules the existing linter is well placed to add

Both are bugs actually hit during the build.

**`Unique()` with a non-null `Default()`.** `isbn` was first given
`Default(schema.Value(""))`, which would have made the *second* ISBN-less
donation collide: Postgres permits many NULLs in a unique index but exactly one
empty string. Caught by reasoning, not by a warning. The fix was `Nullable()`.

**A `ReadOnly` column with a `Default`, on a table exposing `OpCreate`, that
nothing seeds.** `available_copies` silently defaulted to 1 on a three-copy
book, so the library owned three copies it could lend once. Judgement call: this
is harder to detect statically than the first, since the seeding happens in a
hook the linter cannot see — but it is exactly the class of failure the opt-in
philosophy exists to prevent, and a warning that a `ReadOnly` column's default
is the only thing that will ever write it would have been correct here.

### 5. Required-vs-optional in create bodies is inferred from `Default()`

`bio` and `publisher` became *required* API fields purely by not calling
`.Default(schema.Value(""))` — so adding an author to a public library demanded
writing them a biography first. This was found by a test failure, not by
reading.

The mechanism is right: a column with a default may be omitted, and the database
then supplies the value rather than the zero value overwriting it. The
discoverability is not — "a non-nullable text column with no default is a
required API field" is a long way from the call site.

**Suggestion.** An `.Optional()` alias that reads correctly where it is written,
or a lint hint on non-nullable columns without defaults on `OpCreate` tables.
Judgement call, but it cost a round trip.

### 6. Minor

- **Nested expand is unavailable.** `loans → book → author` cannot be expanded in
  one query, so the registry cannot show an author without a second read. Fine
  as a limit; worth documenting as one.
- **Cursor paging appears reachable only through `rest`.** `Builder.Page` is
  offset paging. Offset was the right choice for an HTML pager — numbered pages
  a human clicks, at depths where `OFFSET` is free — but a non-REST caller
  wanting the cursor behaviour the README advertises has no obvious route to it.

## What this did not test

Stated so the report is not read as broader than it is: `Explain`, `Diff`
against a live database, `shadow.Build`, PgBouncer in transaction pooling,
`introspect`, the sqlc adoption path, and any schema change after the baseline —
the migrations here were generated once with `-force` and never evolved. The
hardest real-world question about a schema-first tool is what the second year
looks like, and nothing here speaks to it.

---

# Feedback — building a stock exchange on sqlb  (2026-07-28)

A second build report, from a different shape of application: a toy stock
exchange. Six tables, prices that move on a background clock, an order book that
rests until the price reaches it, and one hard invariant — a trade moves cash
one way and shares the other, in one transaction, and no code path may end with
an account that owes the exchange money.

Read it as a snapshot of one build at `e961870`, with the same local
modifications to `builder.go` and `codegen/` the first report notes — not as a
verdict. Every finding below was checked against the source or against a running
Postgres 18.

**Scope.** schema → codegen → hooks → REST → a `market` package doing concurrent
money movement, verified by 12 tests under `-race` against a real Postgres, plus
a live run of the server against a container. This build leans hard on three
things the first report did not touch at all: **row locking**, **arithmetic
performed in SQL**, and **hooks that run inside a generated write and change what
it returns**. Nothing here touches `Explain`, `Diff` against an existing
database, `shadow`, PgBouncer, `introspect`, or schema evolution.

## Verdict

**Everything needed to hold a money invariant is present, and none of it is in
the documentation.** `ForUpdate`, `SetExpr` with `Raw`, hooks inside the write
transaction — the three primitives the application is built on — were found by
reading `builder.go` and `mutate.go`. Once found, they worked exactly as hoped
and the concurrency test passes on the first run after one fix.

That one fix is the finding that matters: **the recommended pattern — a hook
that enforces a domain rule on a generated create — deadlocks under
concurrency**, for a reason invisible in the Go code, and it presents as a 500
rather than as anything diagnosable.

## Confirming the first report

**Finding 1 (constraint violations arrive as 500) is confirmed from the other
side.** This build never saw one, because three separate code paths exist only
to make sure it cannot: the pre-checks in `market.Place`, `validateOrder`, and a
withdrawal endpoint that carries its rule as a `WHERE` predicate and then does a
*second read* to tell 404 from 422. Each of those is defensible on its own. All
three together are the shape of an application routing around a missing feature.

**Finding 3 (what fires when, and inside which transaction) is confirmed.** I
had to read `hooks.go` to establish that `runAfterCreate` is handed `&rows[i]`
— which is the entire reason `POST /api/orders` can answer with the fill it just
executed. An invariant-critical behaviour, established by reading the source.

## What worked

**Hooks turning an insert into a domain operation, and answering with the
result.** `POST /api/orders` is the generated handler. It decodes, validates,
opens a transaction, inserts — and knows nothing about money. `BeforeCreate`
takes the locks, `AfterCreate` reserves the funds, matches against the current
price and writes the trade, and because it mutates `*T`, the response body
carries `filled_quantity`, `status` and `closed_at`. A market order is placed and
reported filled in one round trip, and a refusal rolls the order row back with
it, so "an order that was recorded but not placed" is not a state that exists.
**This is a better story than the docs currently tell** — the guide lists
`AfterCreate` under "Validation", which undersells it considerably.

**`SetExpr` with `sqlb.Raw`.** Every balance change in the application is
`cash_cents = cash_cents - ?` computed by the database. The `?` renumbering
composes correctly with the builder's own parameters, and the arithmetic never
round-trips through Go. That is what keeps the money code
auditable: each balance change is one statement, and no balance is ever a value
that was true when Go read it.

**`ForUpdate()`.** Exists, works, and is the difference between the application
being correct and being a race. See finding 3 for the complaint.

**Check constraints as the floor.** `reserved_cents <= cash_cents` and
`reserved_quantity <= quantity` are the two lines that make a bug in my engine a
rolled-back transaction rather than an exchange that lent money it does not
have. Being able to write them next to the columns, in the same file, is the
single best thing about the schema DSL.

**The linter changed the design.** It found every `Filterable` column that was not the
leading column of an index — two dozen of them, most of the schema — and the
right response was to delete the capability rather than to add the index — filtering trades by `total_cents` is not a query anyone makes.
A linter that causes a smaller API is doing something unusual and good.

**`Update.One()` returning `ErrNotFound` when the predicate matches nothing.**
The withdrawal endpoint carries its rule in the `WHERE` clause, so the check and
the write are one statement with no window between them, and "no rows" *is* the
refusal. Elegant, and it fell out without being designed for.

**`OnConflictDoNothing` against a declared unique index** made seeding idempotent
in one line, with the conflict target and the index unable to drift apart.

## Findings, in order of value

### 1. A hook that locks a referenced row deadlocks on a generated create

This is a real trap, and it is in the middle of the pattern the library
recommends.

Eighteen of twenty concurrent `POST /api/orders` returned **500**:

```
ERROR: deadlock detected (SQLSTATE 40P01)
  on: SELECT ... FROM "stocks" WHERE "id" = $1 LIMIT 2 FOR UPDATE
```

The cause is invisible in Go. `rest` inserts the order row and *then* runs
`AfterCreate`. The insert takes a `FOR KEY SHARE` lock on the `stocks` and
`traders` rows the new row references — Postgres checking the foreign keys, not
requested by any statement I wrote. Key-share locks are shared, so both
transactions get them; then both try to upgrade the same row to `FOR UPDATE`
inside the hook, and each waits for the other's share lock. Guaranteed deadlock,
scaling with concurrency, on two requests that were each perfectly valid.

The fix is one line of ordering — take the exclusive lock in `BeforeCreate`,
before the row exists — and it is not discoverable from anything in the repo.
After it, the same test passes under `-race`, repeatedly, with the expected mix
of 201s and 422s.

**Suggestion.** A paragraph in `queries-and-hooks.md`: *if an `AfterCreate` hook
locks a row the new row references, take the lock in `BeforeCreate` instead — the
insert has already taken a weaker lock on it, and two transactions cannot both
upgrade.* This costs one paragraph and saves a day. Any application whose hook
enforces a rule about a parent row — stock levels, quotas, balances, seat counts
— meets this the first time two requests arrive together, which for most is
after deployment rather than during development.

### 2. The typed update facade omits exactly the columns only Go may write

`ReadOnly` is documented as a REST-boundary rule: *"Go code going through the
query engine directly is trusted and bypasses `ReadOnly` and `Immutable`; they
are enforced at the REST boundary."* The generated Go update wrapper does not
follow that model. `UpdateStock()` has no `SetPriceCents`, no `SetVolume`, no
`SetHalted`; `UpdateOrder()` has no `SetStatus` or `SetFilledQuantity`.

The consequence is exact and unfortunate: **the trusted code is the only code
that writes those columns, and it is the only code that cannot use the typed
API.** Every statement in `market/engine.go` is `Set("filled_quantity", …)` and
`Set("status", …)` — the untyped form the guide itself warns about, on the
columns where a typo is most expensive. The read facade has the opposite
property and is complete: `StockCols.PriceCents` exists.

**Suggestion.** Emit setters for `ReadOnly` and `Immutable` columns too. They are
already unreachable from REST — the bodies omit them and the handler clears them
— so the generated wrapper is not the boundary being defended, and excluding
them protects nothing while removing type-checking from the code that most needs
it. If that feels too loose, a separate `UpdateStockInternal()` would at least
make the choice explicit rather than silent.

### 3. Row locking is absent from the documentation

`ForUpdate`, `ForShare` and `SkipLocked` appear nowhere in `docs/` or the
README — verified by grep. They are in `builder.go`, and I found them by looking
for them because I already knew what I needed.

An author who follows the guide writes read-then-write, which is correct in
every test and wrong under load. The guide has a section on transactions that
covers `WithTx`, nesting and isolation levels, and stops one step short of the
thing that makes a transaction *do* anything for a balance.

**Suggestion.** Half a page under Transactions, with the ordering rule (always
take locks in the same order), and a cross-reference from the hooks section,
since a hook is where most people will need it.

### 4. Aggregates over an empty set fail, and `Coalesce` cannot rescue them

Verified all three legs against Postgres:

```go
sqlb.Collect[T](ctx, db, q.Select(sqlb.Sum(sqlb.F("total_cents")).As("cents")))
// empty set → sqlb: scanning T: Scan error on column "cents":
//             converting NULL to int64 is unsupported
// one row   → works
```

The obvious fix does not compile: `Coalesce` takes `...Expr`, `Sum` returns
`Selection`, and `Selection` does not implement `Expr`. So the single most common
aggregate idiom in any application — `COALESCE(SUM(x), 0)` — is not expressible
with the two helpers provided for it. The route that works is
`sqlb.RawSel("COALESCE(SUM(\"total_cents\"), 0)").As("cents")`, which gives up
identifier checking for the whole expression.

This is a runtime failure in code that passes every test written against
populated data, and the failing case — a dashboard before the first sale, a new
account, a stock with no trades yet — is the one nobody fixtures.

**Suggestion.** Make `Selection` satisfy `Expr` so the two compose, or emit the
aggregates as `Expr` and let `.As()` live on the selection wrapper. Failing
either, `sqlb.SumOr(f, 0)` covers the case that actually occurs. A note in the
aggregates section would be the minimum.

### 5. No arithmetic upsert

`OnConflictUpdate(target, cols...)` renders `col = EXCLUDED.col` and nothing
else, so "insert or increment" — a counter, a running total, a position that
grows — cannot be expressed. Adding to a position fell back to lock, read,
branch, insert-or-update: four statements and a code path for each case where
one `ON CONFLICT … DO UPDATE SET quantity = holdings.quantity + EXCLUDED.quantity`
would have done.

Judgement call on severity. The lock was needed here anyway for other reasons,
so the cost was code rather than correctness — and a `Set`-style API on the
conflict clause is a real design question, not an oversight. Worth naming
because "increment on conflict" is most of what upserts are used for.

### 6. Minor

- **The `unindexed-filter` fix message names a capability the column does not
  have.** `stocks.description` was declared `Searchable()` only, and the linter
  advised *"or drop `.Filterable()` from the column"* — which is not there to
  drop, because `Searchable` implies it. The same decision also produces two
  warnings (`search-without-trigram` and `unindexed-filter`) for one column, so
  the count of warnings overstates the count of problems. Both are small; the
  first is actively misleading to someone who does not know about the implication.
- **`schema.Index{Where: …}` again.** Independently reached for, independently
  found to exist, this time for a partial index over the open order book. The
  first report's praise stands.
- **Naming the generated package after something other than its directory** —
  `package exchange` at a module root called `ex02` — works but makes every
  import an aliased one. Worth a line in the codegen docs, since the module-root
  layout the examples use makes it easy to walk into.

## What this did not test

`Explain`, `Diff` against a live database, `shadow.Build`, `introspect`,
PgBouncer, the sqlc path, and — as with the first report — any schema change
after the baseline. Also untested here: `Hidden` columns (this schema has none;
a public exchange has nothing to hide), soft deletes (deliberately avoided, on
the strength of the first report's finding 2), and cursor pagination, which the
chart endpoint advertises and no test walks.

---

# Feedback — the same library, built *without* sqlb  (2026-07-28)

A third report, and a different kind: this build does not use sqlb at all. It is
the first report's application — the same public lending library, the same four
tables, the same hard invariant — rebuilt on **Postgres + sqlc + chi** as a
control group. Same schema, same rules, same tests, no framework.

Read it as a comparison rather than a build report. Nothing here is a bug in
sqlb; the value is in seeing which of the first report's findings have
off-the-shelf answers in the other stack, and which of sqlb's advantages the
alternative cannot reach at any price.

Snapshot at `e961870` for the sqlb side (ex01), sqlc v1.31.1 and chi v5.3.1 for
the new one (ex03), verified by 16 tests under `-race` against a real Postgres 18
plus a live run.

**Scope.** schema-as-migrations → `sqlc generate` → a hand-written domain package
→ chi router with server-rendered pages beside a JSON API. Deliberately the same
surface as ex01 so the two are comparable line for line.

## Verdict

**sqlb's leverage is real, and it is concentrated in two places: cross-cutting
rules, and generated API surface.** Everything else the alternative does as well
or better, and it does two things sqlb currently cannot.

The sharper result is that **two of the first report's findings turn out not to
be design questions.** Finding 1 has a twenty-line answer that sqlc does not
supply either — I wrote it by hand in both stacks, and in the sqlc build it is
right, while ex01 is still matching on an index name with `strings.Contains`.
Finding 2 has an answer that is better than the one I suggested, and it is not in
Go at all.

## The first report's findings, seen from outside

### Finding 1 (constraint violations arrive as 500) — this is 20 lines, not a design question

The sqlc build classifies the same errors like this:

```go
func IsUniqueViolation(err error, constraint string) bool {
    var pgErr *pgconn.PgError
    if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
        return false
    }
    return constraint == "" || pgErr.ConstraintName == constraint
}
```

That is the whole thing, plus a copy for `23514`. The constraint name arrives as
a **field**, so the comparison is exact: it cannot be fooled by an error whose
*message* happens to mention the index, and it does not depend on how a driver
renders errors to text. Compare ex01's `humanise()`, which is still doing:

```go
if strings.Contains(err.Error(), "loans_one_open_per_book_per_borrower") {
```

The first report called the string match "fragile and survives no rename". It is
worse than that: it was never necessary. pgx has been carrying
`ConstraintName` the whole time, and the only reason ex01 does not use it is that
sqlb sits in front and does not surface it.

This raises finding 1's priority in my view. The suggestion there —
`sqlb.ErrUnique{Constraint}` — is not a feature to design, it is a translation to
perform.

**Neither version survives a rename, though**, and sqlb is uniquely placed to fix
that half too. Both stacks name the constraint as a string literal in Go, so
renaming it in a migration silently stops the branch firing — a refused loan
quietly becomes "that could not be completed". sqlb *declares* those names:
`AddIndex(schema.Index{Name: "loans_one_open_per_book_per_borrower", …})` is in
the schema, and codegen already emits `LoanCols`. Emitting
`LoanIndexes.OneOpenPerBookPerBorrower` alongside it would make the rename a
compile error, which is the property the string was reaching for and neither
stack currently has. Cheap, and no other tool in this comparison can offer it.

**The one real design question**, which the first report did not name: sqlb takes
an `Executor` and depends on the standard library alone, so it cannot import
`pgconn` to do the `errors.As`.

A duck-typed interface check gets half of it and no more. `SQLState() string` is
a method on `*pgconn.PgError`, so `errors.As` against a local
`interface{ SQLState() string }` yields the class — 23505 versus 23503 versus
23514 — with no dependency. But `ConstraintName` is a struct **field**, not a
method, and it is the field carrying all the value: the class alone is enough for
`rest` to answer 409 instead of 500, and gives an application nothing to branch
on. Reaching the name driver-agnostically would mean reflection, which is not
worth it.

So: a pluggable classifier —
`sqlb.SetErrorClassifier(func(error) *sqlb.ConstraintError)`, with a pgx
implementation in a subpackage — **and** the interface check as a
dependency-free default. That way the common case gets the right status code
with no configuration, and the case that needs the constraint name has a
supported route to it instead of `strings.Contains`.

### Finding 2 (SoftDelete boilerplate) — the better answer is a view, not a helper

Without hooks there was nowhere to put a `deleted_at IS NULL`, so it went into
the schema:

```sql
CREATE VIEW live_books AS
    SELECT * FROM books WHERE deleted_at IS NULL
    WITH CHECK OPTION;
```

Every query reads and writes the view. It is a plain single-table view, so
Postgres makes it auto-updatable — `INSERT INTO live_books` and `UPDATE
live_books` work, and the planner rewrites rather than materialising.

This is strictly stronger than the `BeforeQuery`/`BeforeUpdate` pair I suggested
in finding 2, in three ways:

1. **It covers writers that are not the application.** The migration somebody
   writes next year, and the psql session at 3am, both get the filter. A hook
   covers Go.
2. **`WITH CHECK OPTION` makes it a boundary rather than a convention.** A write
   through the view may not produce a row the view cannot see — so withdrawing a
   book *must* name the underlying table, and there is provably exactly one
   statement in the codebase that can soft-delete. Finding 2's failure mode
   ("forgetting any one of them is a silent leak") is not reachable: forgetting
   is a Postgres error, and there is a test that asserts it.
3. **It removes the two obligations that fail silently.** Finding 2's list was
   four items; the view subsumes items 1 and 2 — the `BeforeQuery` and
   `BeforeUpdate` predicates — which are precisely the two whose absence is
   invisible rather than loud. To be accurate about the other two: ex03 still
   hand-writes its withdraw path (`library.Withdraw` plus a `DELETE` handler),
   and still has to be the only one. The difference is that "the only one" is now
   enforced by the database instead of asserted in a comment.

**Suggestion.** `schema.SoftDelete()` could emit a companion view in the
generated migration and point the model's reads at it. I am flagging this as a
**judgement call with a real obstacle**: a sqlb model binds to one relation, and
reading from a view while writing the tombstone to a table is a genuine design
question, not a small change. But it is worth considering before shipping the
`SoftDeleted[T](reg)` helper, because the helper is the weaker of the two and
would be the thing people then depend on.

**Independent of that**, the footgun deserves promoting from finding 2's
ergonomics argument to a correctness one. Today, `schema.SoftDelete()` together
with `Expose(Ops: … | OpDelete)` compiles, and hard-deletes through an endpoint
whose schema says otherwise. ex01 is prevented from doing this by a paragraph of
comment. **That should be an error at registration.** It is the only thing I
found across three builds where the library will silently do the opposite of what
the schema declares.

### Finding 3 (what fires when, and inside which transaction) — confirmed by its absence

The equivalent question does not exist in the sqlc build. `library.Borrow` is
fifty lines, and every statement it issues is visible in it, in order, inside one
`InTx`. There is nothing to look up.

That is the honest cost of the hook model, and it is worth paying for what hooks
buy — but it is not a documentation gap that can be fully closed, and the doc page
finding 3 asks for is what makes it payable rather than what fixes it.

## What sqlb does that the other stack cannot

Measured, not asserted.

**About 280 lines of plumbing, per project, that sqlb and huma supply.** The
transaction helper, pool wiring and error predicates (`internal/store/tx.go`, 98
lines) plus the request/response plumbing in the web layer — pagination envelope,
JSON read/write, id parsing, error-to-status mapping, template helpers (183
lines across three files). None of it is interesting and all of it is re-written
per project.

**No OpenAPI, at all.** ex01 gets `/docs` from the same declaration that builds
the tables. In ex03 the API contract lives in a README section — a document that
can lie, and will. This is the single largest thing sqlb offers that has no
answer on the other side.

**No filter grammar.** Every facet in ex03 is pre-declared as a
`sqlc.narg('x') IS NULL OR …` guard. Adding one is four touch points — two `WHERE`
clauses that must stay in step, regenerate, params, handler — against one
capability flag. `?available_copies=gt.0` is simply not expressible.

**The `WHERE` clause is duplicated between the list query and its count.** sqlc
has no composition, so `SearchBooks` and `CountBooks` carry the same twelve lines
and nothing enforces that they agree. I ended up asserting it in every search
test, which is a test standing in for a language feature. This is what
`Builder`-as-a-value buys, and it is a real win.

**Honest caveat on that last one:** ex01 already needs `sqlb.RawPred` with a
correlated subquery for the author-name search — the first interesting predicate
on the catalogue page — and again for the registry's email filter. So the
composition advantage leaks at exactly the queries the application is *about*.
Which leads to the one suggestion I would make across all three reports:

**The escape hatches are load-bearing and should be treated as first-class.**
Every build reached for them on its central query: `RawPred` twice in ex01,
`SetExpr`/`Raw` throughout ex02's money code, `RawSel` in ex02 because
`Coalesce` and `Sum` do not compose (report 2, finding 4). Three for three. A DSL
over SQL will always trail SQL, which is fine — but the design currently reads as
though `Raw*` is the exception, and the evidence is that it is the seam every
non-trivial application lands on. Making them compose cleanly (finding 4's
`Selection`/`Expr` problem is the same phenomenon) is probably higher value than
extending the DSL to cover another construct.

## What the other stack does better

**Errors at generate time.** sqlc will not emit a query it cannot parse or type,
so every mistake in the SQL was a build failure. sqlb composes at runtime, so a
malformed predicate is a 500 in production. The linter and `Explain` narrow this;
they do not close it.

**No ceiling on Postgres.** `FILTER (WHERE …)` for conditional aggregates,
auto-updatable views with `WITH CHECK OPTION`, trigram indexes — all directly
available, none of which a DSL will have until someone asks for it. (sqlb's
`schema.Index{Where: …}` is the counter-example and remains the best thing in the
schema package.)

**Reviewability by people who do not know the tool.** `db/query/*.sql` can be
reviewed by any Postgres person. That is a real property for a team, and it is
the flip side of finding 3.

**One thing that is a genuine tie, not a win.** sqlc reads the goose migrations
directly as its schema source, so there is no second description to drift. sqlb
gets the same property from the other direction by generating the migrations from
the schema. Both are right; neither is better.

## What this did not test

Everything about sqlb — this build exercised none of it, so nothing here is
evidence about `Explain`, `Diff`, `shadow`, `introspect`, PgBouncer or codegen.
On the sqlc side: the same blind spot as both previous reports, which is schema
evolution. The migrations were written once and never changed, and the hardest
question about either approach is still what the second year looks like.
