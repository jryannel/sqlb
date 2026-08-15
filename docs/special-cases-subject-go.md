# Special cases — the second corpus (`subject-go`)

A companion to [special-cases.md](special-cases.md), which censuses
`subject-mono`: a modular monolith, 12 applications, per-module schemas. This
one runs the same count over the other port subject, and a deliberately
different architecture: **one** application, one large schema, 84 tables, 124
goose migrations, 76 sqlc query files / 4,378 lines of SQL, pgvector and
tsvector in the hot path, and a hand-built dynamic filter grammar — 13
registries over 15 list endpoints — which is an independent implementation of
what sqlb's REST grammar does.

It exists to *test* the first census rather than repeat it. **One shape the
first census counted as absent is common here, and that flips a stated
permission.**

*Written 2026-07-30, against the tree at `bb7b182`. Method as before: counts are
matched lines from `grep` over query files and migrations, so read them as "this
shape is common here", not as a workload profile; sqlb status is read from the
code and cited by file and line, not from the docs.*

**Deliberately not re-reported.** Constraint errors arriving as 500
([FEEDBACK §1](../FEEDBACK.md)), `SoftDelete` boilerplate (§2), empty-set
aggregates (§4), arithmetic upsert (§5), composite primary keys
([ADR-0034](architecture.md#one-column-addresses-a-row)), null-aware operators and
the missing bind-parameter cast ([review-adoption-port](review-adoption-port.md))
are all recorded already. Where this corpus is independent evidence for one of
them, it is cited as evidence and not re-argued.

## Where the two corpora agree

| Shape | `subject-mono` | `subject-go` | Reading |
|---|---|---|---|
| `ON CONFLICT` upsert | 53 lines | 18 | Confirmed common. The expression-update gap bites here in a second disguise: 6 of the 18 are `SET col = COALESCE(EXCLUDED.col, table.col)` — patch-semantics upsert, which `EXCLUDED.<col>`-only cannot write either |
| Composite primary key | ~15 tables | 4 | Confirmed at a lower rate, same two shapes: a link table, and a `(sheet_id, row_index)` rollup |
| `FOR UPDATE` | 4 | 2 | Confirmed rare-but-load-bearing; both uses here guard a reorder |
| `DISTINCT ON` | 1 | 1 | Confirmed: one per corpus, both "first row per group" |
| `WITH RECURSIVE` | 0 | 0 | Confirmed. The non-goal holds |
| Window function | 1 | 1, in a backfill migration | Confirmed. Neither is in a served query |
| Partial index carrying an invariant | 6 | present, uncounted | Confirmed, including the exact shape the first census names |
| `vector(n)` | 3 lines | **26** | Confirmed and then some — see below |
| jsonb **queried** | 0 | **~20** | **Contradicted — see below** |

## The contradiction: jsonb is queried here

The first census found 96 jsonb-declaring lines and zero filters into them, and
drew the useful conclusion that *not* building `->>`/`@>` operators is earned
rather than lazy. In `subject-go` the filtering is the feature. Customers define
their own fields at runtime: definitions live in a jsonb array on the parent
row, values in a jsonb object on the child.

```sql
-- which fields exist, per project (also this corpus's only DISTINCT ON)
SELECT DISTINCT ON (def->>'key') def->>'key', def->>'label', def->>'type'
FROM work_packages wp, jsonb_array_elements(wp.field_definitions) AS def
WHERE wp.project_id = $1 AND wp.org_id = $2
ORDER BY def->>'key', wp.created_at;

-- and the distribution of one of them, chosen by the caller at request time
SELECT t.custom_field_values->>sqlc.arg('field_key') AS value, COUNT(*)
FROM tasks t
WHERE t.org_id = $1 AND t.custom_field_values ? sqlc.arg('field_key')
GROUP BY value ORDER BY count DESC;
```

Plus `(->> key)::numeric` min/max/avg/sum guarded by a regex,
`(->> key)::boolean` tallies, a filled-count, and — in global search —
`custom_field_values::text ILIKE $2`, so a value typed into the search box is
found without the caller knowing which field held it.

**The correction is not "build jsonb operators".** It is that the two corpora
ask different questions, and only the second asks sqlb's. The filterable surface
here **is data**: the set of legal keys is a query result, different per tenant,
changing while the server runs. sqlb's central claim is that a capability is
opt-in per column and a rejection names what would have been accepted. Either
that claim has an answer when the columns are data, or an application with
user-defined fields keeps a hand-written filter path beside the generated one —
which is, in this repository's own words, the half that tests do not cover.

In sqlb's favour: `subject-go`'s own filter grammar also refuses to reach into
jsonb — its 13 registries are column-only, and every custom-field endpoint is
hand-written. Nobody has solved this. sqlb is the first of the three with the
vocabulary to.

**Suggestion, smaller half first.** (a) A JSON path accessor with a declared
type, usable anywhere a `Field` is — `C.CustomFields.Key("severity").AsInt()` →
`("custom_field_values"->>'severity')::int`, with the key **bound**, never
interpolated. That alone makes the analytics queries expressible without `Raw`.
(b) A capability that delegates to data — a column marked `FilterableKeys(fn)`
where `fn(ctx) []KeySpec` returns the legal keys for *this* request's scope, so
the grammar validates `?fields.severity=gte.3` against a set the application
computed and the 400 still names the accepted set. (b) is a design question, not
a patch: ADR first.

**One thing the escape hatch already handles, written down in one comment and
nowhere a caller reads.** Postgres spells "does this key exist" as `?`, which is
also `Raw`'s placeholder. The compiler takes a doubled `??` as a literal
question mark ([`compile.go:257-259`](../compile.go)), so the working spelling
of the second query above is:

```go
sqlb.RawPred(`"custom_field_values" ?? ?`, key)   // -> "custom_field_values" ? $1
```

A single `?` is an argument-count error rather than a wrong query, which is the
right failure and worth stating out loud: the tempting way around a placeholder
collision is to splice the key into the SQL text, and that is exactly how this
shape becomes an injection. Both halves are now pinned —
`TestAJSONKeyIsBoundAndNeverInterpolated` and
`TestASingleQuestionMarkIsAPlaceholderAndNotTheJSONOperator` in
[`subjectgo_test.go`](../subjectgo_test.go), with the hostile keys run against a
real Postgres in [`pgtest/subjectgo_test.go`](../pgtest/subjectgo_test.go) — so
whatever spells the accessor later inherits a test that says the key is a value.

## Four shapes the first census did not count

### 1. `COUNT(*) FILTER (WHERE …)` — 15 occurrences, and the cheapest win

```sql
SELECT page_url,
       COUNT(*)                                      AS total,
       COUNT(DISTINCT user_id)                       AS unique_users,
       COUNT(*) FILTER (WHERE event_type='pageview') AS pageviews,
       COUNT(*) FILTER (WHERE event_type='click')    AS clicks
FROM analytics_events WHERE … GROUP BY page_url ORDER BY total DESC LIMIT $3;
```

A dashboard row is several *differently-filtered* counts over one scan. Written
without `FILTER` it is N round trips or a `SUM(CASE WHEN … THEN 1 ELSE 0 END)`
per column — and the `CASE` form is where the tenant predicate gets forgotten,
because it is written per column instead of once in the `WHERE`.

sqlb has the aggregates ([`expr.go:580-599`](../expr.go)) and
`GroupBy`/`GroupByExpr`/`Having` ([`builder.go:178-192`](../builder.go)), and no
`FILTER`. The compiler already renders one for expansion
([`expand.go:392`](../expand.go)). So this is a public combinator —
`Selection.Filter(Pred)` — over machinery that exists, and it composes with the
empty-set trap: `Coalesce(Count().Filter(p).Expr(), Raw{SQL: "0"})`.

It is the difference between "sqlb serves lists" and "sqlb serves the dashboard
too", and it is the highest value-per-line item in either census.

### 2. A column the *database* owns — 7 triggers, modelled at no layer

Three variants, all present:

```sql
-- a counter maintained by a trigger on another table's writes
CREATE TRIGGER tasks_update_wp_counters AFTER INSERT OR UPDATE OR DELETE ON tasks
  FOR EACH ROW EXECUTE FUNCTION update_work_package_task_counters();

-- a per-parent sequence allocated at insert, never reused
UPDATE projects SET next_task_number = next_task_number + 1
  WHERE id = NEW.project_id RETURNING next_task_number - 1 INTO NEW.number;

-- a search vector kept in step with the text it indexes
NEW.search_vector := to_tsvector('simple', COALESCE(NEW.content, ''));
```

Such a column is `ReadOnly` in the strongest sense — *Go may not write it
either*. It changes as a side effect of writing a **different table**, so a row
struct held across a write is stale; and the "zero value is omitted from the
insert" rule ([`mutate.go:211`](../mutate.go)) becomes load-bearing in a way
nothing states.

The first census puts this at "DDL not rendered… the one-source-of-truth story
keeps its asterisk". This corpus says the asterisk carries more weight than
that. [`migrate/diff.go:13`](../migrate/diff.go) is a pure function over two
registries, so a trigger — along with the `hnsw` index, the `river_job_state`
enum type and the `pg_uuidv7` extension this schema also carries — is not
*dropped* by a diff. It is invisible to it. Invisible is the right default and
the wrong story, because **no document says so**, and that sentence is the whole
adoption question for any database older than a week.

In order: (a) state it, with a test that pins it — a migration adding a trigger,
then a `Diff` that must not mention it; (b) `Managed()`, a column marker meaning
*the database writes this*, implying `ReadOnly`, excluding the column from
create and patch bodies, and making `Insert.Exec`'s write-back the documented
refresh path rather than an accident; (c) `schema.Raw(up, down)` on a table, so
the trigger that maintains the column lives next to the declaration that it
exists. Whether a diff should ever compare such a body is a separate and harder
question — it should not, in the first version.

### 3. Bulk reposition — `ForUpdate` is there, the write is not

```sql
SELECT id, position, work_package_id FROM tasks
WHERE project_id = $1 AND org_id = $2
ORDER BY position ASC NULLS FIRST, created_at DESC FOR UPDATE;

UPDATE tasks SET position = u.position
FROM (SELECT unnest($1::uuid[]) AS id, unnest($2::text[]) AS position) u
WHERE tasks.id = u.id AND tasks.org_id = $3;
```

Drag-and-drop assigns *different* values to *many* rows in one statement.
Row-per-statement is not merely slower: under the `FOR UPDATE` above it holds
the lock for N round trips, and the normalization pass — the first reorder on a
project assigns keys to every task — becomes O(rows) statements.

`ForUpdate`/`ForShare`/`SkipLocked` are there
([`builder.go:254-267`](../builder.go)). `Update.Set`/`SetExpr` write one value
to every matched row; there is no values-list form. `sqlb.UpdateFrom[T]`
rendering the `unnest` join is the write-side twin of the multi-row insert that
already exists, and it needs no encoder behind it: the two arrays are ordinary
bind parameters since [ADR-0040](architecture.md#the-driver-is-a-dependency), which
deleted the array codec this paragraph used to point at.

### 4. Bulk insert has an exact ceiling, and it is now named — closed

The first census lists this as "nothing measures what happens at a thousand".
The number: `InsertRows` renders **one** `VALUES` list with one bind per column
per row ([`mutate.go`](../mutate.go)) — no batching. The wire protocol caps a
statement at 65,535 bind parameters; at the 10 columns this corpus's chunk table
uses, that is ~6,553 rows. Fine in every test anyone writes, a hard failure on
the first large document in production. (The corpus uses sqlc's `:copyfrom` —
Postgres `COPY` — for exactly these three tables.)

The refusal used to be **pgx's own** — `extended protocol limited to 65535
parameters` — raised before the batch reached the server, and that was the gap:
the message named the unit nobody wrote, so a caller who inserted 6,554 rows had
to divide to learn what happened, and because the wording belonged to the driver
rather than to Postgres it was not sqlb's to lean on.

`compiler.result` now refuses any statement over the ceiling, and `InsertRows`
supplies the arithmetic, so the batch never reaches the driver:

> `sqlb: inserting 6554 rows into chunks binds 65540 values across 10 columns,
> and one statement can carry 65535; insert at most 6553 rows at a time, in a
> transaction if they have to land together`

Which is the [actionable-errors](architecture.md#actionable-errors) answer: rows in,
rows out. Batching inside a transaction was the larger option and is deliberately
*not* taken — a batch silently becoming several statements stops being atomic
outside a transaction, and how to divide the work is the caller's decision.
`COPY` itself is a `database/sql` problem and can stay out.

## Vectors: a second data point for ADR-0026

[ADR-0026](architecture.md#vectors-declare-their-index) is explicit that nothing is
built and that vectors are out of 1.0 unless a port needs them, and it reasons
from `subject-mono`'s `core/rag`. This corpus is an independent instance of the
same module, with three wrinkles the record does not have:

- **the score is a projection *and* a filter** — `1 - (embedding <=> $1)` is
  selected, compared against a minimum, and ordered by, so the distance
  expression appears three times in one statement;
- **retrieval is hierarchical** — company, project and work-package documents
  are searched with a different top-*k* and threshold per level and merged by
  one of three strategies, so "the query" is three queries plus a blending rule;
- **the filter is mixed** — scope predicates sit beside an `EXISTS` over
  archived projects, which is exactly the case the ADR flags as the one Postgres
  behaves unintuitively on under an HNSW index.

**Recommendation unchanged: no example.** Building one before the column type
exists means building on `Raw`, which the ADR itself identifies as the trap.
Adding these three lines to the record costs nothing and keeps it current.

## One number worth quoting elsewhere

**108 optional-predicate guards** — `(sqlc.narg('x')::uuid IS NULL OR col = x)`
— in 4,378 lines of SQL. That is precisely the boilerplate the README opens
with, and it is the first count anyone has put on it. Twelve of them sit in a
`List<X>`/`Count<X>` pair where both copies must be edited together; the
application carries an architecture test whose entire reason to exist is that
three such pairs had already drifted. [comparisons.md](comparisons.md) should
have this number in it.

## Two additions to the proposed examples

The six in [special-cases.md](special-cases.md) already cover most of what this
corpus wants — `meter` in particular absorbs the aggregate, `date_trunc` and
empty-set cases, and should absorb `FILTER` too. Two things it does not cover.

### `forms` — a schema the customer defines at runtime

The example that would settle the jsonb question above, and the one most likely
to *change a decision* rather than confirm one. Two tables — a form owning a
jsonb array of field definitions, a submission holding a jsonb object of values
— and three endpoints: list submissions filtered by a customer-defined field,
aggregate one such field, and the discovery endpoint that says which fields
exist.

| Claim | The check |
|---|---|
| A JSON key is addressable with a declared type | `C.Values.Key("severity").AsInt().Gte(3)` renders a cast comparison with the key **bound** |
| A key the form does not define is a 400 | Not a 500, not a silently empty page — and the message names the keys that would have been accepted |
| The key is not an injection vector | A fuzz corpus of keys containing quotes, `->>` and `--` reaches Postgres as data ([`filter/fuzz_test.go`](../filter/fuzz_test.go) is the existing pattern) |
| One tenant's field names cannot leak through another's error | The rejection set is computed under the request's scope |

**Deliberately not** an argument that jsonb fields are good design: it should say
plainly that a column beats a JSON key whenever the field set is known at deploy
time, and that this exists for when it is not. Largest of the proposals, and the
only one needing an ADR first.

### A graft onto `tasks` — the database owns a column

Not a new example; three additions to the one that already has the right domain
and a real Postgres suite around it.

1. **A trigger-maintained counter** — `work_packages.done_count`, kept in step
   by a trigger on `tasks`. The load-bearing test of "a column Go may not
   write": the create body must not offer it, a patch naming it must be a 400,
   and a row struct held across a task write must be *documented* as stale.
2. **A per-project display key** — `TSK-42`, allocated at insert from a counter
   on the parent, unique per project, never reused after a delete. Then the
   interesting half: it is composed, not stored, so `?search=TSK-4` has to reach
   `code || '-' || number` — a filterable value with no column behind it.
3. **A reorder endpoint** — `FOR UPDATE` over the siblings, one bulk
   reposition, and the assertion that pays for the whole graft: **a keyset
   cursor held across a reorder.** [ADR-0027](architecture.md#keyset-pagination)
   claims a concurrent insert cannot make a client read a row twice. A
   concurrent *reorder* can, and that sentence belongs in the record either way.

## One policy question this corpus raises

`subject-go` runs two planes: tenant-scoped requests, and a superadmin plane
with **no** `org_id` at all — 26 `*_admin.sql` files, under the convention that
a query without an `org_id` predicate belongs in one "or it is a tenant-leak
bug", enforced by file naming and review.

[ADR-0030](architecture.md#declared-scope-is-required) makes the scope hook an
obligation and refuses to mount a resource without one, which is right. What has
no spelling is the *deliberate* exception — a platform admin listing every
organization, a job re-embedding every document, a billing sweep. Today that is
done by omitting the hook, or by writing one that inspects the context, so the
safe path and the bypass look identical at the call site: the property ADR-0030
was written to remove one layer up. `sqlb.Unscoped(ctx, reason)`, honoured by
scope hooks and logged, is a small API whose value is that "this read crosses
tenants" becomes greppable instead of being the absence of a line.

This is distinct from *"cross-tenant admin planes belong in hand-written SQL"*
in the first census: the queries can stay hand-written and the **escape** still
needs to be a declaration.

## What this did not check

Read-only, like the census it extends. Nothing was run against `subject-go`'s
database, no port was attempted, and no performance claim is made — "one
statement instead of N" is an argument about round trips and locks, not a
measurement. Counts are `grep` over query files and migrations, so a shape
assembled in Go (there is some, in the reorder path) is undercounted. `Explain`,
`shadow`, PgBouncer and the generated clients are untouched.
