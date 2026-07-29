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
// Regenerating is two steps because they are two tools, and only the first is
// a go:generate directive:
//
//	go generate ./example/withsqlc/...      renders schema.sql from the declaration
//	cd example/withsqlc && sqlc generate    retypes sqlcgen against it
//
// The second is manual on purpose. Behind a directive it made `go generate
// ./...` — and so `mise run heal`, which CONTRIBUTING.md hands a new
// contributor first — fail on every checkout without sqlc installed. Pinning
// sqlc in mise.toml would fix that by making it a build dependency of a library
// whose whole argument is that it imposes none, which is the same reason the
// sqlc step is absent from `mise run generate-check`.
//
// The cost is that nothing regenerates or gates sqlcgen: after a schema change,
// run the second step by hand. That cost is not new — no gate ever covered it —
// and the drift that is covered still is, because the directive below renders
// schema.sql and `go run ./gen -check` fails in CI when it is stale.
//
//go:generate go run ./gen -dir .
package withsqlc
