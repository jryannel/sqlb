package roomsschema

//go:generate go run github.com/jryannel/sqlb/cmd/sqlb generate .

import "github.com/jryannel/sqlb/codegen"

// SqlbProject tells `sqlb generate` what this example emits: Go only, no
// REST exposure and no clients, because neither Room nor Booking declares a
// schema.REST — this example is about what the schema and the query engine do
// under contention, not about a generated API surface.
func SqlbProject() codegen.Project {
	return codegen.Project{
		Options: codegen.Options{
			Package: "rooms",
		},
	}
}
