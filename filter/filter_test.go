package filter_test

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jryannel/sqlb"
	"github.com/jryannel/sqlb/filter"
)

type Article struct {
	ID        string     `db:"id" sqlb:"pk,default"`
	Title     string     `db:"title" sqlb:"filter,search,sort"`
	Body      string     `db:"body" sqlb:"search"`
	Status    string     `db:"status" sqlb:"filter"`
	Views     int64      `db:"views" sqlb:"filter,sort"`
	AuthorID  string     `db:"author_id" sqlb:"filter"`
	Draft     bool       `db:"draft" sqlb:"filter"`
	Secret    string     `db:"internal_note" sqlb:"hidden"`
	Published *time.Time `db:"published_at" sqlb:"filter,sort"`
	CreatedAt time.Time  `db:"created_at" sqlb:"sort,readonly,default"`
}

func (Article) TableName() string { return "articles" }

func opts() filter.Options {
	return filter.Options{Model: sqlb.ModelOf[Article](), Expandable: []string{"author"}}
}

// compile parses a query string and renders the resulting SQL, which is the
// only way to be sure a filter reached the statement it claimed to.
func compile(t *testing.T, query string) (string, []any) {
	t.Helper()
	values, err := url.ParseQuery(query)
	if err != nil {
		t.Fatalf("bad test query %q: %v", query, err)
	}
	q, err := filter.Parse(values, opts())
	if err != nil {
		t.Fatalf("Parse(%q): %v", query, err)
	}
	b := filter.Apply(sqlb.Query[Article]().Select(sqlb.F("id")), q)
	sql, args, err := b.SQL()
	if err != nil {
		t.Fatalf("SQL(): %v", err)
	}
	return sql, args
}

func TestFilterCompilesToSQL(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  string
		args  []any
	}{
		{
			name:  "operator form",
			query: "status=eq.published",
			want:  `WHERE "status" = $1`,
			args:  []any{"published"},
		},
		{
			name:  "shorthand equality",
			query: "status=published",
			want:  `WHERE "status" = $1`,
			args:  []any{"published"},
		},
		{
			name:  "a dotted value is not mistaken for an operator",
			query: "author_id=alice.smith@example.com",
			want:  `WHERE "author_id" = $1`,
			args:  []any{"alice.smith@example.com"},
		},
		{
			name:  "repeated parameters conjoin into a range",
			query: "views=gte.10&views=lt.100",
			want:  `WHERE ("views" >= $1) AND ("views" < $2)`,
			args:  []any{int64(10), int64(100)},
		},
		{
			name:  "value list",
			query: "status=in.draft,published",
			want:  `WHERE "status" IN ($1, $2)`,
			args:  []any{"draft", "published"},
		},
		{
			name:  "null test",
			query: "published_at=isnull",
			want:  `WHERE "published_at" IS NULL`,
		},
		{
			name:  "between",
			query: "views=between.10,20",
			want:  `WHERE "views" BETWEEN $1 AND $2`,
			args:  []any{int64(10), int64(20)},
		},
		{
			name:  "explicit disjunction",
			query: "or=(status.eq.draft,views.lt.5)",
			want:  `WHERE ("status" = $1) OR ("views" < $2)`,
			args:  []any{"draft", int64(5)},
		},
		{
			name:  "search fans out over searchable columns only",
			query: "search=ada",
			want:  `WHERE ("title" ILIKE $1) OR ("body" ILIKE $2)`,
			args:  []any{"%ada%", "%ada%"},
		},
		{
			name:  "contains escapes wildcards",
			query: "title=contains.50%25",
			want:  `WHERE "title" ILIKE $1`,
			args:  []any{`%50\%%`},
		},
		{
			name:  "boolean coercion",
			query: "draft=true",
			want:  `WHERE "draft" = $1`,
			args:  []any{true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, args := compile(t, tt.query)
			if !strings.Contains(sql, tt.want) {
				t.Errorf("SQL\n got: %s\nwant it to contain: %s", sql, tt.want)
			}
			if len(args) != len(tt.args) {
				t.Fatalf("args = %#v, want %#v", args, tt.args)
			}
			for i := range tt.args {
				if args[i] != tt.args[i] {
					t.Errorf("arg %d = %#v (%T), want %#v (%T)", i, args[i], args[i], tt.args[i], tt.args[i])
				}
			}
		})
	}
}

func TestSortAndPagination(t *testing.T) {
	sql, _ := compile(t, "sort=-views,title&page=3&per_page=10")
	if !strings.Contains(sql, `ORDER BY "views" DESC, "title" ASC`) {
		t.Errorf("ordering missing from: %s", sql)
	}
	if !strings.Contains(sql, "LIMIT 10 OFFSET 20") {
		t.Errorf("pagination missing from: %s", sql)
	}
}

func TestPostgRESTSortSpelling(t *testing.T) {
	sql, _ := compile(t, "sort=views.desc")
	if !strings.Contains(sql, `ORDER BY "views" DESC`) {
		t.Errorf("the `column.desc` spelling should work too, got: %s", sql)
	}
}

func TestSelectAlwaysKeepsThePrimaryKey(t *testing.T) {
	sql, _ := compile(t, "select=title")
	if !strings.Contains(sql, `SELECT "id", "title"`) {
		t.Errorf("a projection must keep the key that addresses the row, got: %s", sql)
	}
}

