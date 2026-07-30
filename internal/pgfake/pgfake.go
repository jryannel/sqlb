// Package pgfake provides the pgx shapes sqlb's own tests run against, so the
// engine's suite stays database-free.
//
// It exists because ADR-0040 made pgx the contract: an executor is now
// pgx-shaped, and a canned result set has to be a pgx.Rows rather than a
// database/sql driver anyone can register. Two of those interfaces are wide —
// Rows has nine methods and Tx eleven — and writing them out in three test
// packages would be three chances to drift. So the boilerplate lives here and
// each package keeps its own policy: what a statement answers, and what gets
// recorded.
//
// This is a test double and nothing here should reach a real database. The
// methods sqlb does not use panic rather than returning a zero value, because a
// test that silently reads nothing from CopyFrom is worse than one that stops.
package pgfake

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Rows is a canned result set: column names, row values, and optionally a
// failure that Err reports once iteration ends.
type Rows struct {
	Cols []string
	Data [][]any
	// Fail is reported by Err, standing in for a statement that failed while
	// its result was being read rather than when it was sent. pgx does this on
	// the extended protocol, which is why every scanner here reads Err.
	Fail error

	pos    int
	closed bool
}

// Closed reports whether Close has been called, which is how a test asserts the
// result set was released.
func (r *Rows) Closed() bool { return r.closed }

func (r *Rows) FieldDescriptions() []pgconn.FieldDescription {
	fields := make([]pgconn.FieldDescription, len(r.Cols))
	for i, name := range r.Cols {
		fields[i] = pgconn.FieldDescription{Name: name}
	}
	return fields
}

func (r *Rows) Next() bool {
	if r.Fail != nil || r.pos >= len(r.Data) {
		return false
	}
	r.pos++
	return true
}

func (r *Rows) Err() error { return r.Fail }

func (r *Rows) Close() { r.closed = true }

// Scan follows pgx's convention closely enough for the scanners to be exercised
// honestly: a destination implementing sql.Scanner is offered the raw value,
// which is pgx's own last-resort plan and the path every pgtype takes, and
// everything else is assigned by reflection.
func (r *Rows) Scan(dest ...any) error {
	if r.pos == 0 || r.pos > len(r.Data) {
		return errors.New("pgfake: Scan called outside a row")
	}
	row := r.Data[r.pos-1]
	if len(dest) != len(row) {
		return fmt.Errorf("pgfake: %d destinations for %d columns", len(dest), len(row))
	}
	for i, d := range dest {
		if s, ok := d.(sql.Scanner); ok {
			if err := s.Scan(row[i]); err != nil {
				return err
			}
			continue
		}
		dv := reflect.ValueOf(d)
		if dv.Kind() != reflect.Pointer || dv.IsNil() {
			return fmt.Errorf("pgfake: destination %d is not a non-nil pointer", i)
		}
		elem := dv.Elem()
		if row[i] == nil {
			elem.Set(reflect.Zero(elem.Type()))
			continue
		}
		if err := assign(elem, reflect.ValueOf(row[i])); err != nil {
			return fmt.Errorf("pgfake: column %d: %w", i, err)
		}
	}
	return nil
}

// assign puts one canned value into one destination, the way a driver would.
func assign(dst, src reflect.Value) error {
	switch {
	// The discard target for an unmapped column is a *any.
	case dst.Kind() == reflect.Interface:
		dst.Set(src)
		return nil
	case src.Type().AssignableTo(dst.Type()):
		dst.Set(src)
		return nil
	// A nullable column is a pointer field, and a non-NULL value fills it by
	// allocating. pgx does the same; the canned row says int64 and the model
	// says *int32.
	case dst.Kind() == reflect.Pointer:
		held := reflect.New(dst.Type().Elem())
		if err := assign(held.Elem(), src); err != nil {
			return err
		}
		dst.Set(held)
		return nil
	// A canned int64 filling an int32 field, and a canned string filling a
	// named string type — an enum column is the ordinary case of the second.
	// pgx does both: it narrows against the column's type, and it finds the
	// underlying type of a named one. The kinds must still agree, so this
	// converts between spellings of a value and never between values.
	case src.Type().ConvertibleTo(dst.Type()) && sameShape(src.Kind(), dst.Kind()):
		dst.Set(src.Convert(dst.Type()))
		return nil
	default:
		return fmt.Errorf("cannot assign %s to %s", src.Type(), dst.Type())
	}
}

