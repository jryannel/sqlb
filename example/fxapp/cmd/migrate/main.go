// Command migrate renders this example's schema into goose migrations.
//
//	go run ./cmd/migrate            write any migration that does not exist yet
//	go run ./cmd/migrate -force     rewrite them, for editing the schema in development
//
// It does not apply anything. sqlb produces migration files and stops there;
// the runner is goose, invoked by the fxkit glue at startup and by the
// tests. That split is the point — see example/tasks/cmd/migrate, which says
// it at length, and whose second migration is the interesting case this one
// does not have.
//
// What this one adds by hand is a single trigger, for a reason that applies to
// every schema declaring Timestamps(): updated_at is set by its column default
// at insert and by nothing at all afterwards. A trigger is the only place that
// covers every writer, including the migration that backfills a column and the
// psql session somebody used to fix a row.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jryannel/sqlb/migrate"
	"github.com/jryannel/sqlb/schema"

	// Imported for its side effects: declaring a table registers it.
	_ "github.com/jryannel/sqlb/example/fxapp/noteschema"
)

func main() {
	dir := flag.String("dir", "migrations", "output directory")
	force := flag.Bool("force", false, "delete and rewrite existing migrations (development only)")
	flag.Parse()

	if err := run(*dir, *force); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}

func run(dir string, force bool) error {
	// The current side of the diff is an empty registry: this is a baseline,
	// building the schema from nothing. Once a database exists, current should
	// come from shadow.Build — replaying the checked-in history — rather than
	// from this program's idea of what has been applied.
	changes, err := migrate.Diff(schema.NewRegistry(), schema.DefaultRegistry(), migrate.MinPostgres(18))
	if err != nil {
		return fmt.Errorf("diffing the schema: %w", err)
	}

	migrations := []migrate.Migration{
		{Version: migrate.SequentialVersion(1), Name: "initial_schema", Changes: changes},
		{Version: migrate.SequentialVersion(2), Name: "touch_updated_at", Changes: triggers()},
	}

	opts := migrate.Options{Format: migrate.Goose}

	if force {
		if err := clear(dir, migrations, opts); err != nil {
			return err
		}
	}

	for _, m := range migrations {
		written, err := migrate.Write(dir, m, opts)
		if err != nil {
			return err
		}
		for _, f := range written {
			fmt.Println("wrote", f)
		}
	}
	return nil
}

// clear removes exactly the files the run is about to write, and nothing else.
//
// migrate.Write refuses to overwrite, because a migration already applied
// somewhere must not change under the runner's feet. -force is the development
// escape from that rule, so it deletes only what Render says it would produce
// rather than everything in the directory.
func clear(dir string, migrations []migrate.Migration, opts migrate.Options) error {
	for _, m := range migrations {
		files, err := migrate.Render(m, opts)
		if err != nil {
			return err
		}
		for name := range files {
			if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

// tables carrying updated_at. Listed rather than derived from the registry
// because the trigger is hand-written anyway, and a list somebody has to
// extend when a table is added is more obvious than a loop that silently
// covers a new one.
var touched = []string{"spaces", "notes"}

func triggers() []migrate.Change {
	changes := []migrate.Change{{
		Comment: "updated_at is set by its column default at insert and by nothing afterwards. " +
			"A trigger is the only place this can live where every writer is covered — including " +
			"the ones that are not this application.",
		Up: `CREATE OR REPLACE FUNCTION touch_updated_at() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END;
$$;`,
		Down: `DROP FUNCTION IF EXISTS touch_updated_at();`,
	}}

	for _, table := range touched {
		changes = append(changes, migrate.Change{
			Up: fmt.Sprintf(
				"CREATE TRIGGER %s_touch_updated_at BEFORE UPDATE ON %q\n"+
					"    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();", table, table),
			Down: fmt.Sprintf("DROP TRIGGER IF EXISTS %s_touch_updated_at ON %q;", table, table),
		})
	}
	return changes
}
