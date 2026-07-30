package pgtest

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// These tests measure the claims ADR-0019 makes from documentation rather than
// observation. Its Context section says so outright, and holds the record at
// Low confidence for it. Each test here turns one of those citations into a
// measurement.
//
// The topology is the deployed one: PgBouncer in transaction pooling in front
// of Postgres, with a direct connection alongside for the components ADR-0019
// carves out.

const pgbouncerImage = "edoburu/pgbouncer:v1.24.1-p1"

// pooledDSNFor renders a connection string to a database through the shared
// pooler, which is configured with a wildcard [databases] entry so that any
// database name is forwarded.
var pooledDSNFor func(database string) string

// startPooler brings up one PgBouncer for the whole run.
//
// One rather than one per test, because a container per test spent most of the
// suite's wall clock on startup and made the Docker API the least reliable thing
// in it — the first version of this file failed that way. Tests still get a
// database each; only the pooler is shared.
func startPooler(ctx context.Context, pg *postgres.PostgresContainer) (func(), error) {
	// PgBouncer reaches Postgres inside the Docker network, so it needs the
	// container address rather than the host-mapped port the tests use.
	pgHost, err := pg.ContainerIP(ctx)
	if err != nil {
		return nil, fmt.Errorf("postgres container IP: %w", err)
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        pgbouncerImage,
			ExposedPorts: []string{"5432/tcp"},
			Env: map[string]string{
				"DB_HOST":     pgHost,
				"DB_PORT":     "5432",
				"DB_USER":     "sqlb",
				"DB_PASSWORD": "sqlb",
				// A wildcard entry, so one pooler serves the database each test
				// creates for itself.
				"DB_NAME":   "*",
				"POOL_MODE": "transaction",
				"AUTH_TYPE": "scram-sha-256",
				// So a test can ask the pooler about its own configuration
				// rather than a comment asserting what it probably is.
				"ADMIN_USERS": "sqlb",
				// A small pool, so that consecutive statements from one client
				// really are multiplexed over a shared server connection rather
				// than each quietly getting a backend of its own — which would
				// mask the very behaviour these tests exist to observe.
				"MAX_DB_CONNECTIONS": "2",
				"DEFAULT_POOL_SIZE":  "2",
			},
			WaitingFor: wait.ForListeningPort("5432/tcp").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		return nil, err
	}

	host, err := container.Host(ctx)
	if err != nil {
		return nil, fmt.Errorf("pgbouncer host: %w", err)
	}
	port, err := container.MappedPort(ctx, "5432/tcp")
	if err != nil {
		return nil, fmt.Errorf("pgbouncer port: %w", err)
	}

	pooledDSNFor = func(database string) string {
		return fmt.Sprintf("postgres://sqlb:sqlb@%s:%s/%s?sslmode=disable",
			host, port.Port(), database)
	}

	return func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			log.Printf("pgtest: terminating pgbouncer: %v", err)
		}
	}, nil
}

// pooler creates a database and returns DSNs to it through the pooler and
// around it.
func pooler(t *testing.T) (pooledDSN, directDSN string) {
	t.Helper()

	name := databaseName(t)
	mustExec(t, admin, `CREATE DATABASE `+quoteIdent(name))
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(),
			`DROP DATABASE IF EXISTS `+quoteIdent(name)+` WITH (FORCE)`)
	})

	return pooledDSNFor(name), dsn(name)
}

