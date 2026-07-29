package pgtest

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jryannel/sqlb"
	"github.com/jryannel/sqlb/schema"
)

// The array codec, against the database it was written for.
//
// The unit tests in the engine check that encode and parse are inverses of each
// other. That is necessary and not sufficient: two functions can agree with
// each other and both disagree with Postgres. These tests are the ones that
// cannot — every literal here is written by sqlb and read back by Postgres, or
// written by Postgres and read back by sqlb (ADR-0033).

// The NOT NULL columns carry `default`, exactly as a generated model does for a
// column the schema gave one: an insert omits them when the Go value is the nil
// slice, so the database fills in {} rather than being handed a NULL. `notes`
// has no default and is nullable, which is what makes it the column that can
// tell NULL from {}.
type ArrayRow struct {
	ID    int64       `db:"id" sqlb:"pk"`
	Tags  []string    `db:"tags" sqlb:"default"`
	Sizes []int64     `db:"sizes" sqlb:"default"`
	Flags []bool      `db:"flags" sqlb:"default"`
	Rates []float64   `db:"rates" sqlb:"default"`
	Seen  []time.Time `db:"seen" sqlb:"default"`
	Notes []string    `db:"notes"`
}

func (ArrayRow) TableName() string { return "array_rows" }

func arrayTable(t *testing.T) *sqlb.DB {
	t.Helper()
	raw := freshDB(t)
	mustExec(t, raw, `
		CREATE TABLE array_rows (
			id     bigint PRIMARY KEY,
			tags   text[] NOT NULL DEFAULT '{}',
			sizes  bigint[] NOT NULL DEFAULT '{}',
			flags  boolean[] NOT NULL DEFAULT '{}',
			rates  double precision[] NOT NULL DEFAULT '{}',
			seen   timestamptz[] NOT NULL DEFAULT '{}',
			notes  text[]
		)`)
	return sqlb.New(raw)
}

// The values that break a naive codec: quoting, escaping, embedded separators,
// the empty string, and the word NULL written as a string.
func TestArrayValuesSurviveTheRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := arrayTable(t)

	want := ArrayRow{
		ID: 1,
		Tags: []string{
			"plain", "with space", "with,comma", `with"quote`, `with\backslash`,
			"{braces}", "", "NULL", "null", "  padded  ", "ünïcode",
		},
		Sizes: []int64{0, -1, 9007199254740993},
		Flags: []bool{true, false, true},
		Rates: []float64{0, 1.5, -0.25},
		Seen: []time.Time{
			time.Date(2026, 7, 29, 11, 15, 18, 0, time.UTC),
			time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		},
	}
	if _, err := sqlb.InsertRows(&want).Exec(ctx, db); err != nil {
		t.Fatalf("inserting arrays: %v", err)
	}

	got, err := sqlb.Query[ArrayRow]().Where(sqlb.F("id").Eq(1)).One(ctx, db)
	if err != nil {
		t.Fatalf("reading arrays back: %v", err)
	}
	if !reflect.DeepEqual(got.Tags, want.Tags) {
		t.Errorf("tags = %#v\nwant %#v", got.Tags, want.Tags)
	}
	if !reflect.DeepEqual(got.Sizes, want.Sizes) {
		t.Errorf("sizes = %#v, want %#v", got.Sizes, want.Sizes)
	}
	if !reflect.DeepEqual(got.Flags, want.Flags) {
		t.Errorf("flags = %#v, want %#v", got.Flags, want.Flags)
	}
	if !reflect.DeepEqual(got.Rates, want.Rates) {
		t.Errorf("rates = %#v, want %#v", got.Rates, want.Rates)
	}
	for i, ts := range got.Seen {
		if !ts.Equal(want.Seen[i]) {
			t.Errorf("seen[%d] = %s, want %s", i, ts, want.Seen[i])
		}
	}
}

// An empty array, a NULL column and an absent value are three different things,
// and the Go side has to keep them apart or a UI cannot.
func TestEmptyArrayIsNotNull(t *testing.T) {
	ctx := context.Background()
	db := arrayTable(t)

	// notes is nullable and left unset; tags takes the database default '{}'.
	if _, err := sqlb.InsertRows(&ArrayRow{ID: 1}).Exec(ctx, db); err != nil {
		t.Fatalf("inserting: %v", err)
	}
	got, err := sqlb.Query[ArrayRow]().Where(sqlb.F("id").Eq(1)).One(ctx, db)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if got.Notes != nil {
		t.Errorf("a NULL array scanned as %#v, want nil", got.Notes)
	}
	if got.Tags == nil || len(got.Tags) != 0 {
		t.Errorf("an empty array scanned as %#v, want an empty non-nil slice", got.Tags)
	}

	// And the empty array written from Go comes back as the empty array.
	if _, err := sqlb.UpdateRows[ArrayRow]().
		Set("notes", []string{}).
		Where(sqlb.F("id").Eq(1)).
		Exec(ctx, db); err != nil {
		t.Fatalf("writing an empty array: %v", err)
	}
	got, err = sqlb.Query[ArrayRow]().Where(sqlb.F("id").Eq(1)).One(ctx, db)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if got.Notes == nil || len(got.Notes) != 0 {
		t.Errorf("an empty array written from Go read back as %#v", got.Notes)
	}
}

