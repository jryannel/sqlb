package sqlb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"reflect"
	"strings"
)

var (
	scannerType = reflect.TypeOf((*sql.Scanner)(nil)).Elem()
	valuerType  = reflect.TypeOf((*driver.Valuer)(nil)).Elem()
)

// Executor is the subset of *sql.DB and *sql.Tx that sqlb uses. Anything
// satisfying it works, including pgx through its stdlib adapter and any
// instrumenting wrapper.
type Executor interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// All runs the query and returns every matching row.
//
// The builder is cloned first, so query hooks amend a copy and running the same
// builder twice does not accumulate their predicates.
func (b *Builder[T]) All(ctx context.Context, db Executor) ([]T, error) {
	q := b.Clone()
	if err := hooksFor[T](db).runBeforeQuery(ctx, q); err != nil {
		return nil, err
	}
	query, args, err := q.SQL()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapQueryErr(err, query)
	}
	defer rows.Close()
	return scanAll[T](rows, b.model)
}

// One runs the query and returns the single matching row. It returns
// ErrNotFound if nothing matched, and an error if more than one row did, since
// a caller asking for one row is asserting that only one exists.
func (b *Builder[T]) One(ctx context.Context, db Executor) (T, error) {
	var zero T
	// Fetching two rows makes the ambiguity detectable without a second query.
	probe := b.Clone().Limit(2)
	rows, err := probe.All(ctx, db)
	if err != nil {
		return zero, err
	}
	switch len(rows) {
	case 0:
		return zero, ErrNotFound
	case 1:
		return rows[0], nil
	default:
		return zero, fmt.Errorf("sqlb: One matched more than one row in %s", b.model.Table)
	}
}

// First returns the first matching row, or ErrNotFound. Unlike One it accepts
// multiple matches, so it should be paired with OrderBy to be deterministic.
func (b *Builder[T]) First(ctx context.Context, db Executor) (T, error) {
	var zero T
	rows, err := b.Clone().Limit(1).All(ctx, db)
	if err != nil {
		return zero, err
	}
	if len(rows) == 0 {
		return zero, ErrNotFound
	}
	return rows[0], nil
}

// Count returns the number of matching rows, ignoring pagination. For a
// grouped query it counts groups.
func (b *Builder[T]) Count(ctx context.Context, db Executor) (int64, error) {
	q := b.Clone()
	if err := hooksFor[T](db).runBeforeQuery(ctx, q); err != nil {
		return 0, err
	}
	query, args, err := q.countSQL()
	if err != nil {
		return 0, err
	}
	var n int64
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, wrapQueryErr(err, query)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return 0, err
		}
		return 0, nil
	}
	if err := rows.Scan(&n); err != nil {
		return 0, err
	}
	return n, rows.Err()
}

// Exists reports whether the query matches at least one row.
func (b *Builder[T]) Exists(ctx context.Context, db Executor) (bool, error) {
	probe := b.Clone()
	probe.sel = []Selection{RawSel("1")}
	probe.orders = nil
	probe.Limit(1)

	if err := hooksFor[T](db).runBeforeQuery(ctx, probe); err != nil {
		return false, err
	}
	query, args, err := probe.SQL()
	if err != nil {
		return false, err
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return false, wrapQueryErr(err, query)
	}
	defer rows.Close()
	found := rows.Next()
	return found, rows.Err()
}

// Collect runs a query and scans its rows into R rather than the model type.
// It is how grouped and aggregated queries are read, where the result shape is
// not the table shape:
//
//	type Revenue struct {
//	    Status string  `db:"status"`
//	    Total  float64 `db:"revenue"`
//	}
//	rows, err := sqlb.Collect[Revenue](ctx, db,
//	    sqlb.Query[Order]().
//	        GroupBy(sqlb.F("status")).
//	        Select(sqlb.F("status"), sqlb.Sum(sqlb.F("total")).As("revenue")))
//
// Query hooks still run, so tenant scoping applies to aggregates too.
func Collect[R, T any](ctx context.Context, db Executor, b *Builder[T]) ([]R, error) {
	q := b.Clone()
	if err := hooksFor[T](db).runBeforeQuery(ctx, q); err != nil {
		return nil, err
	}
	query, args, err := q.SQL()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapQueryErr(err, query)
	}
	defer rows.Close()
	// Exact, unlike All: R was declared specifically to receive this
	// projection, so a field with no matching column is a mistake rather than
	// a deliberate partial select.
	return scan[R](rows, ModelOf[R](), scanExact)
}

