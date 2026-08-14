package studio

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jryannel/sqlb/schema"
)

// testManifest is a small, hand-built fixture rather than a file on disk, so
// this test exercises exactly the shapes handleRows/handleRowDetail read and
// nothing an unrelated schema change could shift underneath it.
func testManifest() *schema.Manifest {
	return &schema.Manifest{
		Version: "1",
		Module:  "widgets-app",
		Tables: []schema.TableManifest{
			{
				Name:       "widgets",
				Comment:    "A test widget.",
				PrimaryKey: "id",
				Columns: []schema.ColumnManifest{
					{Name: "id", Type: "uuid", GoType: "string", ReadOnly: true},
					{Name: "owner_id", Type: "uuid", GoType: "string", References: &schema.RefManifest{
						Relation: "owner", Table: "owners", Column: "id", Enforced: true,
					}},
					{Name: "title", Type: "text", GoType: "string", Capabilities: []string{"filter", "sort"}},
					{Name: "count", Type: "int", GoType: "int"},
					{Name: "status", Type: "enum", GoType: "string", Enum: []string{"draft", "published"}},
					{Name: "note", Type: "text", GoType: "string", Nullable: true},
					{Name: "tags", Type: "text", GoType: "[]string", Array: true},
				},
				REST: &schema.RESTManifest{
					Path:       "/widgets",
					Operations: []string{"create", "read", "update", "list"},
					Filterable: []string{"title"},
					Sortable:   []string{"title"},
				},
			},
			{
				Name:       "hidden",
				PrimaryKey: "id",
				Columns:    []schema.ColumnManifest{{Name: "id", Type: "uuid", GoType: "string"}},
				// No REST: not exposed, so it must never grow a "Browse data" link.
			},
		},
	}
}

// fakeAPI stands in for a running application's generated REST API: it
// checks the bearer token studio attached and serves a rest.Page[T]-shaped
// list, a bare-row detail, and an in-memory PATCH/POST — the response shapes
// and the request-body types client.go and form.go must produce.
func fakeAPI(t *testing.T, wantToken string) *httptest.Server {
	t.Helper()
	store := map[string]map[string]any{
		"w1": {"id": "w1", "owner_id": "o1", "title": "First widget", "count": float64(3), "status": "draft", "note": "hello", "tags": []any{"a", "b"}},
	}
	nextID := 2

	auth := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "Bearer "+wantToken {
			w.WriteHeader(http.StatusUnauthorized)
			return false
		}
		return true
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /widgets", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		items := make([]map[string]any, 0, len(store))
		for _, id := range []string{"w1"} { // stable order for the assertions below
			if row, ok := store[id]; ok {
				items = append(items, row)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": items, "page": 1, "per_page": 20, "has_more": false,
		})
	})
	mux.HandleFunc("GET /widgets/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		row, ok := store[r.PathValue("id")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(row)
	})
	mux.HandleFunc("PATCH /widgets/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		row, ok := store[r.PathValue("id")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var changes map[string]any
		if err := json.NewDecoder(r.Body).Decode(&changes); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if owner, present := changes["owner_id"]; present && owner == "missing-owner" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"detail":"owner does not exist"}`))
			return
		}
		if count, present := changes["count"]; present {
			if _, isNumber := count.(float64); !isNumber {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"detail":"count must be a number, not a string"}`))
				return
			}
		}
		if tags, present := changes["tags"]; present {
			if _, isArray := tags.([]any); !isArray {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"detail":"tags must be an array, not a string"}`))
				return
			}
		}
		for k, v := range changes {
			row[k] = v
		}
		_ = json.NewEncoder(w).Encode(row)
	})
	mux.HandleFunc("POST /widgets", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if title, _ := body["title"].(string); title == "" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"detail":"title is required"}`))
			return
		}
		id := fmt.Sprintf("w%d", nextID)
		nextID++
		body["id"] = id
		store[id] = body
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(body)
	})
	return httptest.NewServer(mux)
}

func newTestClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func TestIndexListsExposedAndUnexposedTables(t *testing.T) {
	srv, err := NewServer(testManifest(), "")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "widgets") || !strings.Contains(string(body), "hidden") {
		t.Fatalf("index missing a table name:\n%s", body)
	}
	if !strings.Contains(string(body), "not exposed") {
		t.Fatalf("index did not mark the REST-less table as not exposed:\n%s", body)
	}
}

