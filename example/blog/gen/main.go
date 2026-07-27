// Command gen regenerates the blog example's models and typed column facade
// from its schema declaration.
//
//	go generate ./example/blog/...
package main

import (
	"github.com/jryannel/sqlb/codegen"
	"github.com/jryannel/sqlb/schema"

	// Imported for its side effects: declaring a table registers it.
	_ "github.com/jryannel/sqlb/example/blog/blogschema"
)

func main() {
	codegen.Must(codegen.Generate(codegen.Options{
		Registry: schema.DefaultRegistry(),
		Dir:      "example/blog",
		Package:  "blog",
	}))
}