// scanMode controls how strictly a result set must match its destination.
type scanMode int

const (
	// scanPartial allows model fields to go unfilled, which is what a
	// projection is: ?select=id,name legitimately leaves the rest zero.
	scanPartial scanMode = iota
	// scanExact requires every model field to be filled by some result
	// column. Used where the destination type was written to match the
	// projection, so an unfilled field means a mismatch rather than an
	// intention.
	scanExact
)

// scanAll maps a result set onto a slice of T, tolerating unfilled fields.
func scanAll[T any](rows *sql.Rows, m *Model) ([]T, error) {
	return scan[T](rows, m, scanPartial)
}

// scan maps a result set onto a slice of T. Result columns with no matching
// model field are read and discarded, so a query selecting extra expressions
// still scans.
func scan[T any](rows *sql.Rows, m *Model, mode scanMode) ([]T, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	targets := make([][]int, len(cols))
	matched := 0
	for i, name := range cols {
		if ci := m.Column(name); ci != nil {
			targets[i] = ci.Index
			matched++
		}
	}
	if matched == 0 {
		return nil, fmt.Errorf("sqlb: none of the result columns %v map to %s; check the db tags or the Select aliases", cols, m.Type)
	}

	// A field left unfilled would scan as its zero value, which is
	// indistinguishable from a real zero — a mistyped alias on a Sum would
	// silently report 0 revenue rather than failing. Name the offenders.
	if mode == scanExact && matched < len(m.Columns) {
		filled := make(map[string]bool, matched)
		for i, name := range cols {
			if targets[i] != nil {
				filled[name] = true
			}
		}
		var missing []string
		for _, col := range m.Columns {
			if !filled[col.Name] {
				missing = append(missing, fmt.Sprintf("%s (db:%q)", col.Field, col.Name))
			}
		}
		return nil, fmt.Errorf(
			"sqlb: %s has no result column for %s; the query returned %v — check the Select aliases match the db tags",
			m.Type, strings.Join(missing, ", "), cols)
	}

	var (
		out     []T
		dest    = make([]any, len(cols))
		discard = make([]any, len(cols))
	)
	for rows.Next() {
		var row T
		rv := reflect.ValueOf(&row).Elem()
		for i := range cols {
			if targets[i] == nil {
				if discard[i] == nil {
					discard[i] = new(any)
				}
				dest[i] = discard[i]
				continue
			}
			field, err := fieldByIndex(rv, targets[i])
			if err != nil {
				return nil, err
			}
			dest[i] = field.Addr().Interface()
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("sqlb: scanning %s: %w", m.Type, err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// fieldByIndex walks an index path, allocating nil embedded pointers on the
// way so that mixins reached through a pointer are scannable.
func fieldByIndex(v reflect.Value, index []int) (reflect.Value, error) {
	for i, x := range index {
		if i > 0 {
			if v.Kind() == reflect.Pointer {
				if v.IsNil() {
					if !v.CanSet() {
						return reflect.Value{}, fmt.Errorf("sqlb: cannot allocate embedded field at index %v", index)
					}
					v.Set(reflect.New(v.Type().Elem()))
				}
				v = v.Elem()
			}
		}
		v = v.Field(x)
	}
	return v, nil
}

// wrapQueryErr attaches the failing SQL to a driver error. Without it a
// Postgres syntax or type error names a column but not the statement, which is
// unhelpful when the statement was assembled from a filter expression.
func wrapQueryErr(err error, query string) error {
	return fmt.Errorf("sqlb: executing %s: %w", truncate(query, 400), err)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
