// Command sqlb keeps a project's generated code and migration history in step
// with its schema declaration, and reports when either has drifted from it.
//
//	sqlb generate ./taskschema              write every artefact the project declares
//	sqlb check ./taskschema                 report stale artefacts, write nothing
//	sqlb migrate -name adds_priority ./taskschema   write the next migration
//	sqlb migrate -check ./taskschema        report whether the schema has moved ahead
//
// # Two gates, and only one of them needs a database
//
// `check` compares committed output with what the emitters produce now, which
// is a pure function of the schema. `migrate -check` asks whether the committed
// migration history *builds* that schema, and the only trustworthy way to
// answer it is to replay the history into an empty Postgres — reading a live
// database reports what it looks like, not whether the migrations produce it
// (ADR-0014). So the first runs on every push and the second needs a scratch
// database, and they are separate for that reason.
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
	"strings"
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

const usage = `sqlb keeps a project's generated code and migration history in step with its schema.

Usage:

    sqlb generate <package>          write every artefact the project declares
    sqlb check [flags] <package>     report stale artefacts, write nothing; with
                                     -database, also report a declaration that no
                                     longer describes the live database
    sqlb migrate [flags] <package>   write the migration that closes the gap
                                     between the history and the schema
    sqlb impact [flags] <package>    report how the schema edit changes the REST
                                     contract, against a checked-in baseline
    sqlb eject [flags] <package>     write the exit: the schema as SQL and the
                                     resources as plain handlers, importing pgx
                                     and the standard library and nothing else
    sqlb version                     print the version this binary was built from

Flags for check:

    -database <dsn>       compare the declared schema against this database too,
                          and exit non-zero if they disagree

Flags for migrate:

    -name <name>          what the migration does; becomes part of the filename
    -check                report whether the schema has moved ahead; write nothing
    -dry-run              print what would be written; write nothing
    -unblock              use the concurrent forms of the long-lock statements
    -allow-destructive    emit destructive statements live, not commented out

Flags for impact:

    -write                record the current REST contract as the new baseline
    -error                exit non-zero if the contract has breaking changes

Flags for eject:

    -check                report whether the committed exit is stale; write nothing

<package> is the Go package that declares the schema, in the form go build
takes — usually ./schema or ./taskschema. It must export:

    func ` + funcSignature + `

Paths in that Project resolve against the module root, so the commands above
mean the same thing from a shell, from a //go:generate directive and from CI.

generate, impact and eject need no database, and neither does check until it is
given one. migrate reads the current schema by
replaying the committed history into a scratch Postgres, so it needs the
Project's ShadowDB — except for the first migration, which diffs against
nothing and needs no database at all.
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
	case "generate", "check", "migrate", "impact", "eject":
	default:
		_, _ = fmt.Fprintf(stderr, "sqlb: unknown command %q\n\n", verb)
		_, _ = fmt.Fprint(stderr, usage)
		return exitCode(2)
	}

	// The package is the last argument rather than the first, so that flags and
	// their values can sit between the verb and it without this having to know
	// which flags take a value. The driver parses them; here they are opaque.
	rest := args[1:]
	if len(rest) == 0 {
		return fmt.Errorf(
			"%s needs a package argument, for example: sqlb %s ./schema", verb, verb)
	}
	pattern, flags := rest[len(rest)-1], rest[:len(rest)-1]
	if strings.HasPrefix(pattern, "-") {
		return fmt.Errorf(
			"the last argument to %s must be the schema package, and %q is a flag. "+
				"Flags go between the verb and the package: sqlb %s -name add_priority ./schema",
			verb, pattern, verb)
	}

	pkg, err := resolve(pattern)
	if err != nil {
		return err
	}
	return drive(pkg, append([]string{verb}, flags...), stdout, stderr)
}
