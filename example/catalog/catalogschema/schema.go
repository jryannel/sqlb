// Package catalogschema is the schema for example/catalog: a product catalog
// whose categories form a tree, and a search box that stops exactly where
// ADR-0037 says `?search` stops.
//
// docs/special-cases.md calls the self-reference "universal and counted zero
// times" and, as of the writing that shipped alongside it, unexpressible: Ref
// needs the target table as a value, and a table cannot be its own value while
// it is still being declared. pgtest/census_test.go's
// TestSelfReferenceIsAPlainColumnWithoutAForeignKey documents the fallback —
// ExternalRef, a string reference with no foreign key — as the only way to
// name a parent category, and it does that by not compiling the direct form at
// all: "there is no AddField".
//
// There is now. TableDef.AddField(f *Field) *Field, added after that comment
// was written, adds one column to an already-declared table and returns it —
// which is exactly the handle a second statement needs to refer back to a
// table its own declaration could not yet see. That turns the self-reference
// from "cannot be declared" into "declared in two statements instead of one":
//
//	var Category = schema.Table("categories", …)
//	var _ = Category.AddField(schema.Ref("parent", Category).Nullable()…)
//
// The second statement runs after the first — Go initialises package-level
// vars in dependency order, and this one depends on Category — so by the time
// Ref reads Category.PrimaryKey() to type and name the parent_id column, the
// table it is pointing at already has one.
package catalogschema

import "github.com/jryannel/sqlb/schema"

// Category is a node in the tree. Its parent is declared below, once the
// table exists to point back at.
var Category = schema.Table("categories",
	schema.UUIDv7("id").PrimaryKey(),
	schema.Text("name").Filterable().Searchable(),
)

// parent is a real foreign key this time, not the string-typed ExternalRef the
// census fell back to — which is the entire point of this example. Nullable,
// because the root of the tree has no parent; Filterable, so a category's
// children are a plain query; Expandable, so a query for a node can resolve
// its parent inline with ?expand=parent instead of a second round trip.
//
// What this does not do: stop a category from naming itself, or a longer
// cycle — a -> b -> a. A foreign key constrains what a column may point at,
// not what shape the graph it draws is; see the README for what that gap
// costs and why nothing here closes it.
var _ = Category.AddField(
	schema.Ref("parent", Category).Nullable().Filterable().Expandable(),
)
