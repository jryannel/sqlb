package rest

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
)

// Config describes the default REST server: the identity its OpenAPI document
// carries, and an escape hatch for anything the named fields do not cover.
//
// The zero value is usable — Title and Version default — so the smallest server
// is rest.NewServer(rest.Config{}).
type Config struct {
	// Title and Version identify the API in its OpenAPI document. They default
	// to "API" and "1.0.0".
	Title   string
	Version string

	// Description is the document's prose summary. Optional.
	Description string

	// Customize, if set, receives the huma.Config after the fields above are
	// applied and before the API is built. It is where a security scheme, a
	// server URL, or a non-default docs path goes — anything this struct does
	// not name — and it may override what the fields above set.
	Customize func(*huma.Config)
}

// Server is a ready-to-serve REST API: a huma.API mounted on a net/http mux,
// with the OpenAPI document and its docs page already served by huma at
// /openapi.json, /openapi.yaml and /docs.
//
// It carries no router beyond the standard library's ServeMux, so an application
// that wants generated CRUD and nothing more needs no third-party router. Build
// one with NewServer; the zero value is not useful.
type Server struct {
	// API is what resources mount on. Pass it to a generated Register, or
	// register hand-written operations on it directly.
	API huma.API

	// Mux is the underlying mux. Mount application routes on it — a health
	// check, authentication — alongside the generated ones.
	Mux *http.ServeMux

	// Handler is Mux. Serve it, or wrap it with application middleware first.
	Handler http.Handler
}

// NewServer builds the default REST server: a huma.API on net/http, whose
// OpenAPI document and docs page huma serves without further wiring.
//
// It is the batteries-included front door to the same surface [Resource] mounts.
// An application that needs a different router, a different huma adapter, or its
// own huma.Config builds the huma.API itself and calls [Resource] — or the
// generated Register — directly. This constructor is a convenience over that
// seam, not a replacement for it: everything it returns is a plain huma.API and
// a plain ServeMux the application still owns.
func NewServer(cfg Config) *Server {
	if cfg.Title == "" {
		cfg.Title = "API"
	}
	if cfg.Version == "" {
		cfg.Version = "1.0.0"
	}

	hc := huma.DefaultConfig(cfg.Title, cfg.Version)
	if cfg.Description != "" {
		hc.Info.Description = cfg.Description
	}
	if cfg.Customize != nil {
		cfg.Customize(&hc)
	}

	mux := http.NewServeMux()
	api := humago.New(mux, hc)
	return &Server{API: api, Mux: mux, Handler: mux}
}
