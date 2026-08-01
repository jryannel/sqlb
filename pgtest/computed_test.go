package pgtest

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jryannel/sqlb"
	"github.com/jryannel/sqlb/filter"
	"github.com/jryannel/sqlb/schema"
)

// Computed columns against a real Postgres.
//
// The engine's own tests compare the compiled statement against a string
// somebody wrote, which cannot answer the questions that matter here:
//
//   - the substituted expression has to be *valid* in every position it is
//     rendered into — a projection, a WHERE and an ORDER BY are three different
//     grammatical slots, and a fragment that parses in one can fail in another;
//   - the projection's alias has to be the name the scan matches on, or the
//     field silently stays zero;
//   - a correlated subquery in the projection has to correlate — a mis-scoped
//     reference to the outer table is accepted by nothing, or worse, resolves
//     to the wrong relation and returns a plausible number;
//   - `RETURNING` has to accept an expression over the row just written.
//
// The last is the one that would otherwise be found in production: an INSERT
// whose RETURNING is rejected fails the whole write, not just the derived field.

// CompProject maps a table with three derived columns, one per tier.
type CompProject struct {
	ID             int64  `db:"id" sqlb:"pk,default"`
	Name           string `db:"name" sqlb:"filter,sort"`
	TotalTasks     int32  `db:"total_tasks" sqlb:"filter,sort,default"`
	CompletedTasks int32  `db:"completed_tasks" sqlb:"filter,sort,default"`

	// Row-local arithmetic, filterable and sortable.
	Progress *int32 `db:"progress" sqlb:"filter,sort,readonly"`
	// A correlated subquery: projection-only.
	StarCount int64 `db:"star_count" sqlb:"readonly"`
	// Per-viewer, and the bind arrives with the query.
	IsStarred bool `db:"is_starred" sqlb:"filter,readonly"`
}

func (CompProject) TableName() string { return "compprojects" }

func (CompProject) ComputedColumns() []sqlb.Computed {
	return []sqlb.Computed{
		{Name: "progress", Expr: "(completed_tasks * 100 / NULLIF(total_tasks, 0))"},
		{Name: "star_count", Expr: "(SELECT count(*) FROM compstars s WHERE s.project_id = compprojects.id)"},
		{
			Name:  "is_starred",
			Expr:  "EXISTS (SELECT 1 FROM compstars s WHERE s.project_id = compprojects.id AND s.member_id = ?)",
			Needs: []string{"viewer"},
		},
	}
}

// compRegistry declares the stored half. The three expressions are declared as
// computed columns, so the DDL this produces must not mention them — which is
// the first thing the test below checks, by applying it.
func compRegistry() *schema.Registry {
	r := schema.NewRegistry()
	r.Table("compprojects",
		schema.BigInt("id").PrimaryKey().Default(schema.Expr("nextval('compprojects_id_seq')")),
		schema.Text("name").Filterable().Sortable(),
		schema.Int("total_tasks").Filterable().Sortable().Default(schema.Value(0)),
		schema.Int("completed_tasks").Filterable().Sortable().Default(schema.Value(0)),

		schema.Computed("progress", schema.TypeInt,
			schema.FromSQL("(completed_tasks * 100 / NULLIF(total_tasks, 0))")).
			Nullable().Filterable().Sortable(),
		schema.Computed("star_count", schema.TypeBigInt,
			schema.FromSQL("(SELECT count(*) FROM compstars s WHERE s.project_id = compprojects.id)")),
		schema.Computed("is_starred", schema.TypeBool,
			schema.FromSQL("EXISTS (SELECT 1 FROM compstars s "+
				"WHERE s.project_id = compprojects.id AND s.member_id = ?)")).
			Needs("viewer").Filterable(),
	)
	return r
}

func seedComputedRows(t *testing.T, db *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	for _, row := range []struct {
		name             string
		total, completed int32
	}{
		{"apollo", 10, 5},
		{"gemini", 4, 4},
		{"mercury", 0, 0}, // the NULLIF case: no tasks, so no progress
	} {
		if _, err := db.Exec(ctx,
			`INSERT INTO compprojects (name, total_tasks, completed_tasks) VALUES ($1, $2, $3)`,
			row.name, row.total, row.completed,
		); err != nil {
			t.Fatalf("inserting %q: %v", row.name, err)
		}
	}
	// apollo is starred by member 7; nothing else is.
	if _, err := db.Exec(ctx,
		`INSERT INTO compstars (project_id, member_id)
		 SELECT id, 7 FROM compprojects WHERE name = 'apollo'`); err != nil {
		t.Fatalf("starring apollo: %v", err)
	}
}

func computedDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	raw := freshDB(t)
	// The sequence and the star table have to exist before the DDL that
	// defaults to one, so the registry is applied after them.
	mustExec(t, raw, `CREATE SEQUENCE compprojects_id_seq`)
	mustExec(t, raw, `CREATE TABLE compstars (project_id bigint NOT NULL, member_id bigint NOT NULL,
	    PRIMARY KEY (project_id, member_id))`)
	applySchema(t, raw, compRegistry())
	return raw
}