// TestTheQueryPathWorksThroughThePooler is the claim that matters most, because
// of how much it covers: if pgx's default statement caching is incompatible
// with transaction pooling, that is not an edge case, it is every query sqlb
// runs.
//
// ADR-0019 records this as its open question. This is the answer.
func TestTheQueryPathWorksThroughThePooler(t *testing.T) {
	pooledDSN, directDSN := pooler(t)

	direct, err := pgxpool.New(context.Background(), directDSN)
	if err != nil {
		t.Fatalf("open direct: %v", err)
	}
	defer direct.Close()
	mustExec(t, direct, `CREATE TABLE items (id int primary key, name text not null)`)

	through, err := pgxpool.New(context.Background(), pooledDSN)
	if err != nil {
		t.Fatalf("open pooled: %v", err)
	}
	defer through.Close()

	// Repeat the same parameterised statement. One execution proves little: the
	// failure is a cached prepared statement reused on a server connection that
	// never saw it, which needs several round trips through the pool to appear.
	for i := range 25 {
		if _, err := through.Exec(context.Background(),
			`INSERT INTO items (id, name) VALUES ($1, $2)`, i, fmt.Sprintf("item-%d", i),
		); err != nil {
			t.Fatalf("insert %d through the pooler: %v\n\n"+
				"This is the prepared-statement incompatibility ADR-0019 names as its open question. "+
				"If it fires, the fix is a pgx exec mode or a PgBouncer new enough to track prepared "+
				"statements — and ADR-0019 needs revising to say which.", i, err)
		}
	}

	var count int
	if err := through.QueryRow(context.Background(),
		`SELECT count(*) FROM items WHERE id >= $1`, 0).Scan(&count); err != nil {
		t.Fatalf("select through the pooler: %v", err)
	}
	if count != 25 {
		t.Errorf("got %d rows through the pooler, want 25", count)
	}

	// Why it works, asked rather than assumed.
	//
	// pgx defaults to caching prepared statements, which transaction pooling
	// cannot support unless the pooler tracks them per client. PgBouncer gained
	// that in 1.21 behind max_prepared_statements, and it is non-zero by
	// default in the version pinned here.
	//
	// Recording the number matters because the test above passes for a reason
	// that is a deployment setting, not a property of pgx: on a pooler with
	// max_prepared_statements = 0, every assertion above fails. Anyone reading
	// "the query path works through PgBouncer" needs that condition attached.
	t.Logf("pooler tracks up to %d prepared statements per client — this is why the statements above survived",
		maxPreparedStatements(t, pooledDSN))
}

// maxPreparedStatements asks the pooler's admin console for its own setting.
func maxPreparedStatements(t *testing.T, pooledDSN string) int {
	t.Helper()
	ctx := context.Background()

	// The admin console lives in a virtual database called "pgbouncer", and
	// SHOW is not available over the extended protocol the driver defaults to.
	adminDSN := pooledDSN[:strings.LastIndex(pooledDSN, "/")] +
		"/pgbouncer?sslmode=disable&default_query_exec_mode=simple_protocol"

	conn, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatalf("connecting to the pgbouncer admin console: %v", err)
	}
	defer conn.Close(ctx)

	rows, err := conn.Query(ctx, `SHOW CONFIG`)
	if err != nil {
		t.Fatalf("SHOW CONFIG: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			t.Fatalf("reading config row: %v", err)
		}
		if len(values) < 2 {
			continue
		}
		if key, _ := values[0].(string); key != "max_prepared_statements" {
			continue
		}
		n, err := strconv.Atoi(fmt.Sprint(values[1]))
		if err != nil {
			t.Fatalf("max_prepared_statements = %v, which is not a number: %v", values[1], err)
		}
		if n == 0 {
			t.Error("the pooler tracks no prepared statements, yet the statements above succeeded — " +
				"something other than what this test believes is explaining the result, so the " +
				"explanation in ADR-0019 would be wrong")
		}
		return n
	}
	t.Fatal("the pooler reported no max_prepared_statements setting at all")
	return 0
}

