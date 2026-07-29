// Command sqlb regenerates a project's models, clients and manifest from its
// schema declaration, and reports when the committed output has drifted from it.
//
//	sqlb generate ./taskschema     write every artefact the project declares
//	sqlb check ./taskschema        report stale artefacts, write nothing
//
// # Why this needs a package argument
//
// The schema is Go, and a table is registered by the side effect of importing
// the package that declares it (ADR-0004). A prebuilt binary therefore cannot
// read a registry — nothing is in it until the schema package is linked in. So
// this command does the only thing that can work: it writes a driver program
// that imports the named package, compiles it inside your module, runs it, and
// deletes it. The argument is the package to import, in the same form `go
// build` takes.
//
// What the driver does once it is running lives in codegen.Main, not in emitted
// source, so that the interesting half of this command is ordinary tested code.
//
// # What it costs
//
// A `go run`, which on a warm build cache is well under a second and on a cold
// one is a compile of your module's dependency graph. That is the price of the
// schema being Go rather than a config file, and it is paid here instead of in
// the per-project `cmd/gen/main.go` this replaces.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		var exit exitCode
		if errors.As(err, &exit) {
			// The driver already said what was wrong, on the stderr this
			// process shares with it. Repeating it here would print every
			// stale file twice.
			os.Exit(int(exit))
		}
		fmt.Fprintln(os.Stderr, "sqlb:", err)
		os.Exit(1)
	}
}

// exitCode carries a child process's status out through the error path, so that
// `sqlb check` in CI fails with the code the driver chose rather than a
// flattened 1.
type exitCode int

func (c exitCode) Error() string { return fmt.Sprintf("exit status %d", int(c)) }

const usage = `sqlb regenerates a project's models, clients and manifest from its schema.

Usage:

    sqlb generate <package>    write every artefact the project declares
    sqlb check <package>       report stale artefacts, write nothing
    sqlb version               print the version this binary was built from

<package> is the Go package that declares the schema, in the form go build
takes — usually ./schema or ./taskschema. It must export:

    func ` + funcSignature + `

Paths in that Project resolve against the module root, so the commands above
mean the same thing from a shell, from a //go:generate directive and from CI.
`

// run is main without the exit, so that the tests can drive the whole command.
//
// The `_, _ =` on every write is the deliberate form: a command whose stderr
// has gone away has no remaining channel on which to report that. codegen.Run
// says the same thing at more length, and this is the only other place in sqlb
// where it applies.
func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return exitCode(2)
	}

	verb := args[0]
	switch verb {
	case "help", "-h", "-help", "--help":
		_, _ = fmt.Fprint(stdout, usage)
		return nil
	case "version":
		_, _ = fmt.Fprintln(stdout, version())
		return nil
	case "generate", "check":
	default:
		_, _ = fmt.Fprintf(stderr, "sqlb: unknown command %q\n\n", verb)
		_, _ = fmt.Fprint(stderr, usage)
		return exitCode(2)
	}

	rest := args[1:]
	if len(rest) != 1 {
		return fmt.Errorf(
			"%s takes exactly one package argument, for example: sqlb %s ./schema", verb, verb)
	}

	pkg, err := resolve(rest[0])
	if err != nil {
		return err
	}
	return drive(pkg, verb, stdout, stderr)
}
