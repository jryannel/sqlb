package pgtest

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jryannel/sqlb"
	"github.com/jryannel/sqlb/codegen"
	"github.com/jryannel/sqlb/introspect"
	"github.com/jryannel/sqlb/migrate"
	"github.com/jryannel/sqlb/schema"
)

// The round trip is a fixpoint, and this is the test that says so.
//
// introspect, RenderSchema and migrate.Diff are each tested. Nothing asserted
// that they agree with each other about one schema, and running the loop by
// hand over a real 69-table database turned up three disagreements — a type
// introspect could read and RenderSchema could not write, an import that failed
// sqlb's own validation, and DDL Postgres rejected (issue #53). Every one of
// them lives *between* two packages that are individually well tested, which is
// why none of their own tests could see it.
//
// The invariant:
//
//	apply(fixture)              → a database
//	introspect(db)              → registry
//	RenderSchema(registry)      → source that compiles
//	apply(Diff(∅, registry))    → a second database
//	introspect(db')             → registry'
//	Diff(registry, registry')   → empty
//
// The last line is the one a consumer needs to be able to trust, because "sqlb
// can own your schema" is exactly this property.

// awkwardSchema is deliberately the schema that has historically broken this
// loop: the types that were skipped, an index whose operator class is its
// meaning, storage parameters, a partial index, a composite unique, a check
// that is not an enum and one that is, arrays, nullable jsonb.
const awkwardSchema = `
CREATE TABLE orgs (
    id   uuid PRIMARY KEY,
    name text NOT NULL,
    plan text NOT NULL DEFAULT 'free',
    CONSTRAINT chk_org_plan CHECK (plan IN ('free', 'pro', 'enterprise'))
);

CREATE TABLE document_chunks (
    id         uuid PRIMARY KEY,
    org_id     uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    title      varchar(200),
    body       text NOT NULL,
    score      numeric,
    weight     double precision,
    revision   bigint NOT NULL DEFAULT 0,
    tags       text[],
    meta       jsonb DEFAULT '{}'::jsonb,
    embedding  vector(1536),
    archived   boolean NOT NULL DEFAULT false,
    published  date,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT document_chunks_body_not_empty CHECK (char_length(body) > 0)
);

CREATE INDEX idx_chunks_org ON document_chunks (org_id);
CREATE INDEX idx_chunks_tags ON document_chunks USING gin (tags);
CREATE INDEX idx_chunks_live ON document_chunks (org_id, created_at) WHERE NOT archived;
CREATE UNIQUE INDEX idx_chunks_org_title ON document_chunks (org_id, title);
CREATE INDEX idx_chunks_embedding ON document_chunks
    USING hnsw (embedding vector_cosine_ops) WITH (m = 16, ef_construction = 64);

COMMENT ON TABLE document_chunks IS 'Chunks of a document, with their embeddings.';
COMMENT ON COLUMN document_chunks.embedding IS 'The embedding, 1536 wide.';
`

// readBack introspects a database and fails the test on anything the DSL could
// not describe: this fixture is chosen so that nothing should be skipped, so a
// report entry is a finding rather than a note.
func readBack(t *testing.T, pool *pgxpool.Pool) *schema.Registry {
	t.Helper()
	reg, rep, err := introspect.Registry(context.Background(), sqlb.New(pool), introspect.Options{})
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	if !rep.Empty() {
		t.Fatalf("the fixture is meant to be fully describable, and this was skipped:\n%s", rep)
	}
	return reg
}

// applyRegistry renders a registry to DDL and applies it, failing on the first
// statement Postgres refuses.
func applyRegistry(t *testing.T, pool *pgxpool.Pool, reg *schema.Registry) {
	t.Helper()
	changes, err := migrate.Diff(nil, reg)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	for _, c := range changes {
		if strings.TrimSpace(c.Up) == "" {
			continue
		}
		if _, err := pool.Exec(context.Background(), c.Up); err != nil {
			t.Fatalf("Postgres refused generated DDL: %v\n%s", err, c.Up)
		}
	}
}