// The three operators, run by Postgres rather than compared as strings. `has`
// binds the element and the other two bind an array, so this is also where the
// encoding half of the codec is exercised on the query path.
func TestArrayOperatorsAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	db := arrayTable(t)

	rows := []ArrayRow{
		{ID: 1, Tags: []string{"go", "sql"}},
		{ID: 2, Tags: []string{"go", "web"}},
		{ID: 3, Tags: []string{"rust"}},
		{ID: 4, Tags: []string{}},
	}
	for i := range rows {
		if _, err := sqlb.InsertRows(&rows[i]).Exec(ctx, db); err != nil {
			t.Fatalf("inserting row %d: %v", rows[i].ID, err)
		}
	}

	ids := func(t *testing.T, pred sqlb.Pred) []int64 {
		t.Helper()
		found, err := sqlb.Query[ArrayRow]().Where(pred).OrderBy(sqlb.F("id").Asc()).All(ctx, db)
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		var out []int64
		for _, r := range found {
			out = append(out, r.ID)
		}
		return out
	}

	tests := []struct {
		name string
		pred sqlb.Pred
		want []int64
	}{
		{"has", sqlb.F("tags").Has("go"), []int64{1, 2}},
		{"has matches nothing", sqlb.F("tags").Has("cobol"), nil},
		{"hasany", sqlb.F("tags").HasAny("sql", "rust"), []int64{1, 3}},
		{"hasall", sqlb.F("tags").HasAll("go", "sql"), []int64{1}},
		// Every array contains the empty one, including the empty array.
		{"hasall of nothing", sqlb.F("tags").HasAll(), []int64{1, 2, 3, 4}},
		{"eq compares whole arrays", sqlb.F("tags").Eq(sqlb.Array("go", "sql")), []int64{1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ids(t, tt.pred); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("matched %v, want %v", got, tt.want)
			}
		})
	}
}

// A value carrying the separator has to survive the round trip through the
// operator too, not only through the column — the encoder is the same one, and
// this is the case that would fail silently by matching the wrong rows.
func TestArrayOperandQuoting(t *testing.T) {
	ctx := context.Background()
	db := arrayTable(t)

	if _, err := sqlb.InsertRows(&ArrayRow{ID: 1, Tags: []string{"a,b", `c"d`}}).Exec(ctx, db); err != nil {
		t.Fatalf("inserting: %v", err)
	}
	for _, tag := range []string{"a,b", `c"d`} {
		found, err := sqlb.Query[ArrayRow]().Where(sqlb.F("tags").Has(tag)).All(ctx, db)
		if err != nil {
			t.Fatalf("querying for %q: %v", tag, err)
		}
		if len(found) != 1 {
			t.Errorf("has(%q) matched %d rows, want 1", tag, len(found))
		}
	}
	found, err := sqlb.Query[ArrayRow]().Where(sqlb.F("tags").HasAll("a,b", `c"d`)).All(ctx, db)
	if err != nil {
		t.Fatalf("hasall: %v", err)
	}
	if len(found) != 1 {
		t.Errorf("hasall matched %d rows, want 1", len(found))
	}
}

// The adoption claim, checked at the level the claim is made: an array column
// that already exists in Postgres reads back as an array column, with its
// element type and its enum values intact.
//
// The whole-schema round trip alongside this one would not catch a failure
// here. An enum array demoted to plain text[] renders as the same SQL type, so
// only its CHECK would differ — and check churn is allowed noise there.
func TestArrayColumnsSurviveIntrospection(t *testing.T) {
	db := freshDB(t)

	target := schema.NewRegistry()
	target.Table("docs",
		schema.UUIDv7("id").PrimaryKey(),
		schema.Text("tags").Array().Default(schema.Value("{}")),
		schema.Int("revisions").Array().Nullable(),
		schema.Varchar("codes", 8).Array().Nullable(),
		schema.Enum("channels", "web", "email", "push").Array().Nullable(),
	)
	applySchema(t, db, target)

	docs := importRegistry(t, db).Get("docs")
	if docs == nil {
		t.Fatal("the imported registry has no docs table")
	}
	for _, tc := range []struct {
		column   string
		wantType schema.Type
	}{
		{"tags", schema.TypeText},
		{"revisions", schema.TypeInt},
		{"codes", schema.TypeVarchar},
		{"channels", schema.TypeEnum},
	} {
		f := docs.Field(tc.column)
		if f == nil {
			t.Errorf("%s was dropped by the import", tc.column)
			continue
		}
		d := f.Desc()
		if !d.Array {
			t.Errorf("%s came back as a scalar %s, not an array", tc.column, d.Type)
		}
		if d.Type != tc.wantType {
			t.Errorf("%s element type = %q, want %q", tc.column, d.Type, tc.wantType)
		}
	}
	if codes := docs.Field("codes"); codes != nil && codes.Desc().Size != 8 {
		t.Errorf("the varchar length did not survive: %d", codes.Desc().Size)
	}
	// The value set stays attached to the element, which is the property the
	// element-plus-flag spelling gets for free.
	if ch := docs.Field("channels"); ch != nil {
		if got := strings.Join(ch.Desc().EnumValues, ","); got != "web,email,push" {
			t.Errorf("enum values = %q", got)
		}
	}
}
