package main

import (
	"os/exec"
	"strings"
	"testing"
)

// go generate's directive scanner is a plain line-based text scan — it does
// not parse Go syntax, so it cannot tell that a "//go:generate" line inside
// initSqlbGo's raw string literal (the template `sqlb init` writes as a new
// project's <pkg>schema/sqlb.go) is a value being written out, not a real
// directive. `go generate ./...` at the repository root used to find that
// line inside cmd/sqlb/init.go's own source and try to run it from
// cmd/sqlb, which is package main and cannot be imported as a schema
// package (issues #200, #205).
//
// This runs the real toolchain rather than re-deriving the scanner's rule,
// because the rule lives in cmd/go, not here, and the whole point is that a
// text scan does not know what this repository's Go source means.
func TestGenerateAtRepoRootIgnoresItsOwnInitTemplate(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to go generate across the whole module; not part of the inner loop")
	}
	t.Chdir("../..")

	out, err := exec.Command("go", "generate", "./...").CombinedOutput()
	if err != nil {
		t.Fatalf("go generate ./... failed:\n%s", out)
	}
	if strings.Contains(string(out), "cmd/sqlb is package main") {
		t.Fatalf("go generate tried to run initSqlbGo's template line as a real "+
			"directive instead of leaving it alone as string content:\n%s", out)
	}
}
