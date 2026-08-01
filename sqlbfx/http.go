package sqlbfx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.uber.org/fx"
)

// HTTPConfig is what the HTTP surface needs. The application provides it,
// like DBConfig.
type HTTPConfig struct {
	// Addr is the listen address, ":8080" when empty. ":0" is valid and is
	// what a test asks for; the bound address is on the *http.Server's Addr
	// after start.
	Addr string

	// Title and Version go into the OpenAPI document.
	Title   string
	Version string

	// ShutdownTimeout bounds the graceful shutdown on stop. Zero means 15
	// seconds.
	ShutdownTimeout time.Duration

	// Huma, when set, edits the OpenAPI config before the API is built —
	// the document description, security schemes, whatever the application
	// owns. The kit's only opinions in the document are Title and Version.
	Huma func(*huma.Config)
}

func (c HTTPConfig) addr() string {
	if c.Addr == "" {
		return ":8080"
	}
	return c.Addr
}

func (c HTTPConfig) shutdownTimeout() time.Duration {
	if c.ShutdownTimeout <= 0 {
		return 15 * time.Second
	}
	return c.ShutdownTimeout
}

type routerParams struct {
	fx.In

	Sets []MiddlewareSet `group:"sqlbfx.middleware"`
	Log  *slog.Logger    `optional:"true"`
}

// newRouter builds the router and installs the contributed middleware.
//
// The two chi middlewares here are the ones no module owns. Notably absent is
// middleware.RealIP: it rewrites RemoteAddr from X-Forwarded-For whether or
// not anything in front of this server sets that header, so a client can
// choose its own address. A service behind a proxy it controls should read
// the header itself and trust it only from that proxy.
func newRouter(p routerParams) chi.Router {
	router := chi.NewRouter()
	router.Use(middleware.RequestID, middleware.Recoverer)

	ordered := append([]MiddlewareSet(nil), p.Sets...)
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
	logger(p.Log).Info("sqlbfx: middleware installed", "order", names)

	// Liveness, on the router rather than through huma: it is not part of the
	// API this document describes, and it must answer while the API is
	// broken.
	router.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	return router
}

type apiParams struct {
	fx.In

	Cfg    HTTPConfig
	Router chi.Router
	Sets   []OperationSet `group:"sqlbfx.operations"`
	Log    *slog.Logger   `optional:"true"`
}

// newAPI mounts a Huma API on the router and lets every module register its
// operations.
//
// An OperationSet that fails takes the boot with it. That is the whole reason
// Register returns an error rather than logging: a resource sqlb refused to
// mount — because the tenant scope its schema declares has no hook behind it
// (ADR-0030) — must not turn into a server that starts and serves the rest.
func newAPI(p apiParams) (huma.API, error) {
	cfg := huma.DefaultConfig(p.Cfg.Title, p.Cfg.Version)
	if p.Cfg.Huma != nil {
		p.Cfg.Huma(&cfg)
	}
	api := humachi.New(p.Router, cfg)

	ordered := append([]OperationSet(nil), p.Sets...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Module < ordered[j].Module })

	log := logger(p.Log)
	for _, set := range ordered {
		if set.Register == nil {
			return nil, fmt.Errorf("sqlbfx: the %q operation set has no Register function", set.Module)
		}
		if err := set.Register(api); err != nil {
			return nil, fmt.Errorf("sqlbfx: registering %s operations: %w", set.Module, err)
		}
		log.Info("sqlbfx: operations registered", "module", set.Module)
	}
	return api, nil
}

type serverParams struct {
	fx.In

	Lc     fx.Lifecycle
	Cfg    HTTPConfig
	Router chi.Router
	Log    *slog.Logger `optional:"true"`

	// Unused, and taken anyway: an ordering edge. Without it nothing would
	// stop fx from constructing the server — and starting the listener —
	// before the operations were registered on an API that only gets built
	// when somebody asks for it.
	API huma.API
}

// newServer builds the server and hands its lifetime to fx.
//
// The listener is opened in OnStart rather than by ListenAndServe in the
// goroutine, so that a port already in use fails the boot instead of being
// logged by a goroutine nobody is reading. It is also what lets a test ask
// for ":0" and find out which port it got.
func newServer(p serverParams) *http.Server {
	log := logger(p.Log)
	srv := &http.Server{
		Handler:           p.Router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	p.Lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			ln, err := net.Listen("tcp", p.Cfg.addr())
			if err != nil {
				return fmt.Errorf("sqlbfx: listening on %s: %w", p.Cfg.addr(), err)
			}
			srv.Addr = ln.Addr().String()
			log.Info("sqlbfx: listening", "addr", srv.Addr)

			go func() {
				if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
					log.Error("sqlbfx: serving", "error", err)
				}
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			ctx, cancel := context.WithTimeout(ctx, p.Cfg.shutdownTimeout())
			defer cancel()
			return srv.Shutdown(ctx)
		},
	})
	return srv
}
