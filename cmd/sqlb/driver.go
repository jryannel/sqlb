package main

// The mechanics of compiling a program against someone else's schema package.
//
// Three steps, each of which can fail in a way worth a different message: find
// the package and the module that holds it, check it declares the function this
// command needs before spending a compile on finding out, and build and run the
// driver from a temporary directory.

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/jryannel/sqlb/codegen"
)

// funcSignature is what the usage text and every error message say a schema
// package must export. Built from the constant the driver actually calls, so
// the documentation cannot drift from the convention.
const funcSignature = codegen.ProjectFunc + "() codegen.Project"

// pkg is the part of `go list` output this command uses.
type pkg struct {
	ImportPath string
	Name       string
	Dir        string
	Module     struct {
		Path string
		Dir  string
	}
}

// resolve turns a package pattern into the one package it names, and refuses
// anything else.
//
// A pattern is accepted rather than an import path because that is what every
// other Go command takes and what a //go:generate directive can write relative
// to itself. `./...` matching four packages is refused rather than guessed at:
// generating from the wrong registry produces plausible Go describing the wrong
// tables, which is worse than an error.
func resolve(pattern string) (*pkg, error) {
	cmd := exec.Command("go", "list", "-json", "--", pattern)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("could not resolve the package %q:\n%s", pattern, indent(msg))
	}

	// go list -json emits one object per package, concatenated rather than
	// wrapped in an array, so this decodes in a loop.
	var found []*pkg
	dec := json.NewDecoder(strings.NewReader(string(out)))
	for dec.More() {
		p := new(pkg)
		if err := dec.Decode(p); err != nil {
			return nil, fmt.Errorf("could not read go list output for %q: %w", pattern, err)
		}
		found = append(found, p)
	}

	switch {
	case len(found) == 0:
		return nil, fmt.Errorf("the pattern %q matched no packages", pattern)
	case len(found) > 1:
		var names []string
		for _, p := range found {
			names = append(names, p.ImportPath)
		}
		return nil, fmt.Errorf(
			"the pattern %q matched %d packages, and sqlb needs exactly one — "+
				"the registry it reads is whichever ones got linked in, so this "+
				"is refused rather than guessed:\n%s",
			pattern, len(found), indent(strings.Join(names, "\n")))
	}

	p := found[0]
	if p.Name == "main" {
		return nil, fmt.Errorf(
			"%s is package main, which cannot be imported. Point sqlb at the package "+
				"that declares the schema, not at the command that used to generate from it",
			p.ImportPath)
	}
	if p.Module.Dir == "" {
		return nil, fmt.Errorf(
			"%s is not in a module, and sqlb resolves every output path against the "+
				"directory holding go.mod", p.ImportPath)
	}
	return p, nil
}

// declaresProject reports whether the package exports the convention function,
// so that a missing one is a sentence rather than a compile error inside a
// temporary file the user never sees.
//
// Build constraints are ignored here. This is a guard whose job is a better
// message, not a second type checker — the compiler in the next step is what
// actually decides, and it will disagree with this only for a schema package
// that hides its declaration behind a build tag.
func declaresProject(p *pkg) (bool, error) {
	entries, err := os.ReadDir(p.Dir)
	if err != nil {
		return false, fmt.Errorf("could not read %s: %w", p.Dir, err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(p.Dir, name), nil, 0)
		if err != nil {
			// A file that does not parse is the compiler's problem to report,
			// with a position and a caret. Failing here instead would replace
			// that with a worse message about a guard.
			continue
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Name.Name != codegen.ProjectFunc {
				continue
			}
			if len(fn.Type.Params.List) != 0 {
				return false, fmt.Errorf(
					"%s.%s takes arguments; sqlb calls it with none. It must be: func %s",
					p.Name, codegen.ProjectFunc, funcSignature)
			}
			return true, nil
		}
	}
	return false, nil
}

// driverSource is the program cmd/sqlb compiles inside the target module.
//
// It is three lines because everything it could otherwise contain is in
// codegen.Main, where it can be tested. Generated code that is never written to
// disk in a form anyone reviews should do as little as it can get away with.
const driverSource = `// Code generated by sqlb. DO NOT EDIT.
//
// Written to a temporary directory, compiled against the module in the working
// directory, and deleted. It exists because a schema is registered by importing
// the package that declares it, so reading one requires linking against it.
package main

import (
	"github.com/jryannel/sqlb/codegen"

	schemapkg %q
)

func main() { codegen.Main(schemapkg.%s()) }
`

// drive builds the driver and runs it with the given verb.
//
// Build and run are separate steps rather than one `go run` so that the child's
// exit code arrives here unmodified. `go run` reports a non-zero exit by
// printing "exit status 1" of its own, which on a `sqlb check` failure lands
// underneath the list of stale files and reads like a second, unexplained
// error.
func drive(p *pkg, verb string, stdout, stderr io.Writer) error {
	ok, err := declaresProject(p)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf(
			"%s does not export %s.\n\nsqlb reads a project's output directories from that "+
				"function, because they are Go rather than a config file that could disagree "+
				"with the type. Add one to %s:\n\n"+
				"    func %s {\n"+
				"        return codegen.Project{\n"+
				"            Options: codegen.Options{Package: %q},\n"+
				"        }\n"+
				"    }",
			p.ImportPath, codegen.ProjectFunc,
			filepath.Join(p.Dir, "sqlb.go"), funcSignature, p.Name)
	}

	tmp, err := os.MkdirTemp("", "sqlb-driver-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	src := filepath.Join(tmp, "main.go")
	body := fmt.Sprintf(driverSource, p.ImportPath, codegen.ProjectFunc)
	if err := os.WriteFile(src, []byte(body), 0o600); err != nil {
		return err
	}

	bin := filepath.Join(tmp, "driver")
	build := exec.Command("go", "build", "-o", bin, src)
	// The module root, not the working directory: this is what makes every
	// path in a Project mean the same thing wherever the command was invoked
	// from, which is the whole reason the old generators needed -dir.
	build.Dir = p.Module.Dir
	var buildErr strings.Builder
	build.Stderr = &buildErr
	if err := build.Run(); err != nil {
		return fmt.Errorf(
			"the generated driver did not compile against %s:\n%s\n"+
				"The driver is three lines, so this is almost always %s itself or "+
				"something it imports",
			p.Module.Path, indent(strings.TrimSpace(buildErr.String())), p.ImportPath)
	}

	cmd := exec.Command(bin, verb)
	cmd.Dir = p.Module.Dir
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return exitCode(ee.ExitCode())
		}
		return err
	}
	return nil
}

// version reports what this binary was built from, which for a tool whose whole
// job is keeping generated files in step with a library matters more than
// usual: a `sqlb check` failure that nobody can reproduce is nearly always two
// machines running two versions.
func version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "sqlb (unknown version: no build info)"
	}
	v := info.Main.Version
	if v == "" || v == "(devel)" {
		// Built from a checkout rather than installed from a tag. The VCS
		// stamp is the only thing that identifies it, and it is absent when
		// the tree was built from an archive rather than a git clone.
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" {
				return "sqlb (devel " + short(s.Value) + ")"
			}
		}
		return "sqlb (devel)"
	}
	return "sqlb " + v
}

func short(rev string) string {
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}

// indent offsets a block quoted inside an error message, so that a multi-line
// compiler error reads as one quoted thing rather than as several errors.
func indent(s string) string {
	if s == "" {
		return s
	}
	return "    " + strings.ReplaceAll(s, "\n", "\n    ")
}
