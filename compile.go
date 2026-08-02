package sqlb

import (
	"fmt"
	"strconv"
	"strings"
)

// Dialect adapts the compiler to a specific database. Postgres is the only
// implementation today; the interface exists so that the AST does not have to
// change when a second one is added.
type Dialect interface {
	// Placeholder renders the nth bind parameter, 1-based.
	Placeholder(n int) string
	// QuoteIdent quotes an identifier.
	QuoteIdent(s string) string
	// Name identifies the dialect in diagnostics.
	Name() string
}

// Postgres is the Postgres dialect: $N placeholders and double-quoted
// identifiers.
type Postgres struct{}

func (Postgres) Placeholder(n int) string { return "$" + strconv.Itoa(n) }
func (Postgres) Name() string             { return "postgres" }

func (Postgres) QuoteIdent(s string) string {
	// Doubling embedded quotes is the only escape Postgres defines. Identifiers
	// reaching here have normally already passed schema validation; this is the
	// backstop for hand-written F() references.
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// defaultDialect is used by every statement that does not override it.
//
// It is deliberately not exported and not settable. A package-level mutable
// dialect would be read on the compile path of every query while being
// writable from anywhere, which is a data race with no legitimate trigger:
// sqlb targets Postgres only (ADR-0001), so there is nothing to switch to
// globally. A caller who genuinely needs a different dialect for one statement
// uses UseDialect on that statement, which is scoped and race-free.
var defaultDialect Dialect = Postgres{}

// compiler accumulates SQL text and its bind parameters.
type compiler struct {
	sb   strings.Builder
	args []any
	d    Dialect
	err  error

	// base is the table an unqualified column belongs to. Empty while the
	// statement names one table, which is the common case and the one whose
	// SQL should stay readable. See qualifyTo.
	base string

	// shared maps a sharedParam's identity onto the placeholder it was given
	// earlier in this statement, so that a value appearing in the projection,
	// the WHERE and the ORDER BY is sent once rather than three times. Keyed by
	// identity rather than by value: two equal vectors that arrived separately
	// are two parameters, and deduplicating them would be a different and much
	// larger promise.
	shared map[*sharedValue]int

	// computed maps a qualifier onto the derived columns that resolve under it:
	// the statement's own table, and "" for the unqualified names a filter, a
	// sort or a projection writes. It is what turns one declaration into four
	// renderings — see column.
	computed map[string]*computedSet
}

// computedSet is one model's derived columns and the binds their expressions
// take, as they stand for the statement being compiled.
type computedSet struct {
	cols  map[string]*ColumnInfo
	binds map[string]*sharedValue
}

// computedSetOf is the set for a statement carrying no binds of its own — a
// mutation, whose WHERE may still name a row-local derived column. Nil when the
// model computes nothing.
func computedSetOf(m *Model) *computedSet {
	if len(m.Derived) == 0 {
		return nil
	}
	return &computedSet{cols: m.byDerived}
}

// withComputed makes set the derived columns of qualifier — and of unqualified
// names, which every filter, sort and projection term writes — returning a
// function that restores the previous mapping.
//
// The restore matters for the same reason qualifyTo's does: compilation nests,
// and a grouped count wrapping the query in a subselect must not leave the
// outer statement holding the inner one's columns.
func (c *compiler) withComputed(qualifier string, set *computedSet) func() {
	if set == nil || len(set.cols) == 0 {
		return func() {}
	}
	if c.computed == nil {
		c.computed = make(map[string]*computedSet, 2)
	}
	prevBase, hadBase := c.computed[""]
	prevQual, hadQual := c.computed[qualifier]
	c.computed[""] = set
	c.computed[qualifier] = set
	return func() {
		restore := func(key string, prev *computedSet, had bool) {
			if had {
				c.computed[key] = prev
				return
			}
			delete(c.computed, key)
		}
		restore("", prevBase, hadBase)
		restore(qualifier, prevQual, hadQual)
	}
}

// derived resolves a column reference to a computed column, or nil.
func (c *compiler) derived(col Column) (*ColumnInfo, *computedSet) {
	if c.computed == nil {
		return nil, nil
	}
	set := c.computed[col.Table]
	if set == nil {
		return nil, nil
	}
	return set.cols[col.Name], set
}

// derivedAlias is the name a projected expression should be aliased back to,
// or "" for a term that is not a bare reference to a derived column.
func (c *compiler) derivedAlias(e Expr) string {
	var col Column
	switch n := e.(type) {
	case Column:
		col = n
	case Field:
		col = n.Column()
	default:
		return ""
	}
	if derived, _ := c.derived(col); derived != nil {
		return derived.Name
	}
	return ""
}

// computedExpr renders a derived column's expression in place of its name,
// parenthesised so that what surrounds it cannot change what it means.
//
// The `?` placeholders are renumbered into the dialect's scheme exactly as raw
// does, except that each one resolves through the bind it was declared to need
// — and binds once: a value named in the projection, the WHERE and the ORDER BY
// is one parameter, which is the facility Near proved worth having and this is
// the general case of.
func (c *compiler) computedExpr(col *ColumnInfo, set *computedSet) {
	// Parenthesised unless the expression already is. Both spellings are
	// common — a subquery brings its own parentheses, and the convention this
	// codebase's own raw fragments follow is to wrap them — and doubling them
	// makes every logged statement and every test assertion carry a pair
	// nobody wrote.
	wrap := !wrapped(col.Expr)
	if wrap {
		c.write("(")
	}
	used := 0
	for i := 0; i < len(col.Expr); i++ {
		ch := col.Expr[i]
		if ch != '?' {
			c.sb.WriteByte(ch)
			continue
		}
		if i+1 < len(col.Expr) && col.Expr[i+1] == '?' {
			c.sb.WriteByte('?')
			i++
			continue
		}
		key := col.Needs[used]
		used++
		slot, bound := set.binds[key]
		if !bound {
			// Rendering NULL instead would return false for every row forever
			// and look like a working feature, which is the failure the
			// declaration exists to close (ADR-0041).
			c.fail("sqlb: computed column %q needs the %q bind, and nothing supplied it; "+
				"call Bind(%q, value) on the query, or register a BeforeQuery hook that does",
				col.Name, key, key)
			return
		}
		c.expr(sharedParam{slot: slot})
	}
	if wrap {
		c.write(")")
	}
}

// wrapped reports whether one pair of parentheses already encloses the whole
// expression.
//
// It is deliberately conservative: the scan does not know about string
// literals, so a quoted parenthesis can only make it answer "not wrapped",
// which adds a redundant pair. The other direction — dropping the parentheses
// from something the surroundings could re-associate — is the one that would
// change meaning, and it requires the first parenthesis to close at the last
// character, which is what being wrapped is.
func wrapped(expr string) bool {
	s := strings.TrimSpace(expr)
	if len(s) < 2 || s[0] != '(' || s[len(s)-1] != ')' {
		return false
	}
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
		}
		if depth == 0 && i < len(s)-1 {
			return false
		}
	}
	return depth == 0
}

