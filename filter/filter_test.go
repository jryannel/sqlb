package filter_test

import (
	"encoding/json"
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
	AuthorID  string     `db:"author_id" json:"author_id" sqlb:"filter,expand"`
	Draft     bool       `db:"draft" sqlb:"filter"`
	Secret    string     `db:"internal_note" sqlb:"hidden"`
	Published *time.Time `db:"published_at" sqlb:"filter,sort"`
	CreatedAt time.Time  `db:"created_at" sqlb:"sort,readonly,default"`

	Author *Author `db:"-" json:"author,omitempty" sqlb:"expands=author_id"`
}

func (Article) TableName() string { return "articles" }

// Author is the expansion target. Its hidden column is here on purpose: a
// hidden column must stay hidden when the row is reached through a join.
type Author struct {
	ID    string `db:"id" json:"id" sqlb:"pk"`
	Name  string `db:"name" json:"name"`
	Email string `db:"email" json:"-" sqlb:"hidden"`
}

func (Author) TableName() string { return "authors" }

func opts() filter.Options {
	return filter.Options{Model: sqlb.ModelOf[Article](), Expandable: []string{"author"}}
}

// Doc carries the document column. It is a model of its own rather than two
// more fields on Article so that the package examples keep documenting a
// resource with an ordinary column set.
//
// Blob is here deliberately: []byte and json.RawMessage are the same reflect
// kind, and only one of them may collect the jsonb operators.
type Doc struct {
	ID       string          `db:"id" sqlb:"pk"`
	Title    string          `db:"title" sqlb:"filter,sort"`
	Metadata json.RawMessage `db:"metadata" sqlb:"filter"`
	Blob     []byte          `db:"blob" sqlb:"filter"`
}

func (Doc) TableName() string { return "docs" }

func docOpts() filter.Options { return filter.Options{Model: sqlb.ModelOf[Doc]()} }

