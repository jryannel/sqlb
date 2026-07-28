// Package taskschema is the schema definition for the task-manager example:
// the single source of truth that an author, or an agent, edits.
//
// It lives in its own package because the declarations here and the model
// structs generated from them share names — taskschema.Task is the table
// declaration, tasks.Task is the row struct. Keeping them apart is what lets
// both be called Task.
//
// # What this example is for
//
// example/blog shows the shortest path from a schema to a server. This one
// shows what the same machinery looks like once the application has a real
// shape: six tables, a tenant boundary that must hold, and an authentication
// story. The parts worth reading for are the ones blog cannot demonstrate —
//
//   - a workspace boundary enforced by one BeforeQuery registration per model
//     rather than by every handler and every call site remembering;
//   - JWT claims arriving from HTTP middleware and reaching the query layer
//     through the context, which is the only channel a hook has;
//   - hand-written endpoints (register, login) alongside generated CRUD, on
//     the same router and in the same OpenAPI document;
//   - a migration history generated from this file and applied by goose.
package taskschema

// The output directory is passed explicitly because go generate runs this with
// the working directory set to taskschema, not to the module root.
//
//go:generate go run ../cmd/gen -dir ..

import "github.com/jryannel/sqlb/schema"

// Workspace is the tenant. Every other table except User is scoped to one, and
// the scoping is enforced in hooks rather than in handlers.
var Workspace = schema.Table("workspaces",
	// Scoped on the key, because on this table the row *is* the tenant. There
	// is no workspace_id to point at, and a convention that only covers the
	// tables carrying the column would leave GET /workspaces listing every
	// tenant in the installation — which is the hole a schema-level convention
	// silently leaves behind when one table does not follow it.
	schema.UUIDv7("id").PrimaryKey().Scoped(),
	schema.Text("name").Searchable().Sortable(),
	schema.Text("slug").Unique().Filterable(),
	schema.Timestamps(),
).
	Describe("A tenant. Lists, tasks and comments all belong to exactly one.").
	Expose(schema.REST{Ops: schema.OpRead | schema.OpList})

// User is a person. Users are global rather than per-workspace — one login
// reaches every workspace the user is a member of — so this is the one table
// the workspace hook does not scope.
var User = schema.Table("users",
	// The one table with no workspace column, and still scoped: which users a
	// caller may see is a question about memberships, so the hook narrows the
	// key with a subquery rather than comparing a column. The declaration goes
	// where the predicate lands, which is here.
	schema.UUIDv7("id").PrimaryKey().Scoped(),
	schema.Text("email").Unique().Searchable(),
	schema.Text("name").Searchable().Sortable(),

	// Never leaves the process. Hidden also means not filterable: a filterable
	// secret can be recovered a character at a time by probing, which is why
	// the schema validator rejects a column that declares both.
	schema.Text("password_hash").Hidden(),

	schema.Timestamps(),
).
	// No OpCreate: accounts are made by POST /auth/register, which also creates
	// the first workspace and hashes the password. Generated CRUD would let a
	// caller write password_hash directly — except that it cannot, because the
	// column is Hidden, which would instead produce an account nobody can log
	// in to. Either way the create belongs in a hand-written handler.
	Expose(schema.REST{Ops: schema.OpRead | schema.OpList, MaxPageSize: 100})

// Membership is what makes a user part of a workspace, and carries the role
// that authorisation reads. It is also the table the login endpoint consults to
// decide which workspaces a token may be issued for.
var Membership = schema.Table("memberships",
	schema.UUIDv7("id").PrimaryKey(),

	// ReadOnly on every workspace_id in this schema, and it is the single most
	// load-bearing word in the file.
	//
	// ReadOnly keeps a column out of the generated create and patch bodies, so
	// no request can name the workspace it is writing into — and leaves the
	// BeforeCreate hook free to supply it from the verified token. The column
	// appears nowhere in the OpenAPI document as an input, and a client that
	// sends one is not silently overruled, because there is nothing to send.
	//
	// The alternative, Immutable, keeps it out of the patch body only. That
	// closes the worse hole (a task moved between workspaces after the fact)
	// and leaves a required create field the server ignores.
	schema.Ref("workspace", Workspace).OnDelete(schema.Cascade).Filterable().ReadOnly().Scoped(),

	schema.Ref("user", User).OnDelete(schema.Cascade).Filterable(),
	schema.Enum("role", "owner", "admin", "member").
		Default(schema.Value("member")).
		Filterable().
		Sortable(),
	schema.Timestamps(),
).
	// One membership per user per workspace. This is the constraint the
	// register and invite paths both rely on rather than checking first.
	UniqueIndex("workspace_id", "user_id").
	Describe("A user's membership of a workspace, and their role in it.").
	Expose(schema.REST{
		Path:            "/memberships",
		Ops:             schema.OpRead | schema.OpList | schema.OpCreate | schema.OpDelete,
		DefaultPageSize: 25,
		MaxPageSize:     100,
	})

