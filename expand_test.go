package sqlb_test

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"

	"github.com/jryannel/sqlb"
)

// The models the expansion tests use. `expand` on the foreign key and
// `expands=` on the field beside it are the two halves of a relation; see
// relation.go for why it is split that way.

type expList struct {
	ID     string `db:"id" json:"id" sqlb:"pk"`
	Name   string `db:"name" json:"name"`
	Secret string `db:"secret" json:"-" sqlb:"hidden"`
}

func (expList) TableName() string { return "lists" }

type expTask struct {
	ID     string `db:"id" json:"id" sqlb:"pk"`
	ListID string `db:"list_id" json:"list_id" sqlb:"filter,expand"`
	Title  string `db:"title" json:"title"`

	List *expList `db:"-" json:"list,omitempty" sqlb:"expands=list_id"`
}

func (expTask) TableName() string { return "tasks" }

func TestExpandCompilesAJoinAndAJSONColumn(t *testing.T) {
	sql, _, err := sqlb.Query[expTask]().Expand("list").SQL()
	if err != nil {
		t.Fatalf("SQL: %v", err)
	}

	for _, want := range []string{
		`LEFT JOIN "lists" AS "__ex_list" ON "__ex_list"."id" = "tasks"."list_id"`,
		`AS "__expand_list"`,
		`json_build_object('id', "__ex_list"."id", 'name', "__ex_list"."name")`,
		// A left join that matched nothing must produce NULL, not an object of
		// nulls — the two say different things.
		`CASE WHEN "__ex_list"."id" IS NULL THEN NULL`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("statement missing %q:\n%s", want, sql)
		}
	}

	// The relation field is db:"-", so it must not reach the projection.
	if strings.Contains(sql, `"list"`) && !strings.Contains(sql, `"__ex_list"`) {
		t.Errorf("the relation field was projected as a column:\n%s", sql)
	}
}

// TestExpandOmitsHiddenColumnsOfTheTarget is the security-relevant one.
// `Hidden` has to survive a join, or expanding a relation becomes a way to read
// a column the target refuses to serve directly.
func TestExpandOmitsHiddenColumnsOfTheTarget(t *testing.T) {
	sql, _, err := sqlb.Query[expTask]().Expand("list").SQL()
	if err != nil {
		t.Fatalf("SQL: %v", err)
	}
	if strings.Contains(sql, "secret") {
		t.Errorf("a hidden column of the expanded target reached the statement:\n%s", sql)
	}
}

func TestExpandScansIntoTheRelationField(t *testing.T) {
	h := newHarness(t,
		[]string{"id", "list_id", "title", "__expand_list"},
		[][]driver.Value{
			{"t1", "l1", "Ship it", []byte(`{"id":"l1","name":"Backlog"}`)},
			{"t2", "l2", "Later", nil},
		})
	defer h.close()

	tasks, err := sqlb.Query[expTask]().Expand("list").All(context.Background(), h.db)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("got %d rows, want 2", len(tasks))
	}

	if tasks[0].List == nil {
		t.Fatal("the expanded relation was not scanned")
	}
	if tasks[0].List.Name != "Backlog" || tasks[0].List.ID != "l1" {
		t.Errorf("expanded list = %+v", tasks[0].List)
	}
	// The ordinary columns still scan.
	if tasks[0].Title != "Ship it" || tasks[0].ListID != "l1" {
		t.Errorf("row = %+v", tasks[0])
	}

	// A NULL expansion is a nil field, not a zero-valued struct: "there is no
	// related row" and "there is one and it is empty" must stay distinguishable.
	if tasks[1].List != nil {
		t.Errorf("a null expansion produced %+v, want nil", tasks[1].List)
	}
}

func TestExpandRejectsAnUnknownRelation(t *testing.T) {
	_, _, err := sqlb.Query[expTask]().Expand("owner").SQL()
	if err == nil {
		t.Fatal("expanding an unknown relation was accepted")
	}
	// ADR-0011: a rejection names what would have worked.
	if !strings.Contains(err.Error(), "list") {
		t.Errorf("the rejection does not name the expandable relations: %v", err)
	}
}

func TestExpandIsIdempotent(t *testing.T) {
	sql, _, err := sqlb.Query[expTask]().Expand("list").Expand("list").SQL()
	if err != nil {
		t.Fatalf("SQL: %v", err)
	}
	if n := strings.Count(sql, "LEFT JOIN"); n != 1 {
		t.Errorf("expanding twice produced %d joins, want 1:\n%s", n, sql)
	}
}

// TestExpandIsNotAppliedUnlessAsked guards the default. Expansion costs a join,
// and a query that did not ask for one must not pay for it.
func TestExpandIsNotAppliedUnlessAsked(t *testing.T) {
	sql, _, err := sqlb.Query[expTask]().SQL()
	if err != nil {
		t.Fatalf("SQL: %v", err)
	}
	if strings.Contains(sql, "LEFT JOIN") || strings.Contains(sql, "__expand_") {
		t.Errorf("an unexpanded query joined anyway:\n%s", sql)
	}
}

// TestRelationRequiresTheColumnToDeclareIt catches the half-written
// declaration: a field claiming to expand a column that does not opt in.
func TestRelationRequiresTheColumnToDeclareIt(t *testing.T) {
	type bad struct {
		ID     string `db:"id" sqlb:"pk"`
		ListID string `db:"list_id"` // no `expand`
		List   *expList
	}
	_ = bad{}

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("a relation naming a non-expandable column was accepted")
		}
		if !strings.Contains(toString(r), "expand") {
			t.Errorf("panic does not explain the problem: %v", r)
		}
	}()

	type badTagged struct {
		ID     string   `db:"id" sqlb:"pk"`
		ListID string   `db:"list_id"`
		List   *expList `db:"-" sqlb:"expands=list_id"`
	}
	_ = sqlb.ModelOf[badTagged]()
}

func toString(v any) string {
	if err, ok := v.(error); ok {
		return err.Error()
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