func TestRowsRedirectsToLoginWithoutAToken(t *testing.T) {
	api := fakeAPI(t, "irrelevant")
	defer api.Close()
	srv, err := NewServer(testManifest(), api.URL)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	client := newTestClient(t)
	resp, err := client.Get(ts.URL + "/tables/widgets/rows")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "/login") {
		t.Fatalf("Location = %q, want a /login redirect", loc)
	}
}

func TestRowsRendersDataAfterLogin(t *testing.T) {
	const token = "secret-token"
	api := fakeAPI(t, token)
	defer api.Close()
	srv, err := NewServer(testManifest(), api.URL)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	client := newTestClient(t)
	client.CheckRedirect = nil // follow the post-login redirect this time

	loginResp, err := client.PostForm(ts.URL+"/login", map[string][]string{"token": {token}})
	if err != nil {
		t.Fatal(err)
	}
	loginResp.Body.Close()

	resp, err := client.Get(ts.URL + "/tables/widgets/rows")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "First widget") {
		t.Fatalf("grid missing the fake row's title:\n%s", body)
	}
	if !strings.Contains(string(body), `href="/tables/widgets/rows/w1"`) {
		t.Fatalf("grid's first cell did not link to the row by its primary key:\n%s", body)
	}

	detail, err := client.Get(ts.URL + "/tables/widgets/rows/w1")
	if err != nil {
		t.Fatal(err)
	}
	defer detail.Body.Close()
	if detail.StatusCode != http.StatusOK {
		t.Fatalf("detail status = %d, want 200", detail.StatusCode)
	}
	detailBody, err := io.ReadAll(detail.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(detailBody), `href="/tables/owners/rows/o1"`) {
		t.Fatalf("detail page did not render owner_id as a link to the referenced table:\n%s", detailBody)
	}
}

func TestStaleTokenRedirectsBackToLogin(t *testing.T) {
	api := fakeAPI(t, "the-real-token")
	defer api.Close()
	srv, err := NewServer(testManifest(), api.URL)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	client := newTestClient(t)
	loginResp, err := client.PostForm(ts.URL+"/login", map[string][]string{"token": {"wrong-token"}})
	if err != nil {
		t.Fatal(err)
	}
	loginResp.Body.Close()

	resp, err := client.Get(ts.URL + "/tables/widgets/rows")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302 back to login on a 401 from the API", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); !strings.HasPrefix(loc, "/login") {
		t.Fatalf("Location = %q, want a /login redirect", loc)
	}
}

