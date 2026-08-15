package rooms_test

// The bootstrap is copied from example/fxapp/main_test.go rather than
// abstracted into a shared package: every one of these lean examples is a
// standalone module by design (its own go.mod, its own gate), so the
// alternative is a fifth module whose only purpose is a helper the other four
// would import — more machinery than the fifteen lines it would save.

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
			"%s is not set.\nThese tests need a Postgres; they do not start one.\n"+
				"  locally: mise run pg-up   (then SQLB_TEST_POSTGRES=... go test ./...)\n"+
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

// freshDatabase returns the DSN of an empty database, migrated by the caller.
func freshDatabase(t *testing.T) string {
	t.Helper()

	once.Do(startPostgres)
	if startErr != nil {
		t.Fatalf("rooms: %v", startErr)
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
