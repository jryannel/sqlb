package codegen_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jryannel/sqlb/codegen"
)

// run drives the driver half of the sqlb command the way cmd/sqlb does, and
// returns the exit code with everything it printed.
func run(t *testing.T, p codegen.Project, args ...string) (int, string) {
	t.Helper()
	var out, errOut strings.Builder
	code := codegen.Run(p, args, &out, &errOut)
	return code, out.String() + errOut.String()
}

// project moves the test into a temp directory and points a Project at a
// relative path inside it.
//
// The chdir is the point rather than a convenience: cmd/sqlb runs the driver
// with the working directory set to the module root, and a Project's paths are
// relative to it. A test holding an absolute Dir would be testing something the
// command cannot do — as the first version of this helper discovered, by being
// refused by Validate.
func project(t *testing.T) codegen.Project {
	t.Helper()
	t.Chdir(t.TempDir())
	return codegen.Project{
		Options: codegen.Options{
			Registry: fixture(),
			Dir:      "out",
			Package:  "gen",
		},
	}
}

func TestProjectGenerateThenCheckIsClean(t *testing.T) {
	p := project(t)

	if code, out := run(t, p, "generate"); code != 0 {
		t.Fatalf("generate: exit %d, output:\n%s", code, out)
	}
	code, out := run(t, p, "check")
	if code != 0 {
		t.Fatalf("check straight after generate reported exit %d, so the emitters are "+
			"not reproducible:\n%s", code, out)
	}
	if !strings.Contains(out, "current") {
		t.Errorf("check said nothing about being current:\n%s", out)
	}
}

// The other direction, which is the one that matters: ADR-0016. A check that
// cannot fail is not a gate, and this one is about to become the gate every
// sqlb project runs in CI.
func TestProjectCheckReportsADriftedFile(t *testing.T) {
	p := project(t)
	if code, out := run(t, p, "generate"); code != 0 {
		t.Fatalf("generate: exit %d, output:\n%s", code, out)
	}

	path := filepath.Join(p.Options.Dir, "models_gen.go")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(body, []byte("\n// edited by hand\n")...), 0o644); err != nil {
		t.Fatal(err)
	}

	code, out := run(t, p, "check")
	if code == 0 {
		t.Fatalf("check passed on a hand-edited models_gen.go, so it verifies nothing:\n%s", out)
	}
	if !strings.Contains(out, "models_gen.go") {
		t.Errorf("check failed without naming the stale file, which is the only part of "+
			"the message a CI log makes useful:\n%s", out)
	}
	// The message has to name the fix, because it is read by someone who is
	// not in the directory and did not write the generator.
	if !strings.Contains(out, "sqlb generate") {
		t.Errorf("check failed without naming the command that fixes it:\n%s", out)
	}
}

func TestProjectCheckReportsAMissingFile(t *testing.T) {
	p := project(t)

	code, out := run(t, p, "check")
	if code == 0 {
		t.Fatalf("check passed against an empty directory:\n%s", out)
	}
	if !strings.Contains(out, "missing") {
		t.Errorf("a never-generated tree was not reported as missing:\n%s", out)
	}
}

// Dir empty means the module root. Options refuses an empty Dir, so this is
// Project's own defaulting and it needs its own assertion.
func TestProjectDirDefaultsToTheModuleRoot(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	p := codegen.Project{Options: codegen.Options{Registry: fixture(), Package: "gen"}}
	if code, out := run(t, p, "generate"); code != 0 {
		t.Fatalf("generate with no Dir: exit %d, output:\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "models_gen.go")); err != nil {
		t.Errorf("an empty Dir did not write into the working directory: %v", err)
	}
}

func TestProjectRefusesAnAbsoluteDir(t *testing.T) {
	p := codegen.Project{
		Options: codegen.Options{Registry: fixture(), Dir: t.TempDir(), Package: "gen"},
	}
	if err := p.Validate(); err == nil {
		t.Fatal("an absolute Dir validated; a Project's paths resolve against the module " +
			"root, so an absolute one writes somewhere different on every machine")
	}

	// And the positive control, without which the assertion above would pass
	// for a Validate that rejected everything.
	rel := codegen.Project{
		Options: codegen.Options{Registry: fixture(), Dir: "example/blog", Package: "gen"},
	}
	if err := rel.Validate(); err != nil {
		t.Fatalf("a relative Dir was refused too, so the check is not about absoluteness: %v", err)
	}
}

func TestProjectRefusesAnUnknownVerb(t *testing.T) {
	code, out := run(t, project(t), "regenerate")
	if code == 0 {
		t.Fatalf("an unknown verb succeeded:\n%s", out)
	}
	if !strings.Contains(out, "regenerate") {
		t.Errorf("the error did not quote the verb it did not understand:\n%s", out)
	}
}
