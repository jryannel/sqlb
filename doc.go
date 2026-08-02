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
