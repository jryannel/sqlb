// Package app assembles the task manager: a chi router, the application's own
// middleware, a Huma API, the generated resources, the hand-written endpoints,
// and the hooks that hold the workspace boundary.
//
// It is a library rather than a main so that the tests can build the same
// server the binary builds. A demo whose tests exercise a different assembly
// than the one that ships is testing the tests.
package app

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/jryannel/sqlb"
	"github.com/jryannel/sqlb/example/tasks"
	"github.com/jryannel/sqlb/example/tasks/auth"
)

// Config is what New needs.
type Config struct {
	// DB is the database. Required.
	DB *sql.DB

	// Secret signs and verifies tokens. Required, at least 32 bytes.
	Secret []byte

	// TokenTTL defaults to 24 hours.
	TokenTTL time.Duration

	// Issuer is the "iss" claim. Defaults to "tasks".
	Issuer string

	// Log defaults to slog.Default().
	Log *slog.Logger
}

// Server is the assembled application.
type Server struct {
	// Handler is the router. Serve it.
	Handler http.Handler
	// API is the Huma API, exposed so a test can read the OpenAPI document
	// without going over HTTP.
	API huma.API
	// Signer is exposed so that a test can mint a token without logging in.
	Signer *auth.Signer
}

// New assembles the server.
func New(cfg Config) (*Server, error) {
	if cfg.DB == nil {
		return nil, fmt.Errorf("app: Config.DB is required")
	}
	if cfg.TokenTTL == 0 {
		cfg.TokenTTL = 24 * time.Hour
	}
	if cfg.Issuer == "" {
		cfg.Issuer = "tasks"
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}

	signer, err := auth.NewSigner(cfg.Secret, cfg.Issuer, cfg.TokenTTL)
	if err != nil {
		return nil, err
	}

	// Two handles over one connection pool.
	//
	// sys resolves against an empty hook registry and is used by exactly two
	// endpoints — register and login — which have to read and write before
	// there is an identity to scope by. Everything else uses hooked, where the
	// workspace boundary applies.
	//
	// They are separate handles rather than one handle and a "skip the hooks"
	// flag, because a flag is a thing that gets passed in from a caller, and the
	// set of callers that may pass it is the whole point. Two values, one of
	// which never leaves this file's neighbours, is harder to misuse.
	sys := sqlb.New(cfg.DB).WithHooks(sqlb.NewRegistry())
	hooked := sys.WithHooks(Register())

	router := chi.NewRouter()
	router.Use(
		middleware.RequestID,
		// No middleware.RealIP. It rewrites r.RemoteAddr from X-Forwarded-For or
		// X-Real-IP whether or not anything in front of this server sets them,
		// so a client can choose its own address — which is worth avoiding in
		// general and doubly so in an example somebody may deploy. A service
		// behind a proxy it actually controls should read the header itself and
		// trust it only from that proxy.
		middleware.Recoverer,
		// Everything is authenticated except what is listed here. See the note
		// on auth.Middleware for why the list is of exceptions rather than of
		// protected routes.
		auth.Middleware(signer,
			"/auth/register",
			"/auth/login",
			"/openapi.json",
			"/openapi.yaml",
			"/docs",
			"/docs/",
			"/health",
		),
	)

	router.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	api := humachi.New(router, openAPIConfig(cfg.Issuer))

	// The generated resources. One call, six tables, with filtering, sorting,
	// search, pagination and an OpenAPI operation per exposed table — and no
	// mention anywhere in it of workspaces, tokens or roles, because the hooks
	// already cover those for every read the handlers issue.
	if err := tasks.Register(api, hooked); err != nil {
		return nil, fmt.Errorf("app: mounting the generated resources: %w", err)
	}

	registerAuthRoutes(api, &authAPI{sys: sys, hooks: hooked.Hooks(), signer: signer})
	registerCommentRoutes(api, &commentAPI{db: hooked, log: cfg.Log})
	registerSoftDeleteRoutes(api, hooked)

	return &Server{Handler: router, API: api, Signer: signer}, nil
}

// openAPIConfig declares bearer authentication once, for the document as a
// whole.
//
// Enforcement is the middleware's job, not this document's — an OpenAPI
// security requirement describes an API, it does not protect one. What it buys
// is that a generated client knows to send the header, and that the two public
// endpoints stand out by overriding it.
func openAPIConfig(issuer string) huma.Config {
	cfg := huma.DefaultConfig("Tasks", "1.0.0")
	cfg.Info.Description = "A multi-tenant task manager built on sqlb: " +
		"generated CRUD over a declared schema, a workspace boundary held by " +
		"query hooks, and JWT bearer authentication."
	cfg.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"bearer": {
			Type:         "http",
			Scheme:       "bearer",
			BearerFormat: "JWT",
			Description:  "A token from POST /auth/login, issued by " + issuer,
		},
	}
	cfg.Security = []map[string][]string{{"bearer": {}}}
	return cfg
}
