package dbbase

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/jryannel/sqlb/example/fxapp/config"
)

// Config is what dbbase needs.
type Config struct {
	// DSN is the Postgres connection string. Required.
	DSN string

	// MaxOpenConns, MaxIdleConns and ConnMaxLifetime size the pool. Behind
	// PgBouncer in transaction pooling mode these numbers mean something
	// different again — see ADR-0019.
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration

	// ConnectTimeout bounds the ping at startup. A server that cannot reach
	// its database should fail while somebody is watching the deploy, not
	// hang.
	ConnectTimeout time.Duration
}

// NewConfig reads the FXAPP_DATABASE_URL and pool variables.
func NewConfig() (Config, error) {
	dsn, err := config.Require("DATABASE_URL")
	if err != nil {
		return Config{}, fmt.Errorf("dbbase: %w", err)
	}
	cfg := Config{DSN: dsn}
	if cfg.MaxOpenConns, err = config.Int("DB_MAX_OPEN_CONNS", 20); err != nil {
		return Config{}, fmt.Errorf("dbbase: %w", err)
	}
	if cfg.MaxIdleConns, err = config.Int("DB_MAX_IDLE_CONNS", 10); err != nil {
		return Config{}, fmt.Errorf("dbbase: %w", err)
	}
	if cfg.ConnMaxLifetime, err = config.Duration("DB_CONN_MAX_LIFETIME", time.Hour); err != nil {
		return Config{}, fmt.Errorf("dbbase: %w", err)
	}
	if cfg.ConnectTimeout, err = config.Duration("DB_CONNECT_TIMEOUT", 10*time.Second); err != nil {
		return Config{}, fmt.Errorf("dbbase: %w", err)
	}
	return cfg, nil
}

// NewDB opens the pool and hands its lifetime to fx.
//
// The OnStop hook is the whole reason this constructor takes a Lifecycle
// rather than being a one-liner: every module that owns something closeable
// registers its own shutdown, and no main has to remember the order fx
// already knows.
//
// There is no OnStart hook, and its absence is deliberate. Opening a pool does
// not connect, so something has to establish that the database is reachable —
// but an OnStart hook runs *after* every constructor, and the migrations below
// have already used the connection by then. The ping therefore lives in
// runMigrations, which is the first thing that needs one.
func NewDB(lc fx.Lifecycle, cfg Config, log *slog.Logger) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("dbbase: reading the database URL: %w", err)
	}
	poolCfg.MaxConns = int32(cfg.MaxOpenConns)
	poolCfg.MinIdleConns = int32(cfg.MaxIdleConns)
	poolCfg.MaxConnLifetime = cfg.ConnMaxLifetime

	pool, err := pgxpool.NewWithConfig(context.Background(), poolCfg)
	if err != nil {
		return nil, fmt.Errorf("dbbase: opening the database: %w", err)
	}
	log.Debug("dbbase: pool opened", "max_open_conns", cfg.MaxOpenConns)

	lc.Append(fx.Hook{
		OnStop: func(context.Context) error {
			pool.Close()
			return nil
		},
	})
	return pool, nil
}
