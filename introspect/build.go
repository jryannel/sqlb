package introspect

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jryannel/sqlb/schema"
)

// build turns catalog rows into a registry. It is a pure function, which is the
// whole reason reading and interpreting are separate: every mapping decision
// below can be tested against rows written by hand, and the only thing that
// needs a real database is whether the queries return the right rows.
func build(cat *catalog, opts Options) (*schema.Registry, *Report, error) {
	rep := &Report{}
	r := schema.NewRegistry()
	if opts.Module != "" {
		r = schema.NewModule(opts.Module)
	}

	byTable := groupByTable(cat)
	order, err := dependencyOrder(cat, byTable, rep)
	if err != nil {
		return nil, rep, err
	}

	built := map[string]*schema.TableDef{}
	for _, name := range order {
		local, ok := localName(name, opts.Module)
		if !ok {
			rep.add(name, "", "table is not prefixed with the module name, and a module "+
				"registry would rename it on the way back out", "")
			continue
		}
		// A name the DSL cannot declare is skipped and reported like any other
		// unrepresentable construct. Letting it through to Validate below would
		// fail the entire import with a message blaming this package, when the
		// cause is a database that quotes its identifiers — a camelCase table
		// is legal Postgres and routine in schemas built by other tools.
		if err := schema.CheckIdent(local); err != nil {
			rep.add(name, "", "table name cannot be declared: "+err.Error(), "")
			continue
		}
		t, err := buildTable(r, name, local, byTable[name], built, rep)
		if err != nil {
			return nil, rep, err
		}
		built[name] = t
	}

	if err := r.Validate(); err != nil {
		return nil, rep, fmt.Errorf("introspect: the imported schema does not validate, "+
			"which means this package built something the DSL considers impossible: %w", err)
	}
	return r, rep, nil
}

// tableParts is one table's rows, gathered from the flat catalog.
type tableParts struct {
	table       tableRow
	columns     []columnRow
	constraints []constraintRow
	indexes     []indexRow
}

func groupByTable(cat *catalog) map[string]*tableParts {
	out := map[string]*tableParts{}
	part := func(name string) *tableParts {
		if p, ok := out[name]; ok {
			return p
		}
		p := &tableParts{}
		out[name] = p
		return p
	}
	for _, t := range cat.tables {
		part(t.Name).table = t
	}
	for _, c := range cat.columns {
		p := part(c.Table)
		p.columns = append(p.columns, c)
	}
	for _, c := range cat.constraints {
		p := part(c.Table)
		p.constraints = append(p.constraints, c)
	}
	for _, i := range cat.indexes {
		p := part(i.Table)
		p.indexes = append(p.indexes, i)
	}
	return out
}

// dependencyOrder returns table names ordered so that every foreign key's
// target is built before the table referencing it.
//
// A cycle is reported rather than broken. The DSL declares a reference by
// passing the target table's own value — schema.Ref("org", Org) — so a cycle
// would be a Go initialisation cycle, which does not compile. There is no
// ordering that fixes it and no point pretending otherwise: the schema has to
// be edited, by making one side an ExternalRef or by adding the second
// reference in a later migration.
func dependencyOrder(cat *catalog, byTable map[string]*tableParts, rep *Report) ([]string, error) {
	names := make([]string, 0, len(byTable))
	for n := range byTable {
		names = append(names, n)
	}
	sort.Strings(names)

	deps := map[string][]string{}
	for _, n := range names {
		seen := map[string]bool{}
		for _, c := range byTable[n].constraints {
			if c.Type != "f" || c.RefTable == "" || c.RefTable == n || seen[c.RefTable] {
				continue
			}
			seen[c.RefTable] = true
			deps[n] = append(deps[n], c.RefTable)
		}
	}

	var out []string
	state := map[string]int{} // 0 unvisited, 1 in progress, 2 done
	var visit func(string, []string) bool
	visit = func(n string, path []string) bool {
		switch state[n] {
		case 2:
			return true
		case 1:
			cycle := append(append([]string{}, path...), n)
			rep.add(n, "", "foreign keys form a cycle ("+strings.Join(cycle, " → ")+
				"), which the DSL cannot express: a reference names the target table's "+
				"own value, so a cycle is a Go initialisation cycle. Break it by making "+
				"one side an ExternalRef", "")
			return false
		}
		state[n] = 1
		for _, d := range deps[n] {
			if _, known := byTable[d]; !known {
				continue // a reference out of the schema being read
			}
			if !visit(d, append(path, n)) {
				state[n] = 2
				return false
			}
		}
		state[n] = 2
		out = append(out, n)
		return true
	}
	for _, n := range names {
		visit(n, nil)
	}
	return out, nil
}

// localName strips a module prefix, reporting whether the table belongs to the
// module at all.
func localName(name, module string) (string, bool) {
	if module == "" {
		return name, true
	}
	prefix := module + "_"
	if !strings.HasPrefix(name, prefix) {
		return "", false
	}
	return strings.TrimPrefix(name, prefix), true
}

