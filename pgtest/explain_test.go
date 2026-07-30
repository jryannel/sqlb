package pgtest

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/jryannel/sqlb"
	"github.com/jryannel/sqlb/example/blog"
	"github.com/jryannel/sqlb/filter"
	"github.com/jryannel/sqlb/schema"

	// Imported for its side effects: declaring a table registers it.
	_ "github.com/jryannel/sqlb/example/blogschema"
)

// This is the practice the README describes, run against the worked example:
// every query shape a resource can produce is planned by Postgres without being
// executed, so a column that does not exist, or a type that no longer matches,
// fails the build.
//
// It is what sqlb offers *instead of* sqlc's compile-time column checking, not
// in addition to it. sqlb's predicates are deliberately untyped
// (ADR-0009), so `sqlb.F("titel")` compiles and fails at runtime. Explain moves
// that failure back to the gate, using the database's own opinion rather than a
// second model of it.

// blogDB applies the blog example's schema to a fresh database.
func blogDB(t *testing.T) *sqlb.DB {
	t.Helper()
	db := freshDB(t)
	applySchema(t, db, schema.DefaultRegistry())
	return sqlb.New(db)
}

// mustPlan is the assertion: the statement is valid against the live schema.
// Explain does not execute, so this is safe on mutations.
func mustPlan(t *testing.T, ctx context.Context, db sqlb.Executor, name string, q sqlb.Compilable) {
	t.Helper()
	if _, err := sqlb.Explain(ctx, db, q); err != nil {
		t.Errorf("%s does not plan against the live schema: %v", name, err)
	}
}

// listShapes returns the query shapes a resource's list endpoint can produce.
// Driving them through filter.Parse rather than building them by hand is the
// point: these are the shapes an HTTP request actually reaches, so a capability
// declared in the schema but broken in the database is caught here.
func listShapes[T any](t *testing.T, name string, queries []url.Values) map[string]*sqlb.Builder[T] {
	t.Helper()
	out := make(map[string]*sqlb.Builder[T], len(queries))
	for _, values := range queries {
		parsed, err := filter.Parse(values, filter.Options{Model: sqlb.ModelOf[T]()})
		if err != nil {
			t.Fatalf("%s: parsing %v: %v", name, values, err)
		}
		out[name+" ?"+values.Encode()] = filter.Apply(sqlb.Query[T](), parsed)
	}
	return out
}

func TestEveryPostQueryShapePlans(t *testing.T) {
	ctx := context.Background()
	db := blogDB(t)

	shapes := listShapes[blog.Post](t, "posts", []url.Values{
		{},
		{"status": {"eq.published"}},
		{"status": {"in.draft,review"}, "order": {"published_at.desc"}},
		{"view_count": {"gte.100"}, "order": {"view_count.desc"}, "limit": {"20"}},
		{"search": {"postgres"}},
		{"select": {"id,title,status"}, "order": {"title.asc"}},
		{"published_at": {"notnull"}},
		{"offset": {"40"}, "limit": {"20"}},
	})
	for name, q := range shapes {
		mustPlan(t, ctx, db, name, q)
	}

	// The read-by-id shape the generated handler issues.
	mustPlan(t, ctx, db, "posts read",
		sqlb.Query[blog.Post]().Where(sqlb.F("id").Eq("00000000-0000-7000-8000-000000000000")))

	// Mutations plan too, which is the half a SELECT-only check would miss.
	published := time.Now()
	mustPlan(t, ctx, db, "posts insert", sqlb.InsertRows(&blog.Post{
		OrgID:       "00000000-0000-7000-8000-000000000000",
		AuthorID:    "00000000-0000-7000-8000-000000000001",
		Title:       "t",
		Body:        "b",
		Status:      "draft",
		PublishedAt: &published,
	}))
	mustPlan(t, ctx, db, "posts update", blog.UpdatePost().
		SetTitle("t").
		Where(sqlb.F("id").Eq("00000000-0000-7000-8000-000000000000")).
		Stmt())
	mustPlan(t, ctx, db, "posts delete", sqlb.DeleteRows[blog.Post]().
		Where(sqlb.F("id").Eq("00000000-0000-7000-8000-000000000000")))
}

