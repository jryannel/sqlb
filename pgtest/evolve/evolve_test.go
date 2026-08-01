// The gate the whole schema-evolution example rests on: that the checked-in
// migration history still builds the schema evolveschema declares.
//
// example/evolve keeps the current state in Go and the history in SQL, and
// nothing in the repository can check that pairing by comparing files —
// generate-check compares generated code against the declaration, and both
// sides of that stay consistent when someone edits schema.go and forgets the
// migration. This replays the history into an empty database and asks Postgres
// what it built.
//
// # Why a package of its own
//
// It imports evolveschema for its side effects, which puts those tables into
// schema.DefaultRegistry() for the whole test binary. The pgtest package next
// door applies DefaultRegistry to a fresh database in several of its tests and
// expects to find the blog example's tables there and nothing else, so this
// cannot live beside them.
//
// shadow.Normalize also rewrites the declared check expressions in place,
// and there is one registry with no way to diff against a copy. A binary of its
// own bounds that too. example/tasks/migrations/drift_test.go is the same
// arrangement for the same two reasons.
package evolve_test

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/jryannel/sqlb/migrate"
	"github.com/jryannel/sqlb/schema"
	"github.com/jryannel/sqlb/shadow"

	// Imported for its side effects: declaring a table registers it, and the
	// registry is the whole subject of this test.
	_ "github.com/jryannel/sqlb/example/evolve/evolveschema"
)

// image is pinned to 18 because the history was generated with
// migrate.MinPostgres(18), so it uses the built-in uuidv7() and would fail on
// 17 at the first CREATE TABLE.
const image = "postgres:18-alpine"

// dir is the migration history, relative to this package.
const dir = "../../example/evolve/migrations"

var dsn string

func TestMain(m *testing.M) {
	code, err := run(m)
	if err != nil {
		log.Fatalf("evolve: %v", err)
	}
	os.Exit(code)
}

func run(m *testing.M) (int, error) {
	ctx := context.Background()

	container, err := postgres.Run(ctx, image,
		postgres.WithDatabase("evolve"),
		postgres.WithUsername("evolve"),
		postgres.WithPassword("evolve"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		return 0, fmt.Errorf("starting %s: %w", image, err)
	}
	defer func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			log.Printf("evolve: terminating container: %v", err)
		}
	}()

	// One database, used once, and written to by nothing else: shadow.Build
	// refuses a database that already has tables in it.
	dsn, err = container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return 0, fmt.Errorf("container connection string: %w", err)
	}
	return m.Run(), nil
}

func TestTheHistoryStillBuildsTheDeclaredSchema(t *testing.T) {
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}
	t.Cleanup(pool.Close)

	// shadow.Build rather than a runner: goose would write a version table that
	// introspection cannot tell apart from schema, and the diff below would
	// then propose dropping it forever.
	current, _, res, err := shadow.Build(ctx, pool, shadow.Options{Dir: dir})
	if err != nil {
		t.Fatalf("replaying the migration history: %v", err)
	}
	if len(res.Files) == 0 {
		t.Fatal("the replay applied no files, so everything below compares against nothing")
	}
	// Six files for five revisions: revision 2 renders two, because its index
	// change needs a file with no transaction around it. If this number moves,
	// the history changed and the document describing it did not.
	if got, want := len(res.Files), 6; got != want {
		t.Errorf("replayed %d files, want %d: %v", got, want, res.Files)
	}

	target := schema.DefaultRegistry()
	defer restore(snapshotChecks(target))

	// Without this the enum CHECKs come back in Postgres's spelling and the
	// declaration in the author's, and the diff below is never empty.
	unprobed, err := shadow.Normalize(ctx, pool, target, shadow.Options{})
	if err != nil {
		t.Fatalf("normalising the declared checks: %v", err)
	}
	if len(unprobed) > 0 {
		t.Fatalf("every declared check should be probeable against a database the whole "+
			"history has been applied to, but these were not: %v", unprobed)
	}

	changes, err := migrate.Diff(current, target, migrate.MinPostgres(18))
	if err != nil {
		t.Fatalf("diffing the history against the declaration: %v", err)
	}
	if len(changes) > 0 {
		t.Fatalf("the migration history no longer builds what evolveschema declares.\n\n"+
			"Someone edited schema.go without adding a migration, or edited a migration "+
			"without the schema. What is missing:\n\n%s\n"+
			"Generate it with:\n"+
			"    sqlb migrate -name <what-changed> ./example/evolve/evolveschema\n",
			describe(changes))
	}
}