// TestListenNeedsADirectConnection measures the asymmetry both ADR-0019 and
// ADR-0012 are built on: NOTIFY survives the pooler and LISTEN does not.
//
// It asserts both halves against the same database in the same test, because
// the direct half is what makes the pooled half meaningful — without it, a
// timeout could just as easily mean the notification was never sent.
func TestListenNeedsADirectConnection(t *testing.T) {
	pooledDSN, directDSN := pooler(t)
	ctx := context.Background()

	notify := func(t *testing.T) {
		t.Helper()
		db, err := pgxpool.New(context.Background(), directDSN)
		if err != nil {
			t.Fatalf("open notifier: %v", err)
		}
		defer db.Close()
		// Inside a transaction, because that is how ADR-0012's outbox trigger
		// fires it.
		mustExec(t, db, `BEGIN; SELECT pg_notify('outbox', 'ping'); COMMIT`)
	}

	// The control: a direct LISTEN receives it. If this half fails, the pooled
	// half below proves nothing at all.
	t.Run("direct", func(t *testing.T) {
		conn, err := pgx.Connect(ctx, directDSN)
		if err != nil {
			t.Fatalf("connect direct: %v", err)
		}
		defer conn.Close(ctx)

		if _, err := conn.Exec(ctx, `LISTEN outbox`); err != nil {
			t.Fatalf("LISTEN on a direct connection: %v", err)
		}
		notify(t)

		wait, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if _, err := conn.WaitForNotification(wait); err != nil {
			t.Fatalf("a direct connection did not receive its notification: %v\n"+
				"ADR-0012's dispatcher is built on this working — if it does not, the doorbell "+
				"is unbuildable as described and the fallback poll is the whole mechanism.", err)
		}
	})

	// The claim: the same sequence through the pooler does not deliver.
	t.Run("pooled", func(t *testing.T) {
		conn, err := pgx.Connect(ctx, pooledDSN)
		if err != nil {
			t.Fatalf("connect through the pooler: %v", err)
		}
		defer conn.Close(ctx)

		if _, err := conn.Exec(ctx, `LISTEN outbox`); err != nil {
			// An outright rejection is a clearer answer than accepting the
			// LISTEN and dropping the notification, and it still confirms the
			// carve-out.
			t.Logf("the pooler rejected LISTEN outright: %v", err)
			return
		}
		notify(t)

		wait, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if _, err := conn.WaitForNotification(wait); err == nil {
			t.Fatal("a LISTEN through the pooler DID receive its notification.\n" +
				"This is good news and it means ADR-0019 is wrong: the dispatcher needs no direct " +
				"connection, and both ADR-0019 and ADR-0012 should shrink accordingly. " +
				"Do not delete this test to make it pass — revise the records.")
		} else if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("waiting through the pooler failed for an unexpected reason: %v", err)
		}
	})
}

// TestNotifyWorksThroughThePooler is the other half of the asymmetry, and the
// half that keeps the blast radius to one connection. If NOTIFY needed a direct
// connection too, every mutation would need one and ADR-0012's outbox trigger
// could not fire from a pooled writer at all.
func TestNotifyWorksThroughThePooler(t *testing.T) {
	pooledDSN, directDSN := pooler(t)
	ctx := context.Background()

	// Listen directly — the arrangement ADR-0019 actually prescribes.
	listener, err := pgx.Connect(ctx, directDSN)
	if err != nil {
		t.Fatalf("connect direct: %v", err)
	}
	defer listener.Close(ctx)
	if _, err := listener.Exec(ctx, `LISTEN outbox`); err != nil {
		t.Fatalf("LISTEN: %v", err)
	}

	// Notify through the pooler, inside a transaction, as a mutation would.
	through, err := pgxpool.New(ctx, pooledDSN)
	if err != nil {
		t.Fatalf("open pooled: %v", err)
	}
	defer through.Close()

	tx, err := through.Begin(ctx)
	if err != nil {
		t.Fatalf("begin through the pooler: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_notify('outbox', 'ping')`); err != nil {
		t.Fatalf("pg_notify through the pooler: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit through the pooler: %v", err)
	}

	wait, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	n, err := listener.WaitForNotification(wait)
	if err != nil {
		t.Fatalf("a NOTIFY issued through the pooler never arrived: %v\n"+
			"ADR-0019 claims only LISTEN needs a direct connection. If NOTIFY needs one too, "+
			"the carve-out is not one connection but every writer, and the record is wrong.", err)
	}
	if n.Payload != "ping" {
		t.Errorf("payload = %q, want %q", n.Payload, "ping")
	}
}
