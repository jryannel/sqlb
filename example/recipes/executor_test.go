package recipes_test

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jryannel/sqlb"
	"github.com/jryannel/sqlb/example/recipes"
)

// Executor is two methods, which is why sqlb ships no tracing API: the seam
// already exists. A wrapper reaches OpenTelemetry, slog or a test double
// without sqlb taking a dependency on any of them — and the wrapper sees the
// compiled SQL, which is the thing a filter produced and the thing an EXPLAIN
// would be run against.
type tracer struct {
	inner sqlb.Executor
	log   func(op, query string, args []any, took time.Duration, err error)
}

func (t tracer) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	start := time.Now()
	rows, err := t.inner.QueryContext(ctx, q, args...)
	t.log("query", q, args, time.Since(start), err)
	return rows, err
}

func (t tracer) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	start := time.Now()
	res, err := t.inner.ExecContext(ctx, q, args...)
	t.log("exec", q, args, time.Since(start), err)
	return res, err
}

// Every statement passes through the wrapper, whoever built it — a hand-written
// query, a REST filter, or a generated handler.
func Example_executorWrappedForTracing() {
	traced := tracer{
		inner: recordingDB(),
		log: func(op, q string, args []any, _ time.Duration, err error) {
			fmt.Printf("%s args=%d err=%v %s\n", op, len(args), err, firstWords(q, 3))
		},
	}

	ctx := context.Background()
	if _, err := sqlb.Query[recipes.Post]().Where(sqlb.F("org_id").Eq("acme")).All(ctx, traced); err != nil {
		panic(err)
	}
	if _, err := sqlb.DeleteRows[recipes.Post]().Where(sqlb.F("id").Eq("p1")).Exec(ctx, traced); err != nil {
		panic(err)
	}
	// Output:
	// query args=1 err=<nil> SELECT "posts"."id", "posts"."org_id",
	// exec args=1 err=<nil> DELETE FROM "posts"
}

// Every terminal method takes an Executor, so *sql.DB, *sql.Tx, a pgx pool
// behind an adapter and a wrapper like the one above are all the same argument.
//
// sqlb.New wraps one in a handle, which is what WithTx and the hook registry
// hang off. It is additive: passing a *sqlb.DB where a *sql.DB used to go
// changes nothing else, because the handle is itself an Executor.
func Example_executorHandleIsAdditive() {
	var exec sqlb.Executor = recordingDB()
	db := sqlb.New(exec)

	fmt.Println("handle is an Executor:", func() bool { var _ sqlb.Executor = db; return true }())
	fmt.Println("can begin a transaction:", db.CanBeginTx())
	fmt.Println("inside one:", db.InTx())
	// Output:
	// handle is an Executor: true
	// can begin a transaction: false
	// inside one: false
}

// CanBeginTx exists so a caller who *requires* transactions can say so at
// startup rather than on the first write. rest.Resource uses it for exactly
// that: a resource wrapping its generated writes refuses to mount over an
// executor that cannot begin one, because discovering it at request time means
// the first POST is the error report.
func Example_executorTransactionCapability() {
	pool := recordingDB() // a handle over *sql.DB — can begin
	fmt.Println("pool:", pool.CanBeginTx())

	err := pool.WithTx(context.Background(), func(_ context.Context, tx *sqlb.DB) error {
		// Inside a transaction it still reports true, where WithTx joins
		// rather than begins.
		fmt.Println("tx:  ", tx.CanBeginTx(), "in a transaction:", tx.InTx())
		return nil
	})
	if err != nil {
		panic(err)
	}
	// Output:
	// pool: true
	// tx:   true in a transaction: true
}
