package sqlb

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// ErrUnscoped is returned by Update and Delete when no WHERE clause was given.
// Rewriting or removing every row is almost never intended, so it must be
// requested explicitly with Everything.
var ErrUnscoped = errors.New("sqlb: statement would affect every row; add a Where clause or call Everything to confirm")

// Insert is an INSERT statement over model T.
//
// Columns carrying a database default are omitted when their Go value is the
// zero value, so generated identifiers and timestamps come from the database
// rather than being overwritten with zeroes. The statement always returns the
// inserted rows, so those values land back in the caller's structs.
type Insert[T any] struct {
	model    *Model
	dialect  Dialect
	rows     []*T
	only     map[string]bool
	omit     map[string]bool
	conflict *conflictClause
	err      error
}

type conflictClause struct {
	target   []string
	doUpdate []string
}

// InsertRows starts an INSERT for one or more rows. The rows are pointers so
// that hooks and returned database values can be written back into them.
func InsertRows[T any](rows ...*T) *Insert[T] {
	m := ModelOf[T]()
	m.markInUse()
	ins := &Insert[T]{model: m, rows: rows}
	if len(rows) == 0 {
		ins.err = errors.New("sqlb: InsertRows called with no rows")
	}
	for _, r := range rows {
		if r == nil {
			ins.err = errors.New("sqlb: InsertRows called with a nil row")
			break
		}
	}
	return ins
}

// Only restricts the insert to the named columns.
func (i *Insert[T]) Only(columns ...string) *Insert[T] {
	i.checkColumns("Only", columns)
	i.only = toSet(columns)
	return i
}

// Omit excludes the named columns, leaving them to their database defaults.
func (i *Insert[T]) Omit(columns ...string) *Insert[T] {
	i.checkColumns("Omit", columns)
	i.omit = toSet(columns)
	return i
}

// checkColumns fails the statement on a name the model does not have.
//
// Update.Set and the conflict target both validate their names, and an
// unvalidated one here fails quietly in the worst way: Only("emial") matches
// nothing, so the column is silently not written, or — if it was the only name
// given — the statement fails with "no columns to write", which names neither
// the typo nor the column it was meant to be.
func (i *Insert[T]) checkColumns(method string, columns []string) {
	for _, name := range columns {
		if i.model.Column(name) == nil {
			if i.err == nil {
				i.err = fmt.Errorf("sqlb: %s names %q, which is not a column of %s (have: %s)",
					method, name, i.model.Table, strings.Join(i.model.ColumnNames(), ", "))
			}
			return
		}
	}
}

// OnConflictDoNothing makes a conflict on the given columns skip the row
// instead of failing. Skipped rows are simply absent from the result.
//
// Because a skipped row cannot be told apart from its neighbours in what
// comes back, a statement that skips any row leaves every caller struct
// untouched — the returned slice is then the only account of what was
// written. See Exec.
func (i *Insert[T]) OnConflictDoNothing(target ...string) *Insert[T] {
	i.conflict = &conflictClause{target: target}
	return i
}

// OnConflictUpdate upserts: a conflict on target updates the named columns
// from the proposed row. With no update columns it behaves as do-nothing.
func (i *Insert[T]) OnConflictUpdate(target []string, update ...string) *Insert[T] {
	i.conflict = &conflictClause{target: target, doUpdate: update}
	return i
}

// UseDialect overrides the dialect for this statement.
func (i *Insert[T]) UseDialect(d Dialect) *Insert[T] {
	i.dialect = d
	return i
}

