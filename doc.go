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
// Go and by parsed URL filter expressions. A value also nests, so a set the
// database computes is a query rather than a list of values:
//
//	sub, err := sqlb.Query[Post]().Select(sqlb.F("author_id")).Resolved(ctx, db)
//	q := sqlb.Query[Author]().Where(sqlb.F("id").InQuery(sub))
//
// Resolving the inner query first is required whenever its model's reads are
// confined by a hook, because nesting compiles a query rather than running it.
// See [Subquery].
//
// Values never reach the SQL text. Every user-supplied value becomes a bind
// parameter; only identifiers validated against the model are interpolated.
package sqlb
