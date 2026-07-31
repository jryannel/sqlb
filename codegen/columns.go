package codegen

import (
	"fmt"

	"github.com/jryannel/sqlb/schema"
)

// renderColumns emits the typed column facade and a typed update statement per
// table.
//
// The facade is what puts column names and comparand types back under the
// compiler: the query engine is reflective, so sqlb.F("titel") is a runtime
// error, while PostCols.Titel does not compile.
//
// Hidden columns are omitted. A predicate against one should not be writable at
// all, which is the compile-time half of what the schema declares.
func renderColumns(opts Options) ([]byte, error) {
	tables := opts.Registry.Tables()

	ov, err := newOverrides(opts.Types, opts.Registry)
	if err != nil {
		return nil, err
	}

	imports := map[string]bool{"github.com/jryannel/sqlb": true}
	for _, path := range ov.imports(opts.Registry) {
		imports[path] = true
	}
	for _, t := range tables {
		for _, f := range t.Fields() {
			if f.Desc().Hidden {
				continue
			}
			if _, replaced := ov.base(t.Name(), f.Desc()); replaced {
				continue
			}
			switch base(f.Desc()) {
			case "time.Time":
				imports["time"] = true
			case "json.RawMessage":
				imports["encoding/json"] = true
			}
		}
	}

	b := header(opts.Package, sortedSet(imports))

	for _, t := range tables {
		typeName := TypeName(t.LocalName())
		setName := unexportedGoName(typeName) + "Columns"

		visible := make([]*schema.Field, 0, len(t.Fields()))
		for _, f := range t.Fields() {
			if !f.Desc().Hidden {
				visible = append(visible, f)
			}
		}

		fmt.Fprintf(b, "\ntype %s struct {\n", setName)
		for _, f := range visible {
			fmt.Fprintf(b, "\t%s %s\n", GoName(f.Desc().Name), colType(typeName, t.Name(), f.Desc(), ov))
		}
		fmt.Fprintln(b, "}")

		fmt.Fprintf(b, "\n// %sCols are the typed columns of %s.\n", typeName, t.Name())
		if hasHidden(t) {
			fmt.Fprintln(b, "// Hidden columns are omitted: a predicate against one should not compile.")
		}
		fmt.Fprintf(b, "var %sCols = %s{\n", typeName, setName)
		for _, f := range visible {
			d := f.Desc()
			fmt.Fprintf(b, "\t%s: %s,\n", GoName(d.Name), colCtor(typeName, t.Name(), d, ov))
		}
		fmt.Fprintln(b, "}")

		renderUpdate(b, t, typeName, ov)
	}

	return gofmt(opts.columnsFile(), b.Bytes())
}

// renderUpdate emits a typed update statement.
//
// The select builder is deliberately not wrapped — its chainable methods would
// each need their return type re-wrapped, for safety the column set already
// gives. An update is different: sqlb.Update.Set takes a string and an any, so
// neither the column name nor the value type is checked without this.
func renderUpdate(b interface{ WriteString(string) (int, error) }, t *schema.TableDef, typeName string, ov *overrides) {
	w := func(format string, args ...any) { _, _ = b.WriteString(fmt.Sprintf(format, args...)) }

	// Everything but the primary key gets a setter, including ReadOnly and
	// Immutable columns.
	//
	// Those two are REST-boundary rules, and the boundary is already defended
	// where it exists: they are absent from the generated request bodies and
	// the handler clears them. Go going through the query engine is trusted, so
	// excluding them here protected nothing — it only meant that the code which
	// is the *sole* writer of a ReadOnly column was the one code that could not
	// write it typed, and had to reach for Set(string, any) on exactly the
	// columns where a typo costs most.
	//
	// The primary key stays out. It addresses the row rather than being part of
	// what an update writes, and Stmt() is there for the rare case that is
	// genuinely meant.
	//
	// A computed column stays out too, and for a different reason: ReadOnly is
	// a rule about who may write, and a computed column has nothing to write
	// to. A setter for one would compile and then fail every statement it was
	// used in.
	var writable []*schema.Field
	for _, f := range t.Fields() {
		if d := f.Desc(); !d.PrimaryKey && !d.Computed() {
			writable = append(writable, f)
		}
	}
	if len(writable) == 0 {
		return
	}

	w("\n// %sUpdate is a typed update statement for %s.\n", typeName, t.Name())
	w("type %sUpdate struct {\n\tstmt *sqlb.Update[%s]\n}\n", typeName, typeName)

	w("\n// Update%s starts a typed update.\n", typeName)
	w("func Update%s() *%sUpdate {\n\treturn &%sUpdate{stmt: sqlb.UpdateRows[%s]()}\n}\n",
		typeName, typeName, typeName, typeName)

	for _, f := range writable {
		d := f.Desc()
		field := GoName(d.Name)
		typ := goType(typeName, t.Name(), d, ov)
		w("\n// Set%s sets %s.\n", field, d.Name)
		w("func (u *%sUpdate) Set%s(v %s) *%sUpdate {\n", typeName, field, typ, typeName)
		w("\tu.stmt.Set(%q, v)\n\treturn u\n}\n", d.Name)
	}

	w("\n// Where narrows the affected rows.\n")
	w("func (u *%sUpdate) Where(preds ...sqlb.Pred) *%sUpdate {\n", typeName, typeName)
	w("\tu.stmt.Where(preds...)\n\treturn u\n}\n")

	w("\n// Stmt exposes the underlying statement for what the wrapper does not\n")
	w("// cover, such as Everything, SetExpr, Exec and One.\n")
	w("func (u *%sUpdate) Stmt() *sqlb.Update[%s] { return u.stmt }\n", typeName, typeName)
}

