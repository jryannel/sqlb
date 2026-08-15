# example/catalog — the tree, and where search stops

`docs/special-cases.md` calls this "the tree, and where search stops": a
product catalog whose categories point at their own table, and a search box
that stops at ILIKE on purpose. This is that example, worked against a real
Postgres.

**The correction, first.** `docs/special-cases.md` and
`pgtest/census_test.go`'s `TestSelfReferenceIsAPlainColumnWithoutAForeignKey`
both describe the self-reference as unexpressible in the direct form — the
test's own doc comment says "there is no `AddField`" and demonstrates the
fallback, `ExternalRef`, instead. That is no longer true.
`TableDef.AddField(f *Field) *Field` (`schema/table.go`) exists now, and it is
exactly the handle a table needs to refer back to itself:

```go
var Category = schema.Table("categories",
	schema.UUIDv7("id").PrimaryKey(),
	schema.Text("name").Filterable().Searchable(),
)

var _ = Category.AddField(
	schema.Ref("parent", Category).Nullable().Filterable().Expandable(),
)
```

`Ref(name, target)` needs `target` as a value, which a table cannot be while
its own `Table(...)` call is still running — that half of the census's claim
still holds. What changed is that the reference no longer has to be declared
in that same call. `AddField` runs as a second statement, after `Category`
already exists, so `Ref("parent", Category)` reads a real, finished
`*TableDef` — including `Category.PrimaryKey()`, which is how `parent_id`
gets its type and its target column. Go's package-level initialisation order
does the rest: `Category.AddField(...)` depends on `Category`, so it runs
after `Category`'s own declaration, every time, without an explicit ordering
annotation.

`catalogschema/schema.go` is the whole schema — one table, one self-reference
— and its own doc comment carries this same argument. `docs/special-cases.md`
and the census test's doc comment are now stale on this specific point and are
worth a follow-up PR; this example does not make that edit itself, on the
theory that the correction belongs beside the tests it corrects, made
deliberately rather than as a side effect of building the thing that proves it
wrong.

**It actually works, measured rather than asserted.**
`TestSelfReferenceIsARealForeignKeyNow` builds a three-level tree — root,
child, grandchild — with plain `InsertRows`, then resolves the grandchild's
parent inline with `Expand("parent")` and checks the resolved row is the
child. The relation `Expand` joins on is the same `Ref` the FK constraint
comes from, so this is one mechanism proving two things at once.

## What the foreign key buys, and what it does not

**It buys referential integrity**, which `ExternalRef` explicitly does not:
`TestForeignKeyRefusesADanglingParent` inserts a category whose `parent_id`
names a UUID that does not exist anywhere and watches it fail with a
`*sqlb.ConstraintError` whose `Kind` is `sqlb.ConstraintForeignKey` — the
opposite of the census test three paragraphs up, which inserts the identical
shape of row against `ExternalRef` and watches it *succeed*, because that
column renders no constraint at all.

**It buys the same refusal on delete**, and this is checked rather than
assumed: `schema.Ref`'s default is `OnDelete: schema.NoAction` unless a caller
overrides it, and `catalogschema` does not.
`TestDeletingAParentWithChildrenIsRefused` deletes a category that still has a
child and gets the same `ConstraintForeignKey` — `NO ACTION` is checked
immediately for a constraint that is not `DEFERRABLE`, so the delete fails in
the same statement rather than cascading or silently orphaning the child. A
catalog that wanted cascading deletes would say so explicitly with
`.OnDelete(schema.Cascade)`; this one does not, and the test is what would
catch it if that changed by accident.

**It does not buy cycle protection**, and nothing here claims otherwise. A
foreign key constrains what a column may point at — a row that exists — not
the shape of the graph those pointers draw. Nothing stops `parent_id = id` on
a single row, or a longer cycle, `a -> b -> a`, each link individually valid
and the whole loop unreachable from any real root. That is a real, distinct
gap the foreign key does not close, and it is not exercised here: catching it
needs either an application-level check before the write or `WITH RECURSIVE`
after it, and `WITH RECURSIVE` is a documented non-goal — the census counted
zero recursive CTEs in the corpus this repository draws its cases from, and
`docs/vision.md` says that kind of query belongs in hand-written SQL, not the
DSL.

## Search stops at ILIKE

`name` is `.Searchable()`. `TestSearchIsSubstringNotFullText` inserts a
handful of rows and queries `sqlb.F("name").Contains(term)` — the same
predicate `filter.go`'s `?search` fan-out compiles every `Searchable` column
down to (`sqlb.F(col.Name).Contains(term)`, `filter/filter.go`). It finds a
substring anywhere in the name, case-insensitively, and nothing else: no
stemming, no ranking, no index required. That is
[ADR-0037](../../docs/architecture.md#search-is-ilike-until-it-cannot-be)'s
decision, written as a passing test instead of a paragraph. Deliberately not
built here: a ranking model, relevance tuning, a trigram index, the `tsvector`
column ADR-0037 leaves for a later record, or the vector column
[ADR-0026](../../docs/architecture.md#vectors-declare-their-index) leaves open.
Widening past ILIKE is a decision for one of those records to make, not a gap
this example papers over.

## Running it

```
mise run pg-up
export SQLB_TEST_POSTGRES='postgres://sqlb:sqlb@localhost:15432/sqlb?sslmode=disable'
cd example/catalog
go test ./... -v -race
```

(If `go build`/`go test` here fails with `compile: version "X" does not match
go tool version "Y"`, a stale `GOROOT` in the shell environment is pointing at
a different Go install than the `go` binary on `PATH` resolves to —
`env -u GOROOT` or `unset GOROOT` before the command works around it. Not
specific to this module; every module in this worktree hits it the same way.)
