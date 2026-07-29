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
// own is the target's own table name: a hook that qualified explicitly wrote
// the table, and inside the join that same reference has to become the alias.
func qualifyPreds(preds []Pred, own, alias string) ([]Pred, error) {
	out := make([]Pred, 0, len(preds))
	for _, p := range preds {
		if p.IsZero() {
			continue
		}
		e, err := qualifyExpr(p.Expr(), own, alias)
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
func qualifyExpr(e Expr, own, alias string) (Expr, error) {
	switch n := e.(type) {
	case nil:
		return nil, fmt.Errorf("sqlb: nil expression in an expansion scope predicate")

	case Column:
		return qualifyColumn(n, own, alias)

	case Field:
		col, err := qualifyColumn(n.Column(), own, alias)
		if err != nil {
			return nil, err
		}
		return col, nil

	case Param:
		return n, nil

	case List:
		items := make([]Expr, len(n.Items))
		for i, item := range n.Items {
			q, err := qualifyExpr(item, own, alias)
			if err != nil {
				return nil, err
			}
			items[i] = q
		}
		return List{Items: items}, nil

	case Binary:
		left, err := qualifyExpr(n.Left, own, alias)
		if err != nil {
			return nil, err
		}
		right, err := qualifyExpr(n.Right, own, alias)
		if err != nil {
			return nil, err
		}
		return Binary{Op: n.Op, Left: left, Right: right}, nil

	case Unary:
		operand, err := qualifyExpr(n.Operand, own, alias)
		if err != nil {
			return nil, err
		}
		return Unary{Op: n.Op, Operand: operand, Postfix: n.Postfix}, nil

	case BetweenExpr:
		operand, err := qualifyExpr(n.Operand, own, alias)
		if err != nil {
			return nil, err
		}
		lo, err := qualifyExpr(n.Lo, own, alias)
		if err != nil {
			return nil, err
		}
		hi, err := qualifyExpr(n.Hi, own, alias)
		if err != nil {
			return nil, err
		}
		return BetweenExpr{Operand: operand, Lo: lo, Hi: hi, Not: n.Not}, nil

	case Call:
		args := make([]Expr, len(n.Args))
		for i, a := range n.Args {
			q, err := qualifyExpr(a, own, alias)
			if err != nil {
				return nil, err
			}
			args[i] = q
		}
		return Call{Name: n.Name, Args: args, Star: n.Star, Distinct: n.Distinct}, nil

	case Cast:
		inner, err := qualifyExpr(n.Inner, own, alias)
		if err != nil {
			return nil, err
		}
		return Cast{Inner: inner, Type: n.Type}, nil

	case Raw:
		// The one node that cannot be rewritten, because its contents are
		// opaque text this package never parsed. Splicing it in unchanged would
		// resolve its bare names to the parent table, which is the silent wrong
		// answer described above.
		return nil, fmt.Errorf(
			"sqlb: a BeforeQuery hook on the expansion target uses raw SQL (%q), "+
				"which cannot be requalified onto the join alias; write the scope "+
				"with F() and the comparison operators, or give the expansion a "+
				"composite foreign key that carries the scoping column so no "+
				"predicate is needed", truncate(n.SQL, 120))

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
func qualifyColumn(col Column, own, alias string) (Expr, error) {
	switch col.Table {
	case "", own, alias:
		return Column{Table: alias, Name: col.Name}, nil
	default:
		return nil, fmt.Errorf(
			"sqlb: a BeforeQuery hook on the expansion target constrains %q.%q, "+
				"which the expansion does not join; an expansion carries the "+
				"target's own predicates and not the joins a hook added to reach "+
				"them", col.Table, col.Name)
	}
}
