// example/catalog exists to settle one row of docs/special-cases.md's census:
// a product catalog whose categories point at their own table. See the
// package doc in catalogschema for the correction — AddField makes the
// self-reference a real foreign key now — and README.md for what it costs
// and what it still does not fix.
package catalog_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jryannel/sqlb"
	// Imported for the side effect: catalogschema.Category registers itself
	// (and, via AddField, its parent column) into schema.DefaultRegistry()
	// on package init. Nothing here calls into the package directly — the
	// row type below is the hand-written mirror of what it declares, the
	// same split example/vault and expand_test.go both use.
	_ "github.com/jryannel/sqlb/example/catalog/catalogschema"
	"github.com/jryannel/sqlb/migrate"
	"github.com/jryannel/sqlb/schema"
)

// Category is the row type for catalogschema's "categories" table, written by
// hand rather than by `sqlb generate`: one table does not earn codegen's
// ceremony. ParentID is the stored column; Parent is the field the query
// resolves it into once asked with Expand("parent") — the same split
// expand_test.go's expTask/expList pair uses, except here both ends are the
// same type.
type Category struct {
	ID       string    `db:"id" json:"id" sqlb:"type:uuid,pk,default"`
	Name     string    `db:"name" json:"name" sqlb:"search"`
	ParentID *string   `db:"parent_id" json:"parent_id" sqlb:"type:uuid,filter,expand"`
	Parent   *Category `db:"-" json:"parent,omitempty" sqlb:"expands=parent_id"`
}

func (Category) TableName() string { return "categories" }

// pgEnv names the Postgres these tests run against. They do not start one —
// mise run pg-up does that, locally and in CI.
const pgEnv = "SQLB_TEST_POSTGRES"

var (
	once     sync.Once
	admin    *pgxpool.Pool
	dsnFor   func(database string) string
	startErr error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if admin != nil {
		admin.Close()
	}
	os.Exit(code)
}

func startPostgres() {
	ctx := context.Background()

	base := os.Getenv(pgEnv)
	if base == "" {
		startErr = fmt.Errorf(
			"%s is not set.\n"+
				"These tests need a Postgres; they do not start one.\n"+
				"  locally: mise run pg-up\n"+
				"  CI:      the service containers in .github/workflows/ci.yml",
			pgEnv)
		return
	}
	u, err := url.Parse(base)
	if err != nil {
		startErr = fmt.Errorf("%s is not a valid URL: %w", pgEnv, err)
		return
	}
	dsnFor = func(database string) string {
		v := *u
		v.Path = "/" + database
		return v.String()
	}

	if admin, err = pgxpool.New(ctx, dsnFor("postgres")); err != nil {
		startErr = fmt.Errorf("opening the admin connection: %w", err)
		return
	}
	if err := admin.Ping(ctx); err != nil {
		startErr = fmt.Errorf("%s is set but nothing answered: %w", pgEnv, err)
	}
}

// freshDatabase returns a pool connected to a freshly created, empty database
// with catalogschema's DDL already applied: migrate.Diff against nothing,
// each Change.Up run in turn. Postgres 18 is required — MinPostgres(18) is
// what lets the UUIDv7 primary key use the built-in uuidv7() rather than an
// extension, and against 17 this fails at the first CREATE TABLE.
func freshDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()

	once.Do(startPostgres)
	if startErr != nil {
		t.Fatalf("catalog: %v", startErr)
	}

	name := databaseName(t)
	// Dropped first, so a crashed run leaves nothing that makes the next one
	// fail with "already exists" instead of its real problem.
	mustExec(t, `DROP DATABASE IF EXISTS `+quoteIdent(name))
	mustExec(t, `CREATE DATABASE `+quoteIdent(name))
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(),
			`DROP DATABASE IF EXISTS `+quoteIdent(name)+` WITH (FORCE)`)
	})

	pool, err := pgxpool.New(context.Background(), dsnFor(name))
	if err != nil {
		t.Fatalf("opening %s: %v", name, err)
	}
	t.Cleanup(pool.Close)

	changes, err := migrate.Diff(nil, schema.DefaultRegistry(), migrate.MinPostgres(18))
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(changes) == 0 {
		t.Fatal("diffing catalogschema from nothing produced no statements")
	}
	for i, c := range changes {
		if strings.TrimSpace(c.Up) == "" {
			continue
		}
		if _, err := pool.Exec(context.Background(), c.Up); err != nil {
			t.Fatalf("statement %d of %d failed: %v\n%s",
				i+1, len(changes), err, strings.TrimSpace(c.Up))
		}
	}

	return pool
}