// Capability enforcement is the security boundary: a column without a
// capability must be unreachable through it, and the rejection must say what
// is reachable instead.
func TestCapabilitiesAreEnforced(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		wantReason string
		wantAllows string
	}{
		{"undeclared column", "created_at=eq.x", "not filterable", "title"},
		{"unknown column", "nonsense=eq.x", "unknown parameter", "title"},
		{"unsortable column", "sort=body", "not sortable", "title"},
		{"hidden column is invisible", "internal_note=eq.x", "unknown parameter", ""},
		{"hidden column cannot be selected", "select=internal_note", "unknown column", ""},
		{"unexpandable relation", "expand=secrets", "not expandable", "author"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values, _ := url.ParseQuery(tt.query)
			_, err := filter.Parse(values, opts())
			if err == nil {
				t.Fatalf("Parse(%q) should have been rejected", tt.query)
			}
			if !strings.Contains(err.Error(), tt.wantReason) {
				t.Errorf("error = %q, want it to mention %q", err, tt.wantReason)
			}
			if tt.wantAllows != "" && !strings.Contains(err.Error(), tt.wantAllows) {
				t.Errorf("error = %q, want it to list %q among the allowed values", err, tt.wantAllows)
			}
		})
	}
}

// A hidden column must not be probeable: neither its name nor its existence
// should show up in a rejection.
func TestHiddenColumnsDoNotLeak(t *testing.T) {
	values, _ := url.ParseQuery("internal_note=eq.x")
	_, err := filter.Parse(values, opts())
	if err == nil {
		t.Fatal("filtering a hidden column should be rejected")
	}
	if strings.Contains(err.Error(), "internal_note") && strings.Contains(err.Error(), "allowed") {
		// The parameter name is echoed, which is fine; it must not appear in
		// the allow-list.
		allowed := err.Error()[strings.Index(err.Error(), "allowed"):]
		if strings.Contains(allowed, "internal_note") {
			t.Errorf("hidden column leaked into the allow-list: %s", err)
		}
	}
}

func TestEveryProblemIsReported(t *testing.T) {
	values, _ := url.ParseQuery("nope=eq.1&sort=body&select=internal_note")
	_, err := filter.Parse(values, opts())
	if err == nil {
		t.Fatal("expected errors")
	}
	errs, ok := err.(filter.Errors)
	if !ok {
		t.Fatalf("error type = %T, want filter.Errors", err)
	}
	if len(errs) != 3 {
		t.Errorf("reported %d problems, want 3: %v", len(errs), errs)
	}
}

func TestTypeCoercionFailures(t *testing.T) {
	for _, query := range []string{"views=eq.notanumber", "draft=eq.maybe", "published_at=gt.yesterday"} {
		values, _ := url.ParseQuery(query)
		if _, err := filter.Parse(values, opts()); err == nil {
			t.Errorf("Parse(%q) should have rejected the value", query)
		}
	}
}

func TestTimestampCoercion(t *testing.T) {
	sql, args := compile(t, "published_at=gte.2024-01-02")
	if !strings.Contains(sql, `"published_at" >= $1`) {
		t.Fatalf("SQL = %s", sql)
	}
	if _, ok := args[0].(time.Time); !ok {
		t.Errorf("arg type = %T, want time.Time", args[0])
	}
}

func TestPageSizeIsCapped(t *testing.T) {
	values, _ := url.ParseQuery("per_page=100000")
	q, err := filter.Parse(values, filter.Options{Model: sqlb.ModelOf[Article](), MaxPageSize: 50})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if q.Limit != 50 {
		t.Errorf("limit = %d, want it capped to 50", q.Limit)
	}
}

func TestUnpaginatedRequestGetsADefaultLimit(t *testing.T) {
	sql, _ := compile(t, "")
	if !strings.Contains(sql, "LIMIT") {
		t.Errorf("a list query must always be bounded, got: %s", sql)
	}
}

func TestFilterCountIsBounded(t *testing.T) {
	var parts []string
	for i := 0; i < 30; i++ {
		parts = append(parts, "status=eq.a")
	}
	values, _ := url.ParseQuery(strings.Join(parts, "&"))
	if _, err := filter.Parse(values, filter.Options{Model: sqlb.ModelOf[Article](), MaxFilters: 5}); err == nil {
		t.Error("an unbounded number of filters should be rejected")
	}
}

func TestGroupNestingIsBounded(t *testing.T) {
	deep := "or=(" + strings.Repeat("or(", 6) + "status.eq.a" + strings.Repeat(")", 6) + ")"
	values, _ := url.ParseQuery(deep)
	if _, err := filter.Parse(values, opts()); err == nil {
		t.Error("deeply nested groups should be rejected")
	}
}

func TestQuotedValuesKeepTheirCommas(t *testing.T) {
	sql, args := compile(t, `status=in."a,b",c`)
	if !strings.Contains(sql, `"status" IN ($1, $2)`) {
		t.Fatalf("SQL = %s", sql)
	}
	if args[0] != "a,b" {
		t.Errorf("arg 0 = %#v, want %q", args[0], "a,b")
	}
}

// TestApplyNeverProjectsHiddenColumns is the last line of defence: a handler
// that forgets to project must still not leak a hidden column into a response.
func TestApplyNeverProjectsHiddenColumns(t *testing.T) {
	values, _ := url.ParseQuery("")
	q, err := filter.Parse(values, opts())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	sql, _, err := filter.Apply(sqlb.Query[Article](), q).SQL()
	if err != nil {
		t.Fatalf("SQL(): %v", err)
	}
	if strings.Contains(sql, "internal_note") {
		t.Errorf("hidden column reached the projection: %s", sql)
	}
	if !strings.Contains(sql, `"title"`) {
		t.Errorf("visible columns should still be projected: %s", sql)
	}
}
