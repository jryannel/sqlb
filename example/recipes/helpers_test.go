package recipes_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jryannel/sqlb"
	"github.com/jryannel/sqlb/filter"
)

// The support code the recipes share. Nothing here is part of sqlb's API;
// it exists so that each recipe file can be about one thing.
//
// Most recipes never reach this file. A query is a value, so compiling it with
// SQL() shows what would run without running it — which is both the honest way
// to demonstrate a query builder and the reason these examples need no
// database. The few recipes that must *execute* something — hooks fire on
// execution, and a transaction is not a statement — run against the recording
// driver below.

// compiled is what Builder, Insert, Update and Delete all satisfy: something
// that renders SQL text and bind parameters without running them.
type compiled interface {
	SQL() (string, []any, error)
}

// show prints a whole statement and its bind parameters. Use it when the shape
// of the statement is the point — a projection, a join, a LIMIT.
func show(c compiled) {
	sql, args, err := c.SQL()
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println(sql)
	if len(args) > 0 {
		fmt.Println("args:", formatArgs(args))
	}
}

// showWhere prints everything from WHERE onwards. Use it when the predicate is
// the point, which for a model with a dozen columns is most of the time: the
// default projection is thirty words of noise in front of the six that matter.
func showWhere(c compiled) {
	sql, args, err := c.SQL()
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	_, where, ok := strings.Cut(sql, " WHERE ")
	if !ok {
		fmt.Println("(no WHERE clause)")
		return
	}
	fmt.Println("WHERE", where)
	if len(args) > 0 {
		fmt.Println("args:", formatArgs(args))
	}
}

// formatArgs renders bind parameters the way the driver will receive them
// rather than the way Go prints them. A value that knows how to encode itself
// — an array parameter, which is a driver.Valuer — is asked to, and a byte
// slice is shown as text, so a jsonb document reads as a document instead of as
// a list of numbers.
func formatArgs(args []any) string {
	parts := make([]string, len(args))
	for i, arg := range args {
		v := arg
		if valuer, ok := v.(driver.Valuer); ok {
			encoded, err := valuer.Value()
			if err != nil {
				parts[i] = fmt.Sprintf("!%v", err)
				continue
			}
			v = encoded
		}
		switch b := v.(type) {
		case json.RawMessage:
			v = string(b)
		case []byte:
			v = string(b)
		}
		parts[i] = fmt.Sprint(v)
	}
	return "[" + strings.Join(parts, " ") + "]"
}

// showError prints an error, or says there was none. Several recipes are about
// a refusal, and the message is the recipe.
func showError(err error) {
	if err == nil {
		fmt.Println("(no error)")
		return
	}
	fmt.Println(err)
}

// showFilterErrors prints every rejected parameter rather than only the first.
// Reporting them all is the package's own promise, and a recipe that printed
// one would hide it.
func showFilterErrors(err error) {
	errs, ok := filter.AsErrors(err)
	if !ok {
		showError(err)
		return
	}
	for _, e := range errs {
		fmt.Println("filter:", e)
	}
}

func showConst(name, value string) { fmt.Printf("%s = %s\n", name, value) }

// showContains reports whether compiled SQL mentions something, for the recipes
// whose claim is that a column is *absent* from a statement.
func showContains(sql, want string) {
	fmt.Printf("mentions %s: %v\n", want, strings.Contains(sql, want))
}

func showExpanded(names []string) { fmt.Println("expanded:", names) }

// showDecodedCursor prints what a cursor decodes to. It is base64 over JSON and
// nothing else, which is the point being made where this is called.
func showDecodedCursor(c sqlb.Cursor) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(string(c), "="))
	if err != nil {
		panic(err)
	}
	fmt.Println(string(raw))
}

// firstWords shortens a recorded statement to its opening words, for the
// recipes whose claim is about the *order* statements ran in rather than their
// contents.
func firstWords(s string, n int) string {
	words := strings.Fields(s)
	if len(words) > n {
		words = words[:n]
	}
	return strings.Join(words, " ")
}

func count(ss []string, want string) int {
	n := 0
	for _, s := range ss {
		if s == want {
			n++
		}
	}
	return n
}

// postColumns is the projection Query[Post] produces by default, in declaration
// order. The recording driver replays a row this wide.
var postColumns = []string{
	"id", "org_id", "author_id", "title", "body", "status",
	"view_count", "tags", "metadata", "published_at", "deleted_at", "created_at",
}