func databaseName(t *testing.T) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return '_'
		}
	}, t.Name())

	// Postgres truncates identifiers at 63 bytes, which would collide two
	// long subtests into one database and produce a failure that looks like a
	// bug in the code under test.
	const max = 40
	if len(name) > max {
		name = name[:max]
	}
	return fmt.Sprintf("t_%s_%d", name, time.Now().UnixNano()%1e9)
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func mustExec(t *testing.T, query string) {
	t.Helper()
	if _, err := admin.Exec(context.Background(), query); err != nil {
		t.Fatalf("exec failed: %v\n%s", err, strings.TrimSpace(query))
	}
}

func strp(s string) *string { return &s }

// wantForeignKeyViolation fails the test unless err unwraps to a
// *sqlb.ConstraintError whose Kind is ConstraintForeignKey.
func wantForeignKeyViolation(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("want a foreign key violation, got nil error")
	}
	var ce *sqlb.ConstraintError
	if !errors.As(err, &ce) {
		t.Fatalf("%v does not unwrap to *sqlb.ConstraintError", err)
	}
	if ce.Kind != sqlb.ConstraintForeignKey {
		t.Fatalf("kind = %q, want %q", ce.Kind, sqlb.ConstraintForeignKey)
	}
}

// TestSelfReferenceIsARealForeignKeyNow is the correction this example
// exists to make. pgtest/census_test.go's
// TestSelfReferenceIsAPlainColumnWithoutAForeignKey — and its doc comment,
// which says outright "there is no AddField" — describe a state that changed
// after that test was written. AddField exists, Ref accepts the table it is
// declared beside, and a category tree is a real foreign key end to end: a
// parent column that resolves through Expand, and a parent_id naming a row
// that is not there gets refused rather than stored.
//
// This also builds and walks the tree three levels deep (root -> child ->
// grandchild) with plain InsertRows, and resolves the grandchild's parent
// through Expand("parent") — the FK relation is not just present, it is the
// one Expand actually joins on.
// TestMigrationRendersAForeignKey checks the specific claim this example
// exists to make, at the level where it would first go missing: the rendered
// DDL, not just a runtime refusal further down. AddField compiling is not
// itself proof — a column that came out as ExternalRef-shaped, with an index
// but no constraint, would also compile and would also pass every other test
// here if the assertion stopped at "the insert failed" without ever looking
// at what migrate actually emitted. This looks at the SQL text directly: the
// diff against nothing must contain a FOREIGN KEY clause naming categories
// twice, once as the table and once as the reference target.
func TestMigrationRendersAForeignKey(t *testing.T) {
	changes, err := migrate.Diff(nil, schema.DefaultRegistry(), migrate.MinPostgres(18))
	if err != nil {
		t.Fatalf("diff: %v", err)
	}

	var ddl strings.Builder
	for _, c := range changes {
		ddl.WriteString(c.Up)
		ddl.WriteString("\n")
	}
	rendered := ddl.String()

	if !strings.Contains(rendered, "FOREIGN KEY") {
		t.Fatalf("no FOREIGN KEY in the rendered migration:\n%s", rendered)
	}
	if !strings.Contains(rendered, `REFERENCES "categories"`) {
		t.Fatalf("the foreign key does not reference categories:\n%s", rendered)
	}
}

func TestSelfReferenceIsARealForeignKeyNow(t *testing.T) {
	ctx := context.Background()
	pool := freshDatabase(t)
	db := sqlb.New(pool)

	root, err := sqlb.InsertRows(&Category{Name: "Electronics"}).One(ctx, db)
	if err != nil {
		t.Fatalf("insert root: %v", err)
	}

	child, err := sqlb.InsertRows(&Category{Name: "Audio", ParentID: strp(root.ID)}).One(ctx, db)
	if err != nil {
		t.Fatalf("insert child: %v", err)
	}

	grandchild, err := sqlb.InsertRows(&Category{Name: "Headphones", ParentID: strp(child.ID)}).One(ctx, db)
	if err != nil {
		t.Fatalf("insert grandchild: %v", err)
	}

	resolved, err := sqlb.Query[Category]().
		Where(sqlb.F("id").Eq(grandchild.ID)).
		Expand("parent").
		One(ctx, db)
	if err != nil {
		t.Fatalf("query grandchild with expand: %v", err)
	}
	if resolved.Parent == nil {
		t.Fatal("expand(parent) did not resolve a parent")
	}
	if resolved.Parent.ID != child.ID {
		t.Errorf("resolved parent = %q, want the child %q", resolved.Parent.ID, child.ID)
	}
	if resolved.Parent.Name != "Audio" {
		t.Errorf("resolved parent name = %q, want %q", resolved.Parent.Name, "Audio")
	}
}

