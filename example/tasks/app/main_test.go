package app_test

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

	"github.com/jryannel/sqlb/example/tasks/migrations"
)

// The tests run against a real Postgres, for the reason pgtest exists: a suite
// that asserts on generated SQL proves the generator produces what somebody
// expected, and a suite that runs it proves Postgres accepts it. This example
// makes claims that only the second kind can check — that the composite foreign
// keys reject a cross-workspace reference, that the completed_at trigger fires,
// that a rolled-back transaction leaves no comment behind.
//
// There is deliberately no skip-when-Docker-is-absent path. A suite that passes
// silently when it cannot reach a database reports coverage it does not have.

// image is pinned, and pinned to 18 specifically: cmd/migrate passes
// migrate.MinPostgres(18), so the DDL uses the built-in uuidv7() and needs no
// extension. Running these against 17 would fail at the first CREATE TABLE,
// which is a true statement about the demo rather than a broken test.
const image = "postgres:18-alpine"

var (
	admin *sql.DB
	dsn   func(database string) string
)

func TestMain(m *testing.M) {
	code, err := run(m)
	if err != nil {
		log.Fatalf("tasks: %v", err)
	}
	os.Exit(code)
}

func run(m *testing.M) (int, error) {
	ctx := context.Background()

	container, err := postgres.Run(ctx, image,
		postgres.WithDatabase("tasks"),
		postgres.WithUsername("tasks"),
		postgres.WithPassword("tasks"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		return 0, fmt.Errorf("starting %s: %w", image, err)
	}
	defer func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			log.Printf("tasks: terminating container: %v", err)
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
		return fmt.Sprintf("postgres://tasks:tasks@%s:%s/%s?sslmode=disable",
			host, port.Port(), database)
	}

	admin, err = sql.Open("pgx", dsn("tasks"))
	if err != nil {
		return 0, fmt.Errorf("opening the admin connection: %w", err)
	}
	defer admin.Close()

	return m.Run(), nil
}

// freshDB returns a connection to an empty database with the migrations
// applied — which is also a test of the migrations, run once per test.
//
// A database per test rather than a container per test: starting Postgres
// dominates, CREATE DATABASE is milliseconds, and a test that shares tables
// with another eventually depends on the order they run in.
func freshDB(t *testing.T) *sql.DB {
	t.Helper()

	name := databaseName(t)
	// Dropped first, so that a crashed run leaves nothing that makes the next
	// one fail with "already exists" instead of its real problem.
	mustExec(t, admin, `DROP DATABASE IF EXISTS `+quoteIdent(name))
	mustExec(t, admin, `CREATE DATABASE `+quoteIdent(name))

	db, err := sql.Open("pgx", dsn(name))
	if err != nil {
		t.Fatalf("opening %s: %v", name, err)
	}
	t.Cleanup(func() {
		db.Close()
		// A database with open connections cannot be dropped, and database/sql
		// pools them, so closing is not always enough on its own.
		_, _ = admin.Exec(`DROP DATABASE IF EXISTS ` + quoteIdent(name) + ` WITH (FORCE)`)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatalf("applying migrations: %v", err)
	}
	return db
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
