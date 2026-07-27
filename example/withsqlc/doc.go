// Package withsqlc demonstrates sqlb and sqlc over one schema.
//
// The claim in the README — that sqlb can be layered over structs it did not
// generate, so adoption need not be all-or-nothing — is easy to assert and easy
// to be wrong about. This package tests it against real sqlc output rather than
// against structs written to make it pass.
//
// The pipeline, and what each step proves:
//
//	blogschema/schema.go            one schema declaration
//	  → gen → schema.sql            sqlb renders the DDL; -check keeps it current
//	  → sqlc → sqlcgen/models.go    sqlc types its queries against that DDL
//	  → sqlb.Describe               sqlb reads those same structs
//
// See docs/with-sqlc.md for which queries belong on which side.
//
// Regenerating is two steps because they are two tools:
//
//	go generate ./example/withsqlc/...
//
//go:generate go run ./gen -dir .
//go:generate sqlc generate
package withsqlc