// List is a task list — the "different task lists" a workspace organises work
// into. Archiving is a flag rather than a delete so that a list's tasks keep
// their home.
var List = schema.Table("lists",
	schema.UUIDv7("id").PrimaryKey(),
	// ReadOnly, supplied by the BeforeCreate hook; see the note on Membership.
	schema.Ref("workspace", Workspace).OnDelete(schema.Cascade).Filterable().ReadOnly().Scoped(),

	schema.Text("name").Searchable().Sortable(),
	schema.Text("description").Searchable(),
	schema.Text("color").Default(schema.Value("#6b7280")).Filterable(),
	schema.Int("position").Default(schema.Value(0)).Sortable(),
	schema.Bool("archived").Default(schema.Value(false)).Filterable().Sortable(),

	schema.Timestamps(),
	schema.SoftDelete(),
).
	Index("workspace_id", "archived").
	// Two lists in one workspace may not share a name. Across workspaces they
	// may, which is why the index is composite rather than on name alone.
	UniqueIndex("workspace_id", "name").
	// Redundant on its own — id is already unique — and declared anyway,
	// because a composite FOREIGN KEY needs a unique constraint covering
	// exactly the columns it references. cmd/migrate adds
	// tasks (workspace_id, list_id) → lists (workspace_id, id), which is what
	// makes it impossible for a task to point at a list in another workspace.
	// The DSL cannot express a two-column foreign key; it can express the index
	// one needs, so half of this lives here and half in the migration.
	UniqueIndex("workspace_id", "id").
	Describe("A named list of tasks within a workspace.").
	// No OpDelete on any table that declares SoftDelete, here or below.
	//
	// schema.SoftDelete adds a deleted_at column and nothing else: the generated
	// DELETE handler issues a real DELETE, and no part of sqlb filters the
	// column back out of reads. A table that declared a soft delete and exposed
	// the generated one would therefore hard-delete through an endpoint whose
	// schema says otherwise, which is worse than not having the feature.
	//
	// So the two halves are supplied here instead: the BeforeQuery hooks in
	// app/hooks.go filter deleted_at, and app/deletes.go serves DELETE as an
	// UPDATE. Both are a few lines, and both are visible.
	Expose(schema.REST{
		Path:            "/lists",
		Ops:             schema.OpCreate | schema.OpRead | schema.OpUpdate | schema.OpList,
		DefaultPageSize: 25,
		MaxPageSize:     100,
	})

