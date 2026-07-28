package introspect

import (
	"strings"
	"testing"

	"github.com/jryannel/sqlb/migrate"
	"github.com/jryannel/sqlb/schema"
)

// The catalog rows below are the ones a real Postgres returned for DDL this
// project generated — the spellings are observed, not invented. That is the
// point of testing the mapping separately from the reading: these rows can be
// written by hand, so every branch is reachable without a database, and the
// only thing a database is still needed for is whether the queries return rows
// of this shape.

func TestBuildMapsAWholeTable(t *testing.T) {
	cat := &catalog{
		tables: []tableRow{{Name: "orgs"}, {Name: "posts", Comment: "articles"}},
		columns: []columnRow{
			{Table: "orgs", Name: "id", Type: "uuid", NotNull: true, Default: "uuid_generate_v7()"},
			{Table: "orgs", Name: "name", Type: "text", NotNull: true},
			{Table: "posts", Name: "id", Type: "uuid", NotNull: true, Default: "uuid_generate_v7()"},
			{Table: "posts", Name: "slug", Type: "text", NotNull: true, Comment: "url key"},
			{Table: "posts", Name: "title", Type: "character varying(200)", NotNull: true},
			{Table: "posts", Name: "views", Type: "integer", NotNull: true, Default: "0"},
			{Table: "posts", Name: "note", Type: "text", NotNull: true, Default: "'hello'::text"},
			{Table: "posts", Name: "created_at", Type: "timestamp with time zone", NotNull: true, Default: "now()"},
			{Table: "posts", Name: "score", Type: "double precision"},
			{Table: "posts", Name: "status", Type: "text", NotNull: true},
			{Table: "posts", Name: "org_id", Type: "uuid", NotNull: true},
		},
		constraints: []constraintRow{
			// Postgres 18 records every NOT NULL as a constraint of its own.
			{Table: "orgs", Name: "orgs_id_not_null", Type: "n", Def: "NOT NULL id"},
			{Table: "orgs", Name: "orgs_pkey", Type: "p", Columns: []string{"id"}},
			{Table: "orgs", Name: "orgs_name_key", Type: "u", Columns: []string{"name"}},
			{Table: "posts", Name: "posts_pkey", Type: "p", Columns: []string{"id"}},
			{Table: "posts", Name: "posts_slug_key", Type: "u", Columns: []string{"slug"}},
			{Table: "posts", Name: "posts_status_check", Type: "c", Columns: []string{"status"},
				Expr: "(status = ANY (ARRAY['draft'::text, 'live'::text]))"},
			{Table: "posts", Name: "views_non_negative", Type: "c", Columns: []string{"views"},
				Expr: "(views >= 0)"},
			{Table: "posts", Name: "posts_org_id_fkey", Type: "f", Columns: []string{"org_id"},
				RefTable: "orgs", RefCols: []string{"id"}, OnDelete: "c", OnUpdate: "a"},
		},
		indexes: []indexRow{
			{Table: "posts", Name: "posts_title_views_idx", Method: "btree",
				Columns: []string{"title", "views"}},
			{Table: "posts", Name: "posts_meta_gin", Method: "gin", Columns: []string{"slug"}},
		},
	}

	r, rep, err := build(cat, Options{})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !rep.Empty() {
		t.Fatalf("nothing here is beyond the DSL, but:\n%s", rep)
	}

	posts := r.Get("posts")
	if posts == nil {
		t.Fatal("posts was not built")
	}
	if posts.Comment() != "articles" {
		t.Errorf("table comment = %q", posts.Comment())
	}

	for _, tc := range []struct {
		column string
		check  func(*testing.T, *schema.FieldDesc)
	}{
		{"id", func(t *testing.T, d *schema.FieldDesc) {
			if !d.PrimaryKey || d.Type != schema.TypeUUID {
				t.Errorf("got %+v", d)
			}
			// Recognised by the exact text the schema package emits, so it
			// comes back as the generator rather than as raw SQL.
			if d.Default == nil || d.Default.Raw != "uuid_generate_v7()" {
				t.Errorf("default = %+v", d.Default)
			}
		}},
		{"slug", func(t *testing.T, d *schema.FieldDesc) {
			if !d.Unique || d.Comment != "url key" {
				t.Errorf("got %+v", d)
			}
		}},
		{"title", func(t *testing.T, d *schema.FieldDesc) {
			// "character varying(200)" is what format_type returns; the
			// spelling the DDL layer emits would have matched nothing.
			if d.Type != schema.TypeVarchar || d.Size != 200 {
				t.Errorf("got type %q size %d", d.Type, d.Size)
			}
		}},
		{"note", func(t *testing.T, d *schema.FieldDesc) {
			// The cast Postgres attaches to every stored literal is stripped,
			// so the default renders as the DDL layer would have written it.
			if d.Default == nil || d.Default.Value != "hello" {
				t.Errorf("default = %+v", d.Default)
			}
		}},
		{"score", func(t *testing.T, d *schema.FieldDesc) {
			if !d.Nullable || d.Type != schema.TypeFloat {
				t.Errorf("got %+v", d)
			}
		}},
		{"status", func(t *testing.T, d *schema.FieldDesc) {
			// An enum is text plus a CHECK, so recovering it means reading the
			// expression — in the normalised form Postgres stores, not the
			// IN () the DDL layer wrote.
			if d.Type != schema.TypeEnum {
				t.Fatalf("type = %q, want enum", d.Type)
			}
			if strings.Join(d.EnumValues, ",") != "draft,live" {
				t.Errorf("values = %v", d.EnumValues)
			}
		}},
		{"org_id", func(t *testing.T, d *schema.FieldDesc) {
			if d.Ref == nil {
				t.Fatal("no reference")
			}
			if d.Ref.Name != "org" {
				t.Errorf("relation = %q, want org (the _id suffix is stripped)", d.Ref.Name)
			}
			if d.Ref.Table == nil || d.Ref.Table.Name() != "orgs" {
				t.Errorf("target = %+v", d.Ref.Table)
			}
			if d.Ref.OnDelete != schema.Cascade {
				t.Errorf("on delete = %q", d.Ref.OnDelete)
			}
		}},
	} {
		f := posts.Field(tc.column)
		if f == nil {
			t.Errorf("%s: missing", tc.column)
			continue
		}
		t.Run(tc.column, func(t *testing.T) { tc.check(t, f.Desc()) })
	}

	// A check that is not an enum stays a check.
	if len(posts.Checks()) != 1 || posts.Checks()[0].Name != "views_non_negative" {
		t.Errorf("checks = %+v", posts.Checks())
	}

	// btree is the dialect default and the DDL layer omits it, so recording it
	// would make an unchanged index look changed.
	for _, idx := range posts.Indexes() {
		switch idx.Name {
		case "posts_title_views_idx":
			if idx.Method != "" {
				t.Errorf("btree should not be recorded, got %q", idx.Method)
			}
		case "posts_meta_gin":
			if idx.Method != "gin" {
				t.Errorf("method = %q", idx.Method)
			}
		}
	}
}

