package studio

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
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
					{Name: "id", Type: "uuid", GoType: "string"},
					{Name: "owner_id", Type: "uuid", GoType: "string", References: &schema.RefManifest{
						Relation: "owner", Table: "owners", Column: "id", Enforced: true,
					}},
					{Name: "title", Type: "text", GoType: "string", Capabilities: []string{"filter", "sort"}},
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
// list and a bare-row detail, the two response shapes client.go decodes.
func fakeAPI(t *testing.T, wantToken string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /widgets", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+wantToken {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"detail":"unauthenticated"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{"id": "w1", "owner_id": "o1", "title": "First widget"},
			},
			"page": 1, "per_page": 20, "has_more": false,
		})
	})
	mux.HandleFunc("GET /widgets/{id}", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+wantToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.PathValue("id") != "w1" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "w1", "owner_id": "o1", "title": "First widget"})
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