func buildTable(r *schema.Registry, name, local string, p *tableParts,
	built map[string]*schema.TableDef, rep *Report) (*schema.TableDef, error) {

	cons := classify(name, p.constraints, rep)

	fields := make([]schema.FieldSpec, 0, len(p.columns))
	for _, col := range p.columns {
		f, ok := buildColumn(name, col, cons, built, rep)
		if ok {
			fields = append(fields, f)
		}
	}

	t := r.Table(local, fields...)
	if p.table.Comment != "" {
		t.Describe(p.table.Comment)
	}
	if cons.pk != nil && cons.pk.Name != name+"_pkey" {
		t.PrimaryKeyNamed(cons.pk.Name)
	}
	for _, c := range cons.tableChecks {
		t.Check(c.Name, c.Expr)
	}
	for _, idx := range p.indexes {
		if idx.Expression || len(idx.Columns) == 0 {
			rep.add(name, idx.Name, "index is over an expression rather than plain columns, "+
				"which the DSL cannot declare", idx.Def)
			continue
		}
		t.AddIndex(schema.Index{
			Name: idx.Name, Columns: idx.Columns, Unique: idx.Unique,
			Method: indexMethod(idx.Method), Where: idx.Where,
		})
	}
	return t, nil
}

// indexMethod omits btree, which is the dialect default and which the DDL layer
// leaves off — so recording it would make an unchanged index look changed.
func indexMethod(m string) string {
	if m == "btree" {
		return ""
	}
	return m
}

// constraints is a table's constraints, sorted into what each one means for the
// columns it covers.
type constraints struct {
	pk          *constraintRow
	unique      map[string]constraintRow // by column, single-column only
	foreign     map[string]constraintRow // by column, single-column only
	enums       map[string][]string      // by column, recovered from a CHECK
	tableChecks []schema.Check
}

// classify sorts a table's constraints, reporting the ones the DSL has no way
// to declare.
func classify(table string, rows []constraintRow, rep *Report) *constraints {
	c := &constraints{
		unique:  map[string]constraintRow{},
		foreign: map[string]constraintRow{},
		enums:   map[string][]string{},
	}
	for _, row := range rows {
		switch row.Type {
		case "n":
			// Postgres 18 records every NOT NULL as a constraint of its own.
			// The column already carries it through attnotnull, and importing
			// these as anything would invent constraints the DSL never emits.
			continue
		case "p":
			if len(row.Columns) != 1 {
				rep.add(table, row.Name, "composite primary key; the DSL declares at most "+
					"one primary key column (a composite unique index is the nearest thing)", row.Def)
				continue
			}
			r := row
			c.pk = &r
		case "u":
			if len(row.Columns) != 1 {
				// UniqueIndex would produce CREATE UNIQUE INDEX, which is a
				// different object from a unique constraint and would diff as
				// one, so this is reported rather than approximated.
				rep.add(table, row.Name, "composite unique constraint; the DSL can declare a "+
					"composite unique index, which is a different object and would diff as one", row.Def)
				continue
			}
			c.unique[row.Columns[0]] = row
		case "f":
			if len(row.Columns) != 1 || len(row.RefCols) != 1 {
				rep.add(table, row.Name, "composite foreign key; the DSL declares single-column "+
					"references only", row.Def)
				continue
			}
			c.foreign[row.Columns[0]] = row
		case "c":
			c.check(table, row, rep)
		default:
			rep.add(table, row.Name, "constraint of a kind the DSL cannot declare (contype "+
				row.Type+")", row.Def)
		}
	}
	return c
}

// check decides whether a CHECK is an enum in disguise or a check in its own
// right. An enum is text plus a CHECK (ADR-0017), so this is the only place the
// two can be told apart.
func (c *constraints) check(table string, row constraintRow, rep *Report) {
	for _, col := range candidateEnumColumns(row) {
		if values, ok := enumValues(col, row.Expr); ok {
			c.enums[col] = values
			return
		}
	}
	c.tableChecks = append(c.tableChecks, schema.Check{Name: row.Name, Expr: row.Expr})
}

// candidateEnumColumns is the columns a CHECK covers, which for an enum's check
// is exactly one.
func candidateEnumColumns(row constraintRow) []string {
	if len(row.Columns) == 1 {
		return row.Columns
	}
	return nil
}

