package schema_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jryannel/sqlb/schema"
)

// The three tiers ADR-0041 names, declared and accepted: a row-local
// expression, a correlated subquery, and one whose answer depends on who is
// asking.
func TestComputedDeclaration(t *testing.T) {
	r := schema.NewRegistry()
	projects := r.Table("projects",
		schema.UUIDv7("id").PrimaryKey(),
		schema.Date("due_date").Filterable().Sortable(),
		schema.Int("open_tasks").Filterable(),

		schema.Computed("is_overdue", schema.TypeBool,
			schema.FromSQL("due_date < current_date AND open_tasks > 0")).
			Filterable(),
		schema.Computed("total_tasks", schema.TypeInt,
			schema.FromSQL("(SELECT count(*) FROM tasks t WHERE t.project_id = projects.id)")),
		schema.Computed("is_starred", schema.TypeBool,
			schema.FromSQL("EXISTS (SELECT 1 FROM stars s "+
				"WHERE s.project_id = projects.id AND s.member_id = ?)")).
			Needs("viewer").Filterable(),
	).Expose(schema.REST{Ops: schema.CRUD | schema.OpList})

	if err := r.Validate(); err != nil {
		t.Fatalf("valid schema rejected: %v", err)
	}

	// A computed column is a column: it is in Fields, so it reaches the model,
	// the clients and the CLI.
	if got, want := len(projects.Fields()), 6; got != want {
		t.Errorf("projects has %d fields, want %d", got, want)
	}
	// It is not storage, so the DDL and the diff do not see it.
	if got, want := len(projects.StoredFields()), 3; got != want {
		t.Errorf("projects has %d stored fields, want %d", got, want)
	}
	if projects.StoredField("is_overdue") != nil {
		t.Error("a computed column must not look like storage")
	}

	// Nothing writes an expression, so the declaration is ReadOnly whether or
	// not the author said so — which is what keeps it out of the generated
	// create and update bodies.
	if !projects.Field("is_overdue").Desc().ReadOnly {
		t.Error("a computed column should be ReadOnly")
	}
	if got := projects.Field("is_starred").Desc().Needs; len(got) != 1 || got[0] != "viewer" {
		t.Errorf("Needs = %v, want [viewer]", got)
	}
}

func TestComputedRefusals(t *testing.T) {
	tests := []struct {
		name  string
		build func(*schema.Registry)
		want  string
	}{
		{
			name: "searchable",
			build: func(r *schema.Registry) {
				r.Table("a", schema.UUIDv7("id").PrimaryKey(),
					schema.Computed("label", schema.TypeText, schema.FromSQL("upper(name)")).Searchable())
			},
			want: "cannot be Searchable",
		},
		{
			name: "sortable over a volatile expression",
			build: func(r *schema.Registry) {
				r.Table("b", schema.UUIDv7("id").PrimaryKey(),
					schema.Computed("is_overdue", schema.TypeBool,
						schema.FromSQL("due_date < now()")).Sortable())
			},
			want: "does not hold still between pages",
		},
		{
			name: "bind count disagrees with Needs",
			build: func(r *schema.Registry) {
				r.Table("c", schema.UUIDv7("id").PrimaryKey(),
					schema.Computed("mine", schema.TypeBool,
						schema.FromSQL("owner_id = ?")).Needs("viewer", "org"))
			},
			want: "takes 1 bind(s) but Needs names 2",
		},
		{
			name: "Needs without an expression",
			build: func(r *schema.Registry) {
				r.Table("d", schema.UUIDv7("id").PrimaryKey(),
					schema.Text("name").Needs("viewer"))
			},
			want: "only meaningful on a Computed column",
		},
		{
			name: "primary key",
			build: func(r *schema.Registry) {
				r.Table("e", schema.Computed("id", schema.TypeText, schema.FromSQL("'x'")).PrimaryKey())
			},
			want: "cannot be the primary key",
		},
		{
			name: "default",
			build: func(r *schema.Registry) {
				r.Table("f", schema.UUIDv7("id").PrimaryKey(),
					schema.Computed("n", schema.TypeInt, schema.FromSQL("1")).Default(schema.Value(0)))
			},
			want: "cannot have a Default",
		},
		{
			name: "indexed",
			build: func(r *schema.Registry) {
				r.Table("g", schema.UUIDv7("id").PrimaryKey(),
					schema.Computed("n", schema.TypeInt, schema.FromSQL("1"))).Index("n")
			},
			want: "covers a computed column",
		},
		{
			name: "enum",
			build: func(r *schema.Registry) {
				r.Table("h", schema.UUIDv7("id").PrimaryKey(),
					schema.Computed("state", schema.TypeEnum, schema.FromSQL("'open'")))
			},
			want: "cannot be an Enum",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := schema.NewRegistry()
			tc.build(r)
			err := r.Validate()
			if err == nil {
				t.Fatalf("want an error mentioning %q, got none", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// Sortable over a stable expression is fine — it is the volatile one the
// keyset cannot page.
func TestComputedSortableIsAllowedWhenStable(t *testing.T) {
	r := schema.NewRegistry()
	r.Table("a", schema.UUIDv7("id").PrimaryKey(),
		schema.Int("total").Filterable(),
		schema.Computed("progress", schema.TypeInt,
			schema.FromSQL("(done * 100 / NULLIF(total, 0))")).Sortable())
	if err := r.Validate(); err != nil {
		t.Fatalf("a stable computed column should be sortable: %v", err)
	}
}

// The manifest is what a program reads to answer "what does this endpoint
// serve, and what did the server have to do to serve it".
func TestComputedInManifest(t *testing.T) {
	r := schema.NewRegistry()
	r.Table("projects",
		schema.UUIDv7("id").PrimaryKey(),
		schema.Computed("is_starred", schema.TypeBool,
			schema.FromSQL("EXISTS (SELECT 1 FROM stars s WHERE s.member_id = ?)")).
			Needs("viewer").Filterable(),
	).Expose(schema.REST{Ops: schema.OpList})

	raw, err := json.Marshal(r.BuildManifest())
	if err != nil {
		t.Fatalf("marshalling the manifest: %v", err)
	}
	for _, want := range []string{`"computed":true`, `"needs":["viewer"]`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("manifest is missing %s:\n%s", want, raw)
		}
	}
}

// The lint rules about indexes have nothing to say about a column that cannot
// be indexed, so they say the one thing that is true instead.
func TestComputedLint(t *testing.T) {
	r := schema.NewRegistry()
	r.Table("projects",
		schema.UUIDv7("id").PrimaryKey(),
		schema.Computed("total_tasks", schema.TypeInt,
			schema.FromSQL("(SELECT count(*) FROM tasks t WHERE t.project_id = projects.id)")).
			Filterable(),
	)

	var found bool
	for _, d := range r.Lint() {
		switch d.Rule {
		case "computed-without-index":
			found = true
			if !strings.Contains(d.Message, "runs a subquery") {
				t.Errorf("a subquery's cost should be named: %s", d.Message)
			}
		case "unindexed-filter", "unindexed-sort":
			t.Errorf("an index rule fired on a column that cannot be indexed: %s", d)
		}
	}
	if !found {
		t.Error("a filterable computed column should be reported once")
	}
}
