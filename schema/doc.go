// Package schema is the declarative schema DSL for sqlb.
//
// A schema is written as ordinary Go values, which makes it the single source
// of truth for migrations, models, REST handlers and the OpenAPI document:
//
//	var User = schema.Table("users",
//	    schema.UUIDv7("id").PrimaryKey(),
//	    schema.Text("email").Unique().Searchable(),
//	    schema.Int("age").Nullable().Filterable(),
//	    schema.Ref("org", Org).OnDelete(schema.Cascade),
//	    schema.Timestamps(),
//	).Expose(schema.REST{Path: "/users", Ops: schema.CRUD | schema.OpList})
//
// Capabilities such as Filterable and Sortable are opt-in per column. A column
// that does not declare a capability can never be reached through it from the
// REST layer, which is what separates sqlb from exposing the database directly.
package schema
