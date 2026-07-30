package recipes_test

import (
	"context"
	"fmt"

	"github.com/jryannel/sqlb"
	"github.com/jryannel/sqlb/example/recipes"
)

// An array column is a plain Go slice. `Tags []string` maps to `text[]`, and
// scanning decodes the Postgres array literal into the slice — there is no
// wrapper type to adopt and no driver-specific `pq.StringArray` in the model,
// which matters because the model is also what a generated client is shaped
// from.
func Example_arrayScansIntoASlice() {
	post, err := sqlb.Query[recipes.Post]().First(context.Background(), recordingDB())
	if err != nil {
		panic(err)
	}
	fmt.Printf("%q\n", post.Tags)
	// Output:
	// ["go" "sql"]
}

// The three array predicates, and the difference between them is the whole
// point:
//
//	Has     the array contains this one element
//	HasAny  the array overlaps these — at least one in common
//	HasAll  the array contains every one of these
//
// Has takes a single value rather than a list, because `$1 = ANY(tags)` is the
// form an index over the column serves.
func Example_arrayContainment() {
	showWhere(sqlb.Query[recipes.Post]().Where(sqlb.F("tags").Has("go")))
	showWhere(sqlb.Query[recipes.Post]().Where(sqlb.F("tags").HasAny("go", "rust")))
	showWhere(sqlb.Query[recipes.Post]().Where(sqlb.F("tags").HasAll("go", "postgres")))
	// Output:
	// WHERE $1 = ANY("tags")
	// args: [go]
	// WHERE "tags" && $1
	// args: [{go,rust}]
	// WHERE "tags" @> $1
	// args: [{go,postgres}]
}

// The empty cases follow from what the operators mean rather than from a
// convention, and they differ: an overlap with nothing is nothing, and every
// array contains the empty array. Knowing which is which is the reason to
// think about the empty case at all.
func Example_arrayEmptyValueSets() {
	var none []any

	showWhere(sqlb.Query[recipes.Post]().Where(sqlb.F("tags").HasAny(none...)))
	showWhere(sqlb.Query[recipes.Post]().Where(sqlb.F("tags").HasAll(none...)))
	// Output:
	// WHERE false
	// WHERE true
}

// Writing an array is the same slice going the other way. Array encodes a Go
// value as a Postgres array literal for the cases where the value is not
// already a slice of the column's element type.
func Example_arrayWritten() {
	show(sqlb.UpdateRows[recipes.Post]().
		Set("tags", []string{"go", "postgres"}).
		Where(sqlb.F("id").Eq("p1")))

	literal, err := sqlb.EncodeArray([]string{"go", "a,b", `quote"d`})
	if err != nil {
		panic(err)
	}
	fmt.Println("encoded:", literal)
	// Output:
	// UPDATE "posts" SET "tags" = $1 WHERE "id" = $2 RETURNING "id", "org_id", "author_id", "title", "body", "status", "view_count", "tags", "metadata", "published_at", "deleted_at", "created_at"
	// args: [{go,postgres} p1]
	// encoded: {go,"a,b","quote\"d"}
}