func buildColumn(table string, col columnRow, cons *constraints,
	built map[string]*schema.TableDef, rep *Report) (*schema.Field, bool) {

	if err := schema.CheckIdent(col.Name); err != nil {
		rep.add(table, col.Name, "column name cannot be declared: "+err.Error(), col.Type)
		return nil, false
	}
	if col.Generated != "" {
		rep.add(table, col.Name, "generated column, which the DSL cannot declare", col.Type)
		return nil, false
	}
	if col.Identity != "" {
		rep.add(table, col.Name, "identity column, which the DSL cannot declare "+
			"(a default is the nearest thing)", col.Type)
		return nil, false
	}

	t, size, ok := columnType(col.Type)
	if !ok {
		rep.add(table, col.Name, "column type "+col.Type+" has no equivalent in the DSL; "+
			"importing it as anything else would propose changing the real column", col.Type)
		return nil, false
	}

	f := newField(col, t, size, cons, built, rep, table)
	if f == nil {
		return nil, false
	}

	if !col.NotNull {
		f.Nullable()
	}
	if d := columnDefault(col.Default, col.Type, t); d != nil {
		f.Default(d)
	}
	if col.Comment != "" {
		f.Comment(col.Comment)
	}

	if u, isUnique := cons.unique[col.Name]; isUnique {
		f.Unique()
		// Pinned only when it differs from what the DDL layer would generate,
		// so an imported schema does not carry a name for every constraint it
		// happens to agree about.
		if u.Name != table+"_"+col.Name+"_key" {
			f.ConstraintNamed(u.Name)
		}
	}
	if cons.pk != nil && cons.pk.Columns[0] == col.Name {
		f.PrimaryKey()
	}
	return f, true
}

// newField creates the column in whichever of the DSL's forms it belongs to: a
// reference, an enum, or a plain typed column.
func newField(col columnRow, t schema.Type, size int, cons *constraints,
	built map[string]*schema.TableDef, rep *Report, table string) *schema.Field {

	if fk, isRef := cons.foreign[col.Name]; isRef {
		// built holds the tables finished so far, so a target that is missing
		// from it is either genuinely outside the schema or the table
		// currently being built — a self-reference, which is common enough
		// (manager_id, parent_id, reply_to) that reporting it as absent would
		// be both wrong and confusing.
		//
		// Either way only the *constraint* is unrepresentable. The column
		// itself is an ordinary typed column and is imported as one: dropping
		// it would leave the registry missing a column the database has, which
		// makes the next Diff propose adding a column that exists, or dropping
		// one that is load-bearing.
		if target := built[fk.RefTable]; target != nil {
			return refField(col, fk, target, rep, table)
		}
		if fk.RefTable == table {
			rep.add(table, col.Name, "self-referential foreign key, which the DSL cannot "+
				"declare; the column is imported without it", fk.Def)
		} else {
			rep.add(table, col.Name, "foreign key points at "+fk.RefTable+
				", which is not in the schema being read; the column is imported "+
				"without it", fk.Def)
		}
	}
	if values, isEnum := cons.enums[col.Name]; isEnum && t == schema.TypeText {
		return schema.Enum(col.Name, values...)
	}
	return plainField(col.Name, t, size)
}

func refField(col columnRow, fk constraintRow, target *schema.TableDef,
	rep *Report, table string) *schema.Field {

	f := schema.Ref(relationName(col.Name), target)
	if f.Name() != col.Name {
		f.Named(col.Name)
	}
	if onDelete, ok := referentialAction(fk.OnDelete); ok {
		f.OnDelete(onDelete)
	} else {
		rep.add(table, fk.Name, "ON DELETE action "+fk.OnDelete+" has no equivalent in the DSL", fk.Def)
	}
	if onUpdate, ok := referentialAction(fk.OnUpdate); ok {
		f.OnUpdate(onUpdate)
	} else {
		rep.add(table, fk.Name, "ON UPDATE action "+fk.OnUpdate+" has no equivalent in the DSL", fk.Def)
	}
	if fk.Name != table+"_"+col.Name+"_fkey" {
		f.ConstraintNamed(fk.Name)
	}
	// The referenced column is whatever the foreign key names, which is not
	// always the target's primary key.
	if len(fk.RefCols) == 1 {
		f.Desc().Ref.Column = fk.RefCols[0]
	}
	return f
}

// relationName strips the conventional _id suffix, so that org_id imports as a
// relation called org and ?expand=org keeps working.
func relationName(column string) string {
	return strings.TrimSuffix(column, "_id")
}

func plainField(name string, t schema.Type, size int) *schema.Field {
	switch t {
	case schema.TypeText:
		return schema.Text(name)
	case schema.TypeVarchar:
		return schema.Varchar(name, size)
	case schema.TypeInt:
		return schema.Int(name)
	case schema.TypeBigInt:
		return schema.BigInt(name)
	case schema.TypeFloat:
		return schema.Float(name)
	case schema.TypeNumeric:
		return schema.Numeric(name)
	case schema.TypeBool:
		return schema.Bool(name)
	case schema.TypeUUID:
		return schema.UUID(name)
	case schema.TypeTimestamp:
		return schema.Timestamp(name)
	case schema.TypeDate:
		return schema.Date(name)
	case schema.TypeTime:
		return schema.Time(name)
	case schema.TypeJSON:
		return schema.JSON(name)
	case schema.TypeBytes:
		return schema.Bytes(name)
	}
	// columnType only returns types this switch covers, so reaching here means
	// the two have drifted apart.
	panic("introspect: no constructor for type " + string(t))
}
