package blog_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/jryannel/sqlb"
	"github.com/jryannel/sqlb/example/blog"
	_ "github.com/jryannel/sqlb/example/blog/blogschema"
)

// This is the assembled server, and the point of the whole exercise: a chi
// router with the application's own middleware, a Huma API on top of it, and one
// generated call that mounts every resource the schema exposes — filtering,
// sorting, search, pagination, OpenAPI and all.
func newServer(t *testing.T, db sqlb.Executor) http.Handler {
	t.Helper()
	router := chi.NewRouter()
	router.Use(middleware.RequestID, middleware.Recoverer)

	api := humachi.New(router, huma.DefaultConfig("Blog", "1.0.0"))
	if err := blog.Register(api, db); err != nil {
		t.Fatalf("mounting the blog resources: %v", err)
	}
	return router
}

func TestGeneratedServerListsPosts(t *testing.T) {
	// The tenant scope is a startup registration, and it reaches the generated
	// handlers without any of them knowing about it.
	hooks := sqlb.On[blog.Post]()
	defer hooks.Reset()
	hooks.BeforeQuery(func(_ context.Context, q *sqlb.Builder[blog.Post]) error {
		q.Where(sqlb.F("org_id").Eq("acme"), sqlb.F("deleted_at").IsNull())
		return nil
	})

	db := newStubDB(t, postColumns(), [][]driver.Value{postValues("p1", "Hello")})
	server := newServer(t, db.db)

	resp := do(t, server, http.MethodGet, "/posts?status=eq.draft&sort=-published_at&per_page=5", nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.Code, resp.Body)
	}

	stmt := db.last()
	for _, want := range []string{
		`"status" = $1`,                // the filter
		`"org_id" = $2`,                // the hook
		`"deleted_at" IS NULL`,         // the hook
		`ORDER BY "published_at" DESC`, // the sort
		"LIMIT 6",                      // per_page plus the has-more probe
	} {
		if !strings.Contains(stmt, want) {
			t.Errorf("statement missing %q:\n%s", want, stmt)
		}
	}

	var body struct {
		Items   []map[string]any `json:"items"`
		PerPage int              `json:"per_page"`
		HasMore bool             `json:"has_more"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding %s: %v", resp.Body, err)
	}
	if len(body.Items) != 1 || body.Items[0]["title"] != "Hello" {
		t.Errorf("items = %v", body.Items)
	}
	if body.PerPage != 5 {
		t.Errorf("per_page = %d, want 5", body.PerPage)
	}
	// The hidden column is absent from the response, as it is from the query.
	if _, present := body.Items[0]["password_hash"]; present {
		t.Error("a hidden column reached the response")
	}
}

func TestGeneratedServerRejectionIsActionable(t *testing.T) {
	db := newStubDB(t, postColumns(), nil)
	server := newServer(t, db.db)

	resp := do(t, server, http.MethodGet, "/posts?sort=body", nil)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.Code, resp.Body)
	}
	var problem struct {
		Errors []struct {
			Message  string   `json:"message"`
			Location string   `json:"location"`
			Allowed  []string `json:"allowed"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decoding %s: %v", resp.Body, err)
	}
	if len(problem.Errors) != 1 {
		t.Fatalf("errors = %v, want one", problem.Errors)
	}
	// ADR-0011: the rejection carries what would have worked, as data.
	if len(problem.Errors[0].Allowed) == 0 {
		t.Errorf("the rejection names no alternatives: %+v", problem.Errors[0])
	}
	for _, name := range problem.Errors[0].Allowed {
		if name == "password_hash" {
			t.Error("the allow-list discloses a hidden column")
		}
	}
}

// An org exposes read and list only, so writing to it is not merely refused by
// a handler — there is no route at all.
func TestUnexposedOperationsHaveNoRoute(t *testing.T) {
	db := newStubDB(t, nil, nil)
	server := newServer(t, db.db)

	if code := do(t, server, http.MethodDelete, "/orgs/o1", nil).Code; code != http.StatusMethodNotAllowed && code != http.StatusNotFound {
		t.Errorf("DELETE /orgs/{id} = %d, want the route to be absent", code)
	}
	if len(db.statements()) != 0 {
		t.Error("an unexposed operation reached the database")
	}
}

func TestGeneratedServerPatchesOnlyTheNamedColumns(t *testing.T) {
	db := newStubDB(t, postColumns(), [][]driver.Value{postValues("p1", "Renamed")})
	server := newServer(t, db.db)

	resp := do(t, server, http.MethodPatch, "/posts/p1", strings.NewReader(`{"title":"Renamed"}`))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.Code, resp.Body)
	}
	if !strings.Contains(db.last(), `SET "title" = $1`) {
		t.Errorf("expected a single-column update:\n%s", db.last())
	}
}

