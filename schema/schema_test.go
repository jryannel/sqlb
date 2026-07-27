package schema_test

import (
	"strings"
	"testing"

	"github.com/jryannel/sqlb/schema"
)

func TestTableDeclaration(t *testing.T) {
	r := schema.NewRegistry()
	org := r.Table("orgs", schema.UUIDv7("id").PrimaryKey(), schema.Text("name"))
	user := r.Table("users",
		schema.UUIDv7("id").PrimaryKey(),
		schema.Text("email").Unique().Searchable(),
		schema.Int("age").Nullable().Filterable().Sortable(),
		schema.Ref("org", org).OnDelete(schema.Cascade).Expandable(),
		schema.Timestamps(),
	).Index("email").Expose(schema.REST{Ops: schema.CRUD | schema.OpList})

	if err := r.Validate(); err != nil {
		t.Fatalf("valid schema rejected: %v", err)
	}

	// Timestamps contributes two columns, so the group must be flattened.
	if got, want := len(user.Fields()), 6; got != want {
		t.Errorf("users has %d columns, want %d", got, want)
	}

	// Ref names the column after the relation and adopts the target's key type.
	ref := user.Field("org_id")
	if ref == nil {
		t.Fatal("Ref should have produced an org_id column")
	}
	if ref.Desc().Ref.Table != org {
		t.Error("org_id does not point at orgs")
	}
	if got := ref.Desc().Type; got != schema.TypeUUID {
		t.Errorf("org_id type = %s, want %s (adopted from the target key)", got, schema.TypeUUID)
	}
	if ref.Desc().Ref.OnDelete != schema.Cascade {
		t.Error("OnDelete was not recorded")
	}

	// Expose defaults the path from the table name.
	if got := user.Rest().Path; got != "/users" {
		t.Errorf("REST path = %q, want %q", got, "/users")
	}
}

func TestCapabilityTagRoundTrip(t *testing.T) {
	r := schema.NewRegistry()
	tbl := r.Table("posts",
		schema.UUIDv7("id").PrimaryKey(),
		schema.Text("title").Searchable().Sortable(),
		schema.Text("secret").Hidden(),
	)

	// The tag body is what the runtime engine reads back off the generated
	// model, so it is the contract between the two halves of the system.
	if got, want := tbl.Field("id").Desc().Capabilities(), "pk,default,filter,readonly"; got != want {
		t.Errorf("id capabilities = %q, want %q", got, want)
	}
	// Searchable implies filterable, so ?title=... works on a searchable column.
	if got, want := tbl.Field("title").Desc().Capabilities(), "filter,sort,search"; got != want {
		t.Errorf("title capabilities = %q, want %q", got, want)
	}
	if got, want := tbl.Field("secret").Desc().Capabilities(), "hidden"; got != want {
		t.Errorf("secret capabilities = %q, want %q", got, want)
	}
}

func TestGoTypeMapping(t *testing.T) {
	r := schema.NewRegistry()
	tbl := r.Table("things",
		schema.UUIDv7("id").PrimaryKey(),
		schema.Int("count"),
		schema.Int("maybe_count").Nullable(),
		schema.Timestamp("at"),
		schema.Bytes("blob").Nullable(),
	)
	for _, tt := range []struct{ column, want string }{
		{"id", "string"},
		{"count", "int32"},
		{"maybe_count", "*int32"},
		{"at", "time.Time"},
		{"blob", "[]byte"}, // already nilable, so it is not wrapped in a pointer
	} {
		if got := tbl.Field(tt.column).Desc().GoType(); got != tt.want {
			t.Errorf("%s: Go type = %q, want %q", tt.column, got, tt.want)
		}
	}
}

