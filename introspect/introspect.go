// Package introspect reads a Postgres schema out of pg_catalog and returns the
// same *schema.Registry the DSL produces.
//
// That symmetry is the point. A registry from here can be handed to
// migrate.Diff as the current state, which makes generating a migration and
// adopting an existing database the same machinery pointed in opposite
// directions (ADR-0014). It is also the only way to check the diff engine
// against a real database: render a schema to DDL, apply it, read it back, and
// the diff between what went in and what came out must be empty.
//
// # Why this is not in migrate
//
// migrate does not connect to a database and says so in its own documentation:
// it produces files, and a runner applies them. This package does connect, so
// it is separate, and migrate stays a pure function over two data structures.
//
// # The connection
//
// Everything here works through a sqlb.Executor, so a pool, a connection or a
// transaction the caller already holds all work, and reading a catalog uses the
// same handle as querying a table (ADR-0040).
//
// # What cannot be represented
//
// The DSL is narrower than Postgres, and the failure that matters is dropping
// something quietly — a schema that looks complete, describes the database
// incorrectly, and produces a migration that reverses work nobody meant to
// reverse. So every construct this cannot express is collected into a Report
// rather than skipped. Read it. A Report with entries means the emitted schema
// is not the whole database.
package introspect

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jryannel/sqlb"
	"github.com/jryannel/sqlb/schema"
)

// Options control what is read and how it is named.
type Options struct {
	// Schema is the Postgres schema to read. Defaults to "public".
	Schema string

	// Module, when set, produces a module registry (schema.NewModule) and
	// strips the module's prefix from every table name it finds, so that a
	// database whose tables are called billing_invoices imports as a module
	// named billing holding a table called invoices.
	//
	// Tables without the prefix are left alone and reported, since a module
	// registry would silently rename them on the way back out.
	Module string

	// Only limits the import to the named tables. Empty reads everything,
	// which is what an import of a database sqlb is taking over wants.
	//
	// A drift gate wants the opposite. An incremental adoption declares a
	// handful of tables while the database holds dozens, and diffing a
	// declaration of five tables against an import of sixty-nine reports the
	// other sixty-four as tables to drop — so the gate has to narrow one side,
	// and this is where it is narrowed (issue #54). Names are storage names,
	// before any module prefix is stripped, because that is what the database
	// calls them.
	//
	// A named table that is not in the database is reported rather than
	// ignored: a typo in this list would otherwise silently shrink what the
	// gate checks, which is the one failure a gate must not have.
	Only []string

	// Exclude drops the named tables from the import. It applies after Only,
	// so the two compose — read this schema except the queue tables — and it
	// is the right shape for the migration-history table every runner keeps,
	// which no declaration will ever describe.
	Exclude []string
}

func (o Options) schemaName() string {
	if o.Schema == "" {
		return "public"
	}
	return o.Schema
}

// Registry reads the database and returns the schema it describes, along with
// a Report of everything that could not be represented.
//
// The registry is validated before it is returned: a registry that does not
// validate would produce DDL for a schema that cannot exist, and finding that
// out here beats finding it out from a migration.
//
// Capabilities are not inferred. Nothing in a database says which columns
// should be filterable or exposed over REST, and guessing would publish
// columns nobody chose to publish — so everything imports with no capabilities
// at all and widening them is a deliberate, reviewable edit (ADR-0014).
func Registry(ctx context.Context, db sqlb.Executor, opts Options) (*schema.Registry, *Report, error) {
	cat, err := read(ctx, db, opts.schemaName())
	if err != nil {
		return nil, nil, err
	}
	return build(cat, opts)
}

// catalog is everything read from the database, before any of it is
// interpreted. Keeping the reading and the interpreting apart is what lets the
// interpretation be tested exhaustively against rows written by hand, without
// a database anywhere near it.
type catalog struct {
	tables      []tableRow
	columns     []columnRow
	constraints []constraintRow
	indexes     []indexRow
}

type tableRow struct {
	Name    string
	Comment string
}

type columnRow struct {
	Table     string
	Name      string
	Type      string // format_type, e.g. "character varying(200)"
	NotNull   bool
	Default   string // pg_get_expr of the default, "" for none
	Comment   string
	Identity  string // attidentity: "" | "a" | "d"
	Generated string // attgenerated: "" | "s"
}

type constraintRow struct {
	Table    string
	Name     string
	Type     string // contype: p, u, f, c, n, x, t
	Columns  []string
	RefTable string
	RefCols  []string
	OnDelete string // confdeltype
	OnUpdate string // confupdtype
	Expr     string // pg_get_expr of a CHECK
	Def      string // pg_get_constraintdef, for reporting what was skipped
}

type indexRow struct {
	Table      string
	Name       string
	Unique     bool
	Method     string
	Where      string
	Columns    []string
	Expression bool // an index over an expression rather than plain columns
	Def        string
}

// The catalog queries.
//
// They are written against what Postgres actually returns rather than against
// what it ought to: a varchar column reports as "character varying(200)", an
// enum's CHECK normalises to "= ANY (ARRAY[...])" rather than the IN () it was
// written as, and a literal default comes back with its cast attached. Each of
// those was observed before the mapping that handles it was written.
//
// Every catalog column of type "char" is cast to text. That is not decoration:
// attgenerated is a zero byte on an ordinary column, and a driver is entitled
// to decode that as a one-character string rather than an empty one — which is
// how every column in a database briefly became a generated column here. The
// cast makes the empty case empty on any driver.

const tableQuery = `
SELECT c.relname, COALESCE(obj_description(c.oid, 'pg_class'), '')
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relkind = 'r'
ORDER BY c.relname`