// loggedInClient returns a cookie-carrying client already signed in with
// token. Redirects are NOT auto-followed (same default as newTestClient) —
// a caller that wants the 302 status of a subsequent POST needs to see it,
// not the 200 of wherever it points.
func loggedInClient(t *testing.T, tsURL, token string) *http.Client {
	t.Helper()
	client := newTestClient(t)
	resp, err := client.PostForm(tsURL+"/login", map[string][]string{"token": {token}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return client
}

func TestEditUpdatesRowEncodesNumbersAndClearsNullable(t *testing.T) {
	const token = "secret-token"
	api := fakeAPI(t, token)
	defer api.Close()
	srv, err := NewServer(testManifest(), api.URL)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	client := loggedInClient(t, ts.URL, token)

	resp, err := client.PostForm(ts.URL+"/tables/widgets/rows/w1/edit", url.Values{
		"title":       {"First widget"},
		"count":       {"42"},
		"status":      {"published"},
		"note__clear": {"on"},
		"owner_id":    {"o1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("edit submit status = %d, want 302", resp.StatusCode)
	}

	detail, err := client.Get(ts.URL + "/tables/widgets/rows/w1")
	if err != nil {
		t.Fatal(err)
	}
	defer detail.Body.Close()
	body, err := io.ReadAll(detail.Body)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	// "42" and not "42.000000" or a quoted string: proves scalarValue sent a
	// JSON number, which the fake API's PATCH handler type-checks.
	if !strings.Contains(got, "<dd class=\"col-9\">\n        42\n") {
		t.Fatalf("count did not round-trip as 42:\n%s", got)
	}
	if !strings.Contains(got, "published") {
		t.Fatalf("status did not update to published:\n%s", got)
	}
	if !strings.Contains(got, "<dd class=\"col-9\">\n        —\n") {
		t.Fatalf("note__clear did not clear note to null:\n%s", got)
	}
}

func TestArrayColumnRoundTripsThroughEditAsCommaSeparated(t *testing.T) {
	const token = "secret-token"
	api := fakeAPI(t, token)
	defer api.Close()
	srv, err := NewServer(testManifest(), api.URL)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	client := loggedInClient(t, ts.URL, token)

	form, err := client.Get(ts.URL + "/tables/widgets/rows/w1/edit")
	if err != nil {
		t.Fatal(err)
	}
	defer form.Body.Close()
	formBody, err := io.ReadAll(form.Body)
	if err != nil {
		t.Fatal(err)
	}
	// The stored value is ["a","b"]; the edit field must show it the way its
	// own submit path expects to parse it back, not as a JSON array literal.
	if !strings.Contains(string(formBody), `value="a, b"`) {
		t.Fatalf("edit form did not render tags as comma-separated text:\n%s", formBody)
	}

	resp, err := client.PostForm(ts.URL+"/tables/widgets/rows/w1/edit", url.Values{
		"title": {"First widget"},
		"tags":  {"a, b, c"},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("edit submit status = %d, want 302 (the fake API's array type-check should pass)", resp.StatusCode)
	}

	detail, err := client.Get(ts.URL + "/tables/widgets/rows/w1")
	if err != nil {
		t.Fatal(err)
	}
	defer detail.Body.Close()
	detailBody, err := io.ReadAll(detail.Body)
	if err != nil {
		t.Fatal(err)
	}
	// html/template escapes the quotes in the JSON array literal; this is
	// that escaped form, not an unescaped one this test should have expected.
	if !strings.Contains(string(detailBody), `[&#34;a&#34;,&#34;b&#34;,&#34;c&#34;]`) {
		t.Fatalf("tags did not round-trip as a 3-element array:\n%s", detailBody)
	}
}

func TestEditRejectedByAPIRedisplaysFormWithError(t *testing.T) {
	const token = "secret-token"
	api := fakeAPI(t, token)
	defer api.Close()
	srv, err := NewServer(testManifest(), api.URL)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	client := loggedInClient(t, ts.URL, token)

	// A blank field is "no change" on an edit (parseFormBody's own contract),
	// so this has to trigger the API's validation with a value it actually
	// sends — count=99 alongside an owner_id the fake API's PATCH handler
	// rejects, the way a real FK violation would surface.
	resp, err := client.PostForm(ts.URL+"/tables/widgets/rows/w1/edit", url.Values{
		"title":    {"Still first widget"},
		"owner_id": {"missing-owner"},
		"count":    {"99"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (form redisplayed, not redirected)", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	if !strings.Contains(got, "owner does not exist") {
		t.Fatalf("redisplayed form missing the API's error:\n%s", got)
	}
	if !strings.Contains(got, `value="99"`) {
		t.Fatalf("redisplayed form lost the operator's other input (count=99):\n%s", got)
	}
}

func TestEditWithUnparsableNumberRedisplaysFormWithoutCallingTheAPI(t *testing.T) {
	const token = "secret-token"
	api := fakeAPI(t, token)
	defer api.Close()
	srv, err := NewServer(testManifest(), api.URL)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	client := loggedInClient(t, ts.URL, token)

	resp, err := client.PostForm(ts.URL+"/tables/widgets/rows/w1/edit", url.Values{
		"title": {"First widget"},
		"count": {"not-a-number"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (form redisplayed)", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "not a number") {
		t.Fatalf("redisplayed form missing the local encoding error:\n%s", body)
	}
}

func TestNewRowCreatesAndRedirectsToItsDetailPage(t *testing.T) {
	const token = "secret-token"
	api := fakeAPI(t, token)
	defer api.Close()
	srv, err := NewServer(testManifest(), api.URL)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	client := loggedInClient(t, ts.URL, token)

	resp, err := client.PostForm(ts.URL+"/tables/widgets/rows/new", url.Values{
		"title":    {"Brand new widget"},
		"owner_id": {"o1"},
		"count":    {"7"},
		"status":   {"draft"},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("create submit status = %d, want 302", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "/tables/widgets/rows/w") {
		t.Fatalf("Location = %q, want a redirect to the new row's detail page", loc)
	}

	detail, err := client.Get(ts.URL + loc)
	if err != nil {
		t.Fatal(err)
	}
	defer detail.Body.Close()
	body, err := io.ReadAll(detail.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Brand new widget") {
		t.Fatalf("new row's detail page missing its own title:\n%s", body)
	}
}