// The three things the history did that a file comparison cannot see, checked
// against the database the replay produced rather than against the SQL that was
// supposed to produce it.
//
// Without this, the test above would pass on a history that reached the right
// shape by the wrong route — dropping and recreating the column a RENAME was
// supposed to move, for instance, which is the exact mistake ADR-0014 says
// inferring renames would cause.
func TestTheReplayedDatabaseShowsWhatEachRevisionDid(t *testing.T) {
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}
	t.Cleanup(pool.Close)

	for _, want := range []struct {
		what  string
		query string
		args  []any
	}{
		// Revision 4, the column rename.
		{"customers.email_address exists",
			`SELECT count(*) FROM information_schema.columns
			 WHERE table_name = 'customers' AND column_name = 'email_address'`, nil},
		// Revision 4, the table rename.
		{"the support_agents table exists",
			`SELECT count(*) FROM information_schema.tables
			 WHERE table_name = 'support_agents'`, nil},
		// Revision 2, the index that needed a file of its own.
		{"the composite index exists",
			`SELECT count(*) FROM pg_indexes
			 WHERE tablename = 'tickets' AND indexname = 'tickets_customer_id_status_idx'`, nil},
	} {
		var n int
		if err := pool.QueryRow(ctx, want.query, want.args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", want.what, err)
		}
		if n != 1 {
			t.Errorf("%s: found %d, want 1", want.what, n)
		}
	}

	// And the other direction, which is the half that would catch a rename done
	// as a drop-and-add: the old names must be gone, not merely shadowed.
	for _, gone := range []struct{ what, query string }{
		{"customers.email", `SELECT count(*) FROM information_schema.columns
			 WHERE table_name = 'customers' AND column_name = 'email'`},
		{"the agents table", `SELECT count(*) FROM information_schema.tables
			 WHERE table_name = 'agents'`},
		// Revision 5, the destructive one. It is live in the checked-in file
		// rather than commented out, which took -allow-destructive to render.
		{"tickets.legacy_ref", `SELECT count(*) FROM information_schema.columns
			 WHERE table_name = 'tickets' AND column_name = 'legacy_ref'`},
	} {
		var n int
		if err := pool.QueryRow(ctx, gone.query).Scan(&n); err != nil {
			t.Fatalf("%s: %v", gone.what, err)
		}
		if n != 0 {
			t.Errorf("%s is still present after the history was replayed", gone.what)
		}
	}
}

// snapshotChecks records the declared check expressions so they can be put back
// after Normalize rewrites them, since the second test in this package
// should not inherit a registry in Postgres's spelling.
func snapshotChecks(reg *schema.Registry) map[*schema.TableDef]map[string]string {
	out := map[*schema.TableDef]map[string]string{}
	for _, t := range reg.Tables() {
		if len(t.Checks()) == 0 {
			continue
		}
		exprs := map[string]string{}
		for _, c := range t.Checks() {
			exprs[c.Name] = c.Expr
		}
		out[t] = exprs
	}
	return out
}

func restore(snapshot map[*schema.TableDef]map[string]string) {
	for t, exprs := range snapshot {
		for name, expr := range exprs {
			t.ReplaceCheckExpr(name, expr)
		}
	}
}

// describe renders the outstanding changes the way a migration file would, so
// the failure message is the thing to paste rather than a summary of it.
func describe(changes []migrate.Change) string {
	var b strings.Builder
	for _, c := range changes {
		if c.Comment != "" {
			b.WriteString("  -- " + c.Comment + "\n")
		}
		for _, l := range strings.Split(strings.TrimSpace(c.Up), "\n") {
			b.WriteString("  " + l + "\n")
		}
	}
	return b.String()
}