// TestForeignKeyRefusesADanglingParent is the opposite of what
// pgtest/census_test.go's ExternalRef test proves. That test inserts a
// category whose parent_id names a row that was never there and watches it
// succeed, because ExternalRef renders no constraint. This is the same
// insert against the real Ref this example declares, and it must fail.
func TestForeignKeyRefusesADanglingParent(t *testing.T) {
	ctx := context.Background()
	pool := freshDatabase(t)
	db := sqlb.New(pool)

	dangling := "00000000-0000-4000-8000-000000000000"
	_, err := sqlb.InsertRows(&Category{Name: "Orphan", ParentID: &dangling}).One(ctx, db)
	wantForeignKeyViolation(t, err)
}

// TestDeletingAParentWithChildrenIsRefused checks Ref's default OnDelete
// rather than assuming it. schema.Ref sets OnDelete: schema.NoAction unless a
// caller overrides it — catalogschema does not — and NO ACTION is checked
// immediately for a non-deferred constraint, which is what this asserts:
// deleting a category that still has a child fails with the same
// ConstraintForeignKey a dangling insert does, rather than cascading or
// silently orphaning the child.
func TestDeletingAParentWithChildrenIsRefused(t *testing.T) {
	ctx := context.Background()
	pool := freshDatabase(t)
	db := sqlb.New(pool)

	root, err := sqlb.InsertRows(&Category{Name: "Kitchen"}).One(ctx, db)
	if err != nil {
		t.Fatalf("insert root: %v", err)
	}
	if _, err := sqlb.InsertRows(&Category{Name: "Cookware", ParentID: strp(root.ID)}).One(ctx, db); err != nil {
		t.Fatalf("insert child: %v", err)
	}

	_, err = sqlb.DeleteRows[Category]().Where(sqlb.F("id").Eq(root.ID)).Exec(ctx, db)
	wantForeignKeyViolation(t, err)
}

// TestSearchIsSubstringNotFullText is ADR-0037 written down as code: `?search`
// (here, Contains directly — the predicate the filter package's search
// fan-out compiles down to for every Searchable column) is ILIKE, not a
// ranked full-text match. It finds a substring anywhere in the name and does
// not stem, rank or need an index. Escalating past that — a trigram index, the
// tsvector ADR-0037 leaves for a later record, the vector column
// docs/architecture.md's "Vectors declare their index" leaves open — is
// deliberately out of scope here.
func TestSearchIsSubstringNotFullText(t *testing.T) {
	ctx := context.Background()
	pool := freshDatabase(t)
	db := sqlb.New(pool)

	names := []string{"Wireless Headphones", "Wired Headphones", "Bluetooth Speaker", "USB Cable"}
	for _, n := range names {
		if _, err := sqlb.InsertRows(&Category{Name: n}).One(ctx, db); err != nil {
			t.Fatalf("insert %q: %v", n, err)
		}
	}

	got, err := sqlb.Query[Category]().Where(sqlb.F("name").Contains("Headphones")).All(ctx, db)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2 (Wireless Headphones, Wired Headphones): %+v", len(got), got)
	}
	for _, c := range got {
		if !strings.Contains(c.Name, "Headphones") {
			t.Errorf("row %q does not contain the search term", c.Name)
		}
	}

	// Case-insensitive, because ILIKE is: a lowercase term still finds the
	// mixed-case rows above.
	got, err = sqlb.Query[Category]().Where(sqlb.F("name").Contains("speaker")).All(ctx, db)
	if err != nil {
		t.Fatalf("case-insensitive search: %v", err)
	}
	if len(got) != 1 || got[0].Name != "Bluetooth Speaker" {
		t.Fatalf("got %+v, want exactly Bluetooth Speaker", got)
	}
}
