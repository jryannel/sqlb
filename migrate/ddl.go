package migrate

import (
	"fmt"
	"strings"
	"time"

	"github.com/jryannel/sqlb/schema"
)

// This file renders Postgres DDL from schema declarations. It is the only
// dialect-specific part of the package: Format decides what a *runner* wants a
// file to look like, this decides what a *database* wants a statement to look
// like. Diff composes the two.

// quoteIdent renders an identifier.
//
// Doubling embedded quotes is the only escape Postgres defines. Names reaching
// here have normally already passed schema validation, which rejects anything
// needing it; this is the backstop for the names that bypass validation,
// namely those pinned with ConstraintNamed and PrimaryKeyNamed.
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// sqlString renders a string as a SQL literal.
func sqlString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// sqlType maps a logical column type onto a concrete Postgres type.
//
// Enums render as text with a CHECK constraint rather than as a native
// Postgres ENUM. A native enum cannot have a value removed at all, and adding
// one is a DDL statement that cannot run inside a transaction — so a schema
// change as ordinary as editing a list of statuses would become an
// irreversible, unbatchable migration. A CHECK constraint is replaced by
// dropping and adding it, which the diff engine already knows how to do.
func sqlType(d *schema.FieldDesc) (string, error) {
	switch d.Type {
	case schema.TypeText, schema.TypeEnum:
		return "text", nil
	case schema.TypeVarchar:
		// A varchar with no length is text; Postgres treats them identically
		// and text is the name that says so.
		if d.Size > 0 {
			return fmt.Sprintf("varchar(%d)", d.Size), nil
		}
		return "text", nil
	case schema.TypeInt:
		return "integer", nil
	case schema.TypeBigInt:
		return "bigint", nil
	case schema.TypeFloat:
		return "double precision", nil
	case schema.TypeNumeric:
		return "numeric", nil
	case schema.TypeBool:
		return "boolean", nil
	case schema.TypeUUID:
		return "uuid", nil
	case schema.TypeTimestamp:
		return "timestamptz", nil
	case schema.TypeDate:
		return "date", nil
	case schema.TypeTime:
		return "time", nil
	case schema.TypeJSON:
		return "jsonb", nil
	case schema.TypeBytes:
		return "bytea", nil
	}
	return "", fmt.Errorf("migrate: column %q has unknown type %q", d.Name, d.Type)
}

// widens reports whether every value of the from type fits in the to type, so
// that changing between them cannot lose data. Anything not listed here is
// treated as potentially lossy, which is the safe default: a false negative
// produces a migration that is commented out until reviewed, a false positive
// produces one that silently truncates.
func widens(from, to *schema.FieldDesc) bool {
	switch from.Type {
	case schema.TypeInt:
		switch to.Type {
		case schema.TypeBigInt, schema.TypeNumeric:
			return true
		}
	case schema.TypeBigInt:
		if to.Type == schema.TypeNumeric {
			return true
		}
	case schema.TypeVarchar:
		switch to.Type {
		case schema.TypeText:
			return true
		case schema.TypeVarchar:
			// Zero means unbounded, so it accepts anything.
			return to.Size == 0 || to.Size >= from.Size
		}
	case schema.TypeEnum:
		// An enum is already text; only the CHECK constraint changes.
		return to.Type == schema.TypeText
	}
	return false
}

// renderDefault renders a column default for a DEFAULT clause.
func renderDefault(d *schema.Default) (string, error) {
	if d == nil {
		return "", nil
	}
	if d.Raw != "" {
		return d.Raw, nil
	}
	return literal(d.Value)
}

// literal renders a Go value as a SQL literal. Only the types a default can
// plausibly hold are supported; anything else is an authoring mistake with an
// obvious fix, so it names the fix.
func literal(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "NULL", nil
	case string:
		return sqlString(x), nil
	case bool:
		if x {
			return "true", nil
		}
		return "false", nil
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return fmt.Sprintf("%d", x), nil
	case float32, float64:
		return fmt.Sprintf("%v", x), nil
	case time.Time:
		return sqlString(x.Format(time.RFC3339Nano)), nil
	}
	return "", fmt.Errorf("migrate: cannot render %T as a SQL literal; use schema.Expr for anything else", v)
}

// columnDef renders a column's definition: everything after the name in a
// CREATE TABLE, and the whole of an ADD COLUMN.
func columnDef(d *schema.FieldDesc) (string, error) {
	t, err := sqlType(d)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(quoteIdent(d.Name) + " " + t)
	if !d.Nullable {
		b.WriteString(" NOT NULL")
	}
	if d.Default != nil {
		def, err := renderDefault(d.Default)
		if err != nil {
			return "", fmt.Errorf("migrate: column %q: %w", d.Name, err)
		}
		b.WriteString(" DEFAULT " + def)
	}
	return b.String(), nil
}

