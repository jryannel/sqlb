package studio

import (
	"embed"
	"html/template"
	"io/fs"
	"net/http"
	"sort"

	"github.com/jryannel/sqlb/schema"
)

//go:embed templates/base.html templates/index.html templates/table.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Server renders a Manifest as a browsable, read-only schema explorer. It
// makes no REST calls of its own yet — that is a later stage, once this one
// has shown that the manifest alone is enough to render the schema (ADR-0053).
type Server struct {
	manifest *schema.Manifest
	index    *template.Template
	table    *template.Template
}

// NewServer parses the embedded templates and pairs them with m.
func NewServer(m *schema.Manifest) (*Server, error) {
	index, err := template.ParseFS(templateFS, "templates/base.html", "templates/index.html")
	if err != nil {
		return nil, err
	}
	table, err := template.ParseFS(templateFS, "templates/base.html", "templates/table.html")
	if err != nil {
		return nil, err
	}
	return &Server{manifest: m, index: index, table: table}, nil
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

	return mux
}

type indexPage struct {
	Module string
	Tables []schema.TableManifest
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	tables := append([]schema.TableManifest(nil), s.manifest.Tables...)
	sort.Slice(tables, func(i, j int) bool { return tables[i].Name < tables[j].Name })
	s.render(w, s.index, indexPage{Module: s.manifest.Module, Tables: tables})
}

type tablePage struct {
	Module string
	Table  schema.TableManifest
}

func (s *Server) handleTable(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	for _, t := range s.manifest.Tables {
		if t.Name == name {
			s.render(w, s.table, tablePage{Module: s.manifest.Module, Table: t})
			return
		}
	}
	http.NotFound(w, r)
}

func (s *Server) render(w http.ResponseWriter, t *template.Template, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "base", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