func newCompiler(d Dialect) *compiler {
	if d == nil {
		d = defaultDialect
	}
	c := &compiler{d: d}
	// One allocation instead of the six a doubling buffer takes to reach the
	// length of an ordinary list statement. Overshooting costs a few hundred
	// bytes that are freed with the compiler; undershooting costs a copy.
	c.sb.Grow(512)
	return c
}

// identWriter is a dialect that can quote an identifier straight into a buffer.
// Postgres implements it, so the common path never builds the intermediate
// string QuoteIdent returns — one allocation per identifier, and a statement
// names one per projected column plus its table, its filters and its sorts.
//
// It is an optional interface rather than a change to Dialect so that a dialect
// outside this package stays valid as written.
type identWriter interface {
	writeIdent(sb *strings.Builder, s string)
}

func (Postgres) writeIdent(sb *strings.Builder, s string) {
	if strings.IndexByte(s, '"') >= 0 {
		sb.WriteString(`"` + strings.ReplaceAll(s, `"`, `""`) + `"`)
		return
	}
	sb.WriteByte('"')
	sb.WriteString(s)
	sb.WriteByte('"')
}

func (c *compiler) fail(format string, args ...any) {
	if c.err == nil {
		c.err = fmt.Errorf(format, args...)
	}
}

func (c *compiler) write(s string) { c.sb.WriteString(s) }