// Validation is the schema author's feedback loop, so it reports every problem
// at once and each message has to name the fix.
func TestValidationCatchesAuthoringMistakes(t *testing.T) {
	tests := []struct {
		name  string
		build func(*schema.Registry)
		want  string
	}{
		{
			name: "two primary keys",
			build: func(r *schema.Registry) {
				r.Table("a", schema.UUIDv7("id").PrimaryKey(), schema.UUIDv7("other").PrimaryKey())
			},
			want: "expected at most one",
		},
		{
			name: "duplicate column",
			build: func(r *schema.Registry) {
				r.Table("b", schema.UUIDv7("id").PrimaryKey(), schema.Text("x"), schema.Int("x"))
			},
			want: "declared twice",
		},
		{
			name: "searchable non-text column",
			build: func(r *schema.Registry) {
				r.Table("c", schema.UUIDv7("id").PrimaryKey(), schema.Int("n").Searchable())
			},
			want: "Searchable requires a text column",
		},
		{
			name: "expandable non-reference",
			build: func(r *schema.Registry) {
				r.Table("d", schema.UUIDv7("id").PrimaryKey(), schema.Text("x").Expandable())
			},
			want: "only meaningful on a Ref",
		},
		{
			name: "hidden and filterable leaks through probing",
			build: func(r *schema.Registry) {
				r.Table("e", schema.UUIDv7("id").PrimaryKey(), schema.Text("pw").Hidden().Filterable())
			},
			want: "leaks its contents",
		},
		{
			name: "index over an unknown column",
			build: func(r *schema.Registry) {
				r.Table("f", schema.UUIDv7("id").PrimaryKey()).Index("nonexistent")
			},
			want: "unknown column",
		},
		{
			name: "exposed for read without a key to address rows by",
			build: func(r *schema.Registry) {
				r.Table("g", schema.Text("x")).Expose(schema.REST{Ops: schema.OpRead})
			},
			want: "no primary key",
		},
		{
			name: "colliding REST paths",
			build: func(r *schema.Registry) {
				r.Table("h", schema.UUIDv7("id").PrimaryKey()).Expose(schema.REST{Path: "/same", Ops: schema.OpList})
				r.Table("i", schema.UUIDv7("id").PrimaryKey()).Expose(schema.REST{Path: "/same", Ops: schema.OpList})
			},
			want: "already used by table",
		},
		{
			name: "page size above the maximum",
			build: func(r *schema.Registry) {
				r.Table("j", schema.UUIDv7("id").PrimaryKey()).
					Expose(schema.REST{Ops: schema.OpList, DefaultPageSize: 500, MaxPageSize: 100})
			},
			want: "exceeds MaxPageSize",
		},
		{
			name: "identifier that is not valid SQL",
			build: func(r *schema.Registry) {
				r.Table("k", schema.UUIDv7("id").PrimaryKey(), schema.Text("Bad Name"))
			},
			want: "not a valid SQL identifier",
		},
		{
			// A rename hint asserts that the old name is gone. If the old
			// column is still declared, either the hint is wrong or the two
			// columns are being swapped — which Postgres cannot do in one
			// statement either.
			name: "column renamed from one that still exists",
			build: func(r *schema.Registry) {
				r.Table("l",
					schema.UUIDv7("id").PrimaryKey(),
					schema.Text("headline"),
					schema.Text("title").RenamedFrom("headline"),
				)
			},
			want: "still declared as a column of its own",
		},
		{
			name: "two columns renamed from the same one",
			build: func(r *schema.Registry) {
				r.Table("m",
					schema.UUIDv7("id").PrimaryKey(),
					schema.Text("title").RenamedFrom("headline"),
					schema.Text("subtitle").RenamedFrom("headline"),
				)
			},
			want: "also claimed by column",
		},
		{
			name: "column renamed from itself",
			build: func(r *schema.Registry) {
				r.Table("n", schema.UUIDv7("id").PrimaryKey(), schema.Text("title").RenamedFrom("title"))
			},
			want: "RenamedFrom names the column itself",
		},
		{
			name: "table renamed from one that still exists",
			build: func(r *schema.Registry) {
				r.Table("orgs", schema.UUIDv7("id").PrimaryKey())
				r.Table("organisations", schema.UUIDv7("id").PrimaryKey()).RenamedFrom("orgs")
			},
			want: "still declared as a table of its own",
		},
		{
			name: "two tables renamed from the same one",
			build: func(r *schema.Registry) {
				r.Table("p", schema.UUIDv7("id").PrimaryKey()).RenamedFrom("old")
				r.Table("q", schema.UUIDv7("id").PrimaryKey()).RenamedFrom("old")
			},
			want: "also claimed by table",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := schema.NewRegistry()
			tt.build(r)
			err := r.Validate()
			if err == nil {
				t.Fatalf("expected validation to fail with %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestValidationReportsEveryProblem(t *testing.T) {
	r := schema.NewRegistry()
	r.Table("multi",
		schema.UUIDv7("id").PrimaryKey(),
		schema.Int("n").Searchable(),
		schema.Text("x").Expandable(),
	).Index("missing")

	err := r.Validate()
	if err == nil {
		t.Fatal("expected validation to fail")
	}
	for _, want := range []string{"Searchable requires", "only meaningful on a Ref", "unknown column"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got: %v", want, err)
		}
	}
}

func TestDuplicateTableNamePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("declaring the same table twice should panic at init rather than produce confusing DDL")
		}
	}()
	r := schema.NewRegistry()
	r.Table("dup", schema.UUIDv7("id").PrimaryKey())
	r.Table("dup", schema.UUIDv7("id").PrimaryKey())
}

func TestOnDeleteOnNonReferencePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("OnDelete on a plain column should panic")
		}
	}()
	schema.Text("x").OnDelete(schema.Cascade)
}

func TestExposedTablesAreListedSeparately(t *testing.T) {
	r := schema.NewRegistry()
	r.Table("internal_audit", schema.UUIDv7("id").PrimaryKey())
	r.Table("public_docs", schema.UUIDv7("id").PrimaryKey()).
		Expose(schema.REST{Ops: schema.OpList})

	if got := len(r.Tables()); got != 2 {
		t.Errorf("registry holds %d tables, want 2", got)
	}
	exposed := r.Exposed()
	if len(exposed) != 1 || exposed[0].Name() != "public_docs" {
		t.Errorf("Exposed() = %v, want only public_docs: a table without Expose has no REST surface", exposed)
	}
}
