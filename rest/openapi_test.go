package rest_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jryannel/sqlb/rest"
)

// paramsOf indexes a list operation's query parameters by name.
func paramsOf(t *testing.T, api huma.API, path string) map[string]*huma.Param {
	t.Helper()
	item := api.OpenAPI().Paths[path]
	if item == nil || item.Get == nil {
		t.Fatalf("no GET operation documented at %s", path)
	}
	out := map[string]*huma.Param{}
	for _, p := range item.Get.Parameters {
		out[p.Name] = p
	}
	return out
}

// The claim ADR-0007 doubted: a compositional filter grammar can be described
// precisely, by enumerating one parameter per filterable column rather than
// trying to express the grammar itself.
func TestListDocumentsOneParameterPerFilterableColumn(t *testing.T) {
	db := newFakeDB(t)
	api := mount(t, db.db, postOptions())
	params := paramsOf(t, api, "/posts")

	for _, name := range []string{"id", "org_id", "title", "status", "view_count"} {
		if params[name] == nil {
			t.Errorf("filterable column %s has no documented parameter", name)
		}
	}
	// body declares only search, and search implies filter, so it does get a
	// parameter. excerpt declares nothing and so gets none: capabilities are
	// opt-in, and the document says exactly which columns opted in.
	if params["body"] == nil {
		t.Error("a searchable column is filterable too and should be a parameter")
	}
	if params["excerpt"] != nil {
		t.Error("excerpt declares no capability and should not be a parameter")
	}
	// The hidden column must not appear anywhere in the document.
	if params["secret"] != nil {
		t.Error("hidden column documented as a parameter")
	}
}

func TestFilterParameterDocumentsItsOperators(t *testing.T) {
	db := newFakeDB(t)
	api := mount(t, db.db, postOptions())
	params := paramsOf(t, api, "/posts")

	text := params["title"].Description
	for _, op := range []string{"eq", "in", "contains", "startswith"} {
		if !strings.Contains(text, op) {
			t.Errorf("title's description omits the %s operator: %s", op, text)
		}
	}
	// The pattern operators need a text column, so an integer must not offer
	// them — documenting a request that parsing rejects is worse than silence.
	number := params["view_count"].Description
	for _, op := range []string{"contains", "startswith", "ilike"} {
		if strings.Contains(number, op) {
			t.Errorf("view_count's description offers the text-only %s operator: %s", op, number)
		}
	}
}

func TestSortParameterEnumeratesSortableColumnsInBothDirections(t *testing.T) {
	db := newFakeDB(t)
	api := mount(t, db.db, postOptions())
	params := paramsOf(t, api, "/posts")

	sort := params["sort"]
	if sort == nil || sort.Schema == nil || sort.Schema.Items == nil {
		t.Fatal("sort is not documented as an array")
	}
	got := map[string]bool{}
	for _, v := range sort.Schema.Items.Enum {
		name, _ := v.(string)
		got[name] = true
	}
	for _, want := range []string{"title", "-title", "status", "-status", "view_count", "-view_count"} {
		if !got[want] {
			t.Errorf("sort enum missing %q: %v", want, sort.Schema.Items.Enum)
		}
	}
	// excerpt is not sortable, in either direction.
	if got["excerpt"] || got["-excerpt"] {
		t.Errorf("sort enum offers a column that is not sortable: %v", sort.Schema.Items.Enum)
	}
}

func TestSelectEnumeratesOnlyVisibleColumns(t *testing.T) {
	db := newFakeDB(t)
	api := mount(t, db.db, postOptions())

	sel := paramsOf(t, api, "/posts")["select"]
	if sel == nil || sel.Schema == nil || sel.Schema.Items == nil {
		t.Fatal("select is not documented as an array")
	}
	for _, v := range sel.Schema.Items.Enum {
		if v == "secret" {
			t.Error("select enum discloses the hidden column")
		}
	}
}

func TestPerPageDocumentsTheResourceCeiling(t *testing.T) {
	db := newFakeDB(t)
	api := mount(t, db.db, postOptions())

	perPage := paramsOf(t, api, "/posts")["per_page"]
	if perPage == nil {
		t.Fatal("per_page is not documented")
	}
	if perPage.Schema.Default != 2 {
		t.Errorf("per_page default = %v, want the resource's 2", perPage.Schema.Default)
	}
	if !strings.Contains(perPage.Description, "10") {
		t.Errorf("per_page should document the ceiling of 10: %s", perPage.Description)
	}
}

func TestErrorResponsesCarryTheAllowedField(t *testing.T) {
	db := newFakeDB(t)
	api := mount(t, db.db, postOptions())

	resp := api.OpenAPI().Paths["/posts"].Get.Responses["400"]
	if resp == nil {
		t.Fatal("the list operation does not document a 400")
	}
	media := resp.Content["application/problem+json"]
	if media == nil || media.Schema == nil {
		t.Fatal("the 400 has no problem+json schema")
	}
	schema := media.Schema
	if schema.Ref != "" {
		schema = api.OpenAPI().Components.Schemas.SchemaFromRef(schema.Ref)
	}
	detail := schema.Properties["errors"]
	if detail == nil || detail.Items == nil {
		t.Fatal("the error schema has no errors array")
	}
	items := detail.Items
	if items.Ref != "" {
		items = api.OpenAPI().Components.Schemas.SchemaFromRef(items.Ref)
	}
	// ADR-0011's substance: the allow-list is a structured field, not prose a
	// client would have to parse out of the message.
	if items.Properties["allowed"] == nil {
		t.Errorf("the error detail schema has no allowed field: %v", keys(items.Properties))
	}
}

func TestDocumentIsSerialisable(t *testing.T) {
	db := newFakeDB(t)
	api := mount(t, db.db, postOptions())

	doc, err := api.OpenAPI().YAML()
	if err != nil {
		t.Fatalf("rendering the document: %v", err)
	}
	if len(doc) == 0 {
		t.Fatal("the document is empty")
	}
	// A hidden column must not survive anywhere in the document, including in
	// the response schema derived from the model struct.
	if strings.Contains(string(doc), "secret") {
		t.Error("the OpenAPI document mentions a hidden column")
	}

	if _, err := json.Marshal(api.OpenAPI()); err != nil {
		t.Fatalf("marshalling the document as JSON: %v", err)
	}
}

func TestOperationsAreRegisteredForTheDeclaredOpsOnly(t *testing.T) {
	db := newFakeDB(t)
	opts := postOptions()
	opts.Ops = rest.OpList
	api := mount(t, db.db, opts)

	if api.OpenAPI().Paths["/posts"].Get == nil {
		t.Error("list was not registered")
	}
	if item := api.OpenAPI().Paths["/posts/{id}"]; item != nil {
		t.Error("single-row operations were registered for a list-only resource")
	}
	if api.OpenAPI().Paths["/posts"].Post != nil {
		t.Error("create was registered for a list-only resource")
	}
}

func keys(m map[string]*huma.Schema) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