// colType is the facade type for a column. Text columns get TextCol, which is
// the only type carrying Contains and the other pattern operators — so
// Contains on an integer does not compile rather than failing at the database.
//
// A nullable column is typed as its base type: nullability is a property of the
// column, not of the value compared against it, so the comparand is a value and
// NULL is expressed with IsNull.
// An array column gets ArrayCol, which carries the containment operators and
// none of the ordering or pattern ones — so Contains on a tag array does not
// compile, and the one name is not overloaded by column type (ADR-0033).
func colType(typeName, table string, d *schema.FieldDesc, ov *overrides) string {
	elem, textual := facadeElem(typeName, table, d, ov)
	switch {
	case d.Array:
		return fmt.Sprintf("sqlb.ArrayCol[%s]", elem)
	case textual:
		return fmt.Sprintf("sqlb.TextCol[%s]", elem)
	}
	return fmt.Sprintf("sqlb.Col[%s]", elem)
}

func colCtor(typeName, table string, d *schema.FieldDesc, ov *overrides) string {
	elem, textual := facadeElem(typeName, table, d, ov)
	switch {
	case d.Array:
		return fmt.Sprintf("sqlb.ArrayColumn[%s](%q)", elem, d.Name)
	case textual:
		return fmt.Sprintf("sqlb.TextColumn[%s](%q)", elem, d.Name)
	}
	return fmt.Sprintf("sqlb.Typed[%s](%q)", elem, d.Name)
}

// facadeElem is the type parameter a column's facade carries, and whether the
// pattern operators apply to it.
//
// An overridden column loses TextCol even when the schema type is text, because
// TextCol is constrained to ~string and the replacement almost never is. That
// is the honest outcome rather than a limitation: Contains on a decimal.Decimal
// would not have compiled anyway, and one that *is* a string kind still cannot
// be assumed to want ILIKE.
func facadeElem(typeName, table string, d *schema.FieldDesc, ov *overrides) (elem string, textual bool) {
	if base, replaced := ov.base(table, d); replaced {
		return base, false
	}
	if isTextual(d) {
		return base(d), true
	}
	return enumOrBase(typeName, d), false
}

// isTextual reports whether the pattern operators apply. An enum is a string in
// SQL but is compared by equality in practice, so it is excluded.
func isTextual(d *schema.FieldDesc) bool {
	return !d.Array && (d.Type == schema.TypeText || d.Type == schema.TypeVarchar)
}

// base strips the pointer a nullable column carries in the model.
func base(d *schema.FieldDesc) string {
	return d.Type.GoType()
}

func enumOrBase(typeName string, d *schema.FieldDesc) string {
	if d.Type == schema.TypeEnum && len(d.EnumValues) > 0 {
		return typeName + GoName(d.Name)
	}
	return base(d)
}

func hasHidden(t *schema.TableDef) bool {
	for _, f := range t.Fields() {
		if f.Desc().Hidden {
			return true
		}
	}
	return false
}