// The whole loop.
func TestRoundTripIsAFixpoint(t *testing.T) {
	// The pgvector image, because the fixture's whole point is the types and
	// the index that were breaking the loop.
	source := vectorDB(t)
	mustExec(t, source, awkwardSchema)

	// 1. Read the database.
	reg := readBack(t, source)

	// 2. Write it back out as the schema package a project would then own. This
	//    is the bootstrap that turns sixty-nine tables into sixty-nine
	//    declarations to review rather than sixty-nine to write, and it used to
	//    stop at the first vector column.
	src, err := codegen.RenderSchema(reg, codegen.SchemaOptions{Package: "ragschema"})
	if err != nil {
		t.Fatalf("RenderSchema over an introspected registry: %v", err)
	}
	if err := buildsAgainstSqlb(t, string(src)); err != nil {
		t.Fatalf("the rendered schema does not compile: %v\n%s", err, src)
	}

	// 3. Build a second database from what was read.
	rebuilt := vectorDB(t)
	applyRegistry(t, rebuilt, reg)

	// 4. Read that one, and the two must agree in both directions. Diff is not
	//    symmetric — one side proposes what the other lacks — so both are
	//    checked rather than assuming.
	reread := readBack(t, rebuilt)
	forward, err := migrate.Diff(reg, reread)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	backward, err := migrate.Diff(reread, reg)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(forward) != 0 || len(backward) != 0 {
		t.Errorf("the round trip is not a fixpoint:\n%s%s",
			renderChanges("read → rebuilt", forward), renderChanges("rebuilt → read", backward))
	}
}

// The vector index specifically: its operator class is not decoration, it is
// the distance function, and pgvector has no default — so an index emitted
// without one is rejected outright.
func TestVectorIndexKeepsItsOperatorClassAndParameters(t *testing.T) {
	source := vectorDB(t)
	mustExec(t, source, awkwardSchema)

	reg := readBack(t, source)
	var found bool
	for _, idx := range reg.Get("document_chunks").Indexes() {
		if idx.Name != "idx_chunks_embedding" {
			continue
		}
		found = true
		if got := idx.Opclasses["embedding"]; got != "vector_cosine_ops" {
			t.Errorf("operator class = %q, want vector_cosine_ops", got)
		}
		if idx.With["m"] != "16" || idx.With["ef_construction"] != "64" {
			t.Errorf("storage parameters = %v, want m=16 ef_construction=64", idx.With)
		}
	}
	if !found {
		t.Fatal("the vector index was not imported at all")
	}

	// And the DDL it produces is DDL Postgres accepts, which is the half that
	// was failing: `USING hnsw (embedding)` is an error, not a lesser index.
	rebuilt := vectorDB(t)
	applyRegistry(t, rebuilt, reg)

	var def string
	if err := rebuilt.QueryRow(context.Background(),
		`SELECT indexdef FROM pg_indexes WHERE indexname = 'idx_chunks_embedding'`).Scan(&def); err != nil {
		t.Fatalf("reading the rebuilt index: %v", err)
	}
	for _, want := range []string{"hnsw", "vector_cosine_ops", "m='16'", "ef_construction='64'"} {
		if !strings.Contains(def, want) {
			t.Errorf("the rebuilt index is missing %q:\n%s", want, def)
		}
	}
}

// A schema rendered from a database has to compile, which is what the bootstrap
// depends on and what a string comparison would not check.
func buildsAgainstSqlb(t *testing.T, src string) error {
	t.Helper()

	// pgtest is a module of its own beside the engine, so the checkout is the
	// parent directory.
	root, err := filepath.Abs("..")
	if err != nil {
		return err
	}
	dir := t.TempDir()
	for name, content := range map[string]string{
		"go.mod": "module fixpointcheck\n\ngo 1.25.0\n\n" +
			"require github.com/jryannel/sqlb v0.0.0\n\n" +
			"replace github.com/jryannel/sqlb => " + root + "\n",
		"schema.go": src,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			return err
		}
	}

	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	return nil
}

func renderChanges(label string, changes []migrate.Change) string {
	if len(changes) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n%s (%d):\n", label, len(changes))
	for _, c := range changes {
		fmt.Fprintf(&b, "  %s\n    %s\n", c.Comment, strings.TrimSpace(c.Up))
	}
	return b.String()
}