// hasForeignKey reports whether the column produces a FOREIGN KEY. An external
// reference deliberately does not: that is the whole point of module
// isolation (ADR-0015).
func hasForeignKey(d *schema.FieldDesc) bool {
	return d.Ref != nil && !d.Ref.External && d.Ref.Table != nil
}

// Constraint names follow the conventions Postgres itself uses, so that a
// schema imported from an existing database matches without every constraint
// needing to be pinned by hand.

func primaryKeyName(t *schema.TableDef) string {
	if n := t.PrimaryKeyName(); n != "" {
		return n
	}
	return t.Name() + "_pkey"
}

// uniqueConstraintName names a column's UNIQUE constraint. ConstraintName
// pins the foreign key when the column is a real reference, so a reference
// that is also unique takes the generated name for its unique constraint.
func uniqueConstraintName(t *schema.TableDef, d *schema.FieldDesc) string {
	if d.ConstraintName != "" && !hasForeignKey(d) {
		return d.ConstraintName
	}
	return t.Name() + "_" + d.Name + "_key"
}

func foreignKeyName(t *schema.TableDef, d *schema.FieldDesc) string {
	if d.ConstraintName != "" {
		return d.ConstraintName
	}
	return t.Name() + "_" + d.Name + "_fkey"
}

func enumCheckName(t *schema.TableDef, d *schema.FieldDesc) string {
	return t.Name() + "_" + d.Name + "_check"
}

// checkConstraintName falls back to a positional name for a check declared
// without one, so that it can still be diffed by name later.
func checkConstraintName(t *schema.TableDef, c schema.Check, i int) string {
	if c.Name != "" {
		return c.Name
	}
	return fmt.Sprintf("%s_check%d", t.Name(), i+1)
}

func enumCheckExpr(d *schema.FieldDesc) string {
	vals := make([]string, len(d.EnumValues))
	for i, v := range d.EnumValues {
		vals[i] = sqlString(v)
	}
	return quoteIdent(d.Name) + " IN (" + strings.Join(vals, ", ") + ")"
}

// constraint is one named table constraint, reduced to a comparable form.
// The diff compares def strings: two constraints with the same name and the
// same def are the same constraint.
type constraint struct {
	name string
	def  string // the SQL after CONSTRAINT <name>, e.g. `UNIQUE ("slug")`

	// fk marks a foreign key. These are ordered separately from other
	// constraints because they are the only ones that depend on another table
	// existing.
	fk bool

	// enum holds the permitted values when this is an enum column's CHECK,
	// so that removing a value can be told from adding one.
	enum []string
}

// constraints returns every named constraint the table declares, in a stable
// order.
//
// Foreign keys are included here rather than inlined into CREATE TABLE so that
// one code path serves both a new table and an altered one, and so that table
// creation never has to be ordered by dependency — the references are added
// once every table exists.
func constraints(t *schema.TableDef) []constraint {
	var out []constraint

	if pk := t.PrimaryKey(); pk != nil {
		out = append(out, constraint{
			name: primaryKeyName(t),
			def:  "PRIMARY KEY (" + quoteIdent(pk.Name()) + ")",
		})
	}

	for _, f := range t.Fields() {
		d := f.Desc()
		if d.Unique && !d.PrimaryKey {
			out = append(out, constraint{
				name: uniqueConstraintName(t, d),
				def:  "UNIQUE (" + quoteIdent(d.Name) + ")",
			})
		}
		if d.Type == schema.TypeEnum && len(d.EnumValues) > 0 {
			out = append(out, constraint{
				name: enumCheckName(t, d),
				def:  "CHECK (" + enumCheckExpr(d) + ")",
				enum: d.EnumValues,
			})
		}
		if hasForeignKey(d) {
			out = append(out, constraint{
				name: foreignKeyName(t, d),
				def:  foreignKeyDef(d),
				fk:   true,
			})
		}
	}

	for i, c := range t.Checks() {
		out = append(out, constraint{
			name: checkConstraintName(t, c, i),
			def:  "CHECK (" + c.Expr + ")",
		})
	}

	return out
}

func foreignKeyDef(d *schema.FieldDesc) string {
	r := d.Ref
	col := r.Column
	if col == "" {
		if pk := r.Table.PrimaryKey(); pk != nil {
			col = pk.Name()
		}
	}
	s := fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s (%s)",
		quoteIdent(d.Name), quoteIdent(r.Table.Name()), quoteIdent(col))
	// NO ACTION is the Postgres default, so emitting it would only add noise
	// and make a diff against an introspected schema look like a change.
	if r.OnDelete != "" && r.OnDelete != schema.NoAction {
		s += " ON DELETE " + string(r.OnDelete)
	}
	if r.OnUpdate != "" && r.OnUpdate != schema.NoAction {
		s += " ON UPDATE " + string(r.OnUpdate)
	}
	return s
}