// SQL compiles the statement without running it.
func (i *Insert[T]) SQL() (string, []any, error) {
	if i.err != nil {
		return "", nil, i.err
	}
	cols := i.columns()
	if len(cols) == 0 {
		return "", nil, fmt.Errorf("sqlb: insert into %s has no columns to write", i.model.Table)
	}

	c := newCompiler(i.dialect)
	c.write("INSERT INTO ")
	c.table(i.model.Table)
	c.write(" (")
	for n, col := range cols {
		if n > 0 {
			c.write(", ")
		}
		c.ident(col.Name)
	}
	c.write(") VALUES ")

	for n, row := range i.rows {
		if n > 0 {
			c.write(", ")
		}
		c.write("(")
		rv := reflect.ValueOf(row).Elem()
		for k, col := range cols {
			if k > 0 {
				c.write(", ")
			}
			fv, err := fieldByIndex(rv, col.Index)
			if err != nil {
				return "", nil, err
			}
			c.bind(fv.Interface())
		}
		c.write(")")
	}

	if i.conflict != nil {
		c.write(" ON CONFLICT")
		if len(i.conflict.target) > 0 {
			c.write(" (")
			for n, name := range i.conflict.target {
				if n > 0 {
					c.write(", ")
				}
				if i.model.Column(name) == nil {
					return "", nil, fmt.Errorf("sqlb: conflict target %q is not a column of %s", name, i.model.Table)
				}
				c.ident(name)
			}
			c.write(")")
		}
		if len(i.conflict.doUpdate) == 0 {
			c.write(" DO NOTHING")
		} else {
			c.write(" DO UPDATE SET ")
			for n, name := range i.conflict.doUpdate {
				if n > 0 {
					c.write(", ")
				}
				if i.model.Column(name) == nil {
					return "", nil, fmt.Errorf("sqlb: conflict update column %q is not a column of %s", name, i.model.Table)
				}
				c.ident(name)
				c.write(" = EXCLUDED.")
				c.ident(name)
			}
		}
	}

	writeReturning(c, i.model)
	return c.result()
}

// columns picks the columns to write: everything mapped, minus Only/Omit, and
// minus database-defaulted columns still holding their zero value.
func (i *Insert[T]) columns() []*ColumnInfo {
	var out []*ColumnInfo
	for _, col := range i.model.Columns {
		if i.only != nil && !i.only[col.Name] {
			continue
		}
		if i.omit[col.Name] {
			continue
		}
		if col.HasDefault && i.only == nil && i.allZero(col) {
			continue
		}
		out = append(out, col)
	}
	return out
}

func (i *Insert[T]) allZero(col *ColumnInfo) bool {
	for _, row := range i.rows {
		fv, err := fieldByIndex(reflect.ValueOf(row).Elem(), col.Index)
		if err != nil || !fv.IsZero() {
			return false
		}
	}
	return true
}

// Exec runs the insert, returning the stored rows with database defaults
// applied. The caller's structs are updated in place as well — except when
// ON CONFLICT DO NOTHING skipped a row, in which case none of them are; see
// writeBack for why.
func (i *Insert[T]) Exec(ctx context.Context, db Executor) ([]T, error) {
	hooks := hooksFor[T](db)
	for _, row := range i.rows {
		if err := hooks.runBeforeCreate(ctx, row); err != nil {
			return nil, err
		}
	}

	query, args, err := i.SQL()
	if err != nil {
		return nil, err
	}
	rows, err := runQuery(ctx, db, query, args...)
	if err != nil {
		return nil, err
	}
	stored, err := scanAllClose[T](rows, i.model)
	if err != nil {
		return nil, asConstraintErr(err)
	}

	i.writeBack(stored)
	if err := hooks.runAfterCreate(ctx, stored); err != nil {
		return nil, err
	}
	return stored, nil
}

// writeBack copies the stored rows into the caller's structs, so generated
// ids are visible without reading the returned slice.
//
// A VALUES insert returns at most one row per row written, in the order they
// were written, so equal lengths mean position identifies a row and nothing
// was skipped. A shorter result means ON CONFLICT DO NOTHING dropped one:
// every later stored row then belongs to an earlier struct than its index
// says, and writing positionally hands one row's generated primary key to a
// different row — silently, since both structs look plausible afterwards.
//
// Which row was skipped is not recoverable from the result. RETURNING reports
// only the target table's columns, so no ordinal can be carried through the
// statement to identify them, and matching on the conflict target fails
// exactly when the target is generated rather than supplied. So a short
// result writes nothing at all, and the returned slice — which is complete
// and correct — is the account of what was written. A struct left holding its
// zero value is a caller reading an obvious absence; a struct holding its
// neighbour's identity is a caller reading a lie.
func (i *Insert[T]) writeBack(stored []T) {
	if len(stored) != len(i.rows) {
		return
	}
	for n := range stored {
		*i.rows[n] = stored[n]
	}
}