// The stronger claim, and the one a consumer actually depends on: the rebuilt
// database *is* the first database.
//
// Comparing the two registries is not enough, and the gap is not academic —
// anything both sides drop is invisible to that comparison. A constraint name
// introspect does not record is dropped identically on each pass, so the
// registries agree while the databases do not, and the project's next diff
// against production proposes dropping and re-adding a constraint forever
// (issue #53's fourth finding).
func TestRebuiltDatabaseMatchesTheOriginal(t *testing.T) {
	source := vectorDB(t)
	mustExec(t, source, awkwardSchema)

	rebuilt := vectorDB(t)
	applyRegistry(t, rebuilt, readBack(t, source))

	before, after := catalogDigest(t, source), catalogDigest(t, rebuilt)
	if before != after {
		t.Errorf("the rebuilt database is not the one that was read:\n%s", diffLines(before, after))
	}
}

// catalogDigest is what the database says about itself, in a form two of them
// can be compared by: every column with its type, nullability and default,
// every constraint by name and definition, every index by definition.
func catalogDigest(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	ctx := context.Background()

	var lines []string
	collect := func(query string) {
		rows, err := pool.Query(ctx, query)
		if err != nil {
			t.Fatalf("reading the catalog: %v", err)
		}
		defer rows.Close()
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatalf("scanning the catalog: %v", err)
			}
			lines = append(lines, line)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("reading the catalog: %v", err)
		}
	}

	collect(`
		SELECT format('column %s.%s %s null=%s default=%s',
		              c.relname, a.attname, format_type(a.atttypid, a.atttypmod),
		              NOT a.attnotnull, COALESCE(pg_get_expr(d.adbin, d.adrelid), '-'))
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_attrdef d ON d.adrelid = c.oid AND d.adnum = a.attnum
		WHERE n.nspname = 'public' AND c.relkind = 'r' AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY 1`)
	collect(`
		SELECT format('constraint %s.%s %s', c.relname, con.conname, pg_get_constraintdef(con.oid))
		FROM pg_constraint con
		JOIN pg_class c ON c.oid = con.conrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND con.contype <> 'n'
		ORDER BY 1`)
	// The indexes, with their storage parameters sorted. reloptions is a set,
	// Postgres hands it back in declaration order, and sqlb renders it sorted so
	// that a generated migration does not reorder itself between runs —
	// comparing the definition text as-is would report that choice as a
	// difference. Normalised here rather than in SQL, where a missing WITH
	// clause turns the whole expression null.
	rows, err := pool.Query(ctx, `
		SELECT pg_get_indexdef(i.oid), COALESCE(i.reloptions, '{}')
		FROM pg_class i
		JOIN pg_index x ON x.indexrelid = i.oid
		JOIN pg_class c ON c.oid = x.indrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public'`)
	if err != nil {
		t.Fatalf("reading the indexes: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var def string
		var options []string
		if err := rows.Scan(&def, &options); err != nil {
			t.Fatalf("scanning the indexes: %v", err)
		}
		if len(options) > 0 {
			sort.Strings(options)
			if cut := strings.Index(def, " WITH ("); cut >= 0 {
				def = def[:cut]
			}
			def += " WITH (" + strings.Join(options, ", ") + ")"
		}
		lines = append(lines, "index "+def)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading the indexes: %v", err)
	}

	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// diffLines reports the lines that differ, because a wall of identical catalog
// text with one changed row in it is not a reviewable failure message.
func diffLines(before, after string) string {
	have := map[string]bool{}
	for _, line := range strings.Split(after, "\n") {
		have[line] = true
	}
	var b strings.Builder
	for _, line := range strings.Split(before, "\n") {
		if !have[line] {
			fmt.Fprintf(&b, "  only in the original: %s\n", line)
		}
	}
	had := map[string]bool{}
	for _, line := range strings.Split(before, "\n") {
		had[line] = true
	}
	for _, line := range strings.Split(after, "\n") {
		if !had[line] {
			fmt.Fprintf(&b, "  only in the rebuild:  %s\n", line)
		}
	}
	return b.String()
}
