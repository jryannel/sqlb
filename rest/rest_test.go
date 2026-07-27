package rest_test

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"
	"github.com/jryannel/sqlb"
	"github.com/jryannel/sqlb/rest"
)

func postOptions() rest.Options {
	return rest.Options{
		Path:            "/posts",
		Name:            "post",
		Ops:             rest.CRUD | rest.OpList,
		DefaultPageSize: 2,
		MaxPageSize:     10,
	}
}

// mount registers the Post resource against a test API backed by db.
func mount(t *testing.T, db sqlb.Executor, opts rest.Options) humatest.TestAPI {
	t.Helper()
	_, api := humatest.New(t, huma.DefaultConfig("Test", "1.0.0"))
	if err := rest.Resource[Post, PostCreate, PostUpdate](api, db, opts); err != nil {
		t.Fatalf("mounting the resource: %v", err)
	}
	return api
}

// decode reads a JSON body, failing the test rather than the caller.
func decode(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	return out
}

func TestListCompilesFiltersIntoSQL(t *testing.T) {
	db := newFakeDB(t, reply{cols: postCols(), rows: [][]driver.Value{postRow("p1", "Hello")}})
	api := mount(t, db.db, postOptions())

	resp := api.Get("/posts?status=eq.draft&view_count=gte.3&sort=-created_at")
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.Code, resp.Body)
	}

	stmt := db.lastStatement()
	for _, want := range []string{`"status" = $1`, `"view_count" >= $2`, `ORDER BY "created_at" DESC`} {
		if !strings.Contains(stmt, want) {
			t.Errorf("statement missing %q:\n%s", want, stmt)
		}
	}
	// The hidden column must not reach the projection, whatever the request
	// asked for.
	if strings.Contains(stmt, "secret") {
		t.Errorf("hidden column reached the query:\n%s", stmt)
	}
}

func TestListPaginationReportsMoreWithoutCounting(t *testing.T) {
	// Three rows come back for a page size of two, which is how the handler
	// learns there is another page.
	db := newFakeDB(t, reply{cols: postCols(), rows: [][]driver.Value{
		postRow("p1", "One"), postRow("p2", "Two"), postRow("p3", "Three"),
	}})
	api := mount(t, db.db, postOptions())

	resp := api.Get("/posts")
	body := decode(t, resp.Body.Bytes())

	items, ok := body["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("items = %v, want 2 rows", body["items"])
	}
	if body["has_more"] != true {
		t.Errorf("has_more = %v, want true", body["has_more"])
	}
	if _, present := body["total"]; present {
		t.Error("total should be absent unless ?count=exact was given")
	}
	if got := len(db.statements()); got != 1 {
		t.Errorf("issued %d statements, want 1: counting must stay opt-in", got)
	}
	// The page query asks for one row beyond the page.
	if !strings.Contains(db.lastStatement(), "LIMIT 3") {
		t.Errorf("expected the page query to over-fetch by one:\n%s", db.lastStatement())
	}
}

func TestListCountExactAddsTotal(t *testing.T) {
	db := newFakeDB(t,
		reply{match: "count(", cols: []string{"count"}, rows: [][]driver.Value{{int64(42)}}},
		reply{cols: postCols(), rows: [][]driver.Value{postRow("p1", "One")}},
	)
	api := mount(t, db.db, postOptions())

	body := decode(t, api.Get("/posts?count=exact").Body.Bytes())
	if body["total"] != float64(42) {
		t.Errorf("total = %v, want 42", body["total"])
	}
	stmts := db.statements()
	if len(stmts) != 2 {
		t.Fatalf("issued %d statements, want 2 (page and count)", len(stmts))
	}
	// The count is of everything matching the filter, not of the page, so the
	// over-fetch limit must not survive into it.
	var count string
	for _, s := range stmts {
		if strings.Contains(s, "count(") {
			count = s
		}
	}
	if count == "" {
		t.Fatalf("no count statement was issued: %v", stmts)
	}
	if strings.Contains(count, "LIMIT") {
		t.Errorf("the count query is capped by the page limit, so total would be wrong:\n%s", count)
	}
}

func TestListRejectionNamesTheAllowedColumns(t *testing.T) {
	db := newFakeDB(t, reply{cols: postCols()})
	api := mount(t, db.db, postOptions())

	resp := api.Get("/posts?sort=body")
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.Code, resp.Body)
	}

	body := decode(t, resp.Body.Bytes())
	details, ok := body["errors"].([]any)
	if !ok || len(details) != 1 {
		t.Fatalf("errors = %v, want one detail", body["errors"])
	}
	detail, ok := details[0].(map[string]any)
	if !ok {
		t.Fatalf("detail is %T, want an object", details[0])
	}
	allowed, ok := detail["allowed"].([]any)
	if !ok || len(allowed) == 0 {
		t.Fatalf("allowed = %v, want the sortable columns", detail["allowed"])
	}
	// ADR-0011: the rejection names what would have worked, and never names a
	// hidden column.
	for _, name := range allowed {
		if name == "secret" {
			t.Error("the allow-list must not disclose a hidden column")
		}
	}
	if detail["location"] != "query.sort" {
		t.Errorf("location = %v, want query.sort", detail["location"])
	}
}

