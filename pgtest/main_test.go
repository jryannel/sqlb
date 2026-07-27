package pgtest

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// image is pinned rather than tracking latest, because a test that measures
// what Postgres does is only meaningful if it says which Postgres. 18 is the
// version ADR-0014's manual measurements were taken against.
const image = "postgres:18-alpine"

// admin is the connection to the container's default database. Tests do not
// use it directly; freshDB creates a database of its own through it, which is
// far cheaper than a container each and keeps tests independent anyway.
var admin *sql.DB

// dsn renders a connection string for a database in the running container,
// reachable from the host.
var dsn func(database string) string

// pgContainer is the running Postgres, kept so that a second container — the
// pooler — can be pointed at it by container address rather than host port.
var pgContainer *postgres.PostgresContainer

func TestMain(m *testing.M) {
	// Failing loudly beats skipping. See the package doc: the whole reason
	// this module exists is to stop a gate from claiming coverage it lacks,
	// and "no Docker, so everything passed" is that same failure wearing a
	// different hat.
	code, err := run(m)
	if err != nil {
		log.Fatalf("pgtest: %v", err)
	}
	os.Exit(code)
}

func run(m *testing.M) (int, error) {
	ctx := context.Background()

	container, err := postgres.Run(ctx, image,
		postgres.WithDatabase("sqlb"),
		postgres.WithUsername("sqlb"),
		postgres.WithPassword("sqlb"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		return 0, fmt.Errorf("starting %s: %w", image, err)
	}
	pgContainer = container
	defer func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			log.Printf("pgtest: terminating container: %v", err)
		}
	}()

	host, err := container.Host(ctx)
	if err != nil {
		return 0, fmt.Errorf("container host: %w", err)
	}
	port, err := container.MappedPort(ctx, "5432/tcp")
	if err != nil {
		return 0, fmt.Errorf("container port: %w", err)
	}

	dsn = func(database string) string {
		return fmt.Sprintf("postgres://sqlb:sqlb@%s:%s/%s?sslmode=disable",
			host, port.Port(), database)
	}

	admin, err = sql.Open("pgx", dsn("sqlb"))
	if err != nil {
		return 0, fmt.Errorf("opening admin connection: %w", err)
	}
	defer admin.Close()

	stopPooler, err := startPooler(ctx, container)
	if err != nil {
		return 0, fmt.Errorf("starting the pooler: %w", err)
	}
	defer stopPooler()

	return m.Run(), nil
}

// freshDB creates an empty database and returns a connection to it, dropped
// when the test ends. A database per test rather than a container per test:
// container startup dominates, and CREATE DATABASE is milliseconds.
func freshDB(t *testing.T) *sql.DB {
	t.Helper()

	name := databaseName(t)
	// Dropped first so that a crashed run leaves nothing that makes the next
	// one fail with a confusing "already exists" instead of its real problem.
	mustExec(t, admin, `DROP DATABASE IF EXISTS `+quoteIdent(name))
	mustExec(t, admin, `CREATE DATABASE `+quoteIdent(name))

	db, err := sql.Open("pgx", dsn(name))
	if err != nil {
		t.Fatalf("opening %s: %v", name, err)
	}
	t.Cleanup(func() {
		db.Close()
		// A dropped database cannot have open connections, and database/sql
		// pools them, so the close above is not always enough on its own.
		_, _ = admin.Exec(`DROP DATABASE IF EXISTS ` + quoteIdent(name) + ` WITH (FORCE)`)
	})

	bootstrap(t, db)
	return db
}

// bootstrap installs what the generated DDL assumes exists but Postgres does
// not provide.
//
// schema.GenUUIDv7 emits uuid_generate_v7(), which is the pg_uuidv7 extension's
// spelling and is documented as requiring it. Postgres 18 has a built-in
// uuidv7(), so a one-line shim gives the generated DDL something to bind to
// without pulling an extension image in.
//
// Worth stating plainly, because the test cannot: generated DDL for a UUIDv7
// primary key does not apply to a stock Postgres. That is a real gap in what
// sqlb emits, not an artefact of this harness.
func bootstrap(t *testing.T, db *sql.DB) {
	t.Helper()
	mustExec(t, db, `
		CREATE FUNCTION uuid_generate_v7() RETURNS uuid
		LANGUAGE sql VOLATILE AS 'SELECT uuidv7()'
	`)
}

// databaseName derives a legal, unique database name from the test name.
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

	// Postgres truncates identifiers at 63 bytes, which would collide two long
	// subtests into one database and produce a failure that looks like a bug in
	// the code under test.
	const max = 40
	if len(name) > max {
		name = name[:max]
	}
	return fmt.Sprintf("t_%s_%d", name, time.Now().UnixNano()%1e9)
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func mustExec(t *testing.T, db *sql.DB, query string) {
	t.Helper()
	if _, err := db.Exec(query); err != nil {
		t.Fatalf("exec failed: %v\n%s", err, strings.TrimSpace(query))
	}
}