func TestBuildPinsOnlyTheNamesThatDiffer(t *testing.T) {
	// Adoption depends on this. A name that matches what the DDL layer would
	// generate is left unpinned, so the schema is not littered with them; one
	// that does not is pinned, or the first diff after an import would drop and
	// recreate the constraint.
	cat := &catalog{
		tables: []tableRow{{Name: "users"}},
		columns: []columnRow{{Table: "users", Name: "id", Type: "uuid", NotNull: true},
			{Table: "users", Name: "email", Type: "text", NotNull: true}},
		constraints: []constraintRow{
			{Table: "users", Name: "users_id_pk", Type: "p", Columns: []string{"id"}},
			{Table: "users", Name: "uq_user_email", Type: "u", Columns: []string{"email"}},
		},
	}
	r, rep, err := build(cat, Options{})
	if err != nil || !rep.Empty() {
		t.Fatalf("build: %v %s", err, rep)
	}
	users := r.Get("users")
	if users.PrimaryKeyName() != "users_id_pk" {
		t.Errorf("primary key name = %q, want it pinned", users.PrimaryKeyName())
	}
	if got := users.Field("email").Desc().ConstraintName; got != "uq_user_email" {
		t.Errorf("constraint name = %q, want it pinned", got)
	}

	// And the conventional names are left alone.
	conv := &catalog{
		tables: []tableRow{{Name: "users"}},
		columns: []columnRow{{Table: "users", Name: "id", Type: "uuid", NotNull: true},
			{Table: "users", Name: "email", Type: "text", NotNull: true}},
		constraints: []constraintRow{
			{Table: "users", Name: "users_pkey", Type: "p", Columns: []string{"id"}},
			{Table: "users", Name: "users_email_key", Type: "u", Columns: []string{"email"}},
		},
	}
	r2, _, err := build(conv, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if n := r2.Get("users").PrimaryKeyName(); n != "" {
		t.Errorf("conventional name should not be pinned, got %q", n)
	}
	if n := r2.Get("users").Field("email").Desc().ConstraintName; n != "" {
		t.Errorf("conventional name should not be pinned, got %q", n)
	}
}

func TestBuildReportsWhatItCannotRepresent(t *testing.T) {
	// The failure that matters is the quiet one: a schema missing a construct
	// still validates and still produces a migration, one that proposes undoing
	// whatever it failed to see.
	cat := &catalog{
		tables: []tableRow{{Name: "t"}},
		columns: []columnRow{
			{Table: "t", Name: "id", Type: "uuid", NotNull: true},
			{Table: "t", Name: "a", Type: "integer", NotNull: true},
			{Table: "t", Name: "b", Type: "integer", NotNull: true},
			{Table: "t", Name: "money", Type: "money"},
			{Table: "t", Name: "total", Type: "integer", Generated: "s"},
			{Table: "t", Name: "seq", Type: "integer", Identity: "a"},
		},
		constraints: []constraintRow{
			{Table: "t", Name: "t_pkey", Type: "p", Columns: []string{"a", "b"}, Def: "PRIMARY KEY (a, b)"},
			{Table: "t", Name: "t_ab_key", Type: "u", Columns: []string{"a", "b"}, Def: "UNIQUE (a, b)"},
			{Table: "t", Name: "t_excl", Type: "x", Def: "EXCLUDE USING gist (a WITH =)"},
		},
		indexes: []indexRow{
			{Table: "t", Name: "t_lower_idx", Expression: true, Def: "CREATE INDEX ... (lower(a))"},
		},
	}
	_, rep, err := build(cat, Options{})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	for _, want := range []string{
		"composite primary key",
		"composite unique constraint",
		"contype x",
		"expression",
		"money",
		"generated column",
		"identity column",
	} {
		if !strings.Contains(rep.String(), want) {
			t.Errorf("report should mention %q:\n%s", want, rep)
		}
	}
	if rep.Err() == nil {
		t.Error("Err should describe a non-empty report")
	}
}

// A self-referential foreign key is common enough — manager_id, parent_id,
// reply_to — that dropping the column it sits on is a serious import bug: the
// registry ends up missing a column the database has, so the next Diff proposes
// adding one that exists, and a drift check proposes dropping a real one.
//
// Only the constraint is unrepresentable. The column is an ordinary uuid.
func TestSelfReferentialForeignKeyKeepsItsColumn(t *testing.T) {
	cat := &catalog{
		tables: []tableRow{{Name: "employees"}},
		columns: []columnRow{
			{Table: "employees", Name: "id", Type: "uuid", NotNull: true},
			{Table: "employees", Name: "name", Type: "text", NotNull: true},
			{Table: "employees", Name: "manager_id", Type: "uuid"},
		},
		constraints: []constraintRow{
			{Table: "employees", Name: "employees_pkey", Type: "p", Columns: []string{"id"}},
			{Table: "employees", Name: "employees_manager_id_fkey", Type: "f",
				Columns: []string{"manager_id"}, RefTable: "employees",
				RefCols: []string{"id"}, Def: "FOREIGN KEY (manager_id) REFERENCES employees(id)"},
		},
	}
	r, rep, err := build(cat, Options{})
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	tbl := r.Get("employees")
	if tbl == nil {
		t.Fatal("employees was not imported at all")
	}
	var found bool
	for _, f := range tbl.Fields() {
		if f.Desc().Name == "manager_id" {
			found = true
		}
	}
	if !found {
		t.Error("manager_id was dropped; only its foreign key is unrepresentable")
	}
	// And the report says so accurately: the old message claimed the target
	// table "is not in the schema being read", which is the table being read.
	if !strings.Contains(rep.String(), "self-referential") {
		t.Errorf("report should name the self-reference:\n%s", rep)
	}
}

// A camelCase identifier is legal Postgres and routine in databases built by
// other tools. It used to abort the whole import at Validate, with an error
// framed as this package having built something impossible.
func TestUndeclarableNamesAreReportedRatherThanFatal(t *testing.T) {
	cat := &catalog{
		tables: []tableRow{{Name: "users"}, {Name: "userProfiles"}},
		columns: []columnRow{
			{Table: "users", Name: "id", Type: "uuid", NotNull: true},
			{Table: "users", Name: "createdAt", Type: "timestamp with time zone"},
			{Table: "userProfiles", Name: "id", Type: "uuid", NotNull: true},
		},
		constraints: []constraintRow{
			{Table: "users", Name: "users_pkey", Type: "p", Columns: []string{"id"}},
			{Table: "userProfiles", Name: "userProfiles_pkey", Type: "p", Columns: []string{"id"}},
		},
	}
	r, rep, err := build(cat, Options{})
	if err != nil {
		t.Fatalf("an undeclarable name should be reported, not fatal: %v", err)
	}
	// The table that *can* be declared still is.
	if r.Get("users") == nil {
		t.Error("a readable table was lost along with the unreadable one")
	}
	if r.Get("userProfiles") != nil {
		t.Error("a table whose name cannot be declared should be skipped")
	}
	for _, want := range []string{"userProfiles", "createdAt", "upper-case"} {
		if !strings.Contains(rep.String(), want) {
			t.Errorf("report should mention %q:\n%s", want, rep)
		}
	}
}

func TestBuildReportsAForeignKeyCycle(t *testing.T) {
	// A reference names the target table's own value, so a cycle is a Go
	// initialisation cycle: there is no ordering that fixes it.
	cat := &catalog{
		tables: []tableRow{{Name: "a"}, {Name: "b"}},
		columns: []columnRow{
			{Table: "a", Name: "id", Type: "uuid", NotNull: true},
			{Table: "a", Name: "b_id", Type: "uuid", NotNull: true},
			{Table: "b", Name: "id", Type: "uuid", NotNull: true},
			{Table: "b", Name: "a_id", Type: "uuid", NotNull: true},
		},
		constraints: []constraintRow{
			{Table: "a", Name: "a_pkey", Type: "p", Columns: []string{"id"}},
			{Table: "b", Name: "b_pkey", Type: "p", Columns: []string{"id"}},
			{Table: "a", Name: "a_b_id_fkey", Type: "f", Columns: []string{"b_id"},
				RefTable: "b", RefCols: []string{"id"}},
			{Table: "b", Name: "b_a_id_fkey", Type: "f", Columns: []string{"a_id"},
				RefTable: "a", RefCols: []string{"id"}},
		},
	}
	_, rep, err := build(cat, Options{})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(rep.String(), "cycle") {
		t.Fatalf("a foreign key cycle must be reported:\n%s", rep)
	}
}

func TestBuildRoundTripsThroughDiff(t *testing.T) {
	// The property ADR-0014 claims: introspection produces the same registry
	// the DSL produces, so diffing what was declared against what came back is
	// empty. This is that claim in miniature — the version against a real
	// database is run by hand, see the package's own notes.
	cat := &catalog{
		tables: []tableRow{{Name: "orgs"}},
		columns: []columnRow{
			{Table: "orgs", Name: "id", Type: "uuid", NotNull: true, Default: "uuid_generate_v7()"},
			{Table: "orgs", Name: "name", Type: "text", NotNull: true},
			{Table: "orgs", Name: "kind", Type: "text", NotNull: true},
		},
		constraints: []constraintRow{
			{Table: "orgs", Name: "orgs_pkey", Type: "p", Columns: []string{"id"}},
			{Table: "orgs", Name: "orgs_name_key", Type: "u", Columns: []string{"name"}},
			{Table: "orgs", Name: "orgs_kind_check", Type: "c", Columns: []string{"kind"},
				Expr: "(kind = ANY (ARRAY['a'::text, 'b'::text]))"},
		},
	}
	imported, _, err := build(cat, Options{})
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	declared := schema.NewRegistry()
	declared.Table("orgs",
		schema.UUIDv7("id").PrimaryKey(),
		schema.Text("name").Unique(),
		schema.Enum("kind", "a", "b"),
	)

	changes, err := migrate.Diff(imported, declared)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(changes) != 0 {
		for _, c := range changes {
			t.Logf("  %s", c.Up)
		}
		t.Fatalf("want no difference between what was declared and what came back, got %d", len(changes))
	}
}

func TestBuildStripsTheModulePrefix(t *testing.T) {
	cat := &catalog{
		tables: []tableRow{{Name: "billing_invoices"}, {Name: "unrelated"}},
		columns: []columnRow{
			{Table: "billing_invoices", Name: "id", Type: "uuid", NotNull: true},
			{Table: "unrelated", Name: "id", Type: "uuid", NotNull: true},
		},
		constraints: []constraintRow{
			{Table: "billing_invoices", Name: "billing_invoices_pkey", Type: "p", Columns: []string{"id"}},
			{Table: "unrelated", Name: "unrelated_pkey", Type: "p", Columns: []string{"id"}},
		},
	}
	r, rep, err := build(cat, Options{Module: "billing"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if r.Get("billing_invoices") == nil {
		t.Error("the module registry should re-apply the prefix it stripped")
	}
	if r.Get("billing_invoices").LocalName() != "invoices" {
		t.Errorf("local name = %q", r.Get("billing_invoices").LocalName())
	}
	// A table without the prefix would be renamed on the way back out, so it
	// is reported rather than quietly absorbed into the module.
	if !strings.Contains(rep.String(), "unrelated") {
		t.Errorf("a table outside the module must be reported:\n%s", rep)
	}
}

func TestColumnType(t *testing.T) {
	for _, tc := range []struct {
		formatted string
		want      schema.Type
		size      int
		ok        bool
	}{
		{"text", schema.TypeText, 0, true},
		{"character varying(200)", schema.TypeVarchar, 200, true},
		{"character varying", schema.TypeText, 0, true},
		{"integer", schema.TypeInt, 0, true},
		{"bigint", schema.TypeBigInt, 0, true},
		{"double precision", schema.TypeFloat, 0, true},
		{"numeric", schema.TypeNumeric, 0, true},
		{"boolean", schema.TypeBool, 0, true},
		{"uuid", schema.TypeUUID, 0, true},
		{"timestamp with time zone", schema.TypeTimestamp, 0, true},
		{"date", schema.TypeDate, 0, true},
		{"time without time zone", schema.TypeTime, 0, true},
		{"jsonb", schema.TypeJSON, 0, true},
		{"bytea", schema.TypeBytes, 0, true},
		// Types with no equivalent are refused rather than approximated: a
		// column imported as the wrong type produces a migration proposing to
		// change the real one.
		{"numeric(10,2)", schema.TypeNumeric, 0, false},
		{"smallint", schema.TypeInt, 0, false},
		{"real", schema.TypeFloat, 0, false},
		{"money", "", 0, false},
		{"timestamp without time zone", "", 0, false},
		{"json", "", 0, false},
	} {
		got, size, ok := columnType(tc.formatted)
		if ok != tc.ok || (ok && (got != tc.want || size != tc.size)) {
			t.Errorf("columnType(%q) = %q,%d,%v; want %q,%d,%v",
				tc.formatted, got, size, ok, tc.want, tc.size, tc.ok)
		}
	}
}

func TestColumnDefault(t *testing.T) {
	for _, tc := range []struct {
		expr, formatted string
		typ             schema.Type
		wantRaw         string
		wantValue       any
	}{
		{"now()", "timestamp with time zone", schema.TypeTimestamp, "now()", nil},
		{"uuid_generate_v7()", "uuid", schema.TypeUUID, "uuid_generate_v7()", nil},
		{"gen_random_uuid()", "uuid", schema.TypeUUID, "gen_random_uuid()", nil},
		// The cast on a stored literal is stripped when it names the column's
		// own type, so the default renders as it was written.
		{"'draft'::text", "text", schema.TypeText, "", "draft"},
		{"'x'::character varying", "character varying(10)", schema.TypeVarchar, "'x'::character varying", nil},
		// Bare literals need no stripping and pass through unchanged.
		{"0", "integer", schema.TypeInt, "0", nil},
		{"false", "boolean", schema.TypeBool, "false", nil},
		// Anything else is faithful rather than understood.
		{"('a'::text || 'b'::text)", "text", schema.TypeText, "('a'::text || 'b'::text)", nil},
	} {
		got := columnDefault(tc.expr, tc.formatted, tc.typ)
		if got == nil {
			t.Errorf("columnDefault(%q) = nil", tc.expr)
			continue
		}
		if got.Raw != tc.wantRaw || got.Value != tc.wantValue {
			t.Errorf("columnDefault(%q) = %+v; want raw %q value %v",
				tc.expr, got, tc.wantRaw, tc.wantValue)
		}
	}
	if columnDefault("", "text", schema.TypeText) != nil {
		t.Error("no default should map to nil")
	}
}

func TestEnumValues(t *testing.T) {
	// The form Postgres stores, not the IN () the DDL layer writes.
	got, ok := enumValues("status", "(status = ANY (ARRAY['draft'::text, 'live'::text]))")
	if !ok || strings.Join(got, ",") != "draft,live" {
		t.Errorf("got %v,%v", got, ok)
	}
	// A value containing a comma survives, because the split is not naive.
	got, ok = enumValues("k", "(k = ANY (ARRAY['a,b'::text, 'c'::text]))")
	if !ok || len(got) != 2 || got[0] != "a,b" {
		t.Errorf("got %v,%v", got, ok)
	}
	// An ordinary check is not an enum.
	if _, ok := enumValues("views", "(views >= 0)"); ok {
		t.Error("a comparison is not an enum")
	}
}
