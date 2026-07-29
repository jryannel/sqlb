package taskschema

//go:generate go run github.com/jryannel/sqlb/cmd/sqlb generate .

import "github.com/jryannel/sqlb/codegen"

// SqlbProject tells `sqlb generate` what this example emits and where.
//
// example/tasks is a module of its own, so the module root is example/tasks and
// every path below is relative to it. That is why Dir is left empty: the
// generated Go lands beside go.mod, which is where the tasks package is.
//
// What this replaces is worth being precise about, because it is the entire
// case for #16. It was cmd/gen/main.go: a flag for -check, a flag for -dir with
// a default that was correct from the module root and wrong from the directory
// go generate actually runs in, two error branches, and this literal. The
// literal is the only part that said anything about this project. Everything
// else was the same thirty lines every sqlb project had to write, and get
// right, before the tool would run at all.
func SqlbProject() codegen.Project {
	return codegen.Project{
		Options: codegen.Options{
			Package: "tasks",

			// The TypeScript client, emitted into the frontend that consumes it
			// rather than published as a package. A client generated against the
			// server it talks to cannot be a version behind it, which is the
			// property models_gen.go already has and a published SDK cannot.
			TSDir: "web/src/api",

			// The Dart client, into the Flutter app's package. Same argument as the
			// TypeScript one, and one more that is specific to a phone: a list on a
			// small screen loads as it is scrolled, which is cursor paging, and
			// cursor paging is the thing hand-written clients reimplement out of
			// has_more and an offset counter.
			DartDir: "mobile/lib/api",

			// The command-line client, for the same reason and for one more: the
			// caller most likely to drive this API is an agent, and `taskctl tasks
			// list --help` is a statement of what the resource accepts that costs
			// no round trip and no 400 to read. cmd/taskctl is the four-line main
			// that runs it.
			CLIDir:  "cli",
			CLIName: "taskctl",
		},
	}
}
