package outboxschema

//go:generate go run github.com/jryannel/sqlb/cmd/sqlb generate .

import "github.com/jryannel/sqlb/codegen"

// SqlbProject tells `sqlb generate` what this example emits: Go only, no REST
// exposure and no clients — this example is about the claim mechanism under
// contention and the retry/dead-letter policy around it, not about a
// generated API surface. Event declares no schema.REST.
func SqlbProject() codegen.Project {
	return codegen.Project{
		Options: codegen.Options{
			Package: "outbox",
		},
	}
}