func TestListRejectionReportsEveryProblemAtOnce(t *testing.T) {
	db := newFakeDB(t, reply{cols: postCols()})
	api := mount(t, db.db, postOptions())

	body := decode(t, api.Get("/posts?sort=body&nonesuch=1").Body.Bytes())
	details, _ := body["errors"].([]any)
	if len(details) != 2 {
		t.Errorf("reported %d problems, want 2: a malformed request should take one round trip to fix", len(details))
	}
}

func TestSelectShapesTheResponseObject(t *testing.T) {
	db := newFakeDB(t, reply{
		cols: []string{"id", "title"},
		rows: [][]driver.Value{{"p1", "Hello"}},
	})
	api := mount(t, db.db, postOptions())

	body := decode(t, api.Get("/posts?select=title").Body.Bytes())
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %v, want one row", body["items"])
	}
	item, _ := items[0].(map[string]any)

	// An unselected column is absent, not present and empty: a zero value from
	// a partial scan is indistinguishable from a real one.
	if _, present := item["body"]; present {
		t.Errorf("unselected column body is present in %v", item)
	}
	// The primary key is added back, since a row that cannot address itself is
	// of little use.
	if item["id"] != "p1" {
		t.Errorf("id = %v, want p1 — the projection should keep the key", item["id"])
	}
	if item["title"] != "Hello" {
		t.Errorf("title = %v, want Hello", item["title"])
	}
}

func TestHiddenColumnIsNeverSerialised(t *testing.T) {
	db := newFakeDB(t, reply{cols: postCols(), rows: [][]driver.Value{postRow("p1", "Hello")}})
	api := mount(t, db.db, postOptions())

	raw := api.Get("/posts").Body.String()
	if strings.Contains(raw, "secret") {
		t.Errorf("response mentions the hidden column: %s", raw)
	}
}

func TestReadNotFound(t *testing.T) {
	db := newFakeDB(t, reply{cols: postCols()})
	api := mount(t, db.db, postOptions())

	resp := api.Get("/posts/p1")
	if resp.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", resp.Code, resp.Body)
	}
}

func TestReadRejectsStrayQueryParameters(t *testing.T) {
	db := newFakeDB(t, reply{cols: postCols(), rows: [][]driver.Value{postRow("p1", "Hello")}})
	api := mount(t, db.db, postOptions())

	// Silently ignoring an unknown parameter would answer a question the client
	// did not ask.
	resp := api.Get("/posts/p1?select=title")
	if resp.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422 for an undeclared parameter: %s", resp.Code, resp.Body)
	}
}

func TestCreateOmitsReadOnlyColumns(t *testing.T) {
	db := newFakeDB(t, reply{cols: postCols(), rows: [][]driver.Value{postRow("p1", "Hello")}})
	api := mount(t, db.db, postOptions())

	resp := api.Post("/posts", map[string]any{
		"org_id": "acme", "title": "Hello", "body": "text",
	})
	if resp.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", resp.Code, resp.Body)
	}

	stmt := db.lastStatement()
	for _, forbidden := range []string{`"id"`, `"view_count"`, `"created_at"`} {
		if strings.Contains(strings.SplitN(stmt, "VALUES", 2)[0], forbidden) {
			t.Errorf("read-only column %s reached the insert:\n%s", forbidden, stmt)
		}
	}
	if !strings.Contains(stmt, `"title"`) {
		t.Errorf("writable column title missing from the insert:\n%s", stmt)
	}
	// status is defaulted and was not given, so the database supplies it.
	if strings.Contains(strings.SplitN(stmt, "VALUES", 2)[0], `"status"`) {
		t.Errorf("a defaulted column left unset should be omitted:\n%s", stmt)
	}
}

func TestUpdateWritesOnlyTheNamedColumns(t *testing.T) {
	db := newFakeDB(t, reply{cols: postCols(), rows: [][]driver.Value{postRow("p1", "Changed")}})
	api := mount(t, db.db, postOptions())

	resp := api.Patch("/posts/p1", map[string]any{"title": "Changed"})
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.Code, resp.Body)
	}
	stmt := db.lastStatement()
	if !strings.Contains(stmt, `SET "title" = $1`) {
		t.Errorf("expected a single-column update:\n%s", stmt)
	}
	if strings.Contains(stmt, `"body"`) && strings.Contains(stmt, "SET") &&
		strings.Contains(strings.SplitN(stmt, "WHERE", 2)[0], `"body"`) {
		t.Errorf("an absent field must not be written:\n%s", stmt)
	}
}