// One inserts a single row and returns it.
func (i *Insert[T]) One(ctx context.Context, db Executor) (T, error) {
	var zero T
	stored, err := i.Exec(ctx, db)
	if err != nil {
		return zero, err
	}
	if len(stored) == 0 {
		// Reachable via ON CONFLICT DO NOTHING.
		return zero, ErrNotFound
	}
	return stored[0], nil
}

// Update is an UPDATE statement over model T.
type Update[T any] struct {
	model   *Model
	dialect Dialect
	sets    []assignment
	where   []Pred
	all     bool
	err     error
}

type assignment struct {
	column string
	value  Expr
}

// UpdateRows starts an UPDATE.
func UpdateRows[T any]() *Update[T] {
	m := ModelOf[T]()
	m.markInUse()
	return &Update[T]{model: m}
}

// Set assigns a value to a column.
func (u *Update[T]) Set(column string, value any) *Update[T] {
	if u.model.Column(column) == nil {
		return u.fail("sqlb: %q is not a column of %s", column, u.model.Table)
	}
	u.sets = append(u.sets, assignment{column: column, value: Param{Value: value}})
	return u
}

// SetExpr assigns an expression, for updates computed from the current row
// such as a counter increment.
func (u *Update[T]) SetExpr(column string, value Expr) *Update[T] {
	if u.model.Column(column) == nil {
		return u.fail("sqlb: %q is not a column of %s", column, u.model.Table)
	}
	u.sets = append(u.sets, assignment{column: column, value: value})
	return u
}

// Where narrows the affected rows.
func (u *Update[T]) Where(preds ...Pred) *Update[T] {
	for _, p := range preds {
		if !p.IsZero() {
			u.where = append(u.where, p)
		}
	}
	return u
}

// Everything confirms an intentionally unscoped update.
func (u *Update[T]) Everything() *Update[T] {
	u.all = true
	return u
}

// UseDialect overrides the dialect for this statement.
func (u *Update[T]) UseDialect(d Dialect) *Update[T] {
	u.dialect = d
	return u
}

func (u *Update[T]) fail(format string, args ...any) *Update[T] {
	if u.err == nil {
		u.err = fmt.Errorf(format, args...)
	}
	return u
}

// SQL compiles the statement without running it.
func (u *Update[T]) SQL() (string, []any, error) {
	if u.err != nil {
		return "", nil, u.err
	}
	if len(u.sets) == 0 {
		return "", nil, fmt.Errorf("sqlb: update of %s assigns no columns", u.model.Table)
	}
	if len(u.where) == 0 && !u.all {
		return "", nil, ErrUnscoped
	}

	c := newCompiler(u.dialect)
	c.write("UPDATE ")
	c.table(u.model.Table)
	c.write(" SET ")
	for n, a := range u.sets {
		if n > 0 {
			c.write(", ")
		}
		c.ident(a.column)
		c.write(" = ")
		c.expr(a.value)
	}
	if len(u.where) > 0 {
		c.write(" WHERE ")
		c.predicates(u.where)
	}
	writeReturning(c, u.model)
	return c.result()
}

// Clone returns an independent copy, so a statement can be reused as the
// starting point for several derived ones.
func (u *Update[T]) Clone() *Update[T] {
	c := *u
	c.sets = append([]assignment(nil), u.sets...)
	c.where = append([]Pred(nil), u.where...)
	return &c
}

// Exec runs the update and returns the updated rows.
//
// The statement is cloned first, for the reason Builder.All clones: a
// BeforeUpdate hook amends what it is given, and the doc comment's own example
// is one that calls Set. Amending the caller's statement would make a second
// Exec assign updated_at twice and narrow a scoping predicate twice.
func (u *Update[T]) Exec(ctx context.Context, db Executor) ([]T, error) {
	hooks := hooksFor[T](db)
	stmt := u.Clone()
	if err := hooks.runBeforeUpdate(ctx, stmt); err != nil {
		return nil, err
	}
	query, args, err := stmt.SQL()
	if err != nil {
		return nil, err
	}
	rows, err := runQuery(ctx, db, query, args...)
	if err != nil {
		return nil, err
	}
	updated, err := scanAllClose[T](rows, u.model)
	if err != nil {
		return nil, asConstraintErr(err)
	}
	if err := hooks.runAfterUpdate(ctx, updated); err != nil {
		return nil, err
	}
	return updated, nil
}

