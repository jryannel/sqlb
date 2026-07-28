package shadow

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The statement splitter is where this package can be wrong in a way that is
// hard to see: a semicolon read as a boundary when it is inside a literal
// splits one statement into two halves that are each invalid, and a boundary
// missed runs two statements as one. Both fail loudly against a real database,
// which is why these cases live here where they can be enumerated cheaply
// rather than in pgtest where each costs a container.

func TestSplitStatements(t *testing.T) {
	for _, tc := range []struct {
		name string
		sql  string
		want []string
	}{
		{
			name: "plain statements",
			sql:  "CREATE TABLE a (id int);\nCREATE TABLE b (id int);",
			want: []string{"CREATE TABLE a (id int)", "CREATE TABLE b (id int)"},
		},
		{
			name: "trailing semicolon is not an empty statement",
			sql:  "SELECT 1;\n\n",
			want: []string{"SELECT 1"},
		},
		{
			name: "a statement need not be terminated",
			sql:  "SELECT 1",
			want: []string{"SELECT 1"},
		},
		{
			name: "semicolon inside a string literal",
			sql:  "INSERT INTO t VALUES ('a;b');\nSELECT 2;",
			want: []string{"INSERT INTO t VALUES ('a;b')", "SELECT 2"},
		},
		{
			name: "escaped quote inside a literal",
			sql:  "INSERT INTO t VALUES ('it''s; fine');\nSELECT 2;",
			want: []string{"INSERT INTO t VALUES ('it''s; fine')", "SELECT 2"},
		},
		{
			// Postgres's escape-string form, where a backslash escapes the
			// next character. Without it the literal ends at the backslashed
			// quote and the semicolon after it splits one statement into two
			// broken halves.
			name: "backslash-escaped quote inside an E'' literal",
			sql:  `INSERT INTO t VALUES (E'it\'s; fine');` + "\nSELECT 2;",
			want: []string{`INSERT INTO t VALUES (E'it\'s; fine')`, "SELECT 2"},
		},
		{
			// A backslash is only special in the E form. In an ordinary
			// literal it is a plain character, so the quote after it closes.
			name: "backslash is not an escape in an ordinary literal",
			sql:  `INSERT INTO t VALUES ('a\');` + "\nSELECT 2;",
			want: []string{`INSERT INTO t VALUES ('a\')`, "SELECT 2"},
		},
		{
			// The E is a prefix only when it stands alone; here it ends an
			// identifier and the literal that follows is an ordinary one.
			name: "a word ending in E does not start an escape string",
			sql:  "SELECT typeE'a;b';\nSELECT 2;",
			want: []string{"SELECT typeE'a;b'", "SELECT 2"},
		},
		{
			name: "semicolon inside a quoted identifier",
			sql:  `CREATE TABLE "we;ird" (id int);` + "\nSELECT 2;",
			want: []string{`CREATE TABLE "we;ird" (id int)`, "SELECT 2"},
		},
		{
			name: "semicolon inside a line comment",
			sql:  "SELECT 1; -- and; then\nSELECT 2;",
			want: []string{"SELECT 1", "-- and; then\nSELECT 2"},
		},
		{
			name: "semicolon inside a block comment",
			sql:  "/* a; b */ SELECT 1;\nSELECT 2;",
			want: []string{"/* a; b */ SELECT 1", "SELECT 2"},
		},
		{
			name: "nested block comment",
			sql:  "/* a /* b; */ c; */ SELECT 1;",
			want: []string{"/* a /* b; */ c; */ SELECT 1"},
		},
		{
			name: "dollar quoted body",
			sql: "CREATE FUNCTION f() RETURNS int AS $$ BEGIN; RETURN 1; END; $$ LANGUAGE plpgsql;\n" +
				"SELECT 2;",
			want: []string{
				"CREATE FUNCTION f() RETURNS int AS $$ BEGIN; RETURN 1; END; $$ LANGUAGE plpgsql",
				"SELECT 2",
			},
		},
		{
			name: "tagged dollar quote",
			sql:  "SELECT $tag$ a; b $tag$;\nSELECT 2;",
			want: []string{"SELECT $tag$ a; b $tag$", "SELECT 2"},
		},
		{
			name: "a positional parameter is not a dollar quote",
			sql:  "SELECT * FROM t WHERE id = $1;\nSELECT 2;",
			want: []string{"SELECT * FROM t WHERE id = $1", "SELECT 2"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := splitStatements(tc.sql)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("splitStatements(%q)\n got %#v\nwant %#v", tc.sql, got, tc.want)
			}
		})
	}
}

