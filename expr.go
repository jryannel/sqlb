// Package sqlb is a composable, type-parameterised SQL builder for Postgres.
//
// A query is a plain value, not a statement executed at the point of
// construction. That is the whole point: predicates can be added conditionally,
// which is what static query generators cannot express.
//
//	q := sqlb.Query[User]().Where(sqlb.F("age").Gte(18))
//	if search != "" {
//	    q = q.Where(sqlb.F("name").Contains(search))
//	}
//	users, err := q.OrderBy(sqlb.F("created_at").Desc()).Limit(50).All(ctx, db)
//
// Because the query is a value, hooks and the REST layer can both mutate it
// before it is compiled, and the same predicate AST is produced by hand-written
// Go and by parsed URL filter expressions.
//
// Values never reach the SQL text. Every user-supplied value becomes a bind
// parameter; only identifiers validated against the model are interpolated.
package sqlb

import "strings"

// Expr is a SQL expression node. The set of implementations is closed apart
// from Raw, which is the escape hatch for expressions the builder cannot model.
type Expr interface {
	exprNode()
}

// Column references a table column. Table may be empty for an unqualified
// reference.
type Column struct {
	Table string
	Name  string
}

// Param is a bind parameter. Its value is never interpolated into SQL text.
type Param struct {
	Value any
}

// List is a parenthesised expression list, used by IN and row constructors.
type List struct {
	Items []Expr
}

// Binary is an infix operation such as `a = b` or `a AND b`.
type Binary struct {
	Op          string
	Left, Right Expr
}

// Unary is a prefix or postfix operation such as `NOT a` or `a IS NULL`.
type Unary struct {
	Op      string
	Operand Expr
	Postfix bool
}

// Call is a function call. Star renders `f(*)`; Distinct renders `f(DISTINCT x)`.
type Call struct {
	Name     string
	Args     []Expr
	Star     bool
	Distinct bool
}

// Raw is verbatim SQL with its own bind parameters, written as `?`
// placeholders which the compiler renumbers. Use it only for expressions the
// builder cannot model: its contents are not validated.
type Raw struct {
	SQL  string
	Args []any
}

// Cast is a type cast, rendered as `expr::type`.
type Cast struct {
	Inner Expr
	Type  string
}

// BetweenExpr is a range test. It is its own node rather than a Binary because
// its right-hand side spans two operands and must not be parenthesised.
type BetweenExpr struct {
	Operand Expr
	Lo, Hi  Expr
	Not     bool
}

func (Column) exprNode() {}
func (Param) exprNode()  {}
func (List) exprNode()   {}
func (Binary) exprNode() {}
func (Unary) exprNode()  {}
func (Call) exprNode()   {}
func (Raw) exprNode()    {}
func (Cast) exprNode()   {}

func (BetweenExpr) exprNode() {}

// Pred is a boolean expression.
//
// The zero Pred is a no-op: Where, And and Or all skip it. That makes
// conditional construction read without branches:
//
//	q.Where(sqlb.If(minAge > 0, sqlb.F("age").Gte(minAge)))
type Pred struct {
	e Expr
}

// IsZero reports whether the predicate is empty and will be skipped.
func (p Pred) IsZero() bool { return p.e == nil }

// Expr returns the underlying expression, or nil for the zero Pred.
func (p Pred) Expr() Expr { return p.e }

func pred(e Expr) Pred { return Pred{e: e} }

// If returns p when cond holds and the zero Pred otherwise.
func If(cond bool, p Pred) Pred {
	if cond {
		return p
	}
	return Pred{}
}

// And conjoins the non-zero predicates. It returns the zero Pred if none are
// non-zero, and the single predicate unwrapped if exactly one is.
func And(preds ...Pred) Pred { return combine("AND", preds) }

// Or disjoins the non-zero predicates.
func Or(preds ...Pred) Pred { return combine("OR", preds) }

func combine(op string, preds []Pred) Pred {
	var kept []Expr
	for _, p := range preds {
		if !p.IsZero() {
			kept = append(kept, p.e)
		}
	}
	switch len(kept) {
	case 0:
		return Pred{}
	case 1:
		return pred(kept[0])
	}
	acc := kept[0]
	for _, e := range kept[1:] {
		acc = Binary{Op: op, Left: acc, Right: e}
	}
	return pred(acc)
}