// The nullable case that a plain pointer cannot express: clearing a column.
func TestGeneratedServerCanClearANullableColumn(t *testing.T) {
	db := newStubDB(t, postColumns(), [][]driver.Value{postValues("p1", "Hello")})
	server := newServer(t, db.db)

	resp := do(t, server, http.MethodPatch, "/posts/p1", strings.NewReader(`{"published_at":null}`))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.Code, resp.Body)
	}
	if !strings.Contains(db.last(), `SET "published_at" = $1`) {
		t.Errorf("an explicit null should write the column:\n%s", db.last())
	}
}

func TestGeneratedServerRefusesAReadOnlyColumn(t *testing.T) {
	db := newStubDB(t, postColumns(), nil)
	server := newServer(t, db.db)

	// view_count is read-only, so it is not a field of the patch body at all
	// and the request is refused before any handler reaches the database.
	resp := do(t, server, http.MethodPatch, "/posts/p1", strings.NewReader(`{"view_count":10}`))
	if resp.Code != http.StatusUnprocessableEntity && resp.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want the property refused: %s", resp.Code, resp.Body)
	}
	if len(db.statements()) != 0 {
		t.Error("a refused patch reached the database")
	}
}

func TestOpenAPIDocumentIsServed(t *testing.T) {
	db := newStubDB(t, nil, nil)
	server := newServer(t, db.db)

	resp := do(t, server, http.MethodGet, "/openapi.json", nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.Code)
	}
	var doc struct {
		Paths map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decoding the document: %v", err)
	}
	for _, path := range []string{"/posts", "/posts/{id}", "/authors", "/orgs"} {
		if doc.Paths[path] == nil {
			t.Errorf("the document has no %s", path)
		}
	}
	// orgs exposes read and list only.
	if _, present := doc.Paths["/orgs"]["post"]; present {
		t.Error("orgs documents a create operation it does not expose")
	}
	if strings.Contains(resp.Body.String(), "password_hash") {
		t.Error("the document mentions a hidden column")
	}
}

func do(t *testing.T, h http.Handler, method, target string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, body)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// stubDB is a database that returns one canned result for every statement. The
// engine's own tests cover scanning; what matters here is the SQL the generated
// handlers compile and the JSON they return.
type stubDB struct {
	db   *sql.DB
	mu   sync.Mutex
	log  []string
	cols []string
	rows [][]driver.Value
}

var stubSeq struct {
	sync.Mutex
	n int
}

func newStubDB(t *testing.T, cols []string, rows [][]driver.Value) *stubDB {
	t.Helper()
	stubSeq.Lock()
	stubSeq.n++
	name := "blogstub" + string(rune('a'+stubSeq.n))
	stubSeq.Unlock()

	s := &stubDB{cols: cols, rows: rows}
	sql.Register(name, stubDriver{s: s})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("opening the stub driver: %v", err)
	}
	s.db = db
	t.Cleanup(func() { _ = db.Close() })
	return s
}

func (s *stubDB) record(q string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, q)
}

func (s *stubDB) statements() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.log...)
}

func (s *stubDB) last() string {
	stmts := s.statements()
	if len(stmts) == 0 {
		return ""
	}
	return stmts[len(stmts)-1]
}

type stubDriver struct{ s *stubDB }

func (d stubDriver) Open(string) (driver.Conn, error) { return stubConn(d), nil }

type stubConn struct{ s *stubDB }

func (c stubConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("stub driver: prepared statements are not used")
}
func (c stubConn) Close() error              { return nil }
func (c stubConn) Begin() (driver.Tx, error) { return nil, errors.New("stub driver: no transactions") }

func (c stubConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.s.record(query)
	return &stubRows{cols: c.s.cols, data: c.s.rows}, nil
}

func (c stubConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	c.s.record(query)
	return stubResult{n: int64(len(c.s.rows))}, nil
}

type stubRows struct {
	cols []string
	data [][]driver.Value
	i    int
}

func (r *stubRows) Columns() []string { return r.cols }
func (r *stubRows) Close() error      { return nil }
func (r *stubRows) Next(dest []driver.Value) error {
	if r.i >= len(r.data) {
		return io.EOF
	}
	copy(dest, r.data[r.i])
	r.i++
	return nil
}

type stubResult struct{ n int64 }

func (r stubResult) LastInsertId() (int64, error) { return 0, nil }
func (r stubResult) RowsAffected() (int64, error) { return r.n, nil }

func postColumns() []string {
	return []string{
		"id", "org_id", "author_id", "title", "body", "status",
		"view_count", "published_at", "created_at", "updated_at", "deleted_at",
	}
}

func postValues(id, title string) []driver.Value {
	now := time.Unix(0, 0).UTC()
	return []driver.Value{
		id, "acme", "a1", title, "body text", "draft",
		int64(3), now, now, now, nil,
	}
}
