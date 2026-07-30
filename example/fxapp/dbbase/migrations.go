package dbbase

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"

	"github.com/pressly/goose/v3"
	"go.uber.org/fx"
)

// MigrationSet is the value-group element a module contributes to register its
// migrations with dbbase.
//
// A module provides one like this:
//
//	fx.Provide(fx.Annotate(provideMigrations, fx.ResultTags(`group:"migrations"`)))
type MigrationSet struct {
	// Module names the set. It is the prefix of the tracking table
	// (<Module>_schema_migrations), so two modules migrate independently and
	// neither can renumber the other's history.
	Module string

	// FS is the embedded filesystem holding the .sql files.
	FS fs.FS

	// Dir is the path within FS the files live at, typically "migrations".
	Dir string
}

// Migrated is the fact that every registered set has been applied.
//
// It is a value rather than an fx.Invoke because ordering in a container is a
// dependency edge, not a position in a list: anything that reads or writes a
// table takes a Migrated, and is then constructed after this ran by
// construction rather than by everyone remembering to list dbbase.Module
// first. sqlbkit's handle takes one, so every query in this application is
// downstream of it.
type Migrated struct{}

type migrationsParam struct {
	fx.In

	DB   *sql.DB
	Cfg  Config
	Log  *slog.Logger
	Sets []MigrationSet `group:"migrations"`
}

// runMigrations applies every registered set at startup.
//
// Doing work in a constructor is against fx's usual advice, and it is the
// right call here for the reason Migrated exists: everything downstream is
// built on the assumption that the tables are there. An OnStart hook would run
// after every constructor, which is after the provider that provisions the
// configured spaces has already gone looking for a table that does not exist.
//
// Applying migrations at startup at all suits a demo and a single-instance
// service. It does not suit a rolling deploy, where several new instances race
// to apply the same migration and the old code briefly runs against the new
// schema. There, migrations are a deployment step that finishes before any new
// instance starts — and this module is where that change would be made: drop
// this provider, and have Migrated assert the schema version instead of
// producing it.
func runMigrations(p migrationsParam) (Migrated, error) {
	ctx, cancel := context.WithTimeout(context.Background(), p.Cfg.ConnectTimeout)
	defer cancel()
	if err := p.DB.PingContext(ctx); err != nil {
		return Migrated{}, fmt.Errorf("dbbase: connecting to the database: %w", err)
	}

	if len(p.Sets) == 0 {
		// Not an error: a module list with no table-owning module in it is a
		// legitimate composition, and saying so is more useful than either
		// failing or staying quiet.
		p.Log.Info("dbbase: no migrations registered")
		return Migrated{}, nil
	}

	// Sorted for a deterministic boot log. Cross-module ordering is not
	// something this can express — see the comment on the single set in the
	// migrations package for why this application has exactly one.
	sets := append([]MigrationSet(nil), p.Sets...)
	sort.Slice(sets, func(i, j int) bool { return sets[i].Module < sets[j].Module })

	for _, set := range sets {
		if err := apply(p.DB, set, p.Log); err != nil {
			return Migrated{}, err
		}
	}
	return Migrated{}, nil
}

func apply(db *sql.DB, set MigrationSet, log *slog.Logger) error {
	if set.Module == "" {
		return fmt.Errorf("dbbase: a MigrationSet has no Module name")
	}
	sub, err := fs.Sub(set.FS, set.Dir)
	if err != nil {
		return fmt.Errorf("dbbase: %s: fs.Sub(%q): %w", set.Module, set.Dir, err)
	}

	// goose keeps the tracking table name and the base FS in package-level
	// state. Sets are applied one at a time from this one goroutine, so the
	// globals are safe here — and this loop is the only place in the process
	// that touches them.
	table := set.Module + "_schema_migrations"
	goose.SetTableName(table)
	goose.SetBaseFS(sub)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("dbbase: %s: %w", set.Module, err)
	}

	log.Info("dbbase: applying migrations", "module", set.Module, "tracking_table", table)
	if err := goose.UpContext(context.Background(), db, "."); err != nil {
		return fmt.Errorf("dbbase: %s: applying migrations: %w", set.Module, err)
	}
	return nil
}
