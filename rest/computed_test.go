package rest_test

import (
	"context"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"
	"github.com/jryannel/sqlb"
	"github.com/jryannel/sqlb/rest"
)

// Starred is what codegen emits for a table with a per-viewer computed column:
// an ordinary field with an ordinary tag, and the expression in a method.
type Starred struct {
	ID        string `db:"id" json:"id" sqlb:"pk,default,filter,readonly"`
	Title     string `db:"title" json:"title" sqlb:"filter,sort"`
	IsStarred bool   `db:"is_starred" json:"is_starred" sqlb:"filter,readonly"`
}

func (Starred) TableName() string { return "starred" }

func (Starred) ComputedColumns() []sqlb.Computed {
	return []sqlb.Computed{{
		Name:  "is_starred",
		Expr:  "EXISTS (SELECT 1 FROM stars s WHERE s.item_id = starred.id AND s.member_id = ?)",
		Needs: []string{"viewer"},
	}}
}

type starredCreate struct {
	Title string `json:"title"`
}

func (c starredCreate) Row() (*Starred, error) { return &Starred{Title: c.Title}, nil }

type starredUpdate struct {
	Title *string `json:"title,omitempty"`
}

func (u starredUpdate) Changes() (map[string]any, error) {
	out := map[string]any{}
	if u.Title != nil {
		out["title"] = *u.Title
	}
	return out, nil
}

func mountStarred(t *testing.T, db sqlb.Executor) error {
	t.Helper()
	_, api := humatest.New(t, huma.DefaultConfig("Test", "1.0.0"))
	return rest.Resource[Starred, starredCreate, starredUpdate](api, db, rest.Options{
		Path: "/starred",
		Name: "starred",
		Ops:  rest.CRUD | rest.OpList,
	})
}

// The failure this closes: a declared bind nobody supplies. Every list would
// fail at the database, one request at a time, for a reason nothing in the
// handler names — so the resource refuses to mount instead (ADR-0041, and
// ADR-0030's shape).
func TestResourceRefusesAnUnboundComputedColumn(t *testing.T) {
	db := sqlb.New(newFakeDB(t).db).WithHooks(sqlb.NewRegistry())

	err := mountStarred(t, db)
	if err == nil {
		t.Fatal("expected mounting to fail: nothing supplies the viewer bind")
	}
	for _, want := range []string{
		"BeforeQuery",
		`is_starred is computed from the "viewer" bind`,
		// The headline says what is actually missing rather than reaching for
		// the tenant vocabulary, which would send a reader hunting for a scope
		// predicate that was never declared.
		"nothing supplies the computed binds of",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v\nwant it to mention %q", err, want)
		}
	}
}

// A registered BeforeQuery hook satisfies it, and — as everywhere else in this
// check — its contents are not inspected. The query itself is the second line
// of defence: an expression whose bind never arrives fails rather than
// rendering NULL and answering false forever.
func TestResourceAcceptsAComputedColumnWithAHook(t *testing.T) {
	reg := sqlb.NewRegistry()
	sqlb.OnIn[Starred](reg).BeforeQuery(func(_ context.Context, q *sqlb.Builder[Starred]) error {
		q.Bind("viewer", "member-1")
		return nil
	})
	db := sqlb.New(newFakeDB(t).db).WithHooks(reg)

	if err := mountStarred(t, db); err != nil {
		t.Fatalf("mounting a resource whose bind a hook supplies: %v", err)
	}
}