// The whole read path, against a database: the DDL has no derived columns in
// it, and the query returns their values anyway.
// compValues are the derived columns these tests read. Since #92 a computed
// column is opt-in per reader, so a test that asserts one has to ask for it —
// which is the point: an unrelated query of the same model pays for none of
// this and needs no viewer.
var compValues = []string{"progress", "star_count", "is_starred"}

func TestComputedColumnsRunAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	raw := computedDB(t)
	seedComputedRows(t, raw)

	rows, err := sqlb.Query[CompProject]().
		WithComputed(compValues...).
		Bind("viewer", int64(7)).
		OrderBy(sqlb.F("name").Asc()).
		All(ctx, sqlb.New(raw))
	if err != nil {
		t.Fatalf("the computed projection did not run: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}

	apollo, gemini, mercury := rows[0], rows[1], rows[2]
	if apollo.Progress == nil || *apollo.Progress != 50 {
		t.Errorf("apollo progress = %v, want 50", apollo.Progress)
	}
	if gemini.Progress == nil || *gemini.Progress != 100 {
		t.Errorf("gemini progress = %v, want 100", gemini.Progress)
	}
	// NULLIF is what keeps a zero denominator from taking the request with it,
	// and a NULL result is why the field is a pointer.
	if mercury.Progress != nil {
		t.Errorf("mercury progress = %v, want NULL — it has no tasks", *mercury.Progress)
	}
	if apollo.StarCount != 1 || gemini.StarCount != 0 {
		t.Errorf("star counts = %d, %d; want 1, 0 — the subquery must correlate to the outer row",
			apollo.StarCount, gemini.StarCount)
	}
	if !apollo.IsStarred || gemini.IsStarred {
		t.Errorf("is_starred = %v, %v; want true, false for member 7", apollo.IsStarred, gemini.IsStarred)
	}
}

// The same expression in a WHERE and an ORDER BY, which are different
// grammatical slots from the projection.
func TestComputedFilterAndSortRunAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	raw := computedDB(t)
	seedComputedRows(t, raw)

	values, err := url.ParseQuery("progress=gte.50&sort=-progress")
	if err != nil {
		t.Fatal(err)
	}
	q, err := filter.Parse(values, filter.Options{
		Model:    sqlb.ModelOf[CompProject](),
		Computed: compValues,
	})
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	rows, err := filter.Apply(sqlb.Query[CompProject]().Bind("viewer", int64(7)), q).
		All(ctx, sqlb.New(raw))
	if err != nil {
		t.Fatalf("filtering and sorting on a computed column: %v", err)
	}

	got := make([]string, len(rows))
	for i, r := range rows {
		got[i] = r.Name
	}
	// mercury's progress is NULL, so it is neither >= 50 nor returned.
	if want := "gemini,apollo"; strings.Join(got, ",") != want {
		t.Errorf("rows = %v, want %s", got, want)
	}
}

// A per-viewer predicate is one bind however many places the expression
// appears, and the answer changes with the viewer.
func TestComputedBindRunsAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	raw := computedDB(t)
	seedComputedRows(t, raw)

	values, err := url.ParseQuery("is_starred=true")
	if err != nil {
		t.Fatal(err)
	}
	q, err := filter.Parse(values, filter.Options{
		Model:    sqlb.ModelOf[CompProject](),
		Computed: compValues,
	})
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}

	for _, tc := range []struct {
		viewer int64
		want   int
	}{{7, 1}, {8, 0}} {
		rows, err := filter.Apply(sqlb.Query[CompProject]().Bind("viewer", tc.viewer), q).
			All(ctx, sqlb.New(raw))
		if err != nil {
			t.Fatalf("viewer %d: %v", tc.viewer, err)
		}
		if len(rows) != tc.want {
			t.Errorf("viewer %d matched %d rows, want %d", tc.viewer, len(rows), tc.want)
		}
	}
}

// RETURNING carries an expression over the row just written. If Postgres
// refused it, the whole INSERT would fail rather than the derived field.
func TestComputedInReturningRunsAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	raw := computedDB(t)

	stored, err := sqlb.InsertRows(&CompProject{Name: "voyager", TotalTasks: 4, CompletedTasks: 1}).
		One(ctx, sqlb.New(raw))
	if err != nil {
		t.Fatalf("insert with a computed RETURNING: %v", err)
	}
	if stored.Progress == nil || *stored.Progress != 25 {
		t.Errorf("progress came back as %v, want 25 without a second read", stored.Progress)
	}
	if stored.StarCount != 0 {
		t.Errorf("star_count = %d, want 0", stored.StarCount)
	}
	// The parameterised one is left out of RETURNING — a write has no viewer to
	// bind — so it holds its zero value here and its real one on the next read.
	if stored.IsStarred {
		t.Error("is_starred should not be answered by a write")
	}
}
