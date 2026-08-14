package studio

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"sort"
	"strconv"

	"github.com/jryannel/sqlb/schema"
)

//go:embed templates/base.html templates/index.html templates/table.html templates/login.html templates/rows.html templates/row.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

var templateFuncs = template.FuncMap{
	"add": func(a, b int) int { return a + b },
	"sub": func(a, b int) int { return a - b },
}

// Server renders a Manifest as a browsable data/schema explorer. Schema
// pages need only the manifest; the data pages call APIBase with the
// operator's own bearer token (see session.go) — the browser inherits
// whatever that token can already see, and nothing more (docs/adr/0053).
type Server struct {
	manifest *schema.Manifest
	apiBase  string

	index *template.Template
	table *template.Template
	login *template.Template
	rows  *template.Template
	row   *template.Template
}

// NewServer parses the embedded templates and pairs them with m. apiBase is
// the running application's REST API root; empty disables the data pages and
// leaves the schema-only view (stage one) working on its own.
func NewServer(m *schema.Manifest, apiBase string) (*Server, error) {
	s := &Server{manifest: m, apiBase: apiBase}
	var err error
	parse := func(files ...string) *template.Template {
		if err != nil {
			return nil
		}
		var t *template.Template
		t, err = template.New(files[0]).Funcs(templateFuncs).ParseFS(templateFS, files...)
		return t
	}
	s.index = parse("templates/base.html", "templates/index.html")
	s.table = parse("templates/base.html", "templates/table.html")
	s.login = parse("templates/base.html", "templates/login.html")
	s.rows = parse("templates/base.html", "templates/rows.html")
	s.row = parse("templates/base.html", "templates/row.html")
	if err != nil {
		return nil, err
	}
	return s, nil
}

// Handler returns the studio's HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err) // embedded at build time; a failure here is a build bug
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))

	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /tables/{name}", s.handleTable)
	mux.HandleFunc("GET /tables/{name}/rows", s.handleRows)
	mux.HandleFunc("GET /tables/{name}/rows/{id}", s.handleRowDetail)
	mux.HandleFunc("GET /login", s.handleLoginForm)
	mux.HandleFunc("POST /login", s.handleLoginSubmit)
	mux.HandleFunc("POST /logout", s.handleLogout)

	return mux
}

// pageHeader is embedded in every page so base.html can render the navbar
// (module name, sign-in state) the same way regardless of which page it
// wraps.
type pageHeader struct {
	Module   string
	LoggedIn bool
}

func (s *Server) header(r *http.Request) pageHeader {
	return pageHeader{Module: s.manifest.Module, LoggedIn: tokenFromRequest(r) != ""}
}

func (s *Server) findTable(name string) *schema.TableManifest {
	for i := range s.manifest.Tables {
		if s.manifest.Tables[i].Name == name {
			return &s.manifest.Tables[i]
		}
	}
	return nil
}

func wireOf(c schema.ColumnManifest) string {
	if c.Wire != "" {
		return c.Wire
	}
	return c.Name
}

func containsOp(ops []string, op string) bool {
	for _, o := range ops {
		if o == op {
			return true
		}
	}
	return false
}

// dispValue renders a decoded JSON value the way an operator wants to read
// it. It exists because {{.}} on a bare `any` holding a JSON-decoded value
// prints Go's %v form (a float64 as "1", a nil map entry as "<no value>"),
// neither of which is what the response actually said.
func dispValue(v any) string {
	if v == nil {
		return "—"
	}
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprintf("%v", t)
		}
		return string(b)
	}
}

type indexPage struct {
	pageHeader
	Tables []schema.TableManifest
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	tables := append([]schema.TableManifest(nil), s.manifest.Tables...)
	sort.Slice(tables, func(i, j int) bool { return tables[i].Name < tables[j].Name })
	s.render(w, s.index, indexPage{pageHeader: s.header(r), Tables: tables})
}

type tablePage struct {
	pageHeader
	Table     schema.TableManifest
	CanBrowse bool
}

func (s *Server) handleTable(w http.ResponseWriter, r *http.Request) {
	t := s.findTable(r.PathValue("name"))
	if t == nil {
		http.NotFound(w, r)
		return
	}
	canBrowse := s.apiBase != "" && t.REST != nil && containsOp(t.REST.Operations, "list")
	s.render(w, s.table, tablePage{pageHeader: s.header(r), Table: *t, CanBrowse: canBrowse})
}

// displayRow is one row of a data grid, pre-rendered so the template stays
// free of lookup logic: PK for the row's detail link, Cells parallel to the
// page's Columns.
type displayRow struct {
	PK    string
	Cells []string
}

