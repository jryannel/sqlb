package sqlb_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jryannel/sqlb"
)

// The examples below run real terminal methods, because a hook only fires when
// a statement executes — asserting on a builder would prove nothing. This is the
// smallest thing that can stand behind Executor: a driver that records every
// statement and replays one canned row. No Postgres, and no pretending.
var exampleLog []string

const exampleDriverName = "sqlb-example"

func init() { sql.Register(exampleDriverName, exampleDriver{}) }

// exampleDB opens a handle over the recording driver and clears the log.
func exampleDB() *sqlb.DB {
	exampleLog = nil
	db, err := sql.Open(exampleDriverName, "")
	if err != nil {
		panic(err)
	}
	return sqlb.New(db)
}

// whereClause returns just the WHERE clause of the last statement, so an example
// can show what a hook contributed without printing the whole projection.
func whereClause() string {
	if len(exampleLog) == 0 {
		return "(no statement ran)"
	}
	last := exampleLog[len(exampleLog)-1]
	_, where, ok := strings.Cut(last, " WHERE ")
	if !ok {
		return "(no WHERE clause)"
	}
	return where
}

// BeforeQuery is the load-bearing hook: it receives the query itself, so one
// registration constrains every read of the model — including the reads the
// generated REST handlers issue. Multi-tenancy and soft deletes stop being
// something each call site has to remember.
func ExampleHooks_BeforeQuery() {
	hooks := sqlb.On[Article]()
	defer hooks.Reset()

	hooks.BeforeQuery(func(_ context.Context, q *sqlb.Builder[Article]) error {
		// In a real application the tenant comes from the request context.
		q.Where(sqlb.F("org_id").Eq("acme"))
		return nil
	})

	db := exampleDB()
	ctx := context.Background()

	// The caller filters on status and knows nothing about tenants.
	if _, err := sqlb.Query[Article]().Where(sqlb.F("status").Eq("published")).All(ctx, db); err != nil {
		panic(err)
	}
	fmt.Println("list: ", whereClause())

	// A different read, through a different entry point, is scoped too.
	if _, err := sqlb.Query[Article]().Count(ctx, db); err != nil {
		panic(err)
	}
	fmt.Println("count:", whereClause())

	// Output:
	// list:  ("status" = $1) AND ("org_id" = $2)
	// count: "org_id" = $1
}

// A hook returning an error aborts the operation, and the error reaches the
// caller unwrapped. This is how "no tenant in this context" becomes impossible
// to forget rather than merely documented.
func ExampleHooks_BeforeQuery_reject() {
	hooks := sqlb.On[Article]()
	defer hooks.Reset()

	errNoTenant := errors.New("no tenant in context")
	hooks.BeforeQuery(func(ctx context.Context, q *sqlb.Builder[Article]) error {
		org, ok := ctx.Value(orgKey{}).(string)
		if !ok {
			return errNoTenant
		}
		q.Where(sqlb.F("org_id").Eq(org))
		return nil
	})

	db := exampleDB()

	_, err := sqlb.Query[Article]().All(context.Background(), db)
	fmt.Println("unscoped:", err)
	fmt.Println("statements run:", len(exampleLog))

	ctx := context.WithValue(context.Background(), orgKey{}, "acme")
	if _, err := sqlb.Query[Article]().All(ctx, db); err != nil {
		panic(err)
	}
	fmt.Println("scoped:  ", whereClause())

	// Output:
	// unscoped: no tenant in context
	// statements run: 0
	// scoped:   "org_id" = $1
}

type orgKey struct{}

// AfterCommit runs work that must not happen if the write does not. AfterCreate
// and its siblings run inside the transaction, which is right for validation and
// wrong for anything the outside world can observe: the transaction may still
// abort after the hook has announced a write that then never happened.
func ExampleAfterCommit() {
	hooks := sqlb.On[Article]()
	defer hooks.Reset()

	hooks.AfterCreate(func(ctx context.Context, a *Article) error {
		// Runs inside the transaction. Returning an error here rolls the insert
		// back, so the event is registered rather than published.
		id := a.ID
		return sqlb.AfterCommit(ctx, func(context.Context) error {
			fmt.Println("published event for", id)
			return nil
		})
	})

	db := exampleDB()
	err := db.WithTx(context.Background(), func(ctx context.Context, tx *sqlb.DB) error {
		a := Article{Title: "Hello", Status: "draft", OrgID: "acme"}
		_, err := sqlb.InsertRows(&a).One(ctx, tx)
		fmt.Println("insert returned, still inside the transaction")
		return err
	})
	if err != nil {
		panic(err)
	}

	// Output:
	// insert returned, still inside the transaction
	// published event for a1
}

// A rollback discards the callbacks by never reaching them, which is the whole
// point: no event is published for a write that did not land.
func ExampleAfterCommit_rollback() {
	hooks := sqlb.On[Article]()
	defer hooks.Reset()

	hooks.AfterCreate(func(ctx context.Context, a *Article) error {
		return sqlb.AfterCommit(ctx, func(context.Context) error {
			fmt.Println("this must not print")
			return nil
		})
	})

	db := exampleDB()
	errPaymentDeclined := errors.New("payment declined")
	err := db.WithTx(context.Background(), func(ctx context.Context, tx *sqlb.DB) error {
		a := Article{Title: "Hello", Status: "draft", OrgID: "acme"}
		if _, err := sqlb.InsertRows(&a).One(ctx, tx); err != nil {
			return err
		}
		return errPaymentDeclined // something later in the unit of work fails
	})

	fmt.Println("WithTx:", err)
	fmt.Println("last statement:", exampleLog[len(exampleLog)-1])

	// Output:
	// WithTx: payment declined
	// last statement: ROLLBACK
}

// --- the recording driver ---------------------------------------------------

type exampleDriver struct{}

func (exampleDriver) Open(string) (driver.Conn, error) { return exampleConn{}, nil }

type exampleConn struct{}

func (exampleConn) Close() error { return nil }

func (exampleConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("example driver: prepared statements are not used")
}

func (exampleConn) Begin() (driver.Tx, error) {
	return nil, errors.New("example driver: use BeginTx")
}

func (c exampleConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	exampleLog = append(exampleLog, "BEGIN")
	return exampleTx{}, nil
}

func (exampleConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	exampleLog = append(exampleLog, query)
	// The result has to match the projection, or database/sql rejects the Scan
	// before sqlb ever sees it. A count is one column; everything else here
	// selects the whole row.
	if strings.HasPrefix(query, "SELECT count(") {
		return &exampleRows{cols: []string{"count"}, vals: []driver.Value{int64(1)}}, nil
	}
	return &exampleRows{
		cols: []string{"id", "title", "status", "view_count", "org_id"},
		vals: []driver.Value{"a1", "Hello", "draft", int64(0), "acme"},
	}, nil
}

func (exampleConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	exampleLog = append(exampleLog, query)
	return driver.RowsAffected(1), nil
}

type exampleTx struct{}

func (exampleTx) Commit() error {
	exampleLog = append(exampleLog, "COMMIT")
	return nil
}

func (exampleTx) Rollback() error {
	exampleLog = append(exampleLog, "ROLLBACK")
	return nil
}

// exampleRows replays a single canned row, which is enough for a RETURNING
// clause to scan and for a count to read.
type exampleRows struct {
	cols []string
	vals []driver.Value
	done bool
}

func (r *exampleRows) Columns() []string { return r.cols }

func (*exampleRows) Close() error { return nil }

func (r *exampleRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	copy(dest, r.vals)
	return nil
}
