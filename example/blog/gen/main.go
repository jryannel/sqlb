// Command gen regenerates the blog example's models and typed column facade
// from its schema declaration.
//
//	go generate ./example/blog/...     regenerate
//	go run ./example/blog/gen -check   fail if the committed output is stale
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/jryannel/sqlb/codegen"
	"github.com/jryannel/sqlb/schema"

	// Imported for its side effects: declaring a table registers it.
	_ "github.com/jryannel/sqlb/example/blog/blogschema"
)

func main() {
	check := flag.Bool("check", false, "report stale generated files instead of writing them")
	flag.Parse()

	opts := codegen.Options{
		Registry: schema.DefaultRegistry(),
		Dir:      "example/blog",
		Package:  "blog",
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
