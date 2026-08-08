package sqlb

import "fmt"

// Rewriting a hook's predicates to name a join alias.
//
// A BeforeQuery hook writes `sqlb.F("workspace_id")` — a bare column, because
// the query it was written for has one table and bare is what reads well there.
// Inside an expansion the same predicate has to say `"__ex_list"."workspace_id"`,
// and the difference is not cosmetic: the compiler resolves a bare name to the
// statement's base table, so an unrequalified predicate would silently filter
// the *parent* by the target's column. That is a wrong answer rather than an
// error, which is why this exists rather than the predicates being spliced in
// as they stand.
//
// The rewrite is total or it fails. A predicate this cannot requalify with
// certainty is refused, and the expansion fails with it — an expansion that
// dropped a scope predicate it did not understand would be the leak this whole
// change is closing, arriving by a different route.

// qualifyPreds rewrites every predicate to name alias, or reports the first one
// it cannot.
//
// target is the expansion target's model. Its table name is what a hook that
// qualified explicitly wrote, and inside the join that same reference has to
// become the alias; its derived columns are the references that cannot become
// anything at all — see qualifyColumn.
func qualifyPreds(preds []Pred, target *Model, alias string) ([]Pred, error) {
	out := make([]Pred, 0, len(preds))
	for _, p := range preds {
		if p.IsZero() {
			continue
		}
		e, err := qualifyExpr(p.Expr(), target, alias)
		if err != nil {
			return nil, err
		}
		out = append(out, pred(e))
	}
	return out, nil
}

// qualifyExpr rebuilds an expression with every column reference pointing at
// alias. It returns a new tree rather than mutating: the predicates belong to
// the hook's throwaway builder, but Raw's Args slice and List's Items are
// shared structure and a hook may hold a Pred it registered once.
func qualifyExpr(e Expr, target *Model, alias string) (Expr, error) {
	switch n := e.(type) {
	case nil:
		return nil, fmt.Errorf("sqlb: nil expression in an expansion scope predicate")

	case Column:
		return qualifyColumn(n, target, alias)

	case Field:
		col, err := qualifyColumn(n.Column(), target, alias)
		if err != nil {
			return nil, err
		}
		return col, nil

	case Param:
		return n, nil

	case List:
		items := make([]Expr, len(n.Items))
		for i, item := range n.Items {
			q, err := qualifyExpr(item, target, alias)
			if err != nil {
				return nil, err
			}
			items[i] = q
		}
		return List{Items: items}, nil

	case Binary:
		left, err := qualifyExpr(n.Left, target, alias)
		if err != nil {
			return nil, err
		}
		right, err := qualifyExpr(n.Right, target, alias)
		if err != nil {
			return nil, err
		}
		return Binary{Op: n.Op, Left: left, Right: right}, nil

	case Unary:
		operand, err := qualifyExpr(n.Operand, target, alias)
		if err != nil {
			return nil, err
		}
		return Unary{Op: n.Op, Operand: operand, Postfix: n.Postfix}, nil

	case BetweenExpr:
		operand, err := qualifyExpr(n.Operand, target, alias)
		if err != nil {
			return nil, err
		}
		lo, err := qualifyExpr(n.Lo, target, alias)
		if err != nil {
			return nil, err
		}
		hi, err := qualifyExpr(n.Hi, target, alias)
		if err != nil {
			return nil, err
		}
		return BetweenExpr{Operand: operand, Lo: lo, Hi: hi, Not: n.Not}, nil

	case Call:
		args := make([]Expr, len(n.Args))
		for i, a := range n.Args {
			q, err := qualifyExpr(a, target, alias)
			if err != nil {
				return nil, err
			}
			args[i] = q
		}
		return Call{Name: n.Name, Args: args, Star: n.Star, Distinct: n.Distinct}, nil

	case Cast:
		inner, err := qualifyExpr(n.Inner, target, alias)
		if err != nil {
			return nil, err
		}
		return Cast{Inner: inner, Type: n.Type}, nil

	case Raw:
		// The one node that cannot be rewritten, because its contents are
		// opaque text this package never parsed. Splicing it in unchanged would
		// resolve its bare names to the parent table, which is the silent wrong
		// answer described above.
		//
		// The remedies are ordered by how often they apply, and the third one is
		// here because the first two do not cover the commonest raw scope there
		// is. A child table scoped through its parent — `cart_lines` belongs to a
		// `carts` row, and the cart belongs to a session — confines with
		// `cart_id IN (SELECT id FROM carts WHERE session_id = ?)`, and there is
		// no F() spelling for a subquery: the predicate vocabulary is value
		// comparison and column-to-column, and Builder.Exists is a terminal, not
		// a node. Sending that reader to "write it with F()" costs them the
		// search before they conclude they have to denormalise the scope column
		// onto the child — which is the one column whose duplication is a leak
		// (#158). Saying so is cheaper than the search.
		return nil, fmt.Errorf(
			"sqlb: a BeforeQuery hook on the expansion target uses raw SQL (%q), "+
				"which cannot be requalified onto the join alias. Three ways out, in "+
				"the order they usually apply: write the scope with F() and the "+
				"comparison operators, if the confinement is a comparison against a "+
				"column of this table; give the expansion a composite foreign key "+
				"that carries the scoping column, so no predicate is needed; or, if "+
				"the scope is a subquery — a membership test, a scope inherited from "+
				"a parent row — accept that it cannot be requalified and do not "+
				"expose this table for expansion, reaching its rows through the "+
				"parent's endpoint instead", truncate(n.SQL, 120))

	default:
		return nil, fmt.Errorf(
			"sqlb: a BeforeQuery hook on the expansion target uses %T, which this "+
				"package cannot requalify onto a join alias", e)
	}
}

// qualifyColumn points one column reference at the alias.
//
// A reference already qualified with a *different* table is refused rather than
// rewritten: it names something the expansion did not join — a table the hook
// added with Join, most likely — and pointing it at the alias would silently
// change which rows it constrains.
//
// A derived column is refused for the opposite reason: there is nothing to
// point at. A computed column has no storage, so `"__ex_tasks"."is_overdue"` is
// a column the database does not have, and the request used to fail with a bare
// Postgres 42703 naming a column the schema plainly declares — at request time,
// and only when the hooked model was *expanded*, so every direct-read test
// passed (#76). Substituting the expression is not the alternative it looks
// like: a computed expression is opaque SQL text whose own column references
// this package never parsed, which is the same reason Raw is refused above.
func qualifyColumn(col Column, target *Model, alias string) (Expr, error) {
	if target.byDerived[col.Name] != nil {
		switch col.Table {
		case "", target.Table, alias:
			return nil, fmt.Errorf(
				"sqlb: a BeforeQuery hook on the expansion target constrains %q, "+
					"which %s declares as a computed column; a computed column has no "+
					"storage to qualify onto the join alias, so the predicate cannot "+
					"be carried across the expansion — scope on a stored column, or "+
					"do not expand this relation",
				col.Name, target.Type.Name())
		}
	}
	switch col.Table {
	case "", target.Table, alias:
		return Column{Table: alias, Name: col.Name}, nil
	default:
		return nil, fmt.Errorf(
			"sqlb: a BeforeQuery hook on the expansion target constrains %q.%q, "+
				"which the expansion does not join; an expansion carries the "+
				"target's own predicates and not the joins a hook added to reach "+
				"them", col.Table, col.Name)
	}
}
