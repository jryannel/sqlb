// Package filter compiles URL query parameters into sqlb predicates.
//
// It is the second producer of the predicate AST: hand-written Go is the first.
// Both go through the same builder, so a filter arriving over HTTP is subject
// to the same compilation, the same bind-parameter discipline and the same
// query hooks as a query written by hand.
//
// Nothing is filterable, sortable or searchable unless the column declares that
// capability, and the parser reports the allowed columns when a request asks
// for one that does not. A request naming an unknown or uncapable column is a
// 400, never a leak and never a silently ignored parameter.
//
// Grammar:
//
//	?status=eq.active            operator form
//	?email=alice@example.com     shorthand, equivalent to eq
//	?age=gte.18&age=lt.65        repeated params conjoin
//	?tag=in.a,b,c                value lists
//	?deleted_at=isnull           null tests
//	?metadata=hasdoc.{"lang":"de"}    jsonb containment
//	?or=(status.eq.draft,age.lt.18)   explicit disjunction
//	?filter={"op":"and",...}     JSON expression tree, for arbitrary nesting
//	?sort=-created_at,name       sorting, "-" for descending
//	?select=id,name              projection
//	?search=ada                  fan-out over searchable columns
//	?page=2&per_page=50          pagination
//
// The same predicates can arrive as a JSON expression tree, in the ?filter=
// parameter above or — via [ParseFilterTree] — on their own. It is a second
// frontend over the same compiler, so a JSON filter is subject to the identical
// column gate, coercion, bind discipline and MaxFilters budget; the URL grammar
// is what a query string can spell, not the limit of what the package accepts.
// A request may carry both, and Parse charges their conditions to one budget.
package filter
