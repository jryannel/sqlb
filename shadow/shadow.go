// Package shadow builds a schema by replaying a migration history into an
// empty database, and reading back what the migrations actually produced.
//
// It answers the question migrate.Diff needs answered and cannot answer itself:
// what is the current schema? Reading production is the obvious source and the
// worse one. It tells you what the database looks like, not whether the
// migration history produces it — so a hand-applied hotfix, a migration edited
// after it ran, or a statement someone skipped are all invisible, and the next
// generated migration is computed against a state no migration file describes
// (ADR-0014).
//
// Replaying into a scratch database is a different claim: this is the schema
// the checked-in history builds. Comparing that with production is drift
// detection, and it needs no extra API — it is migrate.Diff between the two
// registries, and an empty result is the claim that the history and the
// database agree.
//
// # This is not a migration runner
//
// sqlb does not apply migrations, and this does not change that. A runner
// tracks which migrations have run, applies the outstanding ones to a database
// people depend on, and must never get it wrong. This applies all of them, in
// order, to an empty database nobody depends on, and throws away the result.
// The two have almost nothing in common except the word "apply".
//
// What follows from that: no version table is read or written, nothing is
// skipped, and Down sections are never executed.
//
// # The database is yours
//
// Build takes a connection to an empty database and will not create or drop
// one. Creating databases needs credentials beyond what the rest of sqlb asks
// for, and dropping the wrong one is unrecoverable — so the destructive half of
// "scratch database" stays with the caller, who knows which ones are scratch.
package shadow

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/jryannel/sqlb/introspect"
	"github.com/jryannel/sqlb/migrate"
	"github.com/jryannel/sqlb/schema"
)

// Options configures a replay.
type Options struct {
	// Dir is the migration directory. Required.
	Dir string

	// Format is the migration format the directory is written in. Defaults to
	// migrate.Goose.
	//
	// A custom Format is not supported here: rendering a file and reading one
	// back are different problems, and this package only knows how to read the
	// three that ship.
	Format migrate.Format

	// Schema and Module are passed through to introspect when the replayed
	// database is read back.
	Schema string
	Module string
}

// Result reports what was replayed, so a failure or a surprise can be traced to
// a file rather than to "the migrations".
type Result struct {
	// Files are the migration filenames applied, in the order they ran.
	Files []string
	// Statements is how many statements were executed in total.
	Statements int
}

// Build applies every migration in the directory to db and returns the schema
// they produce.
//
// db must be connected to an **empty** database. Replaying a history onto a
// schema that already has tables in it produces a registry describing neither
// one, and the migration generated from it would be wrong in a way nothing
// downstream could detect — so a non-empty database is refused rather than
// worked around.
//
// The introspect.Report is the same one introspect.Registry returns: a
// non-empty one means the replayed schema uses constructs the DSL cannot
// express, so the registry does not describe it completely.
func Build(ctx context.Context, db *sql.DB, opts Options) (*schema.Registry, *introspect.Report, *Result, error) {
	if db == nil {
		return nil, nil, nil, fmt.Errorf("shadow: Build needs a database connection")
	}
	if opts.Dir == "" {
		return nil, nil, nil, fmt.Errorf("shadow: Build needs a migration directory")
	}
	format := opts.Format
	if format == nil {
		format = migrate.Goose
	}

	if err := requireEmpty(ctx, db, opts.Schema); err != nil {
		return nil, nil, nil, err
	}

	files, err := collect(opts.Dir, format.Name())
	if err != nil {
		return nil, nil, nil, err
	}
	if len(files) == 0 {
		return nil, nil, nil, fmt.Errorf(
			"shadow: no migrations found in %s. An empty history would replay to an "+
				"empty schema, and a diff against that proposes creating every table you "+
				"already have — so this is refused rather than answered", opts.Dir)
	}

	res := &Result{}
	for _, f := range files {
		if err := apply(ctx, db, f); err != nil {
			return nil, nil, nil, err
		}
		res.Files = append(res.Files, f.Name)
		res.Statements += len(f.Statements)
	}

	reg, report, err := introspect.Registry(ctx, db, introspect.Options{
		Schema: opts.Schema,
		Module: opts.Module,
	})
	if err != nil {
		return nil, nil, res, fmt.Errorf("shadow: reading back the replayed schema: %w", err)
	}
	return reg, report, res, nil
}

// apply runs one migration's forward statements.
//
// In a transaction unless the file said not to, which mirrors what a runner
// does and is what makes a failure leave the shadow database at a file
// boundary rather than halfway through one.
func apply(ctx context.Context, db *sql.DB, f file) error {
	if f.NoTransaction {
		for i, stmt := range f.Statements {
			if _, err := db.ExecContext(ctx, stmt); err != nil {
				return statementError(f, i, stmt, err)
			}
		}
		return nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("shadow: %s: beginning a transaction: %w", f.Name, err)
	}
	defer func() { _ = tx.Rollback() }()

	for i, stmt := range f.Statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return statementError(f, i, stmt, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("shadow: %s: committing: %w", f.Name, err)
	}
	return nil
}

func statementError(f file, i int, stmt string, err error) error {
	return fmt.Errorf("shadow: %s: statement %d of %d failed: %w\n%s",
		f.Name, i+1, len(f.Statements), err, strings.TrimSpace(stmt))
}

// requireEmpty refuses a database that already has tables in it.
func requireEmpty(ctx context.Context, db *sql.DB, schemaName string) error {
	if schemaName == "" {
		schemaName = "public"
	}

	rows, err := db.QueryContext(ctx, `
		SELECT c.relname
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relkind IN ('r', 'p')
		ORDER BY c.relname
		LIMIT 6
	`, schemaName)
	if err != nil {
		return fmt.Errorf("shadow: checking that the target database is empty: %w", err)
	}
	defer rows.Close()

	var found []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("shadow: checking that the target database is empty: %w", err)
		}
		found = append(found, name)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("shadow: checking that the target database is empty: %w", err)
	}
	if len(found) == 0 {
		return nil
	}

	list := strings.Join(found, ", ")
	if len(found) > 5 {
		list = strings.Join(found[:5], ", ") + ", …"
	}
	return fmt.Errorf(
		"shadow: schema %q already contains tables (%s), and a history replayed on top "+
			"of them describes neither the history nor the database. Point Build at an "+
			"empty database — it will not drop these, because it cannot tell a scratch "+
			"database from a real one", schemaName, list)
}