// Not negates a predicate. Negating the zero Pred yields the zero Pred rather
// than the always-false predicate, so an absent filter stays absent.
func Not(p Pred) Pred {
	if p.IsZero() {
		return p
	}
	return pred(Unary{Op: "NOT", Operand: p.e})
}

// RawPred is a predicate written as verbatim SQL with `?` placeholders.
func RawPred(sql string, args ...any) Pred {
	return pred(Raw{SQL: sql, Args: args})
}

// Field is a reference to a column, and the entry point for building
// predicates against it.
type Field struct {
	table string
	name  string
}

// F references a column. A dotted name is split into table and column, so both
// F("age") and F("users.age") are valid.
func F(name string) Field {
	if table, col, ok := strings.Cut(name, "."); ok {
		return Field{table: table, name: col}
	}
	return Field{name: name}
}

// Name returns the column name without its table qualifier.
func (f Field) Name() string { return f.name }

// Table returns the table qualifier, which may be empty.
func (f Field) Table() string { return f.table }

// Qualify attaches a table name to the reference.
func (f Field) Qualify(table string) Field {
	f.table = table
	return f
}

// Column returns the field as an expression node.
func (f Field) Column() Column { return Column{Table: f.table, Name: f.name} }

func (f Field) exprNode() {}

func (f Field) cmp(op string, v any) Pred {
	// A nil comparand means NULL, and `= NULL` is never true. Translating it to
	// the IS form is what the caller meant in every case where it happens.
	if v == nil {
		switch op {
		case "=":
			return f.IsNull()
		case "<>":
			return f.NotNull()
		}
	}
	return pred(Binary{Op: op, Left: f.Column(), Right: Param{Value: v}})
}

// Comparison operators.

func (f Field) Eq(v any) Pred  { return f.cmp("=", v) }
func (f Field) Neq(v any) Pred { return f.cmp("<>", v) }
func (f Field) Gt(v any) Pred  { return f.cmp(">", v) }
func (f Field) Gte(v any) Pred { return f.cmp(">=", v) }
func (f Field) Lt(v any) Pred  { return f.cmp("<", v) }
func (f Field) Lte(v any) Pred { return f.cmp("<=", v) }

// EqField compares two columns, for join and self-referential conditions.
func (f Field) EqField(other Field) Pred {
	return pred(Binary{Op: "=", Left: f.Column(), Right: other.Column()})
}

// IsNull matches rows where the column is NULL.
func (f Field) IsNull() Pred {
	return pred(Unary{Op: "IS NULL", Operand: f.Column(), Postfix: true})
}

// NotNull matches rows where the column is not NULL.
func (f Field) NotNull() Pred {
	return pred(Unary{Op: "IS NOT NULL", Operand: f.Column(), Postfix: true})
}

// OneOf matches rows whose column equals any of the values. An empty value set
// yields a predicate that matches nothing, which is what `in ()` means.
func (f Field) OneOf(values ...any) Pred {
	if len(values) == 0 {
		return pred(Raw{SQL: "false"})
	}
	items := make([]Expr, len(values))
	for i, v := range values {
		items[i] = Param{Value: v}
	}
	return pred(Binary{Op: "IN", Left: f.Column(), Right: List{Items: items}})
}

// NotOneOf is the negation of OneOf. An empty value set excludes nothing.
func (f Field) NotOneOf(values ...any) Pred {
	if len(values) == 0 {
		return pred(Raw{SQL: "true"})
	}
	items := make([]Expr, len(values))
	for i, v := range values {
		items[i] = Param{Value: v}
	}
	return pred(Binary{Op: "NOT IN", Left: f.Column(), Right: List{Items: items}})
}

// Between matches a closed interval.
func (f Field) Between(lo, hi any) Pred {
	return pred(BetweenExpr{Operand: f.Column(), Lo: Param{Value: lo}, Hi: Param{Value: hi}})
}

// NotBetween excludes a closed interval.
func (f Field) NotBetween(lo, hi any) Pred {
	return pred(BetweenExpr{Operand: f.Column(), Lo: Param{Value: lo}, Hi: Param{Value: hi}, Not: true})
}

// Like matches a caller-supplied LIKE pattern. The pattern is a bind
// parameter, but its wildcards are not escaped: prefer Contains, StartsWith or
// EndsWith for values that came from a user.
func (f Field) Like(pattern string) Pred {
	return pred(Binary{Op: "LIKE", Left: f.Column(), Right: Param{Value: pattern}})
}