func TestGooseStatementBlockIsKeptWhole(t *testing.T) {
	sql := `-- +goose Up
CREATE TABLE a (id int);
-- +goose StatementBegin
CREATE FUNCTION f() RETURNS int AS '
BEGIN;
RETURN 1;
END;
' LANGUAGE plpgsql;
-- +goose StatementEnd
CREATE TABLE b (id int);
-- +goose Down
DROP TABLE a;
`
	up, err := gooseUp("001_x.sql", sql)
	if err != nil {
		t.Fatalf("gooseUp: %v", err)
	}
	got := splitStatements(up)

	if len(got) != 3 {
		t.Fatalf("want 3 statements, got %d: %#v", len(got), got)
	}
	if !strings.HasPrefix(got[0], "CREATE TABLE a") {
		t.Errorf("first statement = %q", got[0])
	}
	if !strings.Contains(got[1], "RETURN 1") || !strings.HasPrefix(got[1], "CREATE FUNCTION") {
		t.Errorf("the block was not kept whole: %q", got[1])
	}
	if !strings.HasPrefix(got[2], "CREATE TABLE b") {
		t.Errorf("third statement = %q", got[2])
	}
	// The Down section must not be replayed. Applying it would undo the
	// history this exists to reproduce.
	for _, s := range got {
		if strings.Contains(s, "DROP TABLE") {
			t.Errorf("a Down statement leaked into the replay: %q", s)
		}
	}
}

func TestGooseUpRequiresAnUpSection(t *testing.T) {
	_, err := gooseUp("001_x.sql", "CREATE TABLE a (id int);\n")
	if err == nil {
		t.Fatal("a file with no Up section should be refused")
	}
	// The likeliest cause is the wrong format, so the error says so.
	if !strings.Contains(err.Error(), "Options.Format") {
		t.Errorf("error should suggest the format setting, got: %v", err)
	}
}

func TestVersionsSortNumericallyNotAsText(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"1_a.sql", "2_b.sql", "10_c.sql"} {
		write(t, filepath.Join(dir, name), "-- +goose Up\nSELECT 1;\n")
	}

	files, err := collect(dir, "goose")
	if err != nil {
		t.Fatalf("collect: %v", err)
	}

	var order []string
	for _, f := range files {
		order = append(order, f.Name)
	}
	want := []string{"1_a.sql", "2_b.sql", "10_c.sql"}
	if !reflect.DeepEqual(order, want) {
		t.Errorf("got %v, want %v — sorted as text, 10 lands before 2 and the "+
			"history replays in an order that never happened", order, want)
	}
}

func TestDownFilesAreNotReplayed(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "1_a.up.sql"), "CREATE TABLE a (id int);\n")
	write(t, filepath.Join(dir, "1_a.down.sql"), "DROP TABLE a;\n")

	files, err := collect(dir, "golang-migrate")
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("want 1 file, got %d: %+v", len(files), files)
	}
	if files[0].Name != "1_a.up.sql" {
		t.Errorf("replayed %q", files[0].Name)
	}
}

func TestDuplicateVersionsAreRefused(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "1_a.sql"), "-- +goose Up\nSELECT 1;\n")
	write(t, filepath.Join(dir, "1_b.sql"), "-- +goose Up\nSELECT 2;\n")

	if _, err := collect(dir, "goose"); err == nil {
		t.Fatal("two migrations sharing a version should be refused: the order they " +
			"applied in is not recorded, so a replay cannot be faithful")
	}
}

func TestANonNumericVersionIsRefused(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "initial_schema.sql"), "-- +goose Up\nSELECT 1;\n")

	if _, err := collect(dir, "goose"); err == nil {
		t.Fatal("a file with no numeric version should be refused rather than sorted somewhere arbitrary")
	}
}

func TestNoTransactionIsDetected(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "1_a.sql"), "-- +goose NO TRANSACTION\n-- +goose Up\nCREATE INDEX CONCURRENTLY i ON t (c);\n")
	write(t, filepath.Join(dir, "2_b.sql"), "-- +goose Up\nCREATE TABLE b (id int);\n")

	files, err := collect(dir, "goose")
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if !files[0].NoTransaction {
		t.Error("the directive was missed; the CONCURRENTLY statement would fail inside a transaction")
	}
	if files[1].NoTransaction {
		t.Error("a file without the directive was marked as needing one")
	}
}

// Only goose has a directive. golang-migrate and plain SQL have no way to say
// it, and migrate.Unblock emits CREATE INDEX CONCURRENTLY for all three — so
// reading the directive alone meant shadow could not replay the histories this
// repository itself generates for two of the formats it supports.
func TestConcurrentStatementsRunOutsideATransactionInEveryFormat(t *testing.T) {
	for _, format := range []string{"golang-migrate", "sql"} {
		dir := t.TempDir()
		name := "1_indexes.up.sql"
		if format == "sql" {
			name = "1_indexes.sql"
		}
		write(t, filepath.Join(dir, name), "CREATE INDEX CONCURRENTLY i ON t (c);\n")
		write(t, filepath.Join(dir, strings.Replace(name, "1_indexes", "2_plain", 1)),
			"CREATE TABLE b (id int);\n")

		files, err := collect(dir, format)
		if err != nil {
			t.Fatalf("%s: collect: %v", format, err)
		}
		if !files[0].NoTransaction {
			t.Errorf("%s: a concurrent index build would be wrapped in a transaction, "+
				"which Postgres rejects", format)
		}
		if files[1].NoTransaction {
			t.Errorf("%s: an ordinary file was marked as needing to run unwrapped", format)
		}
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}