type rowsPage struct {
	pageHeader
	Table         schema.TableManifest
	Columns       []schema.ColumnManifest
	Rows          []displayRow
	Page, PerPage int
	HasMore       bool
}

func (s *Server) handleRows(w http.ResponseWriter, r *http.Request) {
	t := s.findTable(r.PathValue("name"))
	if t == nil || t.REST == nil || !containsOp(t.REST.Operations, "list") {
		http.NotFound(w, r)
		return
	}
	client, ok := s.clientFor(w, r)
	if !ok {
		return
	}

	page := 1
	if p := r.URL.Query().Get("page"); p != "" {
		if n, err := strconv.Atoi(p); err == nil && n > 0 {
			page = n
		}
	}
	q := url.Values{"page": {strconv.Itoa(page)}}

	result, err := client.List(r.Context(), t.REST.Path, q)
	if err != nil {
		s.renderAPIError(w, r, err)
		return
	}

	data := rowsPage{
		pageHeader: s.header(r),
		Table:      *t,
		Columns:    t.Columns,
		Page:       result.Page,
		PerPage:    result.PerPage,
		HasMore:    result.HasMore,
	}
	for _, row := range result.Items {
		dr := displayRow{}
		for _, col := range t.Columns {
			dr.Cells = append(dr.Cells, dispValue(row[wireOf(col)]))
		}
		if pk := findColumn(t.Columns, t.PrimaryKey); pk != nil {
			dr.PK = dispValue(row[wireOf(*pk)])
		}
		data.Rows = append(data.Rows, dr)
	}
	s.render(w, s.rows, data)
}

func findColumn(cols []schema.ColumnManifest, name string) *schema.ColumnManifest {
	for i := range cols {
		if cols[i].Name == name {
			return &cols[i]
		}
	}
	return nil
}

type fieldValue struct {
	Name, Value, Link string
}

type rowPage struct {
	pageHeader
	Table  schema.TableManifest
	Fields []fieldValue
}

func (s *Server) handleRowDetail(w http.ResponseWriter, r *http.Request) {
	t := s.findTable(r.PathValue("name"))
	if t == nil || t.REST == nil {
		http.NotFound(w, r)
		return
	}
	client, ok := s.clientFor(w, r)
	if !ok {
		return
	}

	row, err := client.Get(r.Context(), t.REST.Path+"/"+r.PathValue("id"))
	if err != nil {
		s.renderAPIError(w, r, err)
		return
	}

	data := rowPage{pageHeader: s.header(r), Table: *t}
	for _, col := range t.Columns {
		wire := wireOf(col)
		val := row[wire]
		link := ""
		if col.References != nil && !col.References.External && val != nil {
			link = "/tables/" + col.References.Table + "/rows/" + dispValue(val)
		}
		data.Fields = append(data.Fields, fieldValue{Name: col.Name, Value: dispValue(val), Link: link})
	}
	s.render(w, s.row, data)
}

type loginPage struct {
	pageHeader
	Next, Error, APIBase string
}

func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, s.login, loginPage{
		pageHeader: s.header(r),
		Next:       r.URL.Query().Get("next"),
		APIBase:    s.apiBase,
	})
}

func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	token := r.PostForm.Get("token")
	next := r.PostForm.Get("next")
	if token == "" {
		s.render(w, s.login, loginPage{
			pageHeader: s.header(r),
			Next:       next,
			Error:      "a token is required",
			APIBase:    s.apiBase,
		})
		return
	}
	setTokenCookie(w, token)
	if next == "" {
		next = "/"
	}
	http.Redirect(w, r, next, http.StatusFound)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	clearTokenCookie(w)
	http.Redirect(w, r, "/", http.StatusFound)
}

// clientFor returns an apiClient using the caller's own cookie-stored token,
// or redirects to /login and reports ok=false when there isn't one.
func (s *Server) clientFor(w http.ResponseWriter, r *http.Request) (*apiClient, bool) {
	token := tokenFromRequest(r)
	if token == "" {
		http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
		return nil, false
	}
	return newAPIClient(s.apiBase, token), true
}

// renderAPIError sends a stale token back through login, and reports every
// other API error as a gateway failure with the response body attached — an
// operator staring at a 403 needs to see that, not a generic 500.
func (s *Server) renderAPIError(w http.ResponseWriter, r *http.Request, err error) {
	var ae *apiError
	if errors.As(err, &ae) {
		if ae.Status == http.StatusUnauthorized {
			clearTokenCookie(w)
			http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
			return
		}
		http.Error(w, fmt.Sprintf("%d from API: %s", ae.Status, ae.Body), http.StatusBadGateway)
		return
	}
	http.Error(w, err.Error(), http.StatusBadGateway)
}

func (s *Server) render(w http.ResponseWriter, t *template.Template, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "base", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
