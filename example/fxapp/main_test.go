package fxapp_test

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// The tests that exercise the server run against a real Postgres, for the
// reason pgtest exists: a suite that asserts on generated SQL proves the
// generator produced what somebody expected, and a suite that runs it proves
// Postgres accepts it. The claims here are of the second kind — that the
// migrations apply, that the boot-time provisioning is idempotent, that a
// space boundary held by query hooks actually holds.
//
// There is no skip-when-Docker-is-absent path: a suite that passes silently
// when it cannot reach a database reports coverage it does not have.
//
// The container is started on first use rather than in TestMain, so that the
// tests which need no database — the graph validation, which constructs
// nothing — run on a machine with no Docker. That is not the same thing as
// skipping: a test that needs Postgres and cannot have it still fails.

// image is pinned to 18 specifically: cmd/migrate passes migrate.MinPostgres(18),
// so the DDL uses the built-in uuidv7() and needs no extension. Running these
// against 17 fails at the first CREATE TABLE, which is a true statement about
// the example rather than a broken test.
const image = "postgres:18-alpine"

var (
	once      sync.Once
	container testcontainers.Container
	admin     *pgxpool.Pool
	dsnFor    func(database string) string
	startErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if container != nil {
		if err := testcontainers.TerminateContainer(container); err != nil {
			log.Printf("fxapp: terminating container: %v", err)
		}
	}
	if admin != nil {
		admin.Close()
	}
	os.Exit(code)
}

func startPostgres() {
	ctx := context.Background()

	pg, err := postgres.Run(ctx, image,
		postgres.WithDatabase("fxapp"),
		postgres.WithUsername("fxapp"),
		postgres.WithPassword("fxapp"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		startErr = fmt.Errorf("starting %s: %w", image, err)
		return
	}
	container = pg

	host, err := pg.Host(ctx)
	if err != nil {
		startErr = fmt.Errorf("container host: %w", err)
		return
	}
	port, err := pg.MappedPort(ctx, "5432/tcp")
	if err != nil {
		startErr = fmt.Errorf("container port: %w", err)
		return
	}
	dsnFor = func(database string) string {
		return fmt.Sprintf("postgres://fxapp:fxapp@%s:%s/%s?sslmode=disable",
			host, port.Port(), database)
	}

	if admin, err = pgxpool.New(ctx, dsnFor("fxapp")); err != nil {
		startErr = fmt.Errorf("opening the admin connection: %w", err)
	}
}

// freshDatabase returns the DSN of an empty database.
//
// Empty, not migrated: applying the history is the sqlbfx kit's job, so a test that
// migrated first would be testing a different program than the one that ships.
// Every boot in this file therefore also asserts that the migrations apply.
//
// A database per test rather than a container per test: starting Postgres
// dominates, CREATE DATABASE is milliseconds, and a test that shares tables
// with another eventually depends on the order they run in.
func freshDatabase(t *testing.T) string {
	t.Helper()

	once.Do(startPostgres)
	if startErr != nil {
		t.Fatalf("fxapp: %v", startErr)
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
	return dsnFor(name)
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
	// subtests into one database and produce a failure that looks like a bug
	// in the code under test.
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