// sameShape reports whether a value of one kind may stand in for the other:
// the same kind, or two numbers.
func sameShape(src, dst reflect.Kind) bool {
	return src == dst || (isNumeric(src) && isNumeric(dst))
}

func isNumeric(k reflect.Kind) bool {
	switch k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	}
	return false
}

func (r *Rows) CommandTag() pgconn.CommandTag { return pgconn.NewCommandTag("SELECT") }

func (r *Rows) Values() ([]any, error) {
	if r.pos == 0 || r.pos > len(r.Data) {
		return nil, errors.New("pgfake: Values called outside a row")
	}
	return r.Data[r.pos-1], nil
}

// RawValues has no answer here: the values were never on a wire, so there are
// no unparsed bytes to hand back and inventing some would be a lie a test could
// come to depend on.
func (r *Rows) RawValues() [][]byte { panic("pgfake: RawValues is not available on canned rows") }

func (r *Rows) Conn() *pgx.Conn { return nil }

// Statements is what a Tx runs its statements through — whatever the test
// package uses as its executor. Named for what it accepts rather than for the
// connection it stands in for, because pgx.Tx already spends the name Conn on a
// method.
type Statements interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Tx adapts a Statements onto pgx.Tx. Statements go through unchanged; the
// boundary is reported through OnCommit and OnRollback, which is what lets a
// test assert that a unit of work was wrapped and how it ended.
//
// Commit and Rollback are each answered once. A second call returns
// pgx.ErrTxClosed, as a real transaction does — WithTx relies on that error to
// tell an already-finished transaction from a failed rollback.
type Tx struct {
	Statements
	OnCommit   func() error
	OnRollback func() error

	done bool
}

func (t *Tx) Commit(context.Context) error {
	if t.done {
		return pgx.ErrTxClosed
	}
	t.done = true
	if t.OnCommit != nil {
		return t.OnCommit()
	}
	return nil
}

func (t *Tx) Rollback(context.Context) error {
	if t.done {
		return pgx.ErrTxClosed
	}
	t.done = true
	if t.OnRollback != nil {
		return t.OnRollback()
	}
	return nil
}

func (t *Tx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	rows, err := t.Query(ctx, sql, args...)
	return errRow{rows: rows, err: err}
}

// Begin would be a savepoint. sqlb's nesting joins the outer transaction rather
// than nesting (ADR-0020), so nothing should reach this.
func (t *Tx) Begin(context.Context) (pgx.Tx, error) {
	panic("pgfake: nested transactions are not available")
}

func (t *Tx) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	panic("pgfake: CopyFrom is not available")
}

func (t *Tx) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults {
	panic("pgfake: SendBatch is not available")
}

func (t *Tx) LargeObjects() pgx.LargeObjects {
	panic("pgfake: large objects are not available")
}

func (t *Tx) Prepare(context.Context, string, string) (*pgconn.StatementDescription, error) {
	panic("pgfake: Prepare is not available")
}

func (t *Tx) Conn() *pgx.Conn { return nil }

// errRow carries a query failure to the single-row read that asked for it,
// which is how pgx reports one: QueryRow returns no error of its own and Scan
// reports it instead.
type errRow struct {
	rows pgx.Rows
	err  error
}

func (r errRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	defer r.rows.Close()
	if !r.rows.Next() {
		if err := r.rows.Err(); err != nil {
			return err
		}
		return pgx.ErrNoRows
	}
	return r.rows.Scan(dest...)
}

// The shapes this package exists to satisfy. If pgx widens either interface,
// the build fails here rather than in each test package that embeds one.
var (
	_ pgx.Rows = (*Rows)(nil)
	_ pgx.Tx   = (*Tx)(nil)
)