// createTable renders CREATE TABLE with its columns and every constraint that
// does not depend on another table, followed by any COMMENT statements.
func createTable(t *schema.TableDef) (string, error) {
	var lines []string
	for _, f := range t.Fields() {
		def, err := columnDef(f.Desc())
		if err != nil {
			return "", err
		}
		lines = append(lines, "    "+def)
	}
	for _, c := range constraints(t) {
		if c.fk {
			continue
		}
		lines = append(lines, "    CONSTRAINT "+quoteIdent(c.name)+" "+c.def)
	}

	var b strings.Builder
	if len(lines) == 0 {
		// Postgres accepts a table with no columns, and refusing it here would
		// be a second place that decides what a valid schema is.
		b.WriteString("CREATE TABLE " + quoteIdent(t.Name()) + " ();")
	} else {
		b.WriteString("CREATE TABLE " + quoteIdent(t.Name()) + " (\n")
		b.WriteString(strings.Join(lines, ",\n"))
		b.WriteString("\n);")
	}
	for _, c := range commentStatements(t) {
		b.WriteString("\n" + c)
	}
	return b.String(), nil
}

// commentStatements renders the table's and its columns' descriptions.
func commentStatements(t *schema.TableDef) []string {
	var out []string
	if t.Comment() != "" {
		out = append(out, commentOnTable(t.Name(), t.Comment()))
	}
	for _, f := range t.Fields() {
		if d := f.Desc(); d.Comment != "" {
			out = append(out, commentOnColumn(t.Name(), d.Name, d.Comment))
		}
	}
	return out
}

// commentOnTable renders a table comment. An empty comment renders as NULL,
// which is how Postgres removes one.
func commentOnTable(table, comment string) string {
	return "COMMENT ON TABLE " + quoteIdent(table) + " IS " + commentValue(comment) + ";"
}

func commentOnColumn(table, column, comment string) string {
	return "COMMENT ON COLUMN " + quoteIdent(table) + "." + quoteIdent(column) +
		" IS " + commentValue(comment) + ";"
}

func commentValue(s string) string {
	if s == "" {
		return "NULL"
	}
	return sqlString(s)
}

func dropTable(t *schema.TableDef) string {
	return "DROP TABLE " + quoteIdent(t.Name()) + ";"
}

func addColumn(t *schema.TableDef, d *schema.FieldDesc) (string, error) {
	def, err := columnDef(d)
	if err != nil {
		return "", err
	}
	return "ALTER TABLE " + quoteIdent(t.Name()) + " ADD COLUMN " + def + ";", nil
}

func dropColumn(table, column string) string {
	return "ALTER TABLE " + quoteIdent(table) + " DROP COLUMN " + quoteIdent(column) + ";"
}

func alterColumn(table, column, action string) string {
	return "ALTER TABLE " + quoteIdent(table) + " ALTER COLUMN " + quoteIdent(column) +
		" " + action + ";"
}

func addConstraint(table string, c constraint) string {
	return "ALTER TABLE " + quoteIdent(table) + " ADD CONSTRAINT " + quoteIdent(c.name) +
		" " + c.def + ";"
}

func dropConstraint(table, name string) string {
	return "ALTER TABLE " + quoteIdent(table) + " DROP CONSTRAINT " + quoteIdent(name) + ";"
}

// indexDef reduces an index to a comparable string, so that a changed index is
// recognised as one thing rather than as an unrelated drop and create.
func indexDef(idx schema.Index) string {
	return fmt.Sprintf("unique=%t method=%q columns=%s where=%q",
		idx.Unique, idx.Method, strings.Join(idx.Columns, ","), idx.Where)
}

// createIndex renders CREATE INDEX. concurrent is decided by the caller: an
// index on a table created in the same migration needs no CONCURRENTLY,
// because there is nothing to lock it against and requiring it would force the
// migration into a second file for no benefit.
func createIndex(t *schema.TableDef, idx schema.Index, concurrent bool) string {
	var b strings.Builder
	b.WriteString("CREATE ")
	if idx.Unique {
		b.WriteString("UNIQUE ")
	}
	b.WriteString("INDEX ")
	if concurrent {
		b.WriteString("CONCURRENTLY ")
	}
	b.WriteString(quoteIdent(idx.Name) + " ON " + quoteIdent(t.Name()))
	if idx.Method != "" {
		b.WriteString(" USING " + idx.Method)
	}
	cols := make([]string, len(idx.Columns))
	for i, c := range idx.Columns {
		cols[i] = quoteIdent(c)
	}
	b.WriteString(" (" + strings.Join(cols, ", ") + ")")
	if idx.Where != "" {
		b.WriteString(" WHERE " + idx.Where)
	}
	b.WriteString(";")
	return b.String()
}

func dropIndex(name string, concurrent bool) string {
	if concurrent {
		return "DROP INDEX CONCURRENTLY " + quoteIdent(name) + ";"
	}
	return "DROP INDEX " + quoteIdent(name) + ";"
}