// Task is the table the dynamic data views are built over, and the reason the
// filter grammar exists: filterable by list, assignee, status, priority and due
// date, sortable by most of the same, and searchable over title and
// description.
var Task = schema.Table("tasks",
	schema.UUIDv7("id").PrimaryKey(),
	// ReadOnly, supplied by the BeforeCreate hook; see the note on Membership.
	schema.Ref("workspace", Workspace).OnDelete(schema.Cascade).Filterable().ReadOnly().Scoped(),
	// Expandable in both directions, which are two decisions about two
	// endpoints: ?expand=list on a task pulls in the one list it belongs to,
	// and ?expand=tasks on a list pulls in the tasks that point back at it.
	//
	// The reverse is declared here, on the side that already owns the column
	// and the constraint, and it is capped: a list with two hundred tasks
	// returns twenty and says has_more, and a caller wanting the rest follows
	// /tasks?list_id=eq.<id>, which is the endpoint that already pages and
	// filters. Ordering by position is what the screen wants, and
	// Index("list_id", "position") below is what makes it cheap. ADR-0022.
	schema.Ref("list", List).OnDelete(schema.Cascade).Filterable().Expandable().
		Inverse("tasks").
		InverseExpandable(schema.ExpandOrder("position"), schema.ExpandLimit(20)),

	// Nullable: an unassigned task is the normal state of a new one. The column
	// is typed *string on the model and Col[string] on the typed facade, so
	// "unassigned" is written ?assignee_id=isnull rather than by comparing
	// against a null pointer.
	schema.Ref("assignee", User).OnDelete(schema.SetNull).Nullable().Filterable(),

	// Who filed it. ReadOnly for the same reason workspace_id is: authorship is
	// the token's subject, not something a request gets to assert, so it belongs
	// in neither the create body nor the patch body.
	schema.Ref("author", User).OnDelete(schema.Restrict).ReadOnly(),

	schema.Text("title").Searchable().Sortable(),
	schema.Text("description").Searchable(),

	schema.Enum("status", "todo", "in_progress", "blocked", "done").
		Default(schema.Value("todo")).
		Filterable().
		Sortable(),
	schema.Enum("priority", "low", "medium", "high", "urgent").
		Default(schema.Value("medium")).
		Filterable().
		Sortable(),

	schema.Timestamp("due_at").Nullable().Filterable().Sortable(),

	// Owned by the BeforeUpdate hook that watches status, not by the client:
	// a request that could set status=done and completed_at=null independently
	// would be able to write a state the check constraint below forbids.
	schema.Timestamp("completed_at").Nullable().ReadOnly().Filterable().Sortable(),

	schema.Int("position").Default(schema.Value(0)).Sortable(),

	// Incremented by AddCommentCount in task_ext.go, never by a client, which
	// is what ReadOnly says: there is no SetCommentCount on the patch body.
	schema.Int("comment_count").Default(schema.Value(0)).Filterable().Sortable().ReadOnly(),

	schema.Timestamps(),
	schema.SoftDelete(),
).
	Index("workspace_id", "status").
	Index("list_id", "position").
	Index("assignee_id").
	Index("due_at").
	// The other half of a tenant-safe composite reference, for comments. See
	// the note on List.
	UniqueIndex("workspace_id", "id").
	// The invariant the hook maintains, stated where the database can enforce
	// it too. A hook is a convention; a check constraint is a guarantee, and
	// the two disagreeing is exactly what a demo should not hide.
	Check("done_tasks_have_a_completion_time",
		"status <> 'done' OR completed_at IS NOT NULL").
	Describe("A unit of work, belonging to one list.").
	Expose(schema.REST{
		Path:            "/tasks",
		Ops:             schema.OpCreate | schema.OpRead | schema.OpUpdate | schema.OpList,
		DefaultPageSize: 20,
		MaxPageSize:     100,
		MaxFilters:      12,
	})

// Comment is a note on a task. It exists mainly to give the demo a second
// write path into the same transaction: creating one bumps the task's
// comment_count, and both must land together or not at all.
var Comment = schema.Table("comments",
	schema.UUIDv7("id").PrimaryKey(),
	// ReadOnly, supplied by the BeforeCreate hook; see the note on Membership.
	schema.Ref("workspace", Workspace).OnDelete(schema.Cascade).Filterable().ReadOnly().Scoped(),
	schema.Ref("task", Task).OnDelete(schema.Cascade).Filterable().Immutable(),
	schema.Ref("author", User).OnDelete(schema.Restrict).Filterable().ReadOnly(),

	schema.Text("body").Searchable(),

	schema.Timestamps(),
	schema.SoftDelete(),
).
	Index("task_id", "created_at").
	Describe("A comment on a task.").
	// OpCreate is exposed, and it was not always.
	//
	// Creating a comment has an invariant attached: the task's comment_count
	// must move with it, and the two writes must land together or not at all.
	// While generated writes ran under autocommit a generated handler could not
	// carry that, so the create was a hand-written endpoint and this table was
	// deliberately not exposed for it — exposing both would have left two ways
	// to create a comment, one of which quietly left the counter wrong.
	//
	// `rest` now wraps a generated write in a transaction, which changes where
	// the invariant can live rather than merely making the old endpoint
	// shorter. A hook receives a context carrying that transaction, so
	// `BeforeCreate` can check the task and `AfterCreate` can move the counter,
	// both inside the unit of work that writes the row. The invariant belongs to
	// the *model* now, not to one route — so every path that creates a comment
	// maintains it, including one written later by someone who has not read
	// this comment. That is a stronger guarantee than the endpoint ever gave,
	// and app/comments.go is gone.
	//
	// Still no OpUpdate: editing a comment is a domain question this example
	// does not answer, not a mechanical one.
	Expose(schema.REST{
		Path:            "/comments",
		Ops:             schema.OpCreate | schema.OpRead | schema.OpList,
		DefaultPageSize: 50,
		MaxPageSize:     100,
	})
