package tasksevolved_test

// The bootstrap below is the same pattern example/fxapp/main_test.go uses:
// SQLB_TEST_POSTGRES names a running Postgres, a fresh, empty database is
// created per test, and nothing here starts a server. This module needs
// something narrower than fxapp's harness — one long-lived connection that
// every step in evolve_test.go shares, since the whole point is that data
// carries from one non-additive change to the next rather than starting over.

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

// pgEnv names the Postgres these tests run against. It must be Postgres 18:
// the schema uses schema.UUIDv7, and migrate.MinPostgres(18) below emits the
// built-in uuidv7() rather than the pg_uuidv7 extension's spelling, so a
// migration generated for this module does not apply to an older server at
// all — a true statement about the module rather than a broken test.
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
				"This test needs a Postgres; it does not start one.\n"+
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

// freshDatabase returns the DSN of an empty database, dropped at the end of
// the test.
func freshDatabase(t *testing.T) string {
	t.Helper()

	once.Do(startPostgres)
	if startErr != nil {
		t.Fatalf("tasks-evolved: %v", startErr)
	}

	name := databaseName(t)
	mustExec(t, `DROP DATABASE IF EXISTS `+quoteIdent(name))
	mustExec(t, `CREATE DATABASE `+quoteIdent(name))
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(),
			`DROP DATABASE IF EXISTS `+quoteIdent(name)+` WITH (FORCE)`)
	})
	return dsnFor(name)
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