// postRow is one canned row matching postColumns. The values are driver types —
// what a database/sql driver is allowed to hand back — so the scanner sees
// exactly what Postgres would give it, including the array and the document as
// their wire encodings.
func postRow() []driver.Value {
	return []driver.Value{
		"p1", "acme", "a1", "Hello", "Body text", "published",
		int64(12), []byte(`{go,sql}`), []byte(`{"lang":"en"}`),
		time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC), nil,
		time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC),
	}
}

// recordingDB opens a handle over the recording driver and clears the log. The
// canned result is one Post; pass different columns for a query that projects
// something else.
func recordingDB() *sqlb.DB {
	return recordingDBWith(postColumns, postRow())
}

// failingDB is a handle whose every statement fails with err, for the recipes
// about what an application does with the failure.
func failingDB(err error) *sqlb.DB {
	db := recordingDBWith(postColumns, postRow())
	replay.err = err
	return db
}

func recordingDBWith(cols []string, rows ...[]driver.Value) *sqlb.DB {
	log = nil
	replay.cols = cols
	replay.rows = rows
	replay.err = nil
	db, err := sql.Open(driverName, "")
	if err != nil {
		panic(err)
	}
	return sqlb.New(db)
}

// statements returns every statement the driver saw, including BEGIN and
// COMMIT. A recipe about transactions is a recipe about that sequence.
func statements() []string { return log }

// lastWhere returns the predicate of the statement that ran most recently, so a
// recipe can show what a hook contributed without printing the projection or
// the RETURNING clause the hook did not touch.
func lastWhere() string {
	if len(log) == 0 {
		return "(no statement ran)"
	}
	_, where, ok := strings.Cut(log[len(log)-1], " WHERE ")
	if !ok {
		return "(no WHERE clause)"
	}
	predicate, _, _ := strings.Cut(where, " RETURNING ")
	return predicate
}

// The recording driver. It is the smallest thing that can stand behind
// sqlb.Executor: it records every statement and replays canned rows. No
// Postgres, and no pretending to be one — a recipe that needs a real planner
// says so and lives in pgtest instead.

const driverName = "sqlb-recipes"

var (
	log    []string
	replay struct {
		cols []string
		rows [][]driver.Value
		// err, when set, is what the driver returns instead of a result. It is
		// how a recipe about a refused write gets one to be about.
		err error
	}
)

func init() { sql.Register(driverName, recordingDriver{}) }

type recordingDriver struct{}

func (recordingDriver) Open(string) (driver.Conn, error) { return recordingConn{}, nil }

type recordingConn struct{}

func (recordingConn) Close() error { return nil }

func (recordingConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("recording driver: prepared statements are not used")
}

func (recordingConn) Begin() (driver.Tx, error) {
	return nil, errors.New("recording driver: use BeginTx")
}

func (recordingConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	log = append(log, "BEGIN")
	return recordingTx{}, nil
}

func (recordingConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	log = append(log, query)
	if replay.err != nil {
		return nil, replay.err
	}
	// The result has to match the projection or database/sql rejects the scan
	// before sqlb sees it. A count is one column whatever the model is.
	if strings.HasPrefix(query, "SELECT count(") {
		return &recordedRows{cols: []string{"count"}, rows: [][]driver.Value{{int64(len(replay.rows))}}}, nil
	}
	return &recordedRows{cols: replay.cols, rows: replay.rows}, nil
}

func (recordingConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	log = append(log, query)
	if replay.err != nil {
		return nil, replay.err
	}
	return driver.RowsAffected(1), nil
}

type recordingTx struct{}

func (recordingTx) Commit() error {
	log = append(log, "COMMIT")
	return nil
}

func (recordingTx) Rollback() error {
	log = append(log, "ROLLBACK")
	return nil
}

type recordedRows struct {
	cols []string
	rows [][]driver.Value
	n    int
}

func (r *recordedRows) Columns() []string { return r.cols }

func (*recordedRows) Close() error { return nil }

func (r *recordedRows) Next(dest []driver.Value) error {
	if r.n >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.n])
	r.n++
	return nil
}

func showArgCount(args []any) {
	if len(args) == 1 {
		fmt.Println("1 bind parameter")
		return
	}
	fmt.Printf("%d bind parameters\n", len(args))
}
