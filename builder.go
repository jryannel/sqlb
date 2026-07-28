package sqlb

import (
	"errors"
	"fmt"
	"strconv"
)

// Builder is a SELECT statement under construction against model T.
//
// Its methods mutate the builder in place and return it, so a query can be
// assembled across branches without reassignment gymnastics and hooks can
// amend a query they are handed. Use Clone before sharing a partially built
// query between goroutines or request scopes.
type Builder[T any] struct {
	model    *Model
	dialect  Dialect
	alias    string
	sel      []Selection
	distinct bool
	joins    []joinClause
	where    []Pred
	// seek is the keyset boundary, held apart from where because it is
	// pagination rather than selection: countSQL drops it, exactly as it drops
	// LIMIT and OFFSET. See cursor.go.
	seek   Pred
	groups []Expr
	having []Pred
	orders []Order
	expand []string
	limit  *int
	offset *int
	lock   string
	err    error
}

type joinClause struct {
	kind  string // "JOIN", "LEFT JOIN", ...
	table string
	alias string
	on    Pred
}

// Query starts a SELECT against the table mapped by T.
func Query[T any]() *Builder[T] {
	m := ModelOf[T]()
	m.markInUse()
	return &Builder[T]{model: m, alias: m.Table}
}

// Model returns the reflected model the query runs against.
func (b *Builder[T]) Model() *Model { return b.model }

// Err returns the first error recorded while building, if any. Terminal
// methods return it too, so checking it explicitly is optional.
func (b *Builder[T]) Err() error { return b.err }

// Fail records err and returns the builder, so a package outside sqlb can put
// a query into the same error state the builder uses internally rather than
// having to break the fluent chain with its own error return. Only the first
// error is kept, matching fail.
//
// filter.Apply is the motivating caller: it assembles a builder from a parsed
// request and needs somewhere to put "this request is valid but I cannot
// express it".
func (b *Builder[T]) Fail(err error) *Builder[T] {
	if b.err == nil {
		b.err = err
	}
	return b
}

func (b *Builder[T]) fail(format string, args ...any) *Builder[T] {
	if b.err == nil {
		b.err = fmt.Errorf(format, args...)
	}
	return b
}

// Clone returns an independent copy, so a base query can be reused as the
// starting point for several derived ones.
func (b *Builder[T]) Clone() *Builder[T] {
	c := *b
	c.sel = append([]Selection(nil), b.sel...)
	c.joins = append([]joinClause(nil), b.joins...)
	c.where = append([]Pred(nil), b.where...)
	c.groups = append([]Expr(nil), b.groups...)
	c.having = append([]Pred(nil), b.having...)
	c.orders = append([]Order(nil), b.orders...)
	c.expand = append([]string(nil), b.expand...)
	if b.limit != nil {
		v := *b.limit
		c.limit = &v
	}
	if b.offset != nil {
		v := *b.offset
		c.offset = &v
	}
	return &c
}

// UseDialect overrides the dialect for this query.
func (b *Builder[T]) UseDialect(d Dialect) *Builder[T] {
	b.dialect = d
	return b
}

// As aliases the table, which is required for self-joins.
func (b *Builder[T]) As(alias string) *Builder[T] {
	b.alias = alias
	return b
}

// Select appends to the projection. Without any call the query selects every
// mapped column of T. Use ClearSelect to start the projection over.
func (b *Builder[T]) Select(items ...Selectable) *Builder[T] {
	for _, it := range items {
		b.sel = append(b.sel, it.selection())
	}
	return b
}

// ClearSelect discards the projection built so far, so the next Select starts
// from nothing rather than adding to it.
func (b *Builder[T]) ClearSelect() *Builder[T] {
	b.sel = nil
	return b
}

// Distinct adds DISTINCT to the projection.
func (b *Builder[T]) Distinct() *Builder[T] {
	b.distinct = true
	return b
}

// Where conjoins predicates. Zero predicates are skipped, so conditional
// filters need no surrounding if statement.
func (b *Builder[T]) Where(preds ...Pred) *Builder[T] {
	for _, p := range preds {
		if !p.IsZero() {
			b.where = append(b.where, p)
		}
	}
	return b
}

// Join adds an inner join. Pass an empty alias to use the table name.
func (b *Builder[T]) Join(table, alias string, on Pred) *Builder[T] {
	return b.join("JOIN", table, alias, on)
}

