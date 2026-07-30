package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jryannel/sqlb/codegen"
)

// scratchDirs lists the driver directories left in a module.
//
// The subject of every test here, because a leftover is invisible in the
// command's output — it fails much later, as an untracked directory swept into
// somebody's commit.
func scratchDirs(t *testing.T, moduleDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(moduleDir)
	if err != nil {
		t.Fatalf("could not read %s: %v", moduleDir, err)
	}
	var found []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), driverPrefix) {
			found = append(found, e.Name())
		}
	}
	return found
}

// A failed compile must not leave its scratch directory behind, which is what
// went wrong in practice: the directory is created inside the user's module, so
// a leftover is one `git add -A` away from being committed.
//
// The failure here is a module directory with no go.mod, so the build fails
// before it has to resolve anything. What is under test is the cleanup, not the
// compiler's opinion, and this way the test costs no build.
func TestScratchDirectoryIsRemovedWhenTheDriverDoesNotCompile(t *testing.T) {
	moduleDir := t.TempDir()

	// declaresProject parses this and finds the convention function, so drive
	// gets as far as writing and building a driver.
	pkgDir := t.TempDir()
	src := fmt.Sprintf("package fakeschema\n\nfunc %s() {}\n", codegen.ProjectFunc)
	if err := os.WriteFile(filepath.Join(pkgDir, "schema.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}

	p := &pkg{ImportPath: "example.com/fake/fakeschema", Name: "fakeschema", Dir: pkgDir}
	p.Module.Path = "example.com/fake"
	p.Module.Dir = moduleDir

	var out, errOut strings.Builder
	if err := drive(p, []string{"check"}, &out, &errOut); err == nil {
		t.Fatal("a driver that cannot be compiled reported success")
	}

	if left := scratchDirs(t, moduleDir); len(left) != 0 {
		t.Errorf("the failed run left %v in the module, where git will offer to commit it", left)
	}
}

// The guard for what drive cannot observe. A run killed with SIGKILL leaves a
// directory no defer and no signal handler ever sees, and the next run is the
// only thing left that can remove it.
func TestStaleScratchDirectoriesAreSwept(t *testing.T) {
	moduleDir := t.TempDir()

	mkdir := func(name string, age time.Duration) string {
		path := filepath.Join(moduleDir, name)
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		when := time.Now().Add(-age)
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatal(err)
		}
		return path
	}

	abandoned := mkdir(driverPrefix+"409734944", staleAfter+time.Hour)
	// Both directions, per ADR-0016. A sweep that removed everything would
	// pass the assertion above while deleting a concurrent run's driver
	// mid-compile, so what it must leave alone is asserted too: a scratch
	// directory young enough to belong to a live run, and a neighbour that
	// merely lives in the same module.
	live := mkdir(driverPrefix+"11", 0)
	unrelated := mkdir("migrations", staleAfter+time.Hour)

	sweepScratch(moduleDir)

	if _, err := os.Stat(abandoned); !os.IsNotExist(err) {
		t.Errorf("an abandoned scratch directory survived the sweep (stat: %v), so nothing "+
			"ever removes one left by a kill", err)
	}
	for _, keep := range []string{live, unrelated} {
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("the sweep removed %s, which it must not touch: %v", filepath.Base(keep), err)
		}
	}
}

// The successful path, asserted where it is real: a check against this
// repository's own blog example compiles a driver in the repository root, and
// the root is a tree somebody is working in.
func TestASuccessfulRunLeavesNothingInTheModule(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles a driver against the module; not part of the inner loop")
	}

	code, out := invoke(t, "check", blog)
	if code != 0 {
		t.Fatalf("sqlb check %s reported exit %d:\n%s", blog, code, out)
	}
	// invoke has chdir'd to the repository root, which is the module root the
	// driver was compiled in.
	if left := scratchDirs(t, "."); len(left) != 0 {
		t.Errorf("a successful run left %v behind", left)
	}
}