func TestUpdateRefusesImmutableColumn(t *testing.T) {
	db := newFakeDB(t, reply{cols: postCols()})
	api := mount(t, db.db, postOptions())

	resp := api.Patch("/posts/p1", map[string]any{"org_id": "other"})
	if resp.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", resp.Code, resp.Body)
	}
	body := decode(t, resp.Body.Bytes())
	details, _ := body["errors"].([]any)
	if len(details) != 1 {
		t.Fatalf("errors = %v, want one detail", body["errors"])
	}
	detail, _ := details[0].(map[string]any)
	if !strings.Contains(detail["message"].(string), "cannot be changed") {
		t.Errorf("message = %v, want it to name immutability", detail["message"])
	}
	if len(db.statements()) != 0 {
		t.Error("a rejected update must not reach the database")
	}
}

func TestUpdateWithNoFieldsIsRejected(t *testing.T) {
	db := newFakeDB(t, reply{cols: postCols()})
	api := mount(t, db.db, postOptions())

	resp := api.Patch("/posts/p1", map[string]any{})
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.Code, resp.Body)
	}
	body := decode(t, resp.Body.Bytes())
	details, _ := body["errors"].([]any)
	detail, _ := details[0].(map[string]any)
	if _, ok := detail["allowed"].([]any); !ok {
		t.Errorf("the rejection should name the writable columns: %v", detail)
	}
}

func TestDelete(t *testing.T) {
	db := newFakeDB(t, reply{cols: postCols(), rows: [][]driver.Value{postRow("p1", "Hello")}})
	api := mount(t, db.db, postOptions())

	resp := api.Delete("/posts/p1")
	if resp.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", resp.Code, resp.Body)
	}
	if !strings.Contains(db.lastStatement(), `DELETE FROM "posts"`) {
		t.Errorf("unexpected statement: %s", db.lastStatement())
	}
}

func TestDeleteMissingRowIs404(t *testing.T) {
	db := newFakeDB(t, reply{cols: postCols()})
	api := mount(t, db.db, postOptions())

	if code := api.Delete("/posts/p1").Code; code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", code)
	}
}

func TestBeforeQueryHookAppliesToTheRESTSurface(t *testing.T) {
	sqlb.On[Post]().Reset()
	t.Cleanup(func() { sqlb.On[Post]().Reset() })
	sqlb.On[Post]().BeforeQuery(func(_ context.Context, q *sqlb.Builder[Post]) error {
		q.Where(sqlb.F("org_id").Eq("acme"))
		return nil
	})

	db := newFakeDB(t, reply{cols: postCols(), rows: [][]driver.Value{postRow("p1", "Hello")}})
	api := mount(t, db.db, postOptions())

	api.Get("/posts")
	if !strings.Contains(db.lastStatement(), `"org_id" = $1`) {
		t.Errorf("the tenant scope did not reach the list query:\n%s", db.lastStatement())
	}
}

func TestResourceRefusesSingleRowOpsWithoutAPrimaryKey(t *testing.T) {
	db := newFakeDB(t)
	_, api := humatest.New(t, huma.DefaultConfig("Test", "1.0.0"))

	err := rest.Resource[Keyless, rest.None[Keyless], rest.None[Keyless]](api, db.db, rest.Options{
		Path: "/keyless",
		Ops:  rest.OpList | rest.OpRead,
	})
	if err == nil {
		t.Fatal("expected mounting to fail: a row cannot be addressed without a key")
	}
	if !strings.Contains(err.Error(), "primary key") {
		t.Errorf("error = %v, want it to name the missing primary key", err)
	}
}

func TestResourceRefusesAHiddenColumnThatWouldSerialise(t *testing.T) {
	db := newFakeDB(t)
	_, api := humatest.New(t, huma.DefaultConfig("Test", "1.0.0"))

	err := rest.Resource[Leaky, rest.None[Leaky], rest.None[Leaky]](api, db.db, rest.Options{
		Path: "/leaky",
		Ops:  rest.OpList,
	})
	if err == nil || !strings.Contains(err.Error(), "hidden") {
		t.Fatalf("error = %v, want a complaint about the hidden column's json tag", err)
	}
}

func TestResourceRefusesAnEmptyOpSet(t *testing.T) {
	db := newFakeDB(t)
	_, api := humatest.New(t, huma.DefaultConfig("Test", "1.0.0"))

	err := rest.Resource[Post, PostCreate, PostUpdate](api, db.db, rest.Options{Path: "/posts"})
	if err == nil {
		t.Fatal("a resource exposing nothing should not mount")
	}
}
