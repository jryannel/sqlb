package fxapp

import (
	"fmt"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"go.uber.org/fx"

	"github.com/jryannel/sqlb/example/fxapp/config"
	"github.com/jryannel/sqlb/example/fxapp/logs"
	"github.com/jryannel/sqlb/sqlbfx"
)

// Platform is the reusable half of the module list: the logger, and the sqlbfx
// kit fed by this application's configuration.
//
// The kit reads no environment variable — DBConfig and HTTPConfig are the
// application's to provide, from wherever it keeps configuration (ADR-0044).
// Here that is the FXAPP_-prefixed environment the config package reads, so
// the two constructors below are the entire boundary between "how this app is
// configured" and "what the kit needs".
func Platform() fx.Option {
	return fx.Options(
		logs.Module,
		fx.Provide(newDBConfig, newHTTPConfig),
		sqlbfx.Module(),
	)
}

// newDBConfig reads FXAPP_DATABASE_URL and the pool variables.
func newDBConfig() (sqlbfx.DBConfig, error) {
	dsn, err := config.Require("DATABASE_URL")
	if err != nil {
		return sqlbfx.DBConfig{}, fmt.Errorf("fxapp: %w", err)
	}
	maxConns, err := config.Int("DB_MAX_OPEN_CONNS", 20)
	if err != nil {
		return sqlbfx.DBConfig{}, fmt.Errorf("fxapp: %w", err)
	}
	minIdle, err := config.Int("DB_MAX_IDLE_CONNS", 10)
	if err != nil {
		return sqlbfx.DBConfig{}, fmt.Errorf("fxapp: %w", err)
	}
	lifetime, err := config.Duration("DB_CONN_MAX_LIFETIME", time.Hour)
	if err != nil {
		return sqlbfx.DBConfig{}, fmt.Errorf("fxapp: %w", err)
	}
	connect, err := config.Duration("DB_CONNECT_TIMEOUT", 10*time.Second)
	if err != nil {
		return sqlbfx.DBConfig{}, fmt.Errorf("fxapp: %w", err)
	}
	return sqlbfx.DBConfig{
		DSN:             dsn,
		MaxConns:        int32(maxConns),
		MinIdleConns:    int32(minIdle),
		ConnMaxLifetime: lifetime,
		ConnectTimeout:  connect,
	}, nil
}

// newHTTPConfig reads FXAPP_ADDR and FXAPP_SHUTDOWN_TIMEOUT, and carries the
// parts of the OpenAPI document the application owns: what it is called, and
// how a caller authenticates.
func newHTTPConfig() (sqlbfx.HTTPConfig, error) {
	timeout, err := config.Duration("SHUTDOWN_TIMEOUT", 15*time.Second)
	if err != nil {
		return sqlbfx.HTTPConfig{}, fmt.Errorf("fxapp: %w", err)
	}
	return sqlbfx.HTTPConfig{
		Addr:            config.Get("ADDR", ":8080"),
		Title:           "Notes",
		Version:         "1.0.0",
		ShutdownTimeout: timeout,
		Huma: func(cfg *huma.Config) {
			cfg.Info.Description = "A tenant-scoped notes API built on sqlb and assembled with uber-go/fx: " +
				"generated CRUD over a declared schema, a space boundary held by query hooks, " +
				"and one module per concern."
			cfg.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
				"bearer": {
					Type:        "http",
					Scheme:      "bearer",
					Description: "A space key, as configured in FXAPP_SPACE_KEYS.",
				},
			}
			cfg.Security = []map[string][]string{{"bearer": {}}}
		},
	}, nil
}
