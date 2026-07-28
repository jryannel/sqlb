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
			name: "scoped column a request could write",
			build: func(r *schema.Registry) {
				r.Table("s1", schema.UUIDv7("id").PrimaryKey(),
					schema.UUID("org_id").Filterable().Scoped())
			},
			want: "must be ReadOnly",
		},
		{
			name: "scoped column that may be NULL",
			build: func(r *schema.Registry) {
				r.Table("s2", schema.UUIDv7("id").PrimaryKey(),
					schema.UUID("org_id").Nullable().ReadOnly().Scoped())
			},
			want: "cannot be Nullable",
		},
		{
			name: "two scope columns",
			build: func(r *schema.Registry) {
				r.Table("s3", schema.UUIDv7("id").PrimaryKey(),
					schema.UUID("org_id").ReadOnly().Scoped(),
					schema.UUID("team_id").ReadOnly().Scoped())
			},
			want: "2 Scoped columns declared",
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

// Reverse relations: what a declared Inverse must satisfy. ADR-0022.
func TestInverseValidation(t *testing.T) {
	tests := []struct {
		name  string
		build func(*schema.Registry)
		want  string
	}{
		{
			// The case the whole record was written for. Two references from
			// one table to another would derive the same reverse name, and an
			// author's posts are not the posts an author reviewed.
			name: "two references claim one name on the target",
			build: func(r *schema.Registry) {
				authors := r.Table("authors", schema.UUIDv7("id").PrimaryKey())
				r.Table("posts",
					schema.UUIDv7("id").PrimaryKey(),
					schema.Ref("author", authors).Inverse("posts").InverseExpandable(),
					schema.Ref("reviewer", authors).Inverse("posts").InverseExpandable(),
				)
			},
			want: "already claimed",
		},
		{
			name: "the name collides with a column of the target",
			build: func(r *schema.Registry) {
				authors := r.Table("authors",
					schema.UUIDv7("id").PrimaryKey(),
					schema.Text("posts"),
				)
				r.Table("posts",
					schema.UUIDv7("id").PrimaryKey(),
					schema.Ref("author", authors).Inverse("posts"),
				)
			},
			want: "collides with a column",
		},
		{
			name: "exposed without being named",
			build: func(r *schema.Registry) {
				authors := r.Table("authors", schema.UUIDv7("id").PrimaryKey())
				r.Table("posts",
					schema.UUIDv7("id").PrimaryKey(),
					schema.Ref("author", authors).InverseExpandable(),
				)
			},
			want: "InverseExpandable without Inverse",
		},
		{
			// Nothing about the other side of a module boundary is resolvable,
			// which is the same reason ExternalRef cannot be Expandable.
			name: "declared across a module boundary",
			build: func(r *schema.Registry) {
				r.Table("invoices",
					schema.UUIDv7("id").PrimaryKey(),
					schema.ExternalRef("tenant", "tenants.id").Inverse("invoices"),
				)
			},
			want: "cannot declare an Inverse",
		},
		{
			// The easy mistake: an expanded collection is ordered by the rows
			// it collects, which are the referencing table's, not the target's.
			name: "ordered by a column of the wrong table",
			build: func(r *schema.Registry) {
				authors := r.Table("authors",
					schema.UUIDv7("id").PrimaryKey(),
					schema.Text("name"),
				)
				r.Table("posts",
					schema.UUIDv7("id").PrimaryKey(),
					schema.Ref("author", authors).
						Inverse("posts").
						InverseExpandable(schema.ExpandOrder("name")),
				)
			},
			want: "is not a column of",
		},
		{
			// The one place the library used to silently do the opposite of
			// what the table declares: nothing reads deleted_at, so the
			// generated DELETE removed the row and the column meant to record
			// its removal stayed NULL forever.
			name: "soft delete declared and hard delete exposed",
			build: func(r *schema.Registry) {
				r.Table("posts",
					schema.UUIDv7("id").PrimaryKey(),
					schema.Text("title"),
					schema.SoftDelete(),
				).Expose(schema.REST{Ops: schema.CRUD})
			},
			want: "hard-deletes the row",
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

// Two references to one table are fine as long as they are named apart, which
// is the point of declaring the name rather than deriving it.
func TestTwoInversesOnOneTargetAreFineWhenNamedApart(t *testing.T) {
	r := schema.NewRegistry()
	authors := r.Table("authors", schema.UUIDv7("id").PrimaryKey())
	posts := r.Table("posts",
		schema.UUIDv7("id").PrimaryKey(),
		schema.Ref("author", authors).Filterable().Inverse("written").InverseExpandable(),
		schema.Ref("reviewer", authors).Filterable().Inverse("reviewed").InverseExpandable(),
	).Index("author_id").Index("reviewer_id")
	if err := r.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	_ = posts

	inv := r.Inverses(authors)
	if len(inv) != 2 {
		t.Fatalf("got %d inverses, want 2", len(inv))
	}
	if inv[0].Name != "written" || inv[0].Column != "author_id" {
		t.Errorf("first inverse = %+v", inv[0])
	}
	if inv[1].Name != "reviewed" || inv[1].Column != "reviewer_id" {
		t.Errorf("second inverse = %+v", inv[1])
	}

	// The manifest describes the relationship from the target's side, which is
	// the side that cannot see the declaration.
	m := r.BuildManifest()
	var found int
	for _, tm := range m.Tables {
		if tm.Name != "authors" {
			continue
		}
		found = len(tm.CollectedBy)
	}
	if found != 2 {
		t.Errorf("the manifest describes %d reverse relations on authors, want 2", found)
	}
}

// A named inverse that nothing exposed is still a fact about the schema, and it
// is not an error: exposure is a separate decision (ADR-0006).
func TestAnUnexposedInverseIsNamedButNotExpandable(t *testing.T) {
	r := schema.NewRegistry()
	authors := r.Table("authors", schema.UUIDv7("id").PrimaryKey()).
		Expose(schema.REST{Ops: schema.OpList})
	r.Table("posts",
		schema.UUIDv7("id").PrimaryKey(),
		schema.Ref("author", authors).Inverse("posts"),
	)
	if err := r.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	inv := r.Inverses(authors)
	if len(inv) != 1 || inv[0].Expandable {
		t.Fatalf("inverses = %+v, want one that is not expandable", inv)
	}
	for _, tm := range r.BuildManifest().Tables {
		if tm.Name != "authors" || tm.REST == nil {
			continue
		}
		for _, name := range tm.REST.Expandable {
			if name == "posts" {
				t.Error("an unexposed inverse reached the ?expand vocabulary")
			}
		}
	}
}
