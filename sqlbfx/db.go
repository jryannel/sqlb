package sqlbfx

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"
)

// DBConfig is what the pool needs. The application provides it — from env,
// from flags, from wherever; the kit reads no environment variable, because
// how the pool is sized and where its DSN comes from is the application's
// business (ADR-0040).
type DBConfig struct {
	// DSN is the Postgres connection string. Required.
	DSN string

	// MaxConns and MinIdleConns size the pool; zero keeps pgxpool's default.
	// Behind PgBouncer in transaction pooling mode these numbers mean
	// something different again — see ADR-0019.
	MaxConns     int32
	MinIdleConns int32

	// ConnMaxLifetime bounds a connection's age; zero keeps pgxpool's
	// default.
	ConnMaxLifetime time.Duration

	// ConnectTimeout bounds the ping before migrations run. A server that
	// cannot reach its database should fail while somebody is watching the
	// deploy, not hang. Zero means 10 seconds.
	ConnectTimeout time.Duration
}

func (c DBConfig) connectTimeout() time.Duration {
	if c.ConnectTimeout <= 0 {
		return 10 * time.Second
	}
	return c.ConnectTimeout
}

type poolParams struct {
	fx.In

	Lc  fx.Lifecycle
	Cfg DBConfig

	// Optional: the graph's logger if it has one, slog.Default() otherwise.
	// The kit never provides a logger, so it can never collide with the
	// application's.
	Log *slog.Logger `optional:"true"`
}

func logger(l *slog.Logger) *slog.Logger {
	if l == nil {
		return slog.Default()
	}
	return l
}

// newPool opens the pool and hands its lifetime to fx.
//
// There is no OnStart hook, and its absence is deliberate. Opening a pool
// does not connect, so something has to establish that the database is
// reachable — but an OnStart hook runs *after* every constructor, and the
// migration runner has already used the connection by then. The ping
// therefore lives in runMigrations, which is the first thing that needs one.
func newPool(p poolParams) (*pgxpool.Pool, error) {
	log := logger(p.Log)

	poolCfg, err := pgxpool.ParseConfig(p.Cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("sqlbfx: reading the database URL: %w", err)
	}
	if p.Cfg.MaxConns > 0 {
		poolCfg.MaxConns = p.Cfg.MaxConns
	}
	if p.Cfg.MinIdleConns > 0 {
		poolCfg.MinIdleConns = p.Cfg.MinIdleConns
	}
	if p.Cfg.ConnMaxLifetime > 0 {
		poolCfg.MaxConnLifetime = p.Cfg.ConnMaxLifetime
	}

	pool, err := pgxpool.NewWithConfig(context.Background(), poolCfg)
	if err != nil {
		return nil, fmt.Errorf("sqlbfx: opening the database: %w", err)
	}
	log.Debug("sqlbfx: pool opened", "max_conns", poolCfg.MaxConns)

	p.Lc.Append(fx.Hook{
		OnStop: func(context.Context) error {
			pool.Close()
			return nil
		},
	})
	return pool, nil
}