// LeftJoin adds a left outer join.
func (b *Builder[T]) LeftJoin(table, alias string, on Pred) *Builder[T] {
	return b.join("LEFT JOIN", table, alias, on)
}

func (b *Builder[T]) join(kind, table, alias string, on Pred) *Builder[T] {
	if on.IsZero() {
		return b.fail("sqlb: %s %s has no ON condition, which would produce a cross join", kind, table)
	}
	b.joins = append(b.joins, joinClause{kind: kind, table: table, alias: alias, on: on})
	return b
}

// GroupBy groups by the given columns.
func (b *Builder[T]) GroupBy(fields ...Field) *Builder[T] {
	for _, f := range fields {
		b.groups = append(b.groups, f.Column())
	}
	return b
}

// GroupByExpr groups by arbitrary expressions.
func (b *Builder[T]) GroupByExpr(exprs ...Expr) *Builder[T] {
	b.groups = append(b.groups, exprs...)
	return b
}

// Having filters grouped rows.
func (b *Builder[T]) Having(preds ...Pred) *Builder[T] {
	for _, p := range preds {
		if !p.IsZero() {
			b.having = append(b.having, p)
		}
	}
	return b
}

// OrderBy appends ordering terms.
func (b *Builder[T]) OrderBy(orders ...Order) *Builder[T] {
	b.orders = append(b.orders, orders...)
	return b
}

// OrderColumns names the columns the query orders by, in order, skipping any
// term that orders by an expression rather than a column.
//
// filter.Apply is the motivating caller: it owns the projection and has to
// cover whatever the ordering ended up being, including the tiebreaker Stable
// appended, so that a cursor can be read off the last row.
func (b *Builder[T]) OrderColumns() []string {
	out := make([]string, 0, len(b.orders))
	for _, o := range b.orders {
		if col, ok := o.expr.(Column); ok {
			out = append(out, col.Name)
		}
	}
	return out
}

// Limit caps the number of rows returned. A negative limit is an error rather
// than a silent no-op, since it usually means an unchecked computed value.
func (b *Builder[T]) Limit(n int) *Builder[T] {
	if n < 0 {
		return b.fail("sqlb: negative limit %d", n)
	}
	b.limit = &n
	return b
}

// Offset skips rows.
func (b *Builder[T]) Offset(n int) *Builder[T] {
	if n < 0 {
		return b.fail("sqlb: negative offset %d", n)
	}
	b.offset = &n
	return b
}

// Page applies offset pagination. Pages are 1-based.
func (b *Builder[T]) Page(number, size int) *Builder[T] {
	if number < 1 {
		return b.fail("sqlb: page number %d is below 1", number)
	}
	if size < 1 {
		return b.fail("sqlb: page size %d is below 1", size)
	}
	return b.Limit(size).Offset((number - 1) * size)
}

// ForUpdate takes row locks for the duration of the transaction.
func (b *Builder[T]) ForUpdate() *Builder[T] {
	b.lock = "FOR UPDATE"
	return b
}

// ForShare takes shared row locks.
func (b *Builder[T]) ForShare() *Builder[T] {
	b.lock = "FOR SHARE"
	return b
}

// SkipLocked skips rows already locked, for queue-style consumers. It has no
// effect without ForUpdate or ForShare.
func (b *Builder[T]) SkipLocked() *Builder[T] {
	if b.lock == "" {
		return b.fail("sqlb: SkipLocked requires ForUpdate or ForShare")
	}
	b.lock += " SKIP LOCKED"
	return b
}

// SQL compiles the query to SQL text and its bind parameters. It is the
// inspection point: log it, diff it in tests, or paste it into EXPLAIN.
func (b *Builder[T]) SQL() (string, []any, error) {
	if b.err != nil {
		return "", nil, b.err
	}
	c := newCompiler(b.dialect)
	b.compile(c)
	return c.result()
}

