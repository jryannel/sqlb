package httpkit

import (
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/jryannel/sqlb/example/fxapp/config"
)

// Config is what the HTTP surface needs.
type Config struct {
	// Addr is the listen address, ":8080" by default.
	Addr string

	// Title and Version go into the OpenAPI document.
	Title   string
	Version string

	// ShutdownTimeout bounds the graceful shutdown on SIGINT.
	ShutdownTimeout time.Duration
}

// NewConfig reads FXAPP_ADDR and FXAPP_SHUTDOWN_TIMEOUT.
func NewConfig() (Config, error) {
	timeout, err := config.Duration("SHUTDOWN_TIMEOUT", 15*time.Second)
	if err != nil {
		return Config{}, fmt.Errorf("httpkit: %w", err)
	}
	return Config{
		Addr:            config.Get("ADDR", ":8080"),
		Title:           "Notes",
		Version:         "1.0.0",
		ShutdownTimeout: timeout,
	}, nil
}

// NewRouter builds the router and installs the contributed middleware.
//
// The two chi middlewares here are the ones no module owns. Notably absent is
// middleware.RealIP: it rewrites RemoteAddr from X-Forwarded-For whether or
// not anything in front of this server sets that header, so a client can
// choose its own address. A service behind a proxy it controls should read the
// header itself and trust it only from that proxy.
func NewRouter(log *slog.Logger, sets []MiddlewareSet) chi.Router {
	router := chi.NewRouter()
	router.Use(middleware.RequestID, middleware.Recoverer)

	ordered := append([]MiddlewareSet(nil), sets...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Order != ordered[j].Order {
			return ordered[i].Order < ordered[j].Order
		}
		return ordered[i].Module < ordered[j].Module
	})

	names := make([]string, 0, len(ordered))
	for _, set := range ordered {
		router.Use(set.Wrap)
		names = append(names, set.Module)
	}
	log.Info("httpkit: middleware installed", "order", names)

	// Liveness, on the router rather than through huma: it is not part of the
	// API this document describes, and it must answer while the API is broken.
	router.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	return router
}

// NewAPI mounts a Huma API on the router and lets every module register its
// operations.
//
// An OperationSet that fails takes the boot with it. That is the whole reason
// Register returns an error rather than logging: a resource sqlb refused to
// mount — because the tenant scope its schema declares has no hook behind it —
// must not turn into a server that starts and serves the rest.
func NewAPI(cfg Config, router chi.Router, log *slog.Logger, sets []OperationSet) (huma.API, error) {
	api := humachi.New(router, openAPIConfig(cfg))

	ordered := append([]OperationSet(nil), sets...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Module < ordered[j].Module })

	for _, set := range ordered {
		if set.Register == nil {
			return nil, fmt.Errorf("httpkit: the %q operation set has no Register function", set.Module)
		}
		if err := set.Register(api); err != nil {
			return nil, fmt.Errorf("httpkit: registering %s operations: %w", set.Module, err)
		}
		log.Info("httpkit: operations registered", "module", set.Module)
	}
	return api, nil
}

func openAPIConfig(cfg Config) huma.Config {
	doc := huma.DefaultConfig(cfg.Title, cfg.Version)
	doc.Info.Description = "A tenant-scoped notes API built on sqlb and assembled with uber-go/fx: " +
		"generated CRUD over a declared schema, a space boundary held by query hooks, " +
		"and one module per concern."
	doc.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"bearer": {
			Type:        "http",
			Scheme:      "bearer",
			Description: "A space key, as configured in FXAPP_SPACE_KEYS.",
		},
	}
	doc.Security = []map[string][]string{{"bearer": {}}}
	return doc
}
