package blogschema

//go:generate go run github.com/jryannel/sqlb/cmd/sqlb generate .

import "github.com/jryannel/sqlb/codegen"

// SqlbProject tells `sqlb generate` what this example emits and where.
//
// It replaces a cmd/gen/main.go that was thirty lines of flag parsing around
// this literal, and — more to the point — it removes the -dir argument that
// main had to be given. Paths here resolve against the module root, which for
// this example is the repository root, so "example/blog" means the same thing
// whether the command is run from a shell, from the //go:generate directive in
// schema.go, or from CI.
//
// The blog example emits Go only. It is the short path from a schema to a
// server; example/tasks is the one that adds the TypeScript, Dart and CLI
// clients.
func SqlbProject() codegen.Project {
	return codegen.Project{
		Options: codegen.Options{
			Dir:     "example/blog",
			Package: "blog",
		},
	}
}
