package fxapp_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The tests that exercise the server run against a real Postgres, for the
// reason pgtest exists: a suite that asserts on generated SQL proves the
// generator produced what somebody expected, and a suite that runs it proves
// Postgres accepts it. The claims here are of the second kind — that the
// migrations apply, that the boot-time provisioning is idempotent, that a
// space boundary held by query hooks actually holds.
//
// There is no skip-when-the-database-is-absent path: a suite that passes
// silently when it cannot reach one reports coverage it does not have.
//
// The connection is resolved on first use rather than in TestMain, so that the
// tests which need no database — the graph validation, which constructs
// nothing — run without one configured. That is not the same thing as skipping:
// a test that needs Postgres and cannot have it still fails.

// pgEnv names the Postgres these tests run against. They do not start one:
// provisioning is `mise run pg-up` locally and a service container in CI.
//
// It must be Postgres 18. cmd/migrate passes migrate.MinPostgres(18), so the
// DDL uses the built-in uuidv7() and needs no extension; against 17 it fails at
// the first CREATE TABLE, which is a true statement about the example rather
// than a broken test. The version is pinned where the server is started.
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
				"These tests need a Postgres; they no longer start one.\n"+
				"  locally: mise run pg-up   (then mise run test-fx)\n"+
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

// freshDatabase returns the DSN of an empty database.
//
// Empty, not migrated: applying the history is the fxkit glue's job, so a test that
// migrated first would be testing a different program than the one that ships.
// Every boot in this file therefore also asserts that the migrations apply.
//
// A database per test rather than a server per test: CREATE DATABASE is
// milliseconds, and a test that shares tables with another eventually depends
// on the order they run in.
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