// compileDoc is compile against the Doc model.
func compileDoc(t *testing.T, query string) (string, []any) {
	t.Helper()
	values, err := url.ParseQuery(query)
	if err != nil {
		t.Fatalf("bad test query %q: %v", query, err)
	}
	q, err := filter.Parse(values, docOpts())
	if err != nil {
		t.Fatalf("Parse(%q): %v", query, err)
	}
	b := filter.Apply(sqlb.Query[Doc]().Select(sqlb.F("id")), q)
	sql, args, err := b.SQL()
	if err != nil {
		t.Fatalf("SQL(): %v", err)
	}
	return sql, args
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
	errs, ok := filter.AsErrors(err)
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

// Containment is what a document column is for: the point of `metadata` is
// that a caller attaches keys nobody declared and narrows by them later, which
// is the one filter that cannot be expressed as a column capability.
func TestJSONContainment(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  string
		arg   string
	}{
		{
			name:  "object",
			query: `metadata=contains.{"lang":"de"}`,
			want:  `WHERE "metadata" @> $1::jsonb`,
			arg:   `{"lang":"de"}`,
		},
		{
			// The comma inside the object must not be read as a value
			// separator, and the nesting must survive intact.
			name:  "nested object with commas",
			query: `metadata=contains.{"a":{"b":1,"c":2},"d":[1,2]}`,
			want:  `WHERE "metadata" @> $1::jsonb`,
			arg:   `{"a":{"b":1,"c":2},"d":[1,2]}`,
		},
		{
			name:  "array",
			query: `metadata=contains.["urgent"]`,
			want:  `WHERE "metadata" @> $1::jsonb`,
			arg:   `["urgent"]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, args := compileDoc(t, tt.query)
			if !strings.Contains(sql, tt.want) {
				t.Fatalf("SQL = %s\nwant it to contain %s", sql, tt.want)
			}
			if len(args) != 1 || args[0] != tt.arg {
				t.Errorf("args = %#v, want [%q]", args, tt.arg)
			}
		})
	}
}

// The `,` inside a JSON object sits at the same nesting level as the one
// separating conditions, so a group has to count braces to tell them apart.
func TestJSONContainmentInsideAGroup(t *testing.T) {
	sql, args := compileDoc(t, `or=(metadata.contains.{"a":1,"b":2},title.eq.draft)`)
	if !strings.Contains(sql, `("metadata" @> $1::jsonb) OR ("title" = $2)`) {
		t.Fatalf("SQL = %s", sql)
	}
	if args[0] != `{"a":1,"b":2}` {
		t.Errorf("arg 0 = %#v, want the whole object", args[0])
	}
}

func TestJSONColumnRejections(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		wantReason string
		wantAllows string
	}{
		{
			name:       "malformed document",
			query:      `metadata=contains.{"lang":`,
			wantReason: "not valid JSON",
		},
		{
			name:       "empty document",
			query:      "metadata=contains.",
			wantReason: "needs a JSON document",
		},
		{
			// The request named no operator, so the rejection must not quote
			// back the "eq" that the shorthand rule inferred.
			name:       "shorthand has no meaning here",
			query:      `metadata={"lang":"de"}`,
			wantReason: "no shorthand form",
			wantAllows: "contains",
		},
		{
			name:       "ordering operator",
			query:      "metadata=gt.1",
			wantReason: "cannot be used on metadata",
			wantAllows: "contains",
		},
		{
			name:       "pattern operator",
			query:      "metadata=startswith.x",
			wantReason: "cannot be used on metadata",
			wantAllows: "contains",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values, _ := url.ParseQuery(tt.query)
			_, err := filter.Parse(values, docOpts())
			if err == nil {
				t.Fatalf("Parse(%q) should have been rejected", tt.query)
			}
			if !strings.Contains(err.Error(), tt.wantReason) {
				t.Errorf("error = %q, want it to mention %q", err, tt.wantReason)
			}
			if tt.wantAllows != "" && !strings.Contains(err.Error(), tt.wantAllows) {
				t.Errorf("error = %q, want it to offer %q", err, tt.wantAllows)
			}
			if strings.Contains(err.Error(), "shorthand") && strings.Contains(err.Error(), `"eq"`) {
				t.Errorf("the rejection quotes an operator the request never wrote: %s", err)
			}
		})
	}
}

// Null tests keep working on a document column: "no metadata at all" is a
// different question from "metadata containing nothing", and both are askable.
func TestJSONColumnStillTakesNullTests(t *testing.T) {
	sql, _ := compileDoc(t, "metadata=isnull")
	if !strings.Contains(sql, `"metadata" IS NULL`) {
		t.Fatalf("SQL = %s", sql)
	}
}

// json.RawMessage and []byte are both slices of bytes. Only the first is a
// document, and a bytea column must keep the ordinary operators rather than be
// offered containment it cannot answer.
func TestByteaIsNotTreatedAsJSON(t *testing.T) {
	values, _ := url.ParseQuery(`blob=contains.{"a":1}`)
	_, err := filter.Parse(values, docOpts())
	if err == nil {
		t.Fatal("contains on a bytea column should have been rejected")
	}
	if !strings.Contains(err.Error(), "needs a text column") {
		t.Errorf("error = %q, want the text-column rejection, not the jsonb one", err)
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

// The package contract is that a parameter is never silently ignored. Apply now
// performs the join rather than refusing it, so the assertion is that the
// relation reaches the SQL — an accepted ?expand that compiled to a statement
// without the join would be the same silent drop, wearing a 200.
func TestApplyPerformsAnExpand(t *testing.T) {
	values, _ := url.ParseQuery("expand=author")
	q, err := filter.Parse(values, opts())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(q.Expand) != 1 || q.Expand[0] != "author" {
		t.Fatalf("Expand = %v, want [author]", q.Expand)
	}

	b := filter.Apply(sqlb.Query[Article](), q)
	if err := b.Err(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	sql, _, err := b.SQL()
	if err != nil {
		t.Fatalf("SQL: %v", err)
	}
	for _, want := range []string{
		`LEFT JOIN "authors" AS "__ex_author"`,
		`AS "__expand_author"`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("statement missing %q:\n%s", want, sql)
		}
	}
	// Hidden survives the join: the target's email must not be built into the
	// JSON object, or expansion becomes a way around the capability.
	if strings.Contains(sql, "email") {
		t.Errorf("a hidden column of the expanded target reached the statement:\n%s", sql)
	}
}

// Not asking for an expansion must not pay for one.
func TestApplyWithoutExpandDoesNotJoin(t *testing.T) {
	q, err := filter.Parse(url.Values{}, opts())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	sql, _, err := filter.Apply(sqlb.Query[Article](), q).SQL()
	if err != nil {
		t.Fatalf("SQL: %v", err)
	}
	if strings.Contains(sql, "LEFT JOIN") {
		t.Errorf("an unexpanded query joined anyway:\n%s", sql)
	}
}
