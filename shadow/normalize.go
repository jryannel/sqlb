package shadow

// Making a declared CHECK comparable with an introspected one.
//
// Postgres does not store a check expression as it was written. It stores a
// parse tree, and pg_get_expr renders that back in a canonical spelling: fully
// parenthesised, with explicit casts on literals, and keywords in its own case.
// So this, declared by the DSL —
//
//	status <> 'done' OR completed_at IS NOT NULL
//
// comes back from introspect as
//
//	((status <> 'done'::text) OR (completed_at IS NOT NULL))
//
// and migrate.Diff, which compares constraint definitions as strings, sees two
// different constraints with the same name. The migration it generates drops
// and re-adds the check — every run, forever, with an ACCESS EXCLUSIVE lock
// attached. That is issue #24, and it had gone unnoticed because nothing before
// `sqlb migrate` compared a *declared* check with an *introspected* one.
//
// # Why not normalise the strings
//
// The tempting fix is to canonicalise both sides in Go: strip redundant
// parentheses, drop `::type` casts, collapse whitespace. It was rejected on
// consequence asymmetry, which is the same test ADR-0014 applies to inferring
// renames.
//
// Stripping parentheses loses information. `(a OR b) AND c` and
// `a OR (b AND c)` both reduce to `a OR b AND c`, so a heuristic can report two
// genuinely different constraints as equal — and a diff that says "unchanged"
// about a constraint that changed produces no migration at all. The failure is
// silent, and it is a schema edit that never reaches the database. The failure
// it replaces is churn: loud, visible, and harmless. Trading a loud wrong
// answer for a quiet one is the wrong direction.
//
// # So ask Postgres
//
// The database that can answer this is already open and already has the tables:
// it is the shadow database, moments after the history has been replayed into
// it. Adding the declared expression as a constraint and reading back what
// Postgres stored puts both sides through exactly the same normalisation, on
// exactly the same column types. It is correct by construction rather than by
// approximation, and it costs one round trip per check.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jryannel/sqlb/schema"
)

// probeName is the constraint added and rolled back for each expression. It is
// never committed, so a collision with a real constraint of this name is the
// only way it could matter — hence a name nothing would choose.
const probeName = "sqlb_normalize_probe"

// NormalizeChecks rewrites every CHECK expression in reg into the spelling
// Postgres stores, so that reg can be compared with a registry that introspect
// produced.
//
// db must be connected to a database in which reg's tables already exist —
// which is what the shadow database is, immediately after Build. Everything
// happens inside a transaction that is always rolled back, so nothing is added
// to that database and nothing is left behind on a failure.
//
// It is idempotent. Normalising an expression Postgres already normalised
// yields the same string, so running it over a registry introspect built is
// safe and does nothing.
//
// A check that cannot be probed is left exactly as declared, and named in the
// returned slice. That is the right default rather than an error: a check
// referring to a column this migration is about to add cannot be evaluated
// against the table as it stands today, and it is also, necessarily, a check
// the diff should report as new. Failing the whole run for it would make the
// command unusable at the moment it is most useful.
//
// reg is modified in place. The caller is the one holding the declared
// registry, and the normalised form is what it wants from here on — this is the
// last step before a diff.
//
// Which is worth one warning, because the registry a project hands over is
// usually schema.DefaultRegistry() and that is a global. In `sqlb migrate` it
// does not matter: the process runs one verb and exits. In a long-lived program
// — or a test binary shared with tests that render DDL from the same registry —
// this rewrites declarations underneath them. The rewritten expression is
// semantically identical and Postgres stores the same thing either way, so what
// changes is the text, not the schema; but it does change.
func NormalizeChecks(ctx context.Context, db *sql.DB, reg *schema.Registry, opts Options) ([]string, error) {
	if db == nil {
		return nil, fmt.Errorf("shadow: NormalizeChecks needs a database connection")
	}
	if reg == nil {
		return nil, nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("shadow: normalising checks: %w", err)
	}
	// Always. The commit path does not exist: every statement below is a probe
	// whose only product is the string it returns.
	defer func() { _ = tx.Rollback() }()

	var unprobed []string
	for _, t := range reg.Tables() {
		if len(t.Checks()) == 0 {
			continue
		}
		table := qualify(opts.Schema, t.Name())

		// A table the replay did not produce is one this migration is about to
		// create, so its checks are new and there is nothing to compare them
		// with. Skipped silently rather than reported: every check on every new
		// table would otherwise arrive as a warning about a comparison nobody
		// was going to make.
		exists, err := tableExists(ctx, tx, table)
		if err != nil {
			return nil, fmt.Errorf("shadow: looking for %s: %w", table, err)
		}
		if !exists {
			continue
		}

		for _, c := range t.Checks() {
			normalised, err := probe(ctx, tx, table, c.Expr)
			if err != nil {
				unprobed = append(unprobed, t.Name()+"."+c.Name+": "+oneLine(err))
				continue
			}
			t.ReplaceCheckExpr(c.Name, normalised)
		}
	}
	return unprobed, nil
}

