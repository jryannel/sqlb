package sqlb

import (
	"database/sql/driver"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestEncodeArray(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want string
	}{
		{"empty", []string{}, "{}"},
		{"plain", []string{"a", "b"}, "{a,b}"},
		{"space", []string{"c d"}, `{"c d"}`},
		{"comma", []string{"a,b"}, `{"a,b"}`},
		{"brace", []string{"{x}"}, `{"{x}"}`},
		{"quote", []string{`say "hi"`}, `{"say \"hi\""}`},
		{"backslash", []string{`a\b`}, `{"a\\b"}`},
		// The empty string has to be quoted or the array loses a member, and
		// the word NULL has to be quoted or it stops being a string at all.
		{"empty element", []string{""}, `{""}`},
		{"null word", []string{"NULL"}, `{"NULL"}`},
		{"null lowercase", []string{"null"}, `{"null"}`},
		{"ints", []int32{1, -2, 3}, "{1,-2,3}"},
		{"int64", []int64{9007199254740993}, "{9007199254740993}"},
		{"floats", []float64{1.5, -0.25}, "{1.5,-0.25}"},
		{"bools", []bool{true, false}, "{t,f}"},
		{"any slice", []any{"a", int64(2), true}, "{a,2,t}"},
		{"named string type", []Cursor{"x", "y"}, "{x,y}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := EncodeArray(tt.in)
			if err != nil {
				t.Fatalf("EncodeArray(%v): %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("EncodeArray(%v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestEncodeArrayRefusals(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   any
	}{
		{"not a slice", "tags"},
		{"bytes are bytea", []byte{1, 2}},
		{"nil", nil},
		{"nil element", []any{nil}},
		{"pointer elements cannot say NULL", []*string{nil}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := EncodeArray(tt.in); err == nil {
				t.Fatalf("EncodeArray(%#v) succeeded, want an error", tt.in)
			}
		})
	}
}

func TestParseArrayLiteral(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		want  []string
		nulls []bool
	}{
		{"empty array", "{}", []string{}, []bool{}},
		{"plain", "{a,b}", []string{"a", "b"}, []bool{false, false}},
		{"quoted space", `{"c d"}`, []string{"c d"}, []bool{false}},
		{"escaped quote", `{"say \"hi\""}`, []string{`say "hi"`}, []bool{false}},
		{"escaped backslash", `{"a\\b"}`, []string{`a\b`}, []bool{false}},
		{"empty string element", `{""}`, []string{""}, []bool{false}},
		{"bare null is the SQL null", "{a,NULL,b}", []string{"a", "NULL", "b"}, []bool{false, true, false}},
		{"quoted NULL is the word", `{"NULL"}`, []string{"NULL"}, []bool{false}},
		{"dimension prefix", "[0:1]={a,b}", []string{"a", "b"}, []bool{false, false}},
		{"comma inside quotes", `{"a,b",c}`, []string{"a,b", "c"}, []bool{false, false}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			elems, nulls, err := parseArrayLiteral(tt.in)
			if err != nil {
				t.Fatalf("parseArrayLiteral(%q): %v", tt.in, err)
			}
			if !reflect.DeepEqual(elems, tt.want) {
				t.Errorf("elements = %#v, want %#v", elems, tt.want)
			}
			if !reflect.DeepEqual(nulls, tt.nulls) {
				t.Errorf("nulls = %#v, want %#v", nulls, tt.nulls)
			}
		})
	}
}

func TestParseArrayLiteralRefusals(t *testing.T) {
	for _, in := range []string{
		"a,b",        // no braces at all
		"{a,{b,c}}",  // two dimensions, which no declaration produces
		`{"unclosed`, // the quote never ends
		`{"a\`,       // a trailing backslash has nothing to escape
	} {
		if _, _, err := parseArrayLiteral(in); err == nil {
			t.Errorf("parseArrayLiteral(%q) succeeded, want an error", in)
		}
	}
}

func TestArrayScannerRoundTrip(t *testing.T) {
	type row struct {
		Tags    []string  `db:"tags"`
		Sizes   []int64   `db:"sizes"`
		Flags   []bool    `db:"flags"`
		Renewed []float64 `db:"renewed"`
	}
	var got row
	rv := reflect.ValueOf(&got).Elem()

	cases := []struct {
		field   string
		literal string
		want    any
	}{
		{"Tags", `{a,"c d","",NULLish}`, []string{"a", "c d", "", "NULLish"}},
		{"Sizes", "{1,-2}", []int64{1, -2}},
		{"Flags", "{t,f}", []bool{true, false}},
		{"Renewed", "{1.5,2}", []float64{1.5, 2}},
	}
	for _, tc := range cases {
		field := rv.FieldByName(tc.field)
		if _, isArray := arrayElemKind(field.Type()); !isArray {
			t.Fatalf("%s is not recognised as an array field", tc.field)
		}
		s := arrayScanner{dest: field, col: tc.field}
		if err := s.Scan([]byte(tc.literal)); err != nil {
			t.Fatalf("scanning %s from %q: %v", tc.field, tc.literal, err)
		}
		if !reflect.DeepEqual(field.Interface(), tc.want) {
			t.Errorf("%s = %#v, want %#v", tc.field, field.Interface(), tc.want)
		}
	}
}

// A NULL column and an empty array are different values, and the Go side has
// two spellings for them. Losing the distinction is the failure this pins.
func TestArrayScannerNullIsNotEmpty(t *testing.T) {
	var dest []string
	rv := reflect.ValueOf(&dest).Elem()
	s := arrayScanner{dest: rv, col: "tags"}

	if err := s.Scan(nil); err != nil {
		t.Fatalf("scanning NULL: %v", err)
	}
	if dest != nil {
		t.Errorf("a NULL column scanned as %#v, want nil", dest)
	}
	if err := s.Scan("{}"); err != nil {
		t.Fatalf("scanning the empty array: %v", err)
	}
	if dest == nil || len(dest) != 0 {
		t.Errorf("an empty array scanned as %#v, want an empty non-nil slice", dest)
	}
}

// A NULL element has no declaration behind it, so reading one as the zero value
// would put an empty string where a missing value was.
func TestArrayScannerRefusesNullElement(t *testing.T) {
	var dest []string
	rv := reflect.ValueOf(&dest).Elem()
	s := arrayScanner{dest: rv, col: "tags"}
	if err := s.Scan("{a,NULL}"); err == nil {
		t.Fatal("scanning a NULL element succeeded, want an error naming the column")
	}
}

func TestArrayScannerTimestamps(t *testing.T) {
	var dest []time.Time
	rv := reflect.ValueOf(&dest).Elem()
	s := arrayScanner{dest: rv, col: "seen_at"}
	// The spelling Postgres uses inside an array literal, offset and all.
	if err := s.Scan(`{"2026-07-29 11:15:18+00","2026-07-30 00:00:00+02"}`); err != nil {
		t.Fatalf("scanning timestamps: %v", err)
	}
	if len(dest) != 2 {
		t.Fatalf("scanned %d timestamps, want 2", len(dest))
	}
	if dest[0].UTC().Format(time.RFC3339) != "2026-07-29T11:15:18Z" {
		t.Errorf("first timestamp = %s", dest[0].UTC().Format(time.RFC3339))
	}
}

// FuzzArrayRoundTrip is the guard the ADR asks for: the codec is the kind of
// code that is wrong in exactly the cases nobody writes a test for, so the
// property — encode then parse returns what went in — is checked against
// whatever the fuzzer produces.
func FuzzArrayRoundTrip(f *testing.F) {
	for _, seed := range []string{"", "a", "c d", `say "hi"`, `a\b`, "NULL", "{}", ",", "{a,b}", "\x00", "  "} {
		f.Add(seed, "b")
	}
	f.Fuzz(func(t *testing.T, a, b string) {
		in := []string{a, b}
		literal, err := EncodeArray(in)
		if err != nil {
			t.Fatalf("EncodeArray(%q): %v", in, err)
		}
		elems, nulls, err := parseArrayLiteral(literal)
		if err != nil {
			t.Fatalf("parseArrayLiteral(%q) from %q: %v", literal, in, err)
		}
		for i, isNull := range nulls {
			if isNull {
				t.Fatalf("element %d of %q read back as SQL NULL, but no input element was", i, literal)
			}
		}
		if !reflect.DeepEqual(elems, in) {
			t.Fatalf("round trip of %#v through %q gave %#v", in, literal, elems)
		}
	})
}

// The two choke points, checked through the public surface: a slice bound as a
// parameter renders the array literal, and a slice field scans one back.

func TestArrayBinding(t *testing.T) {
	type post struct {
		ID   string   `db:"id" sqlb:"pk"`
		Tags []string `db:"tags" sqlb:"filter"`
	}

	tests := []struct {
		name string
		pred Pred
		sql  string
		arg  any
	}{
		{"has binds the element", F("tags").Has("go"), `$1 = ANY("tags")`, "go"},
		{"hasany binds an array", F("tags").HasAny("go", "sql"), `"tags" && $1`, "{go,sql}"},
		{"hasall binds an array", F("tags").HasAll("go", "sql"), `"tags" @> $1`, "{go,sql}"},
		{"eq of a whole slice", F("tags").Eq([]string{"go"}), `"tags" = $1`, "{go}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, args, err := Query[post]().Select(F("id")).Where(tt.pred).SQL()
			if err != nil {
				t.Fatalf("SQL(): %v", err)
			}
			if !strings.Contains(sql, tt.sql) {
				t.Errorf("SQL = %s, want it to contain %s", sql, tt.sql)
			}
			if len(args) != 1 {
				t.Fatalf("args = %v, want one", args)
			}
			got := args[0]
			if v, isValuer := got.(driver.Valuer); isValuer {
				out, err := v.Value()
				if err != nil {
					t.Fatalf("Value(): %v", err)
				}
				got = out
			}
			if got != tt.arg {
				t.Errorf("bound %#v, want %#v", got, tt.arg)
			}
		})
	}
}

// An empty overlap matches nothing and an empty containment matches everything,
// and neither reaches the database as a parameter.
func TestEmptyArrayOperands(t *testing.T) {
	render := func(p Pred) (string, []any) {
		t.Helper()
		c := newCompiler(nil)
		c.expr(p.Expr())
		sql, args, err := c.result()
		if err != nil {
			t.Fatal(err)
		}
		return sql, args
	}
	if sql, args := render(F("tags").HasAny()); sql != "false" || len(args) != 0 {
		t.Errorf("HasAny() = %q with %v, want false and no args", sql, args)
	}
	if sql, args := render(F("tags").HasAll()); sql != "true" || len(args) != 0 {
		t.Errorf("HasAll() = %q with %v, want true and no args", sql, args)
	}
}

// A []byte is bytea and must pass through untouched: encoding it as an array of
// smallints would corrupt every binary column.
func TestBytesAreNotBoundAsAnArray(t *testing.T) {
	if v := bindValue([]byte{1, 2, 3}); v == nil {
		t.Fatal("a []byte bound as nil")
	} else if _, isValuer := v.(driver.Valuer); isValuer {
		t.Error("a []byte was wrapped as a Postgres array")
	}
}
