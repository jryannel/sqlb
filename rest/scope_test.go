package rest_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"
	"github.com/jryannel/sqlb"
	"github.com/jryannel/sqlb/rest"
)

// Scoped is what codegen emits for a table whose tenant column declared
// Scoped: the column is ReadOnly, so it is absent from the create body, and
// carries the marker the mount-time check reads.
type Scoped struct {
	ID        string     `db:"id" json:"id" sqlb:"pk,default,filter,readonly"`
	OrgID     string     `db:"org_id" json:"org_id" sqlb:"filter,readonly,scope"`
	Title     string     `db:"title" json:"title" sqlb:"filter,sort"`
	DeletedAt *time.Time `db:"deleted_at" json:"deleted_at" sqlb:"readonly,softdelete"`
}

func (Scoped) TableName() string { return "scoped" }

type scopedCreate struct {
	Title string `json:"title"`
}

func (c scopedCreate) Row() (*Scoped, error) { return &Scoped{Title: c.Title}, nil }

type scopedUpdate struct {
	Title *string `json:"title,omitempty"`
}

func (u scopedUpdate) Changes() (map[string]any, error) {
	out := map[string]any{}
	if u.Title != nil {
		out["title"] = *u.Title
	}
	return out, nil
}

func scopedOptions() rest.Options {
	return rest.Options{
		Path: "/scoped",
		Name: "scoped",
		Ops:  rest.CRUD | rest.OpList,
	}
}

// mountScoped attempts the mount and returns whatever error it produced.
func mountScoped(t *testing.T, db sqlb.Executor, opts rest.Options) error {
	t.Helper()
	_, api := humatest.New(t, huma.DefaultConfig("Test", "1.0.0"))
	return rest.Resource[Scoped, scopedCreate, scopedUpdate](api, db, opts)
}

// The case the whole check exists for: the table says its rows are confined,
// nobody wrote the hook, and without this the resource would answer 200 with
// every tenant's rows in it.
func TestResourceRefusesAScopedModelWithNoHooks(t *testing.T) {
	db := sqlb.New(newFakeDB(t).db).WithHooks(sqlb.NewRegistry())

	err := mountScoped(t, db, scopedOptions())
	if err == nil {
		t.Fatal("expected mounting to fail: nothing confines a model whose schema says it is confined")
	}

	// Every unmet obligation in one message, each naming the hook that would
	// satisfy it and the declaration that asked.
	for _, want := range []string{
		"BeforeQuery", "BeforeCreate", "BeforeUpdate", "BeforeDelete",
		"org_id is Scoped", "deleted_at declares a soft delete",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v\nwant it to mention %q", err, want)
		}
	}
}

// Registering everything the operations require is what makes the resource
// mountable, and nothing about the hooks' contents is inspected.
func TestResourceAcceptsAScopedModelWithHooks(t *testing.T) {
	reg := sqlb.NewRegistry()
	sqlb.OnIn[Scoped](reg).
		BeforeQuery(func(context.Context, *sqlb.Builder[Scoped]) error { return nil }).
		BeforeCreate(func(context.Context, *Scoped) error { return nil }).
		BeforeUpdate(func(context.Context, *sqlb.Update[Scoped]) error { return nil }).
		BeforeDelete(func(context.Context, *sqlb.Delete[Scoped]) error { return nil })

	db := sqlb.New(newFakeDB(t).db).WithHooks(reg)
	if err := mountScoped(t, db, scopedOptions()); err != nil {
		t.Fatalf("mounting a resource whose hooks are all registered: %v", err)
	}
}

// The obligation follows the operations, so a read-only resource needs one
// registration rather than four. This is the case that would otherwise push
// people towards registering empty hooks to get past the check.
func TestScopeObligationsFollowTheExposedOperations(t *testing.T) {
	reg := sqlb.NewRegistry()
	sqlb.OnIn[Scoped](reg).
		BeforeQuery(func(context.Context, *sqlb.Builder[Scoped]) error { return nil })
	db := sqlb.New(newFakeDB(t).db).WithHooks(reg)

	opts := scopedOptions()
	opts.Ops = rest.OpList | rest.OpRead
	if err := mountScoped(t, db, opts); err != nil {
		t.Fatalf("mounting a read-only resource with a read hook: %v", err)
	}

	// Adding update to the same resource asks a question BeforeQuery does not
	// answer: it constrains what a request can see, not what it can overwrite
	// by id.
	opts.Ops = rest.OpList | rest.OpRead | rest.OpUpdate
	err := mountScoped(t, db, opts)
	if err == nil {
		t.Fatal("expected mounting to fail: an exposed update has no hook narrowing it")
	}
	if !strings.Contains(err.Error(), "BeforeUpdate") {
		t.Errorf("error = %v, want it to name BeforeUpdate", err)
	}
	if strings.Contains(err.Error(), "BeforeQuery") {
		t.Errorf("error = %v, want it to leave the satisfied obligation out", err)
	}
}

// A model declaring neither obligation is unaffected, which is what keeps this
// from being a tax on the schemas that never claimed to be multi-tenant.
func TestUndeclaredModelsMountWithoutHooks(t *testing.T) {
	db := sqlb.New(newFakeDB(t).db).WithHooks(sqlb.NewRegistry())
	_, api := humatest.New(t, huma.DefaultConfig("Test", "1.0.0"))

	if err := rest.Resource[Post, PostCreate, PostUpdate](api, db, postOptions()); err != nil {
		t.Fatalf("mounting an undeclared model: %v", err)
	}
}

// The registry the handle carries is the one that is asked, not the process
// default — otherwise a program that scopes its registry would be told its
// hooks are missing while they run on every query.
func TestScopeCheckReadsTheHandlesRegistry(t *testing.T) {
	sqlb.On[Scoped]().Reset()
	t.Cleanup(func() { sqlb.On[Scoped]().Reset() })

	sqlb.On[Scoped]().BeforeQuery(func(context.Context, *sqlb.Builder[Scoped]) error { return nil })

	opts := scopedOptions()
	opts.Ops = rest.OpList

	// The process default has the hook, and the empty registry attached to the
	// handle does not. The handle wins.
	if err := mountScoped(t, sqlb.New(newFakeDB(t).db), opts); err != nil {
		t.Fatalf("mounting against the process-default registry: %v", err)
	}
	scoped := sqlb.New(newFakeDB(t).db).WithHooks(sqlb.NewRegistry())
	if err := mountScoped(t, scoped, opts); err == nil {
		t.Fatal("expected mounting to fail: the handle's own registry is empty")
	}
}