// bind appends a value as the next bind parameter.
//
// It is the one function every value passes through on its way out, and it
// passes them all on untouched. That is worth a sentence, because it used to be
// where the array encoding lived: database/sql has no array case in either
// direction, so a slice had to be wrapped in a driver.Valuer rendering the
// Postgres literal (ADR-0033). pgx encodes slices natively, so the wrapping and
// the codec behind it are gone (ADR-0040).
func (c *compiler) bind(v any) {
	c.args = append(c.args, v)
	c.write(c.d.Placeholder(len(c.args)))
}

func (c *compiler) ident(s string) {
	if s == "" {
		c.fail("sqlb: empty identifier")
		return
	}
	if w, ok := c.d.(identWriter); ok {
		w.writeIdent(&c.sb, s)
		return
	}
	c.write(c.d.QuoteIdent(s))
}

// table renders a table reference, which may name a Postgres schema.
//
// "invoices" renders as one identifier; "billing.invoices" renders as two.
// Quoting the whole thing as a single identifier would make Postgres look for a
// table literally called "billing.invoices", which is a confusing failure a
// long way from its cause.
//
// This is why sqlb itself namespaces with a name prefix rather than a Postgres
// schema — see ADR-0015 — but a caller who has qualified names anyway, from an
// existing database or from Describe, gets correct SQL rather than mangled SQL.
func (c *compiler) table(name string) {
	if ns, rel, ok := strings.Cut(name, "."); ok {
		c.ident(ns)
		c.write(".")
		c.ident(rel)
		return
	}
	c.ident(name)
}

// qualifyTo makes base the table unqualified columns resolve to, and returns a
// function restoring the previous setting.
//
// Only a statement that joins needs this, and only a statement that joins gets
// it: single-table SQL keeps its bare column names, which is what a person
// reading a log wants to see. The restore matters because compilation nests —
// a grouped Count wraps the whole query in a subselect, and the outer statement
// must not inherit the inner one's base.
func (c *compiler) qualifyTo(base string) func() {
	prev := c.base
	c.base = base
	return func() { c.base = prev }
}

// column renders an optionally qualified column reference. The qualifier may
// itself be schema-qualified.
//
// A column with no qualifier of its own takes the statement's base table, if it
// has one. This is not cosmetic: `?sort=name&expand=list` puts two tables with
// a `name` in one statement, and Postgres refuses an ambiguous reference
// outright (SQLSTATE 42702) rather than picking. Resolving to the base table is
// what the caller meant — every unqualified name it can write, from ?select,
// ?sort or a filter, names a column of the model being queried.
// A computed column is intercepted here, and here only. Every consumer already
// resolves through a *ColumnInfo and renders through this function — the filter
// parser builds predicates as F(col.Name), sorts the same way, and the default
// projection names the model's columns — so substituting the expression once is
// what puts a derived value in WHERE, ORDER BY and the projection at the same
// time (ADR-0041).
func (c *compiler) column(col Column) {
	if derived, set := c.derived(col); derived != nil {
		c.computedExpr(derived, set)
		return
	}
	table := col.Table
	if table == "" {
		table = c.base
	}
	if table != "" {
		c.table(table)
		c.write(".")
	}
	c.ident(col.Name)
}

