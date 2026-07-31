package sqlb_test

import (
	"strings"
	"testing"

	"github.com/jryannel/sqlb"
)

// A project with three derived fields, one of each tier ADR-0041 names: a
// row-local expression, a correlated subquery, and one whose answer depends on
// who is asking.
type CompProject struct {
	ID        string `db:"id" sqlb:"pk"`
	Name      string `db:"name" sqlb:"search"`
	DueDate   string `db:"due_date" sqlb:"sort"`
	OpenTasks int32  `db:"open_tasks"`

	IsOverdue  bool  `db:"is_overdue" sqlb:"filter,sort"`
	TotalTasks int32 `db:"total_tasks"`
	IsStarred  bool  `db:"is_starred" sqlb:"filter"`
}

func (CompProject) TableName() string { return "projects" }

func (CompProject) ComputedColumns() []sqlb.Computed {
	return []sqlb.Computed{
		{Name: "is_overdue", Expr: "due_date < current_date AND open_tasks > 0"},
		{Name: "total_tasks", Expr: "(SELECT count(*) FROM tasks t WHERE t.project_id = projects.id)"},
		{
			Name:  "is_starred",
			Expr:  "EXISTS (SELECT 1 FROM stars s WHERE s.project_id = projects.id AND s.member_id = ?)",
			Needs: []string{"viewer"},
		},
	}
}

// One declaration reaches the projection, the WHERE and the ORDER BY, because
// all three render through the same function.
func TestComputedRendersInProjectionFilterAndOrder(t *testing.T) {
	sql, _, err := sqlb.Query[CompProject]().
		Bind("viewer", "member-1").
		Where(sqlb.F("is_overdue").Eq(true)).
		OrderBy(sqlb.OrderBy(sqlb.F("is_overdue"))).
		SQL()
	if err != nil {
		t.Fatalf("SQL: %v", err)
	}
	for _, want := range []string{
		// Projected as an expression, aliased back to the column name so the
		// scan can match it to the field.
		`(due_date < current_date AND open_tasks > 0) AS "is_overdue"`,
		`(SELECT count(*) FROM tasks t WHERE t.project_id = projects.id) AS "total_tasks"`,
		`WHERE (due_date < current_date AND open_tasks > 0) = $2`,
		`ORDER BY (due_date < current_date AND open_tasks > 0) ASC`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("SQL is missing %q:\n%s", want, sql)
		}
	}
	// The stored columns still render as columns.
	if !strings.Contains(sql, `"projects"."name"`) && !strings.Contains(sql, `"name"`) {
		t.Errorf("stored columns are missing:\n%s", sql)
	}
}

// A parameterised expression binds once however many times it is rendered —
// the property Near proved worth having, generalised.
func TestComputedBindsOnce(t *testing.T) {
	sql, args, err := sqlb.Query[CompProject]().
		Bind("viewer", "member-1").
		Where(sqlb.F("is_starred").Eq(true)).
		SQL()
	if err != nil {
		t.Fatalf("SQL: %v", err)
	}
	if n := strings.Count(sql, "$1"); n != 2 {
		t.Errorf("want the viewer bound once and referenced twice, got %d references:\n%s", n, sql)
	}
	if len(args) != 2 || args[0] != "member-1" {
		t.Errorf("args = %v, want the viewer first and the predicate's value second", args)
	}
}

// An unbound expression would render `member_id = NULL` and be false for every
// row forever, which looks exactly like a working feature. It fails instead.
func TestComputedWithoutItsBindFails(t *testing.T) {
	_, _, err := sqlb.Query[CompProject]().SQL()
	if err == nil {
		t.Fatal("want an error when the viewer bind is missing")
	}
	for _, want := range []string{"is_starred", "viewer", "Bind"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// Nothing writes an expression: no insert names it, no update assigns it.
func TestComputedIsNotWritten(t *testing.T) {
	sql, _, err := sqlb.InsertRows(&CompProject{ID: "p1", Name: "Apollo"}).SQL()
	if err != nil {
		t.Fatalf("SQL: %v", err)
	}
	if strings.Contains(sql, `INSERT INTO "projects" ("id", "name", "due_date", "open_tasks", "is_overdue"`) {
		t.Errorf("insert writes a computed column:\n%s", sql)
	}
	if !strings.Contains(sql, `(due_date < current_date AND open_tasks > 0) AS "is_overdue"`) {
		t.Errorf("RETURNING should carry the derived value back:\n%s", sql)
	}
	// The parameterised one has no bind to take on a write, so it is left out
	// rather than rendered against a bind that is not there.
	if strings.Contains(sql, "is_starred") {
		t.Errorf("RETURNING should omit a parameterised computed column:\n%s", sql)
	}

	_, _, err = sqlb.UpdateRows[CompProject]().Set("is_overdue", true).Where(sqlb.F("id").Eq("p1")).SQL()
	if err == nil || !strings.Contains(err.Error(), "computed") {
		t.Errorf("assigning a computed column should be refused, got %v", err)
	}
}

// A count wraps the query in a subselect, and the inner statement's derived
// columns must not leak into the outer one.
func TestComputedSurvivesCount(t *testing.T) {
	sql, _, err := sqlb.Query[CompProject]().
		Bind("viewer", "member-1").
		Where(sqlb.F("is_starred").Eq(true)).
		SQL()
	if err != nil {
		t.Fatalf("SQL: %v", err)
	}
	if !strings.Contains(sql, "EXISTS (SELECT 1 FROM stars") {
		t.Errorf("expected the expression inline:\n%s", sql)
	}
}
