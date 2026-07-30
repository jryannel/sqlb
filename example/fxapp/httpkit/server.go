package httpkit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"
	"go.uber.org/fx"
)

// NewServer builds the server and hands its lifetime to fx.
//
// The huma.API parameter is unused, and taken anyway: it is an ordering edge.
// Without it nothing would stop fx from constructing the server — and starting
// the listener — before the operations were registered on an API that only
// gets built when somebody asks for it.
//
// The listener is opened in OnStart rather than by ListenAndServe in the
// goroutine, so that a port already in use fails the boot instead of being
// logged by a goroutine nobody is reading. It is also what lets a test ask for
// ":0" and find out which port it got.
func NewServer(
	lc fx.Lifecycle,
	cfg Config,
	router chi.Router,
	log *slog.Logger,
	_ huma.API,
) *http.Server {
	srv := &http.Server{
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			ln, err := net.Listen("tcp", cfg.Addr)
			if err != nil {
				return fmt.Errorf("httpkit: listening on %s: %w", cfg.Addr, err)
			}
			srv.Addr = ln.Addr().String()
			log.Info("httpkit: listening",
				"addr", srv.Addr,
				"docs", "http://localhost:"+port(ln.Addr())+"/docs")

			go func() {
				if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
					log.Error("httpkit: serving", "error", err)
				}
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			ctx, cancel := context.WithTimeout(ctx, cfg.ShutdownTimeout)
			defer cancel()
			return srv.Shutdown(ctx)
		},
	})
	return srv
}

// port reports the port actually bound, which is what ":0" makes worth asking.
func port(addr net.Addr) string {
	if tcp, ok := addr.(*net.TCPAddr); ok {
		return fmt.Sprint(tcp.Port)
	}
	_, p, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return p
}
