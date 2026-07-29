package filter_test

import (
	"database/sql/driver"
	"net/url"
	"strings"
	"testing"

	"github.com/jryannel/sqlb"
	"github.com/jryannel/sqlb/filter"
)

// Post carries the array column. It is a model of its own rather than a column
// added to Article, so that the operator sets the two accept stay separable in
// the error messages each produces.
type Post struct {
	ID    string   `db:"id" sqlb:"pk,default"`
	Title string   `db:"title" sqlb:"filter,search,sort"`
	Tags  []string `db:"tags" sqlb:"filter"`
	Sizes []int64  `db:"sizes" sqlb:"filter"`
	Blob  []byte   `db:"blob" sqlb:"filter"`
}

func (Post) TableName() string { return "posts" }

func postOpts() filter.Options { return filter.Options{Model: sqlb.ModelOf[Post]()} }

func compilePost(t *testing.T, query string) (string, []any) {
	t.Helper()
	values, err := url.ParseQuery(query)
	if err != nil {
		t.Fatalf("bad test query %q: %v", query, err)
	}
	q, err := filter.Parse(values, postOpts())
	if err != nil {
		t.Fatalf("Parse(%q): %v", query, err)
	}
	sql, args, err := filter.Apply(sqlb.Query[Post]().Select(sqlb.F("id")), q).SQL()
	if err != nil {
		t.Fatalf("SQL(): %v", err)
	}
	return sql, args
}

// arg renders a bind parameter the way the driver will see it, so a test can
// assert the array literal rather than the wrapper that produces it.
func arg(t *testing.T, v any) any {
	t.Helper()
	if valuer, ok := v.(driver.Valuer); ok {
		out, err := valuer.Value()
		if err != nil {
			t.Fatalf("Value(): %v", err)
		}
		return out
	}
	return v
}

func TestArrayFilters(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  string
		arg   any
	}{
		{
			// has binds the element, which is why the descriptor keeps
			// naming the element type rather than fusing it into an array.
			name:  "has binds one element",
			query: "tags=has.urgent",
			want:  `WHERE $1 = ANY("tags")`,
			arg:   "urgent",
		},
		{
			name:  "hasany is an overlap",
			query: "tags=hasany.a,b",
			want:  `WHERE "tags" && $1`,
			arg:   "{a,b}",
		},
		{
			name:  "hasall is containment",
			query: "tags=hasall.a,b",
			want:  `WHERE "tags" @> $1`,
			arg:   "{a,b}",
		},
		{
			name:  "eq compares whole arrays",
			query: "tags=eq.a,b",
			want:  `WHERE "tags" = $1`,
			arg:   "{a,b}",
		},
		{
			// A member carrying a comma is quoted by the grammar and has to
			// survive into the array literal as one element.
			name:  "a quoted member stays one element",
			query: `tags=hasany.%22a%2Cb%22,c`,
			want:  `WHERE "tags" && $1`,
			arg:   `{"a,b",c}`,
		},
		{
			// The element type decides the binding, so an int array binds
			// integers rather than the strings the URL carried.
			name:  "elements are coerced to the element type",
			query: "sizes=hasany.1,2",
			want:  `WHERE "sizes" && $1`,
			arg:   "{1,2}",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, args := compilePost(t, tt.query)
			if !strings.Contains(sql, tt.want) {
				t.Errorf("SQL = %s, want it to contain %s", sql, tt.want)
			}
			if len(args) != 1 {
				t.Fatalf("args = %v, want one", args)
			}
			if got := arg(t, args[0]); got != tt.arg {
				t.Errorf("bound %#v, want %#v", got, tt.arg)
			}
		})
	}
}

// A NULL array and an empty one are different values, so the null tests keep
// meaning what they mean everywhere else.
func TestArrayNullTests(t *testing.T) {
	sql, _ := compilePost(t, "tags=isnull")
	if !strings.Contains(sql, `WHERE "tags" IS NULL`) {
		t.Errorf("SQL = %s", sql)
	}
}

func TestArrayOperatorRefusals(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  string
	}{
		// contains is the text substring operator, and it does not become
		// array containment on an array column — that overloading is the
		// ambiguity the generated clients exist to remove. The refusal names
		// the operators that would have worked, because a caller who wrote
		// this almost certainly meant `has`.
		{"contains stays text-only", "tags=contains.urgent", "does not apply to the array column"},
		{"contains names the alternative", "tags=contains.urgent", "has, hasall, hasany"},
		{"ordering", "tags=gt.a", "does not apply to the array column"},
		{"between", "tags=between.a,b", "does not apply to the array column"},
		{"in", "tags=in.a,b", "does not apply to the array column"},
		{"startswith", "tags=startswith.a", "does not apply to the array column"},

		// And the reverse: a scalar column has no containment operator.
		{"has on a scalar", "title=has.x", "needs an array column"},
		{"hasany on a scalar", "title=hasany.x", "needs an array column"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values, err := url.ParseQuery(tt.query)
			if err != nil {
				t.Fatalf("bad test query: %v", err)
			}
			_, err = filter.Parse(values, postOpts())
			if err == nil {
				t.Fatalf("%q was accepted, want a refusal mentioning %q", tt.query, tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

// bytea is []byte and is not an array: treating it as one would encode every
// binary column as a list of smallints.
func TestByteaIsNotAnArray(t *testing.T) {
	values, _ := url.ParseQuery("blob=has.x")
	if _, err := filter.Parse(values, postOpts()); err == nil {
		t.Fatal("has was accepted on a bytea column")
	}
}