// ILike is Like, case-insensitively.
func (f Field) ILike(pattern string) Pred {
	return pred(Binary{Op: "ILIKE", Left: f.Column(), Right: Param{Value: pattern}})
}

// Contains matches rows whose column contains v, case-insensitively. Wildcards
// in v are escaped, so it is safe for user input.
func (f Field) Contains(v string) Pred { return f.ILike("%" + escapeLike(v) + "%") }

// StartsWith matches a case-insensitive prefix, with wildcards in v escaped.
func (f Field) StartsWith(v string) Pred { return f.ILike(escapeLike(v) + "%") }

// EndsWith matches a case-insensitive suffix, with wildcards in v escaped.
func (f Field) EndsWith(v string) Pred { return f.ILike("%" + escapeLike(v)) }

// escapeLike neutralises LIKE metacharacters so that a user typing "50%" in a
// search box searches for the literal string rather than a wildcard.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// Cast returns the field cast to a SQL type. The type name is emitted
// verbatim, so it must not come from user input.
func (f Field) Cast(typ string) Expr { return Cast{Inner: f.Column(), Type: typ} }

// Col is a column reference carrying the column's Go type, so that comparands
// are checked at compile time. Generated model packages declare one per column:
//
//	var PostStatus = sqlb.Typed[Status]("status")
//	q.Where(gen.PostStatus.Eq(StatusDraft))   // Eq(42) does not compile
//
// It deliberately does not embed Field. Embedding would promote every operator
// onto every column, so Contains would be callable on an integer — which
// compiles, reaches the database, and fails there. The operators are
// re-declared here instead, and the text-only ones live on TextCol.
type Col[T any] struct {
	f Field
}

// Typed declares a typed column reference.
func Typed[T any](name string) Col[T] { return Col[T]{f: F(name)} }

// Field returns the untyped reference, for the operators the typed surface
// does not cover.
func (c Col[T]) Field() Field { return c.f }

// Name returns the column name without its table qualifier.
func (c Col[T]) Name() string { return c.f.name }

// Qualify attaches a table name to the reference.
func (c Col[T]) Qualify(table string) Col[T] {
	c.f = c.f.Qualify(table)
	return c
}

// Column returns the reference as an expression node.
func (c Col[T]) Column() Column { return c.f.Column() }

func (c Col[T]) exprNode() {}

func (c Col[T]) Eq(v T) Pred           { return c.f.Eq(v) }
func (c Col[T]) Neq(v T) Pred          { return c.f.Neq(v) }
func (c Col[T]) Gt(v T) Pred           { return c.f.Gt(v) }
func (c Col[T]) Gte(v T) Pred          { return c.f.Gte(v) }
func (c Col[T]) Lt(v T) Pred           { return c.f.Lt(v) }
func (c Col[T]) Lte(v T) Pred          { return c.f.Lte(v) }
func (c Col[T]) Between(lo, hi T) Pred { return c.f.Between(lo, hi) }

// EqCol compares two columns of the same type.
func (c Col[T]) EqCol(other Col[T]) Pred { return c.f.EqField(other.f) }

// IsNull matches rows where the column is NULL. It is available on every
// typed column, including those whose Go type is not a pointer, because
// nullability is a property of the column rather than of the comparand.
func (c Col[T]) IsNull() Pred  { return c.f.IsNull() }
func (c Col[T]) NotNull() Pred { return c.f.NotNull() }

// OneOf matches any of the values.
func (c Col[T]) OneOf(values ...T) Pred { return c.f.OneOf(anySlice(values)...) }

// NotOneOf excludes all of the values.
func (c Col[T]) NotOneOf(values ...T) Pred { return c.f.NotOneOf(anySlice(values)...) }

// Asc orders by the column ascending.
func (c Col[T]) Asc() Order { return c.f.Asc() }

// Desc orders by the column descending.
func (c Col[T]) Desc() Order { return c.f.Desc() }

// TextCol is a Col over a string-like column, carrying the pattern operators
// that only make sense there. Generators emit it for text and varchar columns,
// including those with a named string type.
type TextCol[T ~string] struct {
	Col[T]
}

// TextColumn declares a typed text column reference.
func TextColumn[T ~string](name string) TextCol[T] {
	return TextCol[T]{Col: Typed[T](name)}
}