func TestEveryAuthorQueryShapePlans(t *testing.T) {
	ctx := context.Background()
	db := blogDB(t)

	shapes := listShapes[blog.Author](t, "authors", []url.Values{
		{},
		{"search": {"ada"}},
		{"order": {"name.asc"}, "limit": {"25"}},
		{"select": {"id,name,email"}},
	})
	for name, q := range shapes {
		mustPlan(t, ctx, db, name, q)
	}

	mustPlan(t, ctx, db, "authors read",
		sqlb.Query[blog.Author]().Where(sqlb.F("id").Eq("00000000-0000-7000-8000-000000000000")))
	mustPlan(t, ctx, db, "authors insert", sqlb.InsertRows(&blog.Author{
		OrgID: "00000000-0000-7000-8000-000000000000",
		Email: "a@b.c",
		Name:  "Ada",
	}))
	mustPlan(t, ctx, db, "authors delete", sqlb.DeleteRows[blog.Author]().
		Where(sqlb.F("id").Eq("00000000-0000-7000-8000-000000000000")))
}

func TestEveryOrgQueryShapePlans(t *testing.T) {
	ctx := context.Background()
	db := blogDB(t)

	// Orgs are read and list only, so those are the only shapes to check.
	shapes := listShapes[blog.Org](t, "orgs", []url.Values{
		{},
		{"slug": {"eq.acme"}},
		{"search": {"acme"}, "order": {"name.asc"}},
	})
	for name, q := range shapes {
		mustPlan(t, ctx, db, name, q)
	}
	mustPlan(t, ctx, db, "orgs read",
		sqlb.Query[blog.Org]().Where(sqlb.F("id").Eq("00000000-0000-7000-8000-000000000000")))
}

// The count query pagination issues is a different statement from the list, and
// it is the one a resource would break silently: a list that plans and a count
// that does not still returns rows, with a wrong total.
func TestPaginationCountPlans(t *testing.T) {
	ctx := context.Background()
	db := blogDB(t)

	parsed, err := filter.Parse(url.Values{"status": {"eq.published"}},
		filter.Options{Model: sqlb.ModelOf[blog.Post]()})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	q := filter.Apply(sqlb.Query[blog.Post](), parsed)

	// Count compiles a different statement, reachable only by running it.
	if _, err := q.Count(ctx, db); err != nil {
		t.Errorf("the count query does not run against the live schema: %v", err)
	}
}

// ADR-0016: a guard that cannot fail is worse than no guard. This is the same
// check pointed at a column that does not exist, proving the ones above would
// have caught it.
func TestExplainRejectsAColumnThatDoesNotExist(t *testing.T) {
	ctx := context.Background()
	db := blogDB(t)

	// The typo ADR-0009 accepts at compile time, which is exactly the class
	// this practice exists to catch.
	q := sqlb.Query[blog.Post]().Where(sqlb.F("titel").Eq("x"))
	if _, err := sqlb.Explain(ctx, db, q); err == nil {
		t.Fatal("Explain accepted a column that does not exist, so the checks above prove nothing")
	}
}

// A plan regression is the second thing Explain answers, and it needs enough
// rows for the planner to have a choice. Below the threshold Postgres scans
// regardless, so this asserts the diagnostic surface works rather than
// asserting a particular plan.
func TestPlanDiagnosticsAreReadable(t *testing.T) {
	ctx := context.Background()
	db := blogDB(t)

	plan, err := sqlb.Explain(ctx, db,
		sqlb.Query[blog.Post]().Where(sqlb.F("status").Eq("published")))
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if plan == nil {
		t.Fatal("Explain returned no plan")
	}
	// Diagnostics on an empty table are expected to be quiet; what matters is
	// that asking is cheap and does not error.
	_ = plan.Diagnostics()
}
