package rest_test

import (
	"database/sql/driver"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestListDocumentsTheFilterTreeParameter(t *testing.T) {
	db := newFakeDB(t)
	api := mount(t, db.db, postOptions())
	params := paramsOf(t, api, "/posts")

	p := params["filter"]
	if p == nil {
		t.Fatalf("the JSON filter tree has no documented parameter")
	}
	if p.In != "query" {
		t.Errorf("filter param In = %q, want query", p.In)
	}
	if !strings.Contains(p.Description, "JSON") {
		t.Errorf("filter param description does not mention JSON: %q", p.Description)
	}
}

// listWithTree GETs /posts with a URL-encoded JSON filter tree, plus any extra
// raw query string.
func listWithTree(t *testing.T, tree, extra string) string {
	t.Helper()
	q := "filter=" + url.QueryEscape(tree)
	if extra != "" {
		q = extra + "&" + q
	}
	return "/posts?" + q
}

func TestListCompilesJSONTreeFilter(t *testing.T) {
	db := newFakeDB(t, reply{cols: postCols(), rows: [][]driver.Value{postRow("p1", "Hello")}})
	api := mount(t, db.db, postOptions())

	tree := `{"op":"and","children":[
		{"op":"eq","field":"status","value":"draft"},
		{"op":"gte","field":"view_count","value":3}
	]}`
	resp := api.Get(listWithTree(t, tree, ""))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.Code, resp.Body)
	}

	stmt := db.lastStatement()
	for _, want := range []string{`"status" = $1`, `"view_count" >= $2`, " AND "} {
		if !strings.Contains(stmt, want) {
			t.Errorf("statement missing %q:\n%s", want, stmt)
		}
	}
	// A hidden column must never reach the query, whatever a tree names.
	if strings.Contains(stmt, "secret") {
		t.Errorf("hidden column reached the query:\n%s", stmt)
	}
}

func TestListJSONTreeConjoinsWithParams(t *testing.T) {
	db := newFakeDB(t, reply{cols: postCols(), rows: [][]driver.Value{postRow("p1", "Hello")}})
	api := mount(t, db.db, postOptions())

	// A query-parameter filter and a tree in the same request AND together.
	tree := `{"op":"gte","field":"view_count","value":3}`
	resp := api.Get(listWithTree(t, tree, "status=eq.draft"))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.Code, resp.Body)
	}

	stmt := db.lastStatement()
	for _, want := range []string{`"status" = $1`, `"view_count" >= $2`} {
		if !strings.Contains(stmt, want) {
			t.Errorf("statement missing %q:\n%s", want, stmt)
		}
	}
}

func TestListJSONTreeNesting(t *testing.T) {
	db := newFakeDB(t, reply{cols: postCols(), rows: [][]driver.Value{postRow("p1", "Hello")}})
	api := mount(t, db.db, postOptions())

	// status = draft AND (view_count < 3 OR view_count >= 100) — the disjunction
	// the flat ?or grammar can express only at the top level, here nested.
	tree := `{"op":"and","children":[
		{"op":"eq","field":"status","value":"draft"},
		{"op":"or","children":[
			{"op":"lt","field":"view_count","value":3},
			{"op":"gte","field":"view_count","value":100}
		]}
	]}`
	resp := api.Get(listWithTree(t, tree, ""))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.Code, resp.Body)
	}
	stmt := db.lastStatement()
	if !strings.Contains(stmt, " OR ") || !strings.Contains(stmt, " AND ") {
		t.Errorf("statement did not preserve the nested boolean structure:\n%s", stmt)
	}
}

func TestListJSONTreeArrayFilter(t *testing.T) {
	db := newFakeDB(t, reply{cols: postCols(), rows: [][]driver.Value{postRow("p1", "Hello")}})
	api := mount(t, db.db, postOptions())

	// tags is an array column; hasany goes through applyOp's array arm.
	tree := `{"op":"hasany","field":"tags","value":["go","rust"]}`
	resp := api.Get(listWithTree(t, tree, ""))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.Code, resp.Body)
	}
	if stmt := db.lastStatement(); !strings.Contains(stmt, `"tags"`) {
		t.Errorf("array filter did not reach the query:\n%s", stmt)
	}
}

func TestListRejectsBadJSONTree(t *testing.T) {
	cases := []struct{ name, tree, wantMsg string }{
		{"unknown field", `{"op":"eq","field":"nope","value":"x"}`, "unknown parameter"},
		{"unfilterable column", `{"op":"eq","field":"created_at","value":"x"}`, "not filterable"},
		{"unknown operator", `{"op":"zap","field":"status","value":"x"}`, "unknown operator"},
		{"malformed json", `{"op":"eq","field":`, "invalid filter JSON"},
		{"between arity", `{"op":"between","field":"view_count","value":[1]}`, "exactly 2 values"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newFakeDB(t, reply{cols: postCols()})
			api := mount(t, db.db, postOptions())

			resp := api.Get(listWithTree(t, tc.tree, ""))
			if resp.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.Code, resp.Body)
			}
			body := decode(t, resp.Body.Bytes())
			details, ok := body["errors"].([]any)
			if !ok || len(details) == 0 {
				t.Fatalf("errors = %v, want at least one detail", body["errors"])
			}
			var found bool
			for _, d := range details {
				if m, ok := d.(map[string]any); ok {
					if msg, _ := m["message"].(string); strings.Contains(msg, tc.wantMsg) {
						found = true
					}
					// A hidden column must never surface in an allow-list, even
					// via a tree (ADR-0011).
					if allowed, ok := m["allowed"].([]any); ok {
						for _, name := range allowed {
							if name == "secret" {
								t.Error("the allow-list must not disclose a hidden column")
							}
						}
					}
				}
			}
			if !found {
				t.Errorf("no detail mentioned %q: %v", tc.wantMsg, body["errors"])
			}
		})
	}
}

func TestListUnifiedFilterBudget(t *testing.T) {
	db := newFakeDB(t, reply{cols: postCols()})
	o := postOptions()
	o.MaxFilters = 3
	api := mount(t, db.db, o)

	// Two query-parameter conditions and a two-condition tree make four, over a
	// budget of three. The budget is one pool across both formats, so this is a
	// 400 rather than two independent budgets of three that both pass.
	tree := `{"op":"and","children":[
		{"op":"gte","field":"view_count","value":1},
		{"op":"lt","field":"view_count","value":9}
	]}`
	resp := api.Get(listWithTree(t, tree, "status=eq.draft&status=ne.published"))
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.Code, resp.Body)
	}
	body := decode(t, resp.Body.Bytes())
	details, _ := body["errors"].([]any)
	var found bool
	for _, d := range details {
		if m, ok := d.(map[string]any); ok {
			if msg, _ := m["message"].(string); strings.Contains(msg, "filter conditions requested") {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("no detail reported the shared budget: %v", body["errors"])
	}
}

func TestListTreeColumnNamedFilterIsNotShadowed(t *testing.T) {
	// ?filter= is reserved, so a request without it still lists normally: the
	// reservation is what lets the tree share the request, and this guards that
	// it did not turn the empty case into an error.
	db := newFakeDB(t, reply{cols: postCols(), rows: [][]driver.Value{postRow("p1", "Hello")}})
	api := mount(t, db.db, postOptions())

	if resp := api.Get("/posts"); resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.Code, resp.Body)
	}
}
