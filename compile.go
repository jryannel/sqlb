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
}

func newCompiler(d Dialect) *compiler {
	if d == nil {
		d = defaultDialect
	}
	return &compiler{d: d}
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
func (c *compiler) column(col Column) {
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

	case Param:
		c.bind(n.Value)

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
		c.write(" " + n.Op + " ")
		c.operand(n.Right)

	case Unary:
		if n.Postfix {
			c.operand(n.Operand)
			c.write(" " + n.Op)
			return
		}
		c.write(n.Op + " ")
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
		c.write("::" + n.Type)

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
		case nullsFirst:
			c.write(" NULLS FIRST")
		case nullsLast:
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
