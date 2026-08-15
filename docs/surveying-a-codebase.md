# Surveying an existing codebase

An adoption has two halves and only one of them has a command.
[`sqlb survey`](../cmd/sqlb/survey.go) answers the database half — which
tables the schema DSL can describe, which cannot, and which module a single
unmodelable table takes out of the drift gate. This page is the other half: the
routes and the queries sitting in front of that database, and how many of each
sqlb would actually take.

It is a procedure rather than a program, and that is a decision. Every step
below is three lines of shell over one repository's conventions; a program that
handled chi, huma, gin, echo and a hand-rolled mux would be a router parser
before it was an adoption tool. More to the point, the step that decides the
answer — whether a handler is a list surface or a domain verb wearing a `GET` —
is a judgement about intent that no parse recovers. The shell gets you to the
shortlist. You read the shortlist.

**What the numbers are for.** Not a total, a *ratio*.
[Using sqlb with sqlc](with-sqlc.md) says which queries belong on which side;
[Refactoring a sqlc endpoint](refactoring-from-sqlc.md) says what moving one
costs, in four stages. This page multiplies the second by the first. Counting is
only meaningful once you know the per-endpoint price, so read that page first or
the survey produces numbers you cannot value.

**The worked numbers below are from one real application**, anonymised the way
the [adoption reviews](review-adoption-existing-app.md) anonymise theirs: a
multi-tenant Go service on chi, sqlc and goose — 312 routes, 568 named queries,
78 `CREATE TABLE` statements across 96 migrations. They are printed so each
command shows what its output looks like, not as a benchmark. Your ratios are
the point; these are the shape of the report.

---

## Before you count

**Run the database survey first.** A route census that concludes "twelve
resources are ready to mount" is worthless if nine of their tables carry a
composite primary key the DSL cannot declare. The database sets the ceiling:

```bash
go run ./cmd/sqlb survey -modules billing,catalog "$SRC_DSN" "$SCRATCH_DSN"
```

**Then count, in this order.** Each section narrows the one before it: routes
find the resources, queries find the surfaces worth generating, the scope
predicate finds what becomes a hook, and the client tells you what the generator
is worth on the far side of the wire.

---

## 1. The route table

### The leaf patterns

The cheapest useful count, and for classification it is sufficient — a route's
*shape* lives in its last segment, not in its prefix. `GET /{id}` is a read
whether it is mounted under `/api/tasks` or `/api/v2/admin/tasks`.

```bash
find . -name '*.go' -not -name '*_test.go' -print0 \
  | xargs -0 grep -hoE '\br\.(Get|Post|Put|Patch|Delete)\("[^"]*"' \
  | sed -E 's/^r\.//; s/\("/ /; s/"$//' \
  | sort | uniq -c | sort -rn | head -30
```

```
  23 Get /
  17 Get /{id}
  16 Post /
  15 Delete /{id}
  13 Patch /{id}
   3 Get /{id}/messages
   2 Post /{id}/reopen
   2 Post /{id}/cancel
   ...
```

The first five lines are the CRUD vocabulary; everything under them is the tail.
Split them:

```bash
find . -name '*.go' -not -name '*_test.go' -print0 \
  | xargs -0 grep -hoE '\br\.(Get|Post|Put|Patch|Delete)\("[^"]*"' \
  | sed -E 's/^r\.//; s/\("/ /; s/"$//' \
  | awk '{ if (($1=="Get"||$1=="Post"||$1=="Put") && $2=="/") print "crud";
           else if (($1=="Get"||$1=="Patch"||$1=="Put"||$1=="Delete") && $2 ~ /^\/\{[A-Za-z]+\}$/) print "crud";
           else print "other" }' \
  | sort | uniq -c
```

```
  92 crud
 220 other
```

**29% is the number to hold on to.** It is the fraction of the route table sqlb
is even arguing about. The other 220 routes — `/{id}/complete`, `/bulk-assign`,
`/upload`, `/billing/webhook`, `/auth/2fa/*` — stay hand-written on the same
router, by design and not as a shortfall. A survey that reports "sqlb replaces
the API" has miscounted; a survey that reports "sqlb replaces the least novel
third of it" has not.

### The full paths

