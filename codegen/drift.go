package codegen

// `sqlb check -database` asks the question `sqlb check` could not: does the
// declaration still describe the table the database actually has?
//
// Without it, check compares committed output against the declaration and never
// opens a database — so pointing it at a dead one still passes, and nothing in
// the toolchain verifies that the schema a project calls its single source of
// truth is still true (issue #54). Between a hand-edited migration, a hotfix
// applied by someone with psql open, and a column added by another service, a
// declaration drifts from its database silently and stays wrong until the day a
// generated statement names a column that is not there.
//
// # What it compares
//
// Only the tables the schema declares. An incremental adoption declares a
// handful while the database holds dozens, and diffing five tables against
// sixty-nine would report the other sixty-four as tables to drop — which is not
// drift, it is the adoption working as intended. So the introspection is
// narrowed to the declared names, and a declared table the database does not
// have shows up as the create it is.
//
// # What it costs the database
//
// One catalog read, and — for a schema with CHECK constraints — one ADD
// CONSTRAINT … NOT VALID per check inside a transaction that is always rolled
// back. NOT VALID means no table scan, so the probe is a catalog write and a
// brief lock rather than a read of every row. Postgres stores a check as a
// parse tree and hands back its own spelling, so without that probe every check
// in the schema reads as drift forever (issue #24, and Diff's own doc comment).

import (
	"context"
	"flag"
	"io"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jryannel/sqlb"
	"github.com/jryannel/sqlb/introspect"
	"github.com/jryannel/sqlb/migrate"
	"github.com/jryannel/sqlb/schema"
	"github.com/jryannel/sqlb/shadow"
)

// runCheck is the driver's `check` verb: report stale generated files, and —
// with -database — report a declaration that no longer describes the database.
func runCheck(p Project, opts Options, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sqlb check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dsn := fs.String("database", "",
		"also compare the declared schema against this live database, and fail on drift")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		say(stderr, "sqlb: check takes no positional arguments, got %q\n", fs.Arg(0))
		return 2
	}

	code := 0
	stale, err := Check(opts)
	if err != nil {
		line(stderr, err)
		return 1
	}
	if len(stale) > 0 {
		// Naming the command that fixes it matters more here than anywhere else
		// in sqlb: this message is read almost exclusively out of a CI log, by
		// someone who is not in the directory and has no idea what the
		// generator was called.
		line(stderr, "sqlb: generated files are out of date; run: sqlb generate")
		for _, f := range stale {
			line(stderr, "  "+f)
		}
		code = 1
	} else {
		line(stderr, "sqlb: generated files are current")
	}

	if *dsn == "" {
		return code
	}
	if driftCode := checkDrift(p, opts, *dsn, stdout, stderr); driftCode != 0 {
		code = driftCode
	}
	return code
}

// checkDrift compares the declared schema against a live database.
func checkDrift(p Project, opts Options, dsn string, stdout, stderr io.Writer) int {
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		say(stderr, "sqlb: connecting to the database: %v\n", err)
		return 1
	}
	defer pool.Close()

	declared := opts.Registry
	current, rep, err := introspect.Registry(ctx, sqlb.New(pool), introspect.Options{
		Schema: p.PostgresSchema,
		Module: p.Module,
		Only:   declaredTableNames(declared),
	})
	if err != nil {
		say(stderr, "sqlb: reading the database: %v\n", err)
		return 1
	}
	// The report is what the database has and this package cannot describe. It
	// is not drift — nothing here is wrong — but a gate that silently skipped
	// it would be claiming to have checked something it never looked at.
	if !rep.Empty() {
		line(stderr, "sqlb: the database has constructs the schema DSL cannot describe, "+
			"and they were not compared:")
		line(stderr, rep)
	}

	// Both sides have to spell their checks the way Postgres does, or every
	// check in the schema reads as drift.
	if unprobed, err := shadow.Normalize(ctx, pool, declared, shadow.Options{
		Schema: p.PostgresSchema,
	}); err != nil {
		say(stderr, "sqlb: normalising the declared checks: %v\n", err)
		return 1
	} else if len(unprobed) > 0 {
		line(stderr, "sqlb: these checks could not be normalised, so they are compared as written:")
		for _, u := range unprobed {
			line(stderr, "  "+u)
		}
	}

	var diffOpts []migrate.Option
	if p.MinPostgres > 0 {
		diffOpts = append(diffOpts, migrate.MinPostgres(p.MinPostgres))
	}
	changes, err := migrate.Diff(current, declared, diffOpts...)
	if err != nil {
		say(stderr, "sqlb: comparing the schema with the database: %v\n", err)
		return 1
	}
	if len(changes) == 0 {
		line(stderr, "sqlb: the database matches the schema")
		return 0
	}

	// The changes are the answer, so they go to stdout: this is the one thing
	// the command produces, and it is worth piping into a migration.
	say(stderr, "sqlb: the database does not match the schema (%d differences):\n", len(changes))
	for _, c := range changes {
		if c.Comment != "" {
			line(stdout, "-- "+c.Comment)
		}
		line(stdout, strings.TrimSpace(c.Up))
	}
	line(stderr, "sqlb: run `sqlb migrate -name <what_changed>` if the schema is right, "+
		"or edit the declaration if the database is")
	return 1
}

// declaredTableNames is the storage names of the declared tables, which is what
// the database calls them and therefore what narrows the read.
func declaredTableNames(r *schema.Registry) []string {
	if r == nil {
		return nil
	}
	tables := r.Tables()
	out := make([]string, 0, len(tables))
	for _, t := range tables {
		out = append(out, t.Name())
	}
	return out
}
