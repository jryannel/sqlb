package codegen

// This file is the half of the `sqlb` command that has to be compiled inside
// your module. The other half is cmd/sqlb, which cannot see your schema at all.
//
// The schema is Go, and a table is registered by the side effect of importing
// the package that declares it (ADR-0004). So no prebuilt binary can read a
// registry: the only program that can is one linked against the schema package.
// cmd/sqlb writes that program — three imports and a call to Main — compiles it
// inside the module, and throws it away. Everything below is what that program
// runs, and it lives here rather than being emitted as source because generated
// code cannot be tested and this can.
//
// The convention that joins the two halves is ProjectFunc: an exported,
// argument-free function in the schema package returning a Project. It is a
// convention rather than a config file because the alternative is a second
// declaration language that mirrors Options, drifts from it, and reports its
// mistakes at run time instead of at compile time (ADR-0032).

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/jryannel/sqlb/schema"
)

// ProjectFunc is the name cmd/sqlb looks for in a schema package.
//
// Exported so that the command, the error message it prints when the function
// is missing, and the documentation are all reading the same string. A
// convention spelled out in three places is a convention that will eventually
// be spelled two ways.
const ProjectFunc = "SqlbProject"

// Project is everything `sqlb` needs to know about a repository that it cannot
// work out for itself.
//
// A schema package declares one by exporting a function of this name:
//
//	// taskschema/sqlb.go
//	func SqlbProject() codegen.Project {
//		return codegen.Project{
//			Options: codegen.Options{
//				Package: "tasks",
//				TSDir:   "web/src/api",
//				DartDir: "mobile/lib/api",
//				CLIDir:  "cli",
//				CLIName: "taskctl",
//			},
//		}
//	}
//
// # Paths are relative to the module root
//
// Every directory in Options is resolved against the directory holding go.mod,
// not against the schema package and not against wherever the command was
// invoked. That is the one rule that makes `sqlb generate ./taskschema` mean the
// same thing from a shell, from a //go:generate directive, and from CI — the
// three places the old hand-written generator had to be told `-dir ..` and got
// it wrong if any of them disagreed.
//
// # Why this wraps Options rather than being it
//
// Options is the emitters. Project is the repository, and the repository has
// more in it than emitter output — the migration directory and the scratch
// database `sqlb migrate` will need are the next two fields to land here.
// Putting the wrapper in from the start costs one line per project now and
// saves changing the signature of every project's SqlbProject later, which is
// the kind of break a convention cannot absorb quietly.
type Project struct {
	// Options configures the emitters.
	//
	// Registry may be left nil, and usually is: Main fills it from
	// schema.DefaultRegistry(), which is what declaring a table populates. Set
	// it only if the project builds a registry of its own instead of using the
	// default one.
	Options Options
}

// Main runs the driver program cmd/sqlb generates, and exits.
//
// The verb arrives in os.Args because one driver serves both `generate` and
// `check`; baking it into the emitted source instead would mean compiling twice
// to do the two things CI does on every push.
func Main(p Project) {
	code := Run(p, os.Args[1:], os.Stdout, os.Stderr)
	os.Exit(code)
}

// Run is Main without the exit, which is what makes it testable.
//
// It returns a process exit code rather than an error because "the tree is
// stale" is not an error — it is the answer `check` exists to give, and it has
// to be distinguishable from a schema that would not compile.
func Run(p Project, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		say(stderr, "sqlb: driver expected exactly one verb, got %d\n", len(args))
		return 2
	}

	if err := p.Validate(); err != nil {
		line(stderr, err)
		return 1
	}

	opts := p.Options
	if opts.Registry == nil {
		opts.Registry = schema.DefaultRegistry()
	}
	// Empty means the module root. Options requires Dir because a caller
	// writing its own generator has no root to default to and a silent "." is
	// a file written somewhere surprising; a Project does have one, so the
	// common case — a repository generating into itself — costs no line.
	if opts.Dir == "" {
		opts.Dir = "."
	}

	switch args[0] {
	case "generate":
		written, err := Generate(opts)
		if err != nil {
			line(stderr, err)
			return 1
		}
		for _, f := range written {
			line(stdout, f)
		}
		say(stderr, "sqlb: wrote %d files\n", len(written))
		return 0

	case "check":
		stale, err := Check(opts)
		if err != nil {
			line(stderr, err)
			return 1
		}
		if len(stale) > 0 {
			// Naming the command that fixes it matters more here than
			// anywhere else in sqlb: this message is read almost exclusively
			// out of a CI log, by someone who is not in the directory and has
			// no idea what the generator was called.
			line(stderr, "sqlb: generated files are out of date; run: sqlb generate")
			for _, f := range stale {
				line(stderr, "  "+f)
			}
			return 1
		}
		line(stderr, "sqlb: generated files are current")
		return 0

	default:
		say(stderr, "sqlb: driver does not know the verb %q\n", args[0])
		return 2
	}
}

// say and line are the driver's output, and they drop the write error on
// purpose.
//
// This is the one place in sqlb where an unchecked write is right. Everything
// else here is a library, where swallowing an error hides a failure from a
// caller who could act on it; this is a command, and a program whose stderr has
// gone away has no remaining channel to report that on. The alternative is ten
// error checks that can only be ignored, which is how a package trains its
// readers to skip them.
func say(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

func line(w io.Writer, v any) {
	_, _ = fmt.Fprintln(w, v)
}

// Validate reports what is wrong with a Project before anything is compiled
// against it.
//
// Options.validate covers the emitters; this covers the one thing it cannot,
// which is that Dir means something different here. Options is happy with an
// absolute path — a caller writing its own generator may well want one — and a
// Project is not, because a path that resolves against the module root cannot
// be absolute and still mean the same thing on another machine.
func (p Project) Validate() error {
	if filepath.IsAbs(p.Options.Dir) {
		return fmt.Errorf(
			"codegen: Project Options.Dir is %q, which is absolute; paths in a Project "+
				"resolve against the module root, so this would write outside the repository "+
				"on the machine that ran it and somewhere else on yours", p.Options.Dir)
	}
	return nil
}