Nested routers hide the prefix, so a leaf census cannot tell you which
*resource* a route belongs to. This flattens a chi tree by tracking brace depth:

```awk
# routes.awk — flatten a chi route tree into full paths
{ saved = $0; nopen = gsub(/\{/, "{"); nclose = gsub(/\}/, "}"); $0 = saved }
/(^|[^A-Za-z_])r\.Route\("/ {
  match($0, /r\.Route\("[^"]*"/); seg = substr($0, RSTART, RLENGTH)
  sub(/.*\("/, "", seg); sub(/"$/, "", seg)
  sp++; stack[sp] = seg; depthAt[sp] = depth
}
/(^|[^A-Za-z_])r\.(Get|Post|Put|Patch|Delete)\("/ {
  match($0, /r\.(Get|Post|Put|Patch|Delete)\("[^"]*"/); m = substr($0, RSTART, RLENGTH)
  meth = m; sub(/^r\./, "", meth); sub(/\(.*/, "", meth)
  pat = m; sub(/.*\("/, "", pat); sub(/"$/, "", pat)
  full = ""; for (i = 1; i <= sp; i++) full = full stack[i]
  full = full pat; gsub(/\/+/, "/", full)
  if (length(full) > 1) sub(/\/$/, "", full)
  printf "%-6s %s\n", meth, full
}
{ depth += nopen - nclose; while (sp > 0 && depth <= depthAt[sp]) sp-- }
```

```bash
awk -f routes.awk cmd/server/serve.go
```

```
Get    /api/tasks
Post   /api/tasks
Post   /api/tasks/bulk-assign
Get    /api/tasks/{id}
Patch  /api/tasks/{id}
Delete /api/tasks/{id}
Post   /api/tasks/{id}/complete
Post   /api/tasks/{id}/cancel
Post   /api/tasks/{id}/hold
...
```

That single resource is the whole finding in miniature: **a five-operation CRUD
core with seven domain verbs hanging off it.** `rest.Resource` takes the first
five. The seven are either hand-written handlers on the same router or
[declared actions](rest/actions.md), which generate the envelope and leave the
verb as plain Go — but they are not free either way, and they are the majority.

Note the trailing-slash normalisation. `r.Route("/tasks")` plus `r.Get("/")`
composes to `/api/tasks/`, which sorts apart from `/api/tasks` and splits one
resource into two half-populated ones. That single `sub` is the difference
between "no collection has a full CRUD set" and the table below.

### Which resources are actually mountable

```bash
awk -f routes.awk cmd/server/serve.go | awk '
  { m=$1; p=$2; v=""
    if (p ~ /\/\{[A-Za-z]+\}$/) { sub(/\/\{[A-Za-z]+\}$/,"",p); v = (m=="Get"?"read":(m=="Delete"?"delete":"update")) }
    else if (p !~ /\{/)         { v = (m=="Get"?"list":(m=="Post"?"create":"")) }
    if (v && index(ops[p], v)==0) { ops[p] = ops[p] " " v; n[p]++ }
  }
  END { for (p in ops) printf "%d\t%s\t%s\n", n[p], p, ops[p] }' | sort -rn
```

```
5	/api/tasks	           list create read update delete
5	/api/qualifications	   list create read update delete
5	/api/projects	       list create read update delete
5	/api/documents	       list create read update delete
...
4	/api/time-entries	   list read update delete
4	/api/superadmin/organizations   list create read delete
3	/api/work-package-contacts      read update delete
```

| Operations present | Collections | What it means |
|---:|---:|---|
| 5 of 5 | **10** | `rest.Resource` candidates, unmodified |
| 4 of 5 | 6 | Mountable with `Ops:` narrowed — check *why* the fifth is absent |
| 3 of 5 | 10 | A sub-resource or a deliberately closed surface |
| 1–2 of 5 | 121 | Not resources. Verbs, singletons, webhooks, static reads |

**Ten full-CRUD collections out of 147** is the honest headline, and it is the
number a pilot is chosen from. The 4-of-5 row deserves a read rather than a
count: a missing `delete` is usually a soft-delete policy, which is a
`BeforeQuery` hook and not a missing operation; a missing `list` is usually a
singleton, which is not a resource at all.

### Other routers

