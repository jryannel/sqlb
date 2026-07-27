package app

import (
	"context"

	"github.com/jryannel/sqlb"
	"github.com/jryannel/sqlb/example/tasks"
	"github.com/jryannel/sqlb/example/tasks/auth"
)

// The workspace boundary.
//
// This file is the reason the example exists. Everything a request may read or
// write is confined to one workspace, and the confinement is expressed once per
// model here rather than once per handler, once per query, and once per
// background job — which is the arrangement that eventually leaks, because it
// only takes one call site written on a Friday.
//
// It works because BeforeQuery is handed the *query*. A hook that could only
// veto would have to be given the rows, which means fetching them first; a hook
// that is given the builder can add a predicate, and the database never sees
// the other workspace's rows at all. That is also why the generated REST
// handlers need to know nothing: they call sqlb.Query[T], and so does everyone
// else.
//
// # Fail closed
//
// Every hook here returns an error when the context carries no claims. It never
// falls back to "no restriction", which is the shape of most tenancy bugs: the
// unauthenticated path is the one nobody tests, and an unscoped query there
// returns every tenant's rows with a 200 next to it. Refusing means the worst
// case is a broken endpoint, which is noticed.

// Register installs the hooks into a registry and returns it.
//
// A scoped registry rather than the process-wide On[T]() default: two servers
// in one test binary would otherwise share hook registrations, and the second
// one's Register would stack a duplicate set of predicates onto the first.
// Handing the registry to the handle with WithHooks keeps each server's rules
// its own.
func Register() *sqlb.Registry {
	reg := sqlb.NewRegistry()

	// Reads. Every workspace-scoped model gets the same treatment, and the
	// models with a deleted_at get the soft-delete predicate too.
	//
	// Nothing in sqlb applies a soft-delete filter on its own — schema.SoftDelete
	// adds the column and stops there — so a model that declares one and does
	// not filter it returns deleted rows from every list endpoint. Doing it here
	// means it is done once, for reads issued by anyone.
	scopeReads[tasks.List](reg, softDeleted)
	scopeReads[tasks.Task](reg, softDeleted)
	scopeReads[tasks.Comment](reg, softDeleted)
	scopeReads[tasks.Membership](reg, hardDeleted)

	// Workspaces are scoped by identity rather than by a workspace_id column:
	// the row *is* the tenant. Without this, GET /workspaces would list every
	// tenant in the installation, which is the kind of hole that a schema-level
	// convention silently leaves behind when one table does not follow it.
	sqlb.OnIn[tasks.Workspace](reg).BeforeQuery(func(ctx context.Context, q *sqlb.Builder[tasks.Workspace]) error {
		workspace, err := workspaceOf(ctx)
		if err != nil {
			return err
		}
		q.Where(sqlb.F("id").Eq(workspace))
		return nil
	})

	// Users are global — one account reaches every workspace it is a member of
	// — so "which users may I see" is a question about memberships, and the
	// predicate has to reach another table.
	//
	// RawPred is the escape hatch for what the builder cannot model, and this is
	// what it is for. The identifiers are literals in this file rather than
	// anything derived from a request, and the one value is a bind parameter, so
	// the usual objection to hand-written SQL does not apply. It is the honest
	// version of the alternative — fetching the member ids first and passing
	// them to In() — which is the same query split in two with a race in the
	// middle.
	sqlb.OnIn[tasks.User](reg).BeforeQuery(func(ctx context.Context, q *sqlb.Builder[tasks.User]) error {
		workspace, err := workspaceOf(ctx)
		if err != nil {
			return err
		}
		q.Where(sqlb.RawPred(
			`"id" IN (SELECT "user_id" FROM "memberships" WHERE "workspace_id" = ?)`,
			workspace))
		return nil
	})

	// Writes. An UPDATE or DELETE gets the same workspace predicate, which is a
	// separate registration because it is a separate statement: a BeforeQuery
	// hook constrains what a request can *see*, and says nothing about what it
	// can overwrite by id.
	scopeWrites[tasks.List](reg)
	scopeWrites[tasks.Task](reg)
	scopeWrites[tasks.Comment](reg)
	scopeWrites[tasks.Membership](reg)

	// Creates. The columns a client is not allowed to assert are stamped from
	// the token. They are ReadOnly in the schema, so they are absent from the
	// generated request bodies — the hook is not overriding a value the caller
	// sent, it is supplying the only value there is.
	sqlb.OnIn[tasks.List](reg).BeforeCreate(func(ctx context.Context, l *tasks.List) error {
		c, err := claimsOrError(ctx)
		if err != nil {
			return err
		}
		l.WorkspaceID = c.Workspace
		return nil
	})

	sqlb.OnIn[tasks.Task](reg).BeforeCreate(func(ctx context.Context, t *tasks.Task) error {
		c, err := claimsOrError(ctx)
		if err != nil {
			return err
		}
		t.WorkspaceID = c.Workspace
		t.AuthorID = c.Subject
		return nil
	})

	sqlb.OnIn[tasks.Comment](reg).BeforeCreate(func(ctx context.Context, cm *tasks.Comment) error {
		c, err := claimsOrError(ctx)
		if err != nil {
			return err
		}
		cm.WorkspaceID = c.Workspace
		cm.AuthorID = c.Subject
		return nil
	})

	sqlb.OnIn[tasks.Membership](reg).BeforeCreate(func(ctx context.Context, m *tasks.Membership) error {
		c, err := claimsOrError(ctx)
		if err != nil {
			return err
		}
		// Inviting somebody is an administrative act, so this is the one place
		// the role in the token is read for a decision rather than carried
		// along. A member may list the workspace's memberships and may not add
		// to them.
		if !c.AtLeast(auth.RoleAdmin) {
			return errForbidden("adding a member needs the admin role")
		}
		m.WorkspaceID = c.Workspace
		return nil
	})

	return reg
}