const columnQuery = `
SELECT c.relname, a.attname, format_type(a.atttypid, a.atttypmod), a.attnotnull,
       COALESCE(pg_get_expr(d.adbin, d.adrelid), ''),
       COALESCE(col_description(c.oid, a.attnum), ''),
       a.attidentity::text, a.attgenerated::text
FROM pg_attribute a
JOIN pg_class c ON c.oid = a.attrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_attrdef d ON d.adrelid = c.oid AND d.adnum = a.attnum
WHERE n.nspname = $1 AND c.relkind = 'r' AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY c.relname, a.attnum`

// constraintQuery keeps conkey and confkey in their declared order. Postgres
// stores them as arrays whose order is meaningful — it is the column order of a
// composite key — and unnesting without WITH ORDINALITY loses it.
const constraintQuery = `
SELECT c.relname, con.conname, con.contype::text,
       COALESCE(k.cols, ''), COALESCE(ft.relname, ''), COALESCE(fk.cols, ''),
       con.confdeltype::text, con.confupdtype::text,
       COALESCE(pg_get_expr(con.conbin, con.conrelid), ''),
       pg_get_constraintdef(con.oid)
FROM pg_constraint con
JOIN pg_class c ON c.oid = con.conrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_class ft ON ft.oid = con.confrelid
LEFT JOIN LATERAL (
  SELECT string_agg(a.attname, ',' ORDER BY u.ord) AS cols
  FROM unnest(con.conkey) WITH ORDINALITY AS u(attnum, ord)
  JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = u.attnum
) k ON true
LEFT JOIN LATERAL (
  SELECT string_agg(a.attname, ',' ORDER BY u.ord) AS cols
  FROM unnest(con.confkey) WITH ORDINALITY AS u(attnum, ord)
  JOIN pg_attribute a ON a.attrelid = con.confrelid AND a.attnum = u.attnum
) fk ON true
WHERE n.nspname = $1
ORDER BY c.relname, con.conname`

// indexQuery skips the indexes that exist only to enforce a constraint, which
// are reported as the constraint instead.
//
// The match is on the constraint's own table as well as its index: a foreign
// key's conindid points at the *referenced* table's unique index, so testing
// conindid alone would hide the primary key of every table anything references.
const indexQuery = `
SELECT c.relname, i.relname, x.indisunique, am.amname,
       COALESCE(pg_get_expr(x.indpred, x.indrelid), ''),
       COALESCE(k.cols, ''), (0 = ANY (x.indkey::int2[])),
       pg_get_indexdef(x.indexrelid)
FROM pg_index x
JOIN pg_class i ON i.oid = x.indexrelid
JOIN pg_class c ON c.oid = x.indrelid
JOIN pg_am am ON am.oid = i.relam
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN LATERAL (
  SELECT string_agg(a.attname, ',' ORDER BY u.ord) AS cols
  FROM unnest(x.indkey::int2[]) WITH ORDINALITY AS u(attnum, ord)
  JOIN pg_attribute a ON a.attrelid = x.indrelid AND a.attnum = u.attnum
) k ON true
WHERE n.nspname = $1
  AND NOT EXISTS (
    SELECT 1 FROM pg_constraint con
    WHERE con.conindid = x.indexrelid AND con.conrelid = x.indrelid
      AND con.contype IN ('p', 'u', 'x')
  )
ORDER BY c.relname, i.relname`

func read(ctx context.Context, db sqlb.Executor, nspname string) (*catalog, error) {
	if db == nil {
		return nil, fmt.Errorf("introspect: no database given")
	}
	cat := &catalog{}

	if err := query(ctx, db, tableQuery, nspname, func(rows pgx.Rows) error {
		var r tableRow
		if err := rows.Scan(&r.Name, &r.Comment); err != nil {
			return err
		}
		cat.tables = append(cat.tables, r)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("introspect: reading tables: %w", err)
	}

	if err := query(ctx, db, columnQuery, nspname, func(rows pgx.Rows) error {
		var r columnRow
		if err := rows.Scan(&r.Table, &r.Name, &r.Type, &r.NotNull, &r.Default,
			&r.Comment, &r.Identity, &r.Generated); err != nil {
			return err
		}
		cat.columns = append(cat.columns, r)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("introspect: reading columns: %w", err)
	}

	if err := query(ctx, db, constraintQuery, nspname, func(rows pgx.Rows) error {
		var r constraintRow
		var cols, refCols string
		if err := rows.Scan(&r.Table, &r.Name, &r.Type, &cols, &r.RefTable, &refCols,
			&r.OnDelete, &r.OnUpdate, &r.Expr, &r.Def); err != nil {
			return err
		}
		r.Columns, r.RefCols = splitList(cols), splitList(refCols)
		cat.constraints = append(cat.constraints, r)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("introspect: reading constraints: %w", err)
	}

	if err := query(ctx, db, indexQuery, nspname, func(rows pgx.Rows) error {
		var r indexRow
		var cols string
		if err := rows.Scan(&r.Table, &r.Name, &r.Unique, &r.Method, &r.Where,
			&cols, &r.Expression, &r.Def); err != nil {
			return err
		}
		r.Columns = splitList(cols)
		cat.indexes = append(cat.indexes, r)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("introspect: reading indexes: %w", err)
	}

	return cat, nil
}

func query(ctx context.Context, db sqlb.Executor, sqlText, arg string, scan func(pgx.Rows) error) error {
	rows, err := db.Query(ctx, sqlText, arg)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}
