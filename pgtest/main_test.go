package pgtest

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// pgEnv names the Postgres these tests run against. Provisioning is the
// caller's job — `mise run pg-up` locally, a service container in CI — and this
// module no longer starts one itself.
//
// That is a deliberate reversal of how this suite began, and the reason is in
// doc.go: testcontainers brought docker/docker and forty modules behind it, and
// its reaper reaps by label, which means a test run could and did remove
// long-lived containers somebody else was using. A DSN has neither problem.
const pgEnv = "SQLB_TEST_POSTGRES"

// admin is the connection to the server's maintenance database. Tests do not
// use it directly; freshDB creates a database of its own through it, which is
// far cheaper than a server each and keeps tests independent anyway.
var admin *pgxpool.Pool

// dsn renders a connection string for a database on the same server.
var dsn func(database string) string

func TestMain(m *testing.M) {
	// Failing loudly beats skipping. See the package doc: the whole reason
	// this module exists is to stop a gate from claiming coverage it lacks,
	// and "no database, so everything passed" is that same failure wearing a
	// different hat.
	code, err := run(m)
	if err != nil {
		log.Fatalf("pgtest: %v", err)
	}
	os.Exit(code)
}

func run(m *testing.M) (int, error) {
	ctx := context.Background()

	var err error
	if dsn, err = dsnRenderer(pgEnv); err != nil {
		return 0, err
	}

	admin, err = pgxpool.New(ctx, dsn("postgres"))
	if err != nil {
		return 0, fmt.Errorf("opening the admin connection: %w", err)
	}
	defer admin.Close()
	if err := admin.Ping(ctx); err != nil {
		return 0, fmt.Errorf("%s is set but nothing answered: %w", pgEnv, err)
	}

	return m.Run(), nil
}

// dsnRenderer reads a base DSN from the environment and returns a function that
// points it at any database on the same server.
//
// A URL rather than the keyword form, because swapping the database means
// replacing one path segment and leaving every other parameter — sslmode, an
// application_name, a channel binding requirement — exactly as the caller wrote
// it. Rebuilding a DSN out of parsed components would silently drop whatever
// this file had not thought of.
func dsnRenderer(env string) (func(string) string, error) {
	base := os.Getenv(env)
	if base == "" {
		return nil, fmt.Errorf(
			"%s is not set.\n"+
				"These tests need a Postgres; they no longer start one.\n"+
				"  locally: mise run pg-up   (then mise run test-pg)\n"+
				"  CI:      the service containers in .github/workflows/ci.yml",
			env)
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("%s is not a valid URL: %w", env, err)
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return nil, fmt.Errorf("%s must be a postgres:// URL, got scheme %q", env, u.Scheme)
	}
	return func(database string) string {
		v := *u
		v.Path = "/" + database
		return v.String()
	}, nil
}

// poolSize caps what one test's pool may open. pgxpool's default is the core
// count, which is the wrong basis here: with the suite parallel, the total is
// that number squared, and on a large machine it walks into max_connections
// while every one of those connections sits idle.
//
// Eight rather than four, which would also fit every test today: census's queue
// test runs four workers competing for the tail of the same queue, and a pool
// sized exactly to the workers deadlocks the moment anything else in that test
// wants a connection at the same time.
//
// It pairs with the -parallel cap in mise.toml's test-pg. Eight connections by
// eight concurrent tests is 64, which fits inside a stock server's 100 without
// the harness having to configure the server at all — and it cannot configure
// it any more, now that provisioning lives outside this module.
const poolSize = 8

// freshDB creates an empty database with the uuid_generate_v7() shim installed,
// and returns a connection to it, dropped when the test ends. A database per
// test rather than a server per test: CREATE DATABASE is milliseconds, and the
// server is already running before `go test` starts.
//
// It is also what lets the suite run in parallel. Tests here share a Postgres
// but not a database, so they were already independent by construction — the
// serial run was leaving that on the table. Adding t.Parallel() to the tests
// that could take it cut the module from 49s to about 12s, and the per-test
// database is the whole reason that was safe rather than a rewrite.
//
// Two tests do not take it, and the compiler will not tell you why: t.Chdir and
// t.Setenv panic under t.Parallel, because they mutate process-wide state. Those
// are in drift_test.go and sqlbmigrate_test.go and they stay serial.
func freshDB(t testing.TB) *pgxpool.Pool {
	t.Helper()
	db := freshStockDB(t)
	bootstrap(t, db)
	return db
}

// freshStockDB is the same thing without the shim: a Postgres exactly as it
// ships. It is what proves migrate.MinPostgres(18) produces DDL that needs no
// extension, which is a claim the shimmed database cannot test — with
// uuid_generate_v7() defined, both spellings work and the difference is
// invisible.
func freshStockDB(t testing.TB) *pgxpool.Pool {
	t.Helper()

	name := databaseName(t)
	// Dropped first so that a crashed run leaves nothing that makes the next
	// one fail with a confusing "already exists" instead of its real problem.
	mustExec(t, admin, `DROP DATABASE IF EXISTS `+quoteIdent(name))
	mustExec(t, admin, `CREATE DATABASE `+quoteIdent(name))

	cfg, err := pgxpool.ParseConfig(dsn(name))
	if err != nil {
		t.Fatalf("parsing the connection string for %s: %v", name, err)
	}
	cfg.MaxConns = poolSize
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("opening %s: %v", name, err)
	}
	t.Cleanup(func() {
		pool.Close()
		// A dropped database cannot have open connections, and a pool holds
		// them, so the close above is not always enough on its own.
		_, _ = admin.Exec(context.Background(),
			`DROP DATABASE IF EXISTS `+quoteIdent(name)+` WITH (FORCE)`)
	})

	return pool
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
func bootstrap(t testing.TB, db *pgxpool.Pool) {
	t.Helper()
	mustExec(t, db, `
		CREATE FUNCTION uuid_generate_v7() RETURNS uuid
		LANGUAGE sql VOLATILE AS 'SELECT uuidv7()'
	`)
	// btree_gist, because an exclusion that pairs a scalar `=` with a range
	// `&&` needs gist to have an operator class for the scalar — which is the
	// shape every real double-booking constraint has. It ships with Postgres's
	// contrib, so no image change; it does have to be created, which is exactly
	// the step Diff renders nothing for and the extension report exists to name
	// (issues #121, #115).
	mustExec(t, db, `CREATE EXTENSION IF NOT EXISTS btree_gist`)
}

// databaseName derives a legal, unique database name from the test name.
func databaseName(t testing.TB) string {
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

func mustExec(t testing.TB, db *pgxpool.Pool, query string) {
	t.Helper()
	if _, err := db.Exec(context.Background(), query); err != nil {
		t.Fatalf("exec failed: %v\n%s", err, strings.TrimSpace(query))
	}
}