The recipes above assume chi's method-per-line style. The census does not change
shape for the others, only its first step:

- **Huma** — registrations are struct literals, so extract `Method:` and `Path:`
  from `huma.Register` call sites rather than parsing a nesting. There is no
  prefix to flatten, which makes this the easy case.
- **gin / echo** — `router.GET("/path", …)`; the leaf recipe works after
  changing the method names, and groups (`r.Group("/api")`) flatten the same way
  chi's `Route` does.
- **net/http with `ServeMux` patterns** — `mux.HandleFunc("GET /tasks/{id}", …)`
  already carries the method and the full path in one string, so both recipes
  collapse into one `grep -o`.

---

## 2. The queries

### By kind

```bash
grep -rhoE '^-- name: [A-Za-z0-9_]+ :[a-z]+' internal/db/queries \
  | awk '{print $NF}' | sort | uniq -c
```

```
   2 :copyfrom
 100 :exec
   1 :execresult
   5 :execrows
 141 :many
 319 :one
```

`:one` dominating at 56% is typical and is *not* an adoption signal on its own —
most of those are a single-row read inside a domain verb, which sqlb does not
touch.

### By shape

This is the classification that matters. Each named query is bucketed by the SQL
constructs in its body:

```bash
awk '
  /^-- name: / { if (n) classify(); n=$3; body=""; next }
  { body = body " " toupper($0) }
  END { if (n) classify() }
  function classify() {
    if (body ~ /WITH [A-Z_]+ AS|OVER *\(|GROUP BY|UNION|DISTINCT ON|GENERATE_SERIES/) print "reporting"
    else if (body ~ /SQLC\.NARG|COALESCE\(\$|IS NULL OR/) print "dynamic-workaround"
    else if (body ~ / JOIN /) print "join"
    else print "single-table"
  }
' internal/db/queries/*.sql | sort | uniq -c | sort -rn
```

```
 395 single-table
  99 dynamic-workaround
  49 join
  25 reporting
```

| Bucket | Count | Where it goes |
|---|---:|---|
| `dynamic-workaround` | **99** | **sqlb.** This is the structural win, and the bucket to read first |
| `single-table` | 395 | sqlb where it backs a mounted resource; otherwise leave it alone |
| `join` | 49 | Candidates for [`?expand`](rest/expand.md) — needs a declared reference, so check against the survey |
| `reporting` | 25 | **sqlc, permanently.** `Raw` is an escape hatch, not a feature ([with-sqlc](with-sqlc.md)) |

**The 99 is the number the adoption argument rests on.** Those queries contain
`sqlc.narg`, `COALESCE($1, column)` or `($1::text IS NULL OR column = $1)` —
each one a hand-written simulation of a `WHERE` clause that depends on which
parameters the request happened to carry. sqlc cannot express that, not for want
of a feature but because the query does not exist yet when sqlc runs. That is
the one thing sqlb is structurally better at, and here it is, counted.

**Deflate that bucket before believing it.** A `COALESCE($1, x)` in an
`UPDATE`'s `SET` list is a partial-update idiom, not a dynamic filter, and it
counts here without belonging here. Split it out:

```bash
awk '
  /^-- name: / { if (n) classify(); n=$3; body=""; next }
  { body = body " " toupper($0) }
  END { if (n) classify() }
  function classify() {
    if (body ~ /WITH [A-Z_]+ AS|OVER *\(|GROUP BY|UNION|DISTINCT ON|GENERATE_SERIES/) return
    if (body ~ /SQLC\.NARG|COALESCE\(\$|IS NULL OR/) print (body ~ /UPDATE /) ? "update-idiom" : "real-filter"
  }
' internal/db/queries/*.sql | sort | uniq -c
```

```
  75 real-filter
  24 update-idiom
```

**75, then.** A quarter of the bucket evaporated on one line of shell, which is
the argument for running this check rather than quoting the first number: a
census that survives its own deflation is worth showing to someone who has to
approve the work.

---

## 3. The scope predicate

Every multi-tenant read carries the tenant somewhere. Find out where, and how
consistently:

```bash
awk '/^-- name: /{ if(n) print (body ~ /ORG_ID|TENANT_ID|ORGANIZATION_ID/) ? "scoped" : "unscoped";
                   n=$3; body=""; next }
     { body = body " " toupper($0) }
     END { if(n) print (body ~ /ORG_ID|TENANT_ID|ORGANIZATION_ID/) ? "scoped" : "unscoped" }' \
  internal/db/queries/*.sql | sort | uniq -c
```

```
 339 scoped
 229 unscoped
```

Both halves are findings.

**The 339 are what becomes one hook per model.** A predicate repeated 339 times
by convention — enforced, if you are careful, by an architecture test that greps
for it — becomes a `BeforeQuery` registration that the query builder cannot be
asked to skip. This is the part of an adoption that is a safety argument rather
than a line-count argument, and it is why
[ADR-0030](architecture.md#declared-scope-is-required) makes a model whose schema
declares its rows confined refuse to mount without one.

**The 229 need reading, not counting.** Some are legitimately global — lookup
tables, the migration bookkeeping, the auth path before a tenant is known. Some
are joins that inherit scope from a parent table. And some are the bug the hook
would have prevented. You cannot tell which from the count; the value of the
count is that it bounds how long the reading takes.

---

## 4. The client

The generated TypeScript client is usually the largest single win, so size what
it would replace:

```bash
# Hand-written SDK, non-test
find web/src -path '*sdk*' -name '*.ts' -not -name '*.test.ts' | xargs wc -l | tail -1
# Hand-maintained cache keys
grep -rho 'queryKey:' web/src | wc -l
```

```
 456 queryKey:
```

456 hand-written cache keys is 456 chances to invalidate the wrong one, and a
generated key factory makes the whole class of bug unavailable. Count the
`invalidateQueries` call sites alongside it — the ratio between the two is a
decent proxy for how much of the client's complexity is cache bookkeeping rather
than product code.

---

## Reading the result

| If | Then |
|---|---|
| `real-filter` is a large share of the queries | The strongest case. This is the row sqlb exists for |
| ≥ 5 collections have a full CRUD set | A pilot has an obvious first subject |
| `scoped` is high and `unscoped` is unexplained | Adopt for the safety argument before the line-count one |
| `reporting` is a large share | A smaller adoption than it first looked; sqlc keeps that half |
| The survey blocks most tables | Stop. Fix the schema half first — the route census cannot be acted on |
| CRUD-shaped routes are under ~15% of the table | The API is mostly domain verbs. sqlb has little to say about it |

---

## What this cannot tell you

Stated because a census that reads as complete is worse than one that reads as
partial.

- **Whether a `GET` collection is a list surface.** Returning every row of a
  small table is not the same endpoint as filter-sort-paginate over a large one,
  and only one of them is worth generating. Grep the handler for query-parameter
  reads (`URL.Query().Get("sort"|"limit"|"search")`) to shortlist, then read
  them.
- **What the handlers do besides query.** Status transitions, activity emission,
  billing limits and geocoding live inside the same functions and *relocate*
  into hooks rather than disappearing. A LOC count of the handler layer
  overstates the saving by whatever fraction that is — in the reviewed corpora,
  about two thirds of the single-row surface.
- **Whether the wire format can change.** Field casing, error envelopes and
  pagination shape are a client contract, and whether it can break is a product
  question with no signal in the source.
- **Whether the migration history is honest.** That is `shadow.Build` against
  `introspect`, in [Adopting a database](migrations/adopting.md), and a
  disagreement there invalidates the whole schema half.
- **Which of the 220 non-CRUD routes are secretly resources.** A
  `POST /{id}/duplicate` is a verb; a `GET /{id}/messages` is very often a
  nested collection that would mount cleanly. The pattern census flags them;
  only reading resolves them.

---

## The pilot falls out of the numbers

Take the intersection of three lists: collections with a full CRUD set, whose
tables `sqlb survey` reports adoptable, and whose queries fall in the deflated
`real-filter` bucket. Ten, then fewer, then fewer again — and the *smallest*
surviving member is the pilot, not the most important one —
[§8 of the adoption review](review-adoption-existing-app.md#8-if-you-want-to-try-it--the-smallest-honest-experiment)
is what to do with it once chosen: read-only, one endpoint, behind a flag, with
the old handler still live.

If the intersection is empty, the survey has done its job. That is a cheaper
answer than a pilot would have been.
