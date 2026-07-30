package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/jryannel/sqlb/codegen"
)

// The subject of the end-to-end tests is this repository's own blog example.
//
// Using a real package rather than a scaffolded temp module is what makes the
// assertion worth something: it proves the generated driver compiles against a
// module and reproduces output that is committed, which is the entire claim. A
// fixture module would prove only that the fixture compiled.
const blog = "./example/blogschema"

// invoke runs the command from the repository root and reports the exit code
// alongside everything it printed.
func invoke(t *testing.T, args ...string) (int, string) {
	t.Helper()
	t.Chdir("../..")

	var out, errOut strings.Builder
	err := run(args, &out, &errOut)
	printed := out.String() + errOut.String()

	var code exitCode
	switch {
	case err == nil:
		return 0, printed
	case errors.As(err, &code):
		return int(code), printed
	default:
		// A failure before the driver ran, which main prints itself.
		return 1, printed + "sqlb: " + err.Error()
	}
}

// The load-bearing test. It compiles a driver against the root module, links in
// blogschema, runs every emitter, and compares the result with what is
// committed — so a green run here means the whole mechanism works and that it
// agrees with the hand-written generator it replaced.
func TestCheckAgreesWithTheCommittedOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles a driver against the module; not part of the inner loop")
	}

	code, out := invoke(t, "check", blog)
	if code != 0 {
		t.Fatalf("sqlb check %s reported exit %d:\n%s", blog, code, out)
	}
	if !strings.Contains(out, "current") {
		t.Errorf("check passed without saying so, so a CI log would show nothing:\n%s", out)
	}
}

// ADR-0016: the test above passes for a command that silently does nothing, so
// this one proves the driver is really being compiled and run. A package with
// no SqlbProject must be refused, by name.
func TestPackageWithoutAProjectIsRefusedByName(t *testing.T) {
	code, out := invoke(t, "check", "./schema")
	if code == 0 {
		t.Fatalf("a package with no %s was accepted:\n%s", codegen.ProjectFunc, out)
	}
	for _, want := range []string{codegen.ProjectFunc, "github.com/jryannel/sqlb/schema"} {
		if !strings.Contains(out, want) {
			t.Errorf("the error did not mention %q, and someone hitting it has no other "+
				"clue what to write:\n%s", want, out)
		}
	}
}

// A pattern matching several packages is the mistake `sqlb generate ./...`
// makes, and guessing at it would generate from whichever registries happened
// to be linked in.
func TestAmbiguousPatternIsRefused(t *testing.T) {
	code, out := invoke(t, "check", "./example/...")
	if code == 0 {
		t.Fatalf("a pattern matching several packages was accepted:\n%s", out)
	}
	if !strings.Contains(out, "blogschema") {
		t.Errorf("the error did not list what the pattern matched, which is the only way "+
			"to see how to narrow it:\n%s", out)
	}
}

func TestPackageMainIsRefused(t *testing.T) {
	code, out := invoke(t, "check", "./cmd/sqlb")
	if code == 0 {
		t.Fatalf("package main was accepted as a schema package:\n%s", out)
	}
	if !strings.Contains(out, "main") {
		t.Errorf("the error did not say what was wrong with it:\n%s", out)
	}
}

func TestUnknownCommandPrintsUsage(t *testing.T) {
	code, out := invoke(t, "regenerate", blog)
	if code == 0 {
		t.Fatalf("an unknown command succeeded:\n%s", out)
	}
	if !strings.Contains(out, "Usage:") {
		t.Errorf("an unknown command did not print usage:\n%s", out)
	}
}

func TestNoArgumentsPrintsUsage(t *testing.T) {
	code, out := invoke(t)
	if code != 2 {
		t.Errorf("bare sqlb exited %d, want 2 (usage), so a shell cannot tell a misuse "+
			"from a failed check:\n%s", code, out)
	}
	if !strings.Contains(out, funcSignature) {
		t.Errorf("usage does not say what a schema package must export:\n%s", out)
	}
}

func TestGenerateNeedsAPackage(t *testing.T) {
	code, out := invoke(t, "generate")
	if code == 0 {
		t.Fatalf("generate with no package succeeded:\n%s", out)
	}
	if !strings.Contains(out, "needs a package argument") {
		t.Errorf("the error did not say what was missing:\n%s", out)
	}
}

func TestVersionSaysSomething(t *testing.T) {
	code, out := invoke(t, "version")
	if code != 0 {
		t.Fatalf("version exited %d:\n%s", code, out)
	}
	if !strings.HasPrefix(out, "sqlb ") {
		t.Errorf("version printed %q, which does not name the tool", out)
	}
}