func (b *Builder[T]) compile(c *compiler) {
	// Once a second table is in the statement, an unqualified column is a
	// coin toss Postgres refuses to make. Everything a caller can name — the
	// projection, a filter, a sort — is a column of T, so T's table is the
	// answer.
	if b.joined() {
		defer c.qualifyTo(b.from())()
	}

	c.write("SELECT ")
	if b.distinct {
		c.write("DISTINCT ")
	}
	b.compileProjection(c)
	b.compileExpansionSelections(c)

	c.write(" FROM ")
	c.table(b.model.Table)
	if b.alias != "" && b.alias != b.model.Table {
		c.write(" AS ")
		c.ident(b.alias)
	}

	for _, j := range b.joins {
		c.write(" " + j.kind + " ")
		c.table(j.table)
		if j.alias != "" && j.alias != j.table {
			c.write(" AS ")
			c.ident(j.alias)
		}
		c.write(" ON ")
		c.expr(j.on.Expr())
	}
	b.compileExpansions(c)

	if preds := b.filters(); len(preds) > 0 {
		c.write(" WHERE ")
		c.predicates(preds)
	}

	if len(b.groups) > 0 {
		c.write(" GROUP BY ")
		for i, g := range b.groups {
			if i > 0 {
				c.write(", ")
			}
			c.expr(g)
		}
	}

	if len(b.having) > 0 {
		c.write(" HAVING ")
		c.predicates(b.having)
	}

	if len(b.orders) > 0 {
		c.write(" ORDER BY ")
		c.orders(b.orders)
	}

	// Limit and offset are rendered as literals, not bind parameters, so that
	// the planner can see them. Both are ints validated above, so there is no
	// injection surface.
	if b.limit != nil {
		c.write(" LIMIT " + strconv.Itoa(*b.limit))
	}
	if b.offset != nil {
		c.write(" OFFSET " + strconv.Itoa(*b.offset))
	}
	if b.lock != "" {
		c.write(" " + b.lock)
	}
}

func (b *Builder[T]) compileProjection(c *compiler) {
	if len(b.sel) == 0 {
		for i, col := range b.model.Columns {
			if i > 0 {
				c.write(", ")
			}
			c.column(Column{Table: b.alias, Name: col.Name})
		}
		return
	}
	for i, s := range b.sel {
		if i > 0 {
			c.write(", ")
		}
		c.expr(s.expr)
		if s.alias != "" {
			c.write(" AS ")
			c.ident(s.alias)
		}
	}
}

// countSQL compiles the row count for the query, ignoring projection, ordering
// and pagination. Grouped queries are wrapped, since counting them means
// counting groups rather than rows.
func (b *Builder[T]) countSQL() (string, []any, error) {
	if b.err != nil {
		return "", nil, b.err
	}
	c := newCompiler(b.dialect)

	if len(b.groups) > 0 {
		inner := b.Clone()
		inner.orders = nil
		inner.limit, inner.offset = nil, nil
		inner.seek = Pred{}
		inner.lock = ""
		c.write("SELECT count(*) FROM (")
		inner.compile(c)
		c.write(") AS grouped")
		return c.result()
	}

	counted := b.Clone()
	counted.sel = []Selection{Sel(Call{Name: "count", Star: true})}
	counted.orders = nil
	counted.limit, counted.offset = nil, nil
	// A count is the size of the matching set, not of what is left to page
	// through. Keeping the boundary would make ?count=exact shrink as a client
	// paged, which is a worse answer than no count at all.
	counted.seek = Pred{}
	counted.lock = ""
	counted.distinct = false
	// Expansions join on the target's primary key, so they never change how
	// many rows match — and counting is the one place the joined row is not
	// read at all. Dropping them keeps ?count=exact from paying for a join
	// whose result is discarded.
	counted.expand = nil
	counted.compile(c)
	return c.result()
}

// ErrNotFound is returned by One when the query matches no rows. It is a
// sentinel so that HTTP handlers can map it to 404 without inspecting text.
var ErrNotFound = errors.New("sqlb: no rows matched")

// filters is everything that reaches the WHERE clause: the caller's predicates
// and, last, the keyset boundary. The boundary goes last so that the SQL reads
// in the order it was asked for, with paging as a suffix.
func (b *Builder[T]) filters() []Pred {
	if b.seek.IsZero() {
		return b.where
	}
	out := make([]Pred, 0, len(b.where)+1)
	out = append(out, b.where...)
	return append(out, b.seek)
}

// from is the name the base table is referenced by: its alias if it has one,
// otherwise the table itself. Expansion joins qualify the foreign key with it.
func (b *Builder[T]) from() string {
	if b.alias != "" {
		return b.alias
	}
	return b.model.Table
}

// joined reports whether the statement brings in a second table, by an explicit
// join or by an expansion. It is what decides whether the default projection
// has to be qualified.
func (b *Builder[T]) joined() bool { return len(b.joins) > 0 || len(b.expand) > 0 }
