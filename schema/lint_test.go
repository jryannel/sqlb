package schema_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jryannel/sqlb/schema"
)

func rules(ds schema.Diagnostics) map[string]bool {
	out := map[string]bool{}
	for _, d := range ds {
		out[d.Rule] = true
	}
	return out
}

func TestLintCatchesUnindexedFilter(t *testing.T) {
	r := schema.NewRegistry()
	r.Table("a",
		schema.UUIDv7("id").PrimaryKey(),
		schema.Text("email").Filterable(), // no index
		schema.Text("slug").Unique().Filterable(),
	)
	got := rules(r.Lint())
	if !got["unindexed-filter"] {
		t.Error("a filterable column with no index should be flagged")
	}
	for _, d := range r.Lint() {
		if d.Rule == "unindexed-filter" && d.Column == "slug" {
			t.Error("a unique column is already indexed and should not be flagged")
		}
		if d.Rule == "unindexed-filter" && d.Fix == "" {
			t.Error("the diagnostic should carry a concrete fix")
		}
	}
}

func TestLintIgnoresLowCardinalityColumns(t *testing.T) {
	r := schema.NewRegistry()
	r.Table("b",
		schema.UUIDv7("id").PrimaryKey(),
		schema.Bool("active").Filterable(),
		schema.Enum("state", "draft", "live").Filterable(),
	)
	for _, d := range r.Lint() {
		if d.Rule == "unindexed-filter" {
			t.Errorf("a boolean or short enum should not be flagged: %s", d)
		}
	}
}

func TestLintCatchesSearchWithoutTrigram(t *testing.T) {
	r := schema.NewRegistry()
	r.Table("c", schema.UUIDv7("id").PrimaryKey(), schema.Text("title").Searchable())
	if !rules(r.Lint())["search-without-trigram"] {
		t.Error("a searchable column with no GIN index should be flagged")
	}

	r2 := schema.NewRegistry()
	r2.Table("d", schema.UUIDv7("id").PrimaryKey(), schema.Text("title").Searchable()).
		AddIndex(schema.Index{Columns: []string{"title"}, Method: "gin"})
	if rules(r2.Lint())["search-without-trigram"] {
		t.Error("a GIN index should satisfy the search rule")
	}
}

func TestLintCatchesUnstablePagination(t *testing.T) {
	r := schema.NewRegistry()
	r.Table("e", schema.UUIDv7("id").PrimaryKey(), schema.Text("x").Filterable()).
		Index("x").
		Expose(schema.REST{Ops: schema.OpList, MaxPageSize: 50})
	if !rules(r.Lint())["list-without-sort"] {
		t.Error("a list endpoint with no sortable column should be flagged")
	}
}

func TestLintCatchesUnindexedExpansion(t *testing.T) {
	r := schema.NewRegistry()
	org := r.Table("orgs", schema.UUIDv7("id").PrimaryKey())
	r.Table("f", schema.UUIDv7("id").PrimaryKey(), schema.Ref("org", org).Expandable())
	if !rules(r.Lint())["unindexed-expand"] {
		t.Error("an expandable relation with no index on its key should be flagged")
	}
}

func TestLintSeparatesWarningsFromInfo(t *testing.T) {
	r := schema.NewRegistry()
	r.Table("g", schema.UUIDv7("id").PrimaryKey(), schema.Text("x").Sortable()).
		Expose(schema.REST{Ops: schema.OpList})

	all := r.Lint()
	warn := all.Warnings()
	if len(warn) >= len(all) {
		t.Error("this schema should produce info-level diagnostics that are not warnings")
	}
	for _, d := range warn {
		if d.Severity != schema.SeverityWarn {
			t.Errorf("Warnings returned a %s diagnostic", d.Severity)
		}
	}
}

func TestLintIsQuietOnAWellFormedSchema(t *testing.T) {
	r := schema.NewRegistry()
	r.Table("h",
		schema.UUIDv7("id").PrimaryKey(),
		schema.Text("email").Unique().Filterable(),
		schema.Timestamp("created_at").Sortable(),
	).
		Index("created_at").
		Expose(schema.REST{Ops: schema.OpList, MaxPageSize: 100})

	if w := r.Lint().Warnings(); len(w) > 0 {
		t.Errorf("a well-formed schema should produce no warnings, got:\n%s", w)
	}
}

func TestManifestDescribesTheQueryableSurface(t *testing.T) {
	r := schema.NewRegistry()
	org := r.Table("orgs", schema.UUIDv7("id").PrimaryKey(), schema.Text("name"))
	r.Table("posts",
		schema.UUIDv7("id").PrimaryKey(),
		schema.Ref("org", org).Expandable(),
		schema.Text("title").Searchable().Sortable(),
		schema.Enum("status", "draft", "live").Filterable(),
		schema.Text("secret").Hidden(),
	).Expose(schema.REST{Ops: schema.CRUD | schema.OpList, MaxPageSize: 100})

	m := r.BuildManifest()
	var posts *schema.TableManifest
	for i := range m.Tables {
		if m.Tables[i].Name == "posts" {
			posts = &m.Tables[i]
		}
	}
	if posts == nil {
		t.Fatal("posts missing from the manifest")
	}

	// A hidden column must not appear at all: the manifest is publishable, and
	// the name is itself information.
	for _, c := range posts.Columns {
		if c.Name == "secret" {
			t.Error("a hidden column leaked into the manifest")
		}
	}
	if posts.REST == nil {
		t.Fatal("REST surface missing")
	}
	if !contains(posts.REST.Filterable, "status") || !contains(posts.REST.Searchable, "title") {
		t.Errorf("capabilities not reported: %+v", posts.REST)
	}
	// The manifest reports what a caller can actually ask for. "org" is
	// declared Expandable above, but ?expand performs no join yet, so
	// advertising it would send a client into a request that returns 200 with
	// the relation missing. This assertion inverts when expansion lands.
	if len(posts.REST.Expandable) != 0 {
		t.Errorf("manifest advertises expansion, which is not implemented: %+v", posts.REST.Expandable)
	}
	for _, c := range posts.Columns {
		if contains(c.Capabilities, "expand") {
			t.Errorf("column %s advertises the expand capability, which is not implemented", c.Name)
		}
	}
	if len(posts.REST.Examples) == 0 {
		t.Error("no worked examples emitted")
	}
	if posts.PrimaryKey != "id" {
		t.Errorf("primary key = %q", posts.PrimaryKey)
	}

	// The enum's values are what a client needs to send a valid filter.
	for _, c := range posts.Columns {
		if c.Name == "status" && len(c.Enum) != 2 {
			t.Errorf("enum values not reported: %+v", c)
		}
	}

	b, err := m.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if !json.Valid(b) {
		t.Error("manifest is not valid JSON")
	}
	if !strings.Contains(string(b), "filterOperators") {
		t.Error("the operator vocabulary should be in the manifest")
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