// One runs an update expected to touch exactly one row.
//
// The check is on the result, so an update matching several rows has already
// changed all of them when the error returns. Under autocommit that is durable;
// inside WithTx the error rolls it back, which is the way to make "expected
// one" a refusal rather than a report.
func (u *Update[T]) One(ctx context.Context, db Executor) (T, error) {
	var zero T
	updated, err := u.Exec(ctx, db)
	if err != nil {
		return zero, err
	}
	switch len(updated) {
	case 0:
		return zero, ErrNotFound
	case 1:
		return updated[0], nil
	default:
		return zero, fmt.Errorf("sqlb: update matched %d rows in %s, expected one; "+
			"they have already been updated — wrap the call in WithTx if the count "+
			"needs to be able to refuse it", len(updated), u.model.Table)
	}
}

// Delete is a DELETE statement over model T.
type Delete[T any] struct {
	model   *Model
	dialect Dialect
	where   []Pred
	all     bool
	err     error
}

// DeleteRows starts a DELETE.
func DeleteRows[T any]() *Delete[T] {
	m := ModelOf[T]()
	m.markInUse()
	return &Delete[T]{model: m}
}

// Where narrows the affected rows.
func (d *Delete[T]) Where(preds ...Pred) *Delete[T] {
	for _, p := range preds {
		if !p.IsZero() {
			d.where = append(d.where, p)
		}
	}
	return d
}

// Everything confirms an intentionally unscoped delete.
func (d *Delete[T]) Everything() *Delete[T] {
	d.all = true
	return d
}

// UseDialect overrides the dialect for this statement.
func (d *Delete[T]) UseDialect(dl Dialect) *Delete[T] {
	d.dialect = dl
	return d
}

// SQL compiles the statement without running it.
func (d *Delete[T]) SQL() (string, []any, error) {
	if d.err != nil {
		return "", nil, d.err
	}
	if len(d.where) == 0 && !d.all {
		return "", nil, ErrUnscoped
	}
	c := newCompiler(d.dialect)
	c.write("DELETE FROM ")
	c.table(d.model.Table)
	if len(d.where) > 0 {
		c.write(" WHERE ")
		c.predicates(d.where)
	}
	return c.result()
}

// Clone returns an independent copy, so a statement can be reused as the
// starting point for several derived ones.
func (d *Delete[T]) Clone() *Delete[T] {
	c := *d
	c.where = append([]Pred(nil), d.where...)
	return &c
}

// Exec runs the delete and returns the number of rows removed.
//
// The statement is cloned first, for the reason Update.Exec clones: a
// BeforeDelete hook narrowing the statement must narrow one execution, not
// every later one.
func (d *Delete[T]) Exec(ctx context.Context, db Executor) (int64, error) {
	hooks := hooksFor[T](db)
	stmt := d.Clone()
	if err := hooks.runBeforeDelete(ctx, stmt); err != nil {
		return 0, err
	}
	query, args, err := stmt.SQL()
	if err != nil {
		return 0, err
	}
	tag, err := db.Exec(ctx, query, args...)
	if err != nil {
		return 0, wrapQueryErr(err, query)
	}
	n := tag.RowsAffected()
	if err := hooks.runAfterDelete(ctx, n); err != nil {
		return 0, err
	}
	return n, nil
}

// writeReturning appends RETURNING over every mapped column, so that callers
// see database-generated values without a follow-up read.
func writeReturning(c *compiler, m *Model) {
	c.write(" RETURNING ")
	for n, col := range m.Columns {
		if n > 0 {
			c.write(", ")
		}
		c.ident(col.Name)
	}
}

// scanAllClose scans a RETURNING result set and closes it.
func scanAllClose[T any](rows rowSource, m *Model) ([]T, error) {
	defer rows.Close()
	return scanAll[T](rows, m)
}

func toSet(items []string) map[string]bool {
	if len(items) == 0 {
		return nil
	}
	out := make(map[string]bool, len(items))
	for _, s := range items {
		out[s] = true
	}
	return out
}
