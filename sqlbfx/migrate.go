package sqlbfx

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"go.uber.org/fx"
)

// Migrated is the fact that every registered migration set has been applied.
//
// It is a value rather than an fx.Invoke because ordering in a container is a
// dependency edge, not a position in a list: anything that reads or writes a
// table takes a Migrated, and is then constructed after this ran by
// construction rather than by everyone remembering to list the kit first.
// The handles take one, so every query through them is downstream of it.
//
// Applying migrations at startup suits a demo and a single-instance service.
// It does not suit a rolling deploy, where several new instances race to
// apply the same migration and the old code briefly runs against the new
// schema. There, migrations are a deployment step that finishes before any
// new instance starts — replace the kit's runner (fx.Decorate on Migrated is
// not enough; provide DB() without Module and produce Migrated from a version
// assertion instead).
type Migrated struct{}

type migrateParams struct {
	fx.In

	Pool *pgxpool.Pool
	Cfg  DBConfig
	Sets []MigrationSet `group:"sqlbfx.migrations"`
	Log  *slog.Logger   `optional:"true"`
}

// runMigrations pings the database and applies every registered set.
//
// Doing work in a constructor is against fx's usual advice, and it is the
// right call here for the reason Migrated exists: everything downstream is
// built on the assumption that the tables are there. An OnStart hook would
// run after every constructor — after whatever boot-time provisioning has
// already gone looking for a table that does not exist.
func runMigrations(p migrateParams) (Migrated, error) {
	log := logger(p.Log)

	ctx, cancel := context.WithTimeout(context.Background(), p.Cfg.connectTimeout())
	defer cancel()
	if err := p.Pool.Ping(ctx); err != nil {
		return Migrated{}, fmt.Errorf("sqlbfx: connecting to the database: %w", err)
	}

	if len(p.Sets) == 0 {
		// Not an error: a module list with no table-owning module in it is a
		// legitimate composition, and saying so is more useful than either
		// failing or staying quiet.
		log.Info("sqlbfx: no migrations registered")
		return Migrated{}, nil
	}

	// Sorted for a deterministic boot log — and sets therefore apply in
	// alphabetical order by module name, which cross-module references must
	// not depend on. Independent histories need independent tables, which is
	// what ADR-0015's module prefixes are for.
	sets := append([]MigrationSet(nil), p.Sets...)
	sort.Slice(sets, func(i, j int) bool { return sets[i].Module < sets[j].Module })

	// goose is a database/sql runner and stays one. It gets a handle over the
	// pool the application already opened rather than a second connection of
	// its own, which is what keeps "the migrations ran" and "the queries run"
	// statements about one client (ADR-0040).
	gooseDB := stdlib.OpenDBFromPool(p.Pool)
	defer func() { _ = gooseDB.Close() }()

	for _, set := range sets {
		if err := applySet(gooseDB, set, log); err != nil {
			return Migrated{}, err
		}
	}
	return Migrated{}, nil
}

func applySet(db *sql.DB, set MigrationSet, log *slog.Logger) error {
	if set.Module == "" {
		return fmt.Errorf("sqlbfx: a MigrationSet has no Module name")
	}
	sub, err := fs.Sub(set.FS, set.Dir)
	if err != nil {
		return fmt.Errorf("sqlbfx: %s: fs.Sub(%q): %w", set.Module, set.Dir, err)
	}

	// goose keeps the tracking table name and the base FS in package-level
	// state. Sets are applied one at a time from this one goroutine, so the
	// globals are safe here — and this loop is the only place in the process
	// that touches them.
	table := set.Module + "_schema_migrations"
	goose.SetTableName(table)
	goose.SetBaseFS(sub)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("sqlbfx: %s: %w", set.Module, err)
	}

	log.Info("sqlbfx: applying migrations", "module", set.Module, "tracking_table", table)
	if err := goose.UpContext(context.Background(), db, "."); err != nil {
		return fmt.Errorf("sqlbfx: %s: applying migrations: %w", set.Module, err)
	}
	return nil
}
