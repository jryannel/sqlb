// Command taskctl drives the task manager's API from a shell.
//
//	export TASKCTL_BASE_URL=http://localhost:8080
//	export TASKCTL_TOKEN="$(curl -s -X POST "$TASKCTL_BASE_URL/auth/login" \
//	    -H 'content-type: application/json' \
//	    -d '{"email":"you@example.com","password":"..."}' | jq -r .token)"
//
//	go run ./cmd/taskctl tasks list --status eq.todo --sort -created_at
//	go run ./cmd/taskctl tasks list --help
//
// Every command below this one is generated from taskschema, so a column that
// gains a capability gains its flag at the next `go generate ./...` and one
// that loses it loses the flag rather than starting to 400.
//
// This file is the part that is not generated, and it is deliberately the whole
// of it: the transport, the credential and the exit code are decisions a schema
// cannot make.
package main

import (
	"os"

	"github.com/jryannel/sqlb/example/tasks/cli"
)

func main() {
	// The error is not printed here: cobra has already written it to stderr,
	// including the list of values the server said it would have accepted, and
	// printing it twice would bury that under a repetition.
	if err := cli.New(nil).Execute(); err != nil {
		os.Exit(1)
	}
}