// tableExists asks whether the name resolves, without raising the error a cast
// to regclass raises when it does not.
func tableExists(ctx context.Context, tx *sql.Tx, table string) (bool, error) {
	var oid sql.NullString
	if err := tx.QueryRowContext(ctx, "SELECT to_regclass($1)::text", table).Scan(&oid); err != nil {
		return false, err
	}
	return oid.Valid, nil
}

// probe adds the expression as a constraint, reads back what Postgres stored,
// and undoes it.
//
// The savepoint is what makes a failure survivable. Postgres aborts the whole
// transaction on any error, so without one a single unprobeable check — a
// reference to a column that does not exist yet is the ordinary case — would
// take every check after it down with it, and the failure would look like a
// property of the *next* table.
func probe(ctx context.Context, tx *sql.Tx, table, expr string) (string, error) {
	if _, err := tx.ExecContext(ctx, "SAVEPOINT "+probeName); err != nil {
		return "", err
	}

	out, err := probeInner(ctx, tx, table, expr)
	if err != nil {
		if _, rbErr := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+probeName); rbErr != nil {
			// The transaction is unusable from here, and saying so beats
			// reporting every remaining check as unprobeable.
			return "", errors.Join(err, rbErr)
		}
		return "", err
	}

	_, err = tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+probeName)
	return out, err
}

func probeInner(ctx context.Context, tx *sql.Tx, table, expr string) (string, error) {
	// NOT VALID because the point is what Postgres *stores*, not whether the
	// rows satisfy it. The shadow database has no rows, so this buys nothing
	// today and keeps the probe cheap on a database that is not empty.
	add := fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s CHECK (%s) NOT VALID",
		table, quoteIdent(probeName), expr)
	if _, err := tx.ExecContext(ctx, add); err != nil {
		return "", err
	}

	const read = `
		SELECT pg_get_expr(con.conbin, con.conrelid)
		FROM pg_constraint con
		WHERE con.conname = $1 AND con.conrelid = $2::regclass`

	var normalised string
	if err := tx.QueryRowContext(ctx, read, probeName, table).Scan(&normalised); err != nil {
		return "", err
	}
	if normalised == "" {
		return "", fmt.Errorf("postgres stored no expression for the probe")
	}
	return normalised, nil
}

// qualify renders the table name for a statement, in the schema the replay
// used. Unqualified when no schema was given, which leaves it to search_path —
// the same thing every other statement this package runs does.
func qualify(schemaName, table string) string {
	if schemaName == "" {
		return quoteIdent(table)
	}
	return quoteIdent(schemaName) + "." + quoteIdent(table)
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// oneLine flattens a driver error for a report line. Postgres errors carry a
// DETAIL and a HINT on their own lines, which is useful in a log and unreadable
// in a list.
func oneLine(err error) string {
	return strings.Join(strings.Fields(err.Error()), " ")
}