// expr renders an expression node.
func (c *compiler) expr(e Expr) {
	switch n := e.(type) {
	case nil:
		c.fail("sqlb: nil expression")

	case Column:
		c.column(n)

	case Field:
		c.column(n.Column())

	case ConflictRef:
		// EXCLUDED is a keyword rather than a table, so it is written bare —
		// quoting it would produce "EXCLUDED", which Postgres would look for as
		// a case-sensitive relation and not find. The stored side is the real
		// table and is quoted like any other.
		if n.excluded {
			c.write("EXCLUDED.")
		} else if c.base != "" {
			c.table(c.base)
			c.write(".")
		}
		c.ident(n.name)

	case Param:
		c.bind(n.Value)

	case sharedParam:
		// The second and later appearances of one value reuse its placeholder.
		// An embedding is about twenty kilobytes and a similarity search names
		// it three times; without this the wire carries all three.
		if idx, ok := c.shared[n.slot]; ok {
			c.write(c.d.Placeholder(idx))
			break
		}
		c.args = append(c.args, n.slot.value)
		if c.shared == nil {
			c.shared = make(map[*sharedValue]int, 1)
		}
		c.shared[n.slot] = len(c.args)
		c.write(c.d.Placeholder(len(c.args)))

	case List:
		c.write("(")
		for i, item := range n.Items {
			if i > 0 {
				c.write(", ")
			}
			c.expr(item)
		}
		c.write(")")

	case Binary:
		c.operand(n.Left)
		c.write(" ")
		c.write(n.Op)
		c.write(" ")
		c.operand(n.Right)

	case Unary:
		if n.Postfix {
			c.operand(n.Operand)
			c.write(" ")
			c.write(n.Op)
			return
		}
		c.write(n.Op)
		c.write(" ")
		c.operand(n.Operand)

	case BetweenExpr:
		c.operand(n.Operand)
		if n.Not {
			c.write(" NOT")
		}
		c.write(" BETWEEN ")
		c.operand(n.Lo)
		c.write(" AND ")
		c.operand(n.Hi)

	case Call:
		c.write(n.Name)
		c.write("(")
		switch {
		case n.Star:
			c.write("*")
		default:
			if n.Distinct {
				c.write("DISTINCT ")
			}
			for i, a := range n.Args {
				if i > 0 {
					c.write(", ")
				}
				c.expr(a)
			}
		}
		c.write(")")

	case Cast:
		c.operand(n.Inner)
		c.write("::")
		c.write(n.Type)

	case Raw:
		c.raw(n)

	default:
		c.fail("sqlb: unsupported expression node %T", e)
	}
}

// operand renders a nested expression, parenthesising compound nodes so that
// operator precedence never depends on the order predicates were added in.
func (c *compiler) operand(e Expr) {
	switch e.(type) {
	case Binary, Unary, BetweenExpr, Raw:
		// Raw is included because its contents are opaque: a fragment such as
		// "a OR b" would otherwise bind more loosely than the operator it is
		// nested under, and change the meaning of the surrounding predicate.
		c.write("(")
		c.expr(e)
		c.write(")")
	default:
		c.expr(e)
	}
}

// raw splices verbatim SQL, renumbering its `?` placeholders into the
// dialect's scheme. A doubled `??` emits a literal question mark, which
// Postgres needs for its JSON operators.
func (c *compiler) raw(n Raw) {
	used := 0
	for i := 0; i < len(n.SQL); i++ {
		ch := n.SQL[i]
		if ch != '?' {
			c.sb.WriteByte(ch)
			continue
		}
		if i+1 < len(n.SQL) && n.SQL[i+1] == '?' {
			c.sb.WriteByte('?')
			i++
			continue
		}
		if used >= len(n.Args) {
			c.fail("sqlb: raw SQL %q has more placeholders than the %d arguments given", n.SQL, len(n.Args))
			return
		}
		c.bind(n.Args[used])
		used++
	}
	if used != len(n.Args) {
		c.fail("sqlb: raw SQL %q uses %d of %d arguments", n.SQL, used, len(n.Args))
	}
}

// predicates renders a conjunction of predicates, skipping the zero ones.
func (c *compiler) predicates(preds []Pred) bool {
	combined := And(preds...)
	if combined.IsZero() {
		return false
	}
	c.expr(combined.Expr())
	return true
}

func (c *compiler) orders(orders []Order) {
	for i, o := range orders {
		if i > 0 {
			c.write(", ")
		}
		c.expr(o.expr)
		if o.desc {
			c.write(" DESC")
		} else {
			c.write(" ASC")
		}
		switch o.nulls {
		case NullsFirst:
			c.write(" NULLS FIRST")
		case NullsLast:
			c.write(" NULLS LAST")
		}
	}
}

func (c *compiler) result() (string, []any, error) {
	if c.err != nil {
		return "", nil, c.err
	}
	return c.sb.String(), c.args, nil
}
