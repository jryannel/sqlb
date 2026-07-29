// Package blogschema is the schema definition for the blog example: the single
// source of truth that an author, or an agent, edits.
//
// It lives in its own package because the declarations here and the model
// structs generated from them share names — blogschema.Post is the table
// declaration, blog.Post is the row struct. Keeping them apart is what lets
// both be called Post.
package blogschema

import "github.com/jryannel/sqlb/schema"

// Org is a tenant. Everything else hangs off it.
var Org = schema.Table("orgs",
	schema.UUIDv7("id").PrimaryKey(),
	schema.Text("name").Searchable().Sortable(),
	schema.Text("slug").Unique().Filterable(),
	schema.Timestamps(),
).
	Describe("A tenant. Every other table is scoped to one.").
	Expose(schema.REST{Ops: schema.OpRead | schema.OpList})

// Author is a person who writes posts.
var Author = schema.Table("authors",
	schema.UUIDv7("id").PrimaryKey(),
	// Expandable both ways: an author pulls in its org, and an org pulls in its
	// authors. The reverse is capped and reports has_more; a caller wanting all
	// of them follows /authors?org_id=eq.<id>, which is why the column is
	// Filterable. ADR-0022.
	schema.Ref("org", Org).OnDelete(schema.Cascade).Filterable().Expandable().
		Inverse("authors").
		InverseExpandable(schema.ExpandOrder("name")),
	schema.Text("email").Unique().Searchable(),
	schema.Text("name").Searchable().Sortable(),
	// The hash is readable by Go code but never leaves the process, and is not
	// filterable either: a filterable secret can be recovered by probing.
	schema.Text("password_hash").Hidden(),
	schema.Timestamps(),
).
	Index("org_id").
	Expose(schema.REST{
		Path:            "/authors",
		Ops:             schema.CRUD | schema.OpList,
		DefaultPageSize: 25,
		MaxPageSize:     100,
	})

// Post is the table the dynamic data views are built over: filterable by
// status and author, sortable by several columns, full-text searchable, and
// groupable for the dashboard.
var Post = schema.Table("posts",
	schema.UUIDv7("id").PrimaryKey(),
	schema.Ref("org", Org).OnDelete(schema.Cascade),
	schema.Ref("author", Author).OnDelete(schema.Restrict).Filterable().Expandable().
		Inverse("posts").
		InverseExpandable(schema.ExpandOrder("-published_at"), schema.ExpandLimit(2)),

	schema.Text("title").Searchable().Sortable(),
	schema.Text("body").Searchable(),
	schema.Enum("status", "draft", "review", "published").
		Default(schema.Value("draft")).
		Filterable().
		Sortable(),

	schema.BigInt("view_count").Default(schema.Value(0)).Filterable().Sortable().ReadOnly(),
	schema.Timestamp("published_at").Nullable().Filterable().Sortable(),

	schema.Timestamps(),
	schema.SoftDelete(),
).
	Index("org_id", "status").
	Index("author_id").
	Check("published_posts_have_a_date",
		"status <> 'published' OR published_at IS NOT NULL").
	Describe("A blog post.").
	// OpDelete is deliberately absent. The generated delete is a real DELETE,
	// and this table declares SoftDelete — blog.RegisterPostSoftDelete serves
	// DELETE /posts/{id} as an update to deleted_at instead.
	Expose(schema.REST{
		Path:            "/posts",
		Ops:             schema.OpCreate | schema.OpRead | schema.OpUpdate | schema.OpList,
		DefaultPageSize: 20,
		MaxPageSize:     100,
		MaxFilters:      12,
	})
