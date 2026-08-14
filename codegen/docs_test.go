package codegen_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jryannel/sqlb/codegen"
	"github.com/jryannel/sqlb/schema"
)

// docsFixture is one exposed table with a table comment and one declared
// action, which between them exercise every source `sqlb docs` reads: the
// Ops mask, TableDef.Comment, and Action.Description.
func docsFixture(ops schema.Op) *schema.Registry {
	r := schema.NewRegistry()
	r.Table("posts",
		schema.UUIDv7("id").PrimaryKey(),
		schema.Text("title").Sortable(),
	).Describe("Blog posts belonging to an org.").
		Expose(schema.REST{Path: "/posts", Ops: ops}).
		AddAction(schema.Action{
			Name:        "publish",
			Description: "Publishes a draft post and notifies subscribers.",
		})
	return r
}

func docsProject(reg *schema.Registry, featuresFile string) codegen.Project {
	return codegen.Project{
		Options:      codegen.Options{Dir: ".", Package: "blog", Registry: reg},
		FeaturesFile: featuresFile,
	}
}

func TestDocsWritesEndpointsCommentsAndPlaceholderNotes(t *testing.T) {
	file := filepath.Join(t.TempDir(), "FEATURES.md")
	code, out := run(t, docsProject(docsFixture(schema.CRUD|schema.OpList), file), "docs")
	if code != 0 {
		t.Fatalf("docs should succeed, got %d (%s)", code, out)
	}

	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("FEATURES.md not written: %v", err)
	}
	got := string(raw)

	for _, want := range []string{
		"Blog posts belonging to an org.",
		"`GET /posts` — list",
		"`POST /posts` — create",
		"`GET /posts/{id}` — read",
		"`PATCH /posts/{id}` — update",
		"`DELETE /posts/{id}` — delete",
		"`POST /posts/{id}/publish` — action: publish",
		"Publishes a draft post and notifies subscribers.",
		"<!-- sqlb:notes GET /posts -->",
		"_Describe what this endpoint does",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("FEATURES.md missing %q\n\n%s", want, got)
		}
	}
}

func TestDocsPreservesNotesWrittenIntoTheFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "FEATURES.md")
	p := docsProject(docsFixture(schema.CRUD|schema.OpList), file)

	if code, out := run(t, p, "docs"); code != 0 {
		t.Fatalf("first run should succeed, got %d (%s)", code, out)
	}

	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("FEATURES.md not written: %v", err)
	}
	filled := strings.Replace(string(raw),
		"<!-- sqlb:notes GET /posts/{id} -->\n_Describe what this endpoint does: request/response shape, side effects, invariants._\n<!-- /sqlb:notes -->",
		"<!-- sqlb:notes GET /posts/{id} -->\nFetches one post, expanding its author by default.\n<!-- /sqlb:notes -->",
		1)
	if filled == string(raw) {
		t.Fatal("test setup: the placeholder text to replace was not found in the rendered file")
	}
	if err := os.WriteFile(file, []byte(filled), 0o644); err != nil {
		t.Fatal(err)
	}

	if code, out := run(t, p, "docs"); code != 0 {
		t.Fatalf("second run should succeed, got %d (%s)", code, out)
	}

	raw, err = os.ReadFile(file)
	if err != nil {
		t.Fatalf("FEATURES.md not written: %v", err)
	}
	if !strings.Contains(string(raw), "Fetches one post, expanding its author by default.") {
		t.Errorf("a rerun should keep the hand-written note, got:\n%s", raw)
	}
}

func TestDocsArchivesNotesForARemovedEndpoint(t *testing.T) {
	file := filepath.Join(t.TempDir(), "FEATURES.md")
	full := docsProject(docsFixture(schema.CRUD|schema.OpList), file)

	if code, out := run(t, full, "docs"); code != 0 {
		t.Fatalf("first run should succeed, got %d (%s)", code, out)
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	filled := strings.Replace(string(raw),
		"<!-- sqlb:notes DELETE /posts/{id} -->\n_Describe what this endpoint does: request/response shape, side effects, invariants._\n<!-- /sqlb:notes -->",
		"<!-- sqlb:notes DELETE /posts/{id} -->\nSoft-deletes; the row is kept for 30 days.\n<!-- /sqlb:notes -->",
		1)
	if filled == string(raw) {
		t.Fatal("test setup: the placeholder text to replace was not found in the rendered file")
	}
	if err := os.WriteFile(file, []byte(filled), 0o644); err != nil {
		t.Fatal(err)
	}

	// Rerun against a schema that no longer exposes delete.
	narrowed := docsProject(docsFixture(schema.OpCreate|schema.OpRead|schema.OpUpdate|schema.OpList), file)
	code, out := run(t, narrowed, "docs")
	if code != 0 {
		t.Fatalf("second run should succeed, got %d (%s)", code, out)
	}
	if !strings.Contains(out, "1 note(s) archived") {
		t.Errorf("should report the archived note, got: %s", out)
	}

	raw, err = os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if !strings.Contains(got, "## Archived notes") {
		t.Errorf("removed endpoint should land under an Archived notes section, got:\n%s", got)
	}
	if !strings.Contains(got, "Soft-deletes; the row is kept for 30 days.") {
		t.Errorf("the archived note's content should be kept verbatim, got:\n%s", got)
	}
	if strings.Contains(got, "`DELETE /posts/{id}` — delete") {
		t.Errorf("the endpoint is no longer exposed and should not appear as a live section, got:\n%s", got)
	}
}