// softDeleted and hardDeleted name the two shapes scopeReads handles, because
// scopeReads(reg, true) at the call site says nothing about what is true.
const (
	softDeleted = true
	hardDeleted = false
)

// scopeReads confines every SELECT against T to the caller's workspace.
//
// It is generic over the model because the predicate is the same for all of
// them — the column is called workspace_id everywhere, which is a convention
// this schema keeps deliberately so that the boundary can be one function
// instead of four near-copies.
func scopeReads[T any](reg *sqlb.Registry, soft bool) {
	sqlb.OnIn[T](reg).BeforeQuery(func(ctx context.Context, q *sqlb.Builder[T]) error {
		workspace, err := workspaceOf(ctx)
		if err != nil {
			return err
		}
		q.Where(sqlb.F("workspace_id").Eq(workspace))
		if soft {
			q.Where(sqlb.F("deleted_at").IsNull())
		}
		return nil
	})
}

// scopeWrites confines every UPDATE and DELETE against T to the caller's
// workspace.
//
// The predicate is added rather than checked, so a PATCH naming a task id in
// another workspace matches no rows and comes back 404 — the same answer the
// caller would get for an id that does not exist, which is the answer they
// should get. A check would have to read the row first, and would then have to
// decide between 403 and 404; adding the predicate makes the question not
// arise.
//
// This also satisfies sqlb's guard against unscoped mutations: an UPDATE with
// no WHERE is refused rather than executed, so a handler that forgets its own
// predicate is stopped by this one rather than rewriting the table.
func scopeWrites[T any](reg *sqlb.Registry) {
	hooks := sqlb.OnIn[T](reg)

	hooks.BeforeUpdate(func(ctx context.Context, u *sqlb.Update[T]) error {
		workspace, err := workspaceOf(ctx)
		if err != nil {
			return err
		}
		u.Where(sqlb.F("workspace_id").Eq(workspace))
		return nil
	})

	hooks.BeforeDelete(func(ctx context.Context, d *sqlb.Delete[T]) error {
		workspace, err := workspaceOf(ctx)
		if err != nil {
			return err
		}
		d.Where(sqlb.F("workspace_id").Eq(workspace))
		return nil
	})
}
