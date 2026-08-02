package codegen_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jryannel/sqlb/codegen"
	"github.com/jryannel/sqlb/schema"
)

// wireFixture is one table with one column whose two spellings differ, exposed
// so every emitter has something to say about it.
func wireFixture(c schema.WireCase) *schema.Registry {
	r := schema.NewRegistry().WireCase(c)
	r.Table("articles",
		schema.UUIDv7("id").PrimaryKey(),
		schema.Text("title").Filterable().Sortable(),
		schema.Timestamp("created_at").Filterable().Sortable(),
	).Expose(schema.REST{Path: "/articles", Ops: schema.CRUD | schema.OpList})
	return r
}

func generateAll(t *testing.T, r *schema.Registry) map[string]string {
	t.Helper()
	dir := t.TempDir()
	files, err := codegen.Generate(codegen.Options{
		Registry: r, Dir: dir, Package: "gen",
		TSDir: "web", DartDir: "mobile", CLIDir: "cli", CLIName: "artctl",
		ClientImportPath: "example.com/app/cli/client",
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	out := map[string]string{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(f)] = string(b)
	}
	return out
}

// One setting, five surfaces. The whole claim of ADR-0036 is that they cannot
// disagree, so this asserts the same spelling reaches all of them from one
// declaration rather than testing each emitter's own idea of it.
func TestWireCaseReachesEverySurface(t *testing.T) {
	files := generateAll(t, wireFixture(schema.Camel))

	for name, want := range map[string]string{
		"models_gen.go":   `json:"createdAt"`,
		"client.gen.ts":   "createdAt",
		"client.gen.dart": "'createdAt'",
	} {
		src, ok := files[name]
		if !ok {
			t.Fatalf("%s was not generated (got %v)", name, wireKeysOf(files))
		}
		if !strings.Contains(src, want) {
			t.Errorf("%s does not carry the wire spelling %q", name, want)
		}
	}

	// The model also has to *tell the runtime*, because nothing on the request
	// path may import the schema package and so nothing there can compute a
	// spelling of its own.
	if !strings.Contains(files["models_gen.go"], "wire:createdAt") {
		t.Errorf("models.go does not carry the wire spelling into the runtime:\n%s", files["models_gen.go"])
	}
	// And the database's own name is still what the row scans from.
	if !strings.Contains(files["models_gen.go"], `db:"created_at"`) {
		t.Error("models.go lost the column name, which is what reaches Postgres")
	}
}

// Verbatim is the default and emits exactly what it always did — no wire entry,
// no camel anywhere. This is what makes the amendment additive: an existing
// project regenerates to byte-identical output.
func TestVerbatimEmitsNothingNew(t *testing.T) {
	files := generateAll(t, wireFixture(schema.Verbatim))

	if !strings.Contains(files["models_gen.go"], `json:"created_at"`) {
		t.Error("the default stopped spelling a column the way the database does")
	}
	if strings.Contains(files["models_gen.go"], "wire:") {
		t.Error("Verbatim wrote a wire entry, so existing output is no longer byte-identical")
	}
	// Checked against the *wire* strings rather than the whole file. A Dart
	// getter is createdAt under either setting, because dartMember camel-cases
	// a column name to make a legal Dart identifier — that is a language
	// convention and not a wire format, and conflating the two would assert
	// something false about the default.
	for name, want := range map[string]string{
		"client.gen.ts":   "created_at",
		"client.gen.dart": "'created_at'",
	} {
		if !strings.Contains(files[name], want) {
			t.Errorf("%s does not spell the column %q on the wire", name, want)
		}
	}
	if strings.Contains(files["client.gen.dart"], "'createdAt'") {
		t.Error("client.gen.dart sends a camelCase key under Verbatim")
	}
}

// A CLI flag is a local affordance rather than a wire format, so it is stable
// across the setting: switching WireCase must not rewrite every documented
// command line.
func TestCLIFlagsAreStableAcrossWireCases(t *testing.T) {
	camel := generateAll(t, wireFixture(schema.Camel))["cli_gen.go"]
	verbatim := generateAll(t, wireFixture(schema.Verbatim))["cli_gen.go"]

	for _, src := range []string{camel, verbatim} {
		if !strings.Contains(src, "created-at") {
			t.Errorf("the flag is not kebab-cased:\n%s", firstLines(src, 40))
		}
	}
	// But what it sends does move.
	if !strings.Contains(camel, `q.Add("createdAt"`) {
		t.Error("the CLI sends the column spelling rather than the wire spelling")
	}
	if !strings.Contains(verbatim, `q.Add("created_at"`) {
		t.Error("Verbatim's CLI stopped sending the column name")
	}
}

func wireKeysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