// Contains matches rows whose column contains v, case-insensitively, with
// wildcards in v escaped.
func (c TextCol[T]) Contains(v string) Pred { return c.f.Contains(v) }

// StartsWith matches a case-insensitive prefix, with wildcards in v escaped.
func (c TextCol[T]) StartsWith(v string) Pred { return c.f.StartsWith(v) }

// EndsWith matches a case-insensitive suffix, with wildcards in v escaped.
func (c TextCol[T]) EndsWith(v string) Pred { return c.f.EndsWith(v) }

// Like matches a caller-supplied pattern, whose wildcards are not escaped.
func (c TextCol[T]) Like(pattern string) Pred { return c.f.Like(pattern) }

// ILike is Like, case-insensitively.
func (c TextCol[T]) ILike(pattern string) Pred { return c.f.ILike(pattern) }

func anySlice[T any](vs []T) []any {
	out := make([]any, len(vs))
	for i, v := range vs {
		out[i] = v
	}
	return out
}

// Order is one ORDER BY term.
type Order struct {
	expr  Expr
	desc  bool
	nulls nullsOrder
}

type nullsOrder uint8

const (
	nullsDefault nullsOrder = iota
	nullsFirst
	nullsLast
)

// Asc orders by the column ascending.
func (f Field) Asc() Order { return Order{expr: f.Column()} }

// Desc orders by the column descending.
func (f Field) Desc() Order { return Order{expr: f.Column(), desc: true} }

// NullsFirst places NULLs before other values.
func (o Order) NullsFirst() Order { o.nulls = nullsFirst; return o }

// NullsLast places NULLs after other values.
func (o Order) NullsLast() Order { o.nulls = nullsLast; return o }

// OrderBy orders by an arbitrary expression, ascending.
func OrderBy(e Expr) Order { return Order{expr: e} }

// OrderByDesc orders by an arbitrary expression, descending.
func OrderByDesc(e Expr) Order { return Order{expr: e, desc: true} }

// Selection is one item in a SELECT list: an expression and an optional alias.
type Selection struct {
	expr  Expr
	alias string
}

// As names the selection. The alias must be a plain identifier; it is the name
// the result is scanned into.
func (s Selection) As(alias string) Selection {
	s.alias = alias
	return s
}

// Expr returns the selected expression.
func (s Selection) Expr() Expr { return s.expr }

// Alias returns the selection's alias, which may be empty.
func (s Selection) Alias() string { return s.alias }

// Selectable is anything that can appear in a SELECT list.
type Selectable interface {
	selection() Selection
}

func (s Selection) selection() Selection { return s }
func (f Field) selection() Selection     { return Selection{expr: f.Column()} }
func (c Col[T]) selection() Selection    { return Selection{expr: c.Column()} }

// Sel selects an arbitrary expression.
func Sel(e Expr) Selection { return Selection{expr: e} }

// RawSel selects verbatim SQL with `?` placeholders.
func RawSel(sql string, args ...any) Selection {
	return Selection{expr: Raw{SQL: sql, Args: args}}
}

// Aggregates. Each returns a Selection so it can be aliased inline:
//
//	sqlb.Sum(sqlb.F("total")).As("revenue")

// Count is COUNT(*).
func Count() Selection { return Selection{expr: Call{Name: "count", Star: true}, alias: "count"} }

// CountOf is COUNT(col), which skips NULLs.
func CountOf(f Field) Selection {
	return Selection{expr: Call{Name: "count", Args: []Expr{f.Column()}}, alias: "count"}
}

// CountDistinct is COUNT(DISTINCT col).
func CountDistinct(f Field) Selection {
	return Selection{expr: Call{Name: "count", Args: []Expr{f.Column()}, Distinct: true}, alias: "count"}
}

func agg(name string, f Field) Selection {
	return Selection{expr: Call{Name: name, Args: []Expr{f.Column()}}, alias: name + "_" + f.name}
}

func Sum(f Field) Selection { return agg("sum", f) }
func Avg(f Field) Selection { return agg("avg", f) }
func Min(f Field) Selection { return agg("min", f) }
func Max(f Field) Selection { return agg("max", f) }

// Coalesce returns the first non-NULL argument.
func Coalesce(exprs ...Expr) Selection {
	return Selection{expr: Call{Name: "coalesce", Args: exprs}}
}
