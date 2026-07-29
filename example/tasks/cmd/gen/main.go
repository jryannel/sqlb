// Command gen regenerates the task-manager example's models, typed column
// facade, REST bodies, manifest and TypeScript client from its schema
// declaration.
//
//	go generate ./...     regenerate
//	go run ./cmd/gen -check   fail if the committed output is stale
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/jryannel/sqlb/codegen"
	"github.com/jryannel/sqlb/schema"

	// Imported for its side effects: declaring a table registers it.
	_ "github.com/jryannel/sqlb/example/tasks/taskschema"
)

func main() {
	check := flag.Bool("check", false, "report stale generated files instead of writing them")
	// go generate runs a directive with the working directory set to the
	// package that declares it — taskschema, not the module root — so the
	// output directory has to be given rather than assumed. The default suits a
	// run from the module root; the directive in taskschema passes "..".
	dir := flag.String("dir", ".", "output directory, relative to the working directory")
	flag.Parse()

	opts := codegen.Options{
		Registry: schema.DefaultRegistry(),
		Dir:      *dir,
		Package:  "tasks",

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
	}

	if *check {
		stale, err := codegen.Check(opts)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if len(stale) > 0 {
			fmt.Fprintln(os.Stderr, "generated code is out of date; run: go generate ./...")
			for _, f := range stale {
				fmt.Fprintln(os.Stderr, "  "+f)
			}
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "generated code is current")
		return
	}
	codegen.Must(codegen.Generate(opts))
}
