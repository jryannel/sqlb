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

	imports := map[string]bool{"github.com/jryannel/sqlb": true}
	for _, t := range tables {
		for _, f := range t.Fields() {
			if f.Desc().Hidden {
				continue
			}
			if base(f.Desc()) == "time.Time" {
				imports["time"] = true
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
			fmt.Fprintf(b, "\t%s %s\n", GoName(f.Desc().Name), colType(typeName, f.Desc()))
		}
		fmt.Fprintln(b, "}")

		fmt.Fprintf(b, "\n// %sCols are the typed columns of %s.\n", typeName, t.Name())
		if hasHidden(t) {
			fmt.Fprintln(b, "// Hidden columns are omitted: a predicate against one should not compile.")
		}
		fmt.Fprintf(b, "var %sCols = %s{\n", typeName, setName)
		for _, f := range visible {
			d := f.Desc()
			fmt.Fprintf(b, "\t%s: %s,\n", GoName(d.Name), colCtor(typeName, d))
		}
		fmt.Fprintln(b, "}")

		renderUpdate(b, t, typeName)
	}

	return gofmt(opts.columnsFile(), b.Bytes())
}

// renderUpdate emits a typed update statement.
//
// The select builder is deliberately not wrapped — its chainable methods would
// each need their return type re-wrapped, for safety the column set already
// gives. An update is different: sqlb.Update.Set takes a string and an any, so
// neither the column name nor the value type is checked without this.
func renderUpdate(b interface{ WriteString(string) (int, error) }, t *schema.TableDef, typeName string) {
	w := func(format string, args ...any) { _, _ = b.WriteString(fmt.Sprintf(format, args...)) }

	var writable []*schema.Field
	for _, f := range t.Fields() {
		if !f.Desc().ReadOnly {
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
		typ := goType(typeName, d)
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
func colType(typeName string, d *schema.FieldDesc) string {
	if isTextual(d) {
		return fmt.Sprintf("sqlb.TextCol[%s]", base(d))
	}
	return fmt.Sprintf("sqlb.Col[%s]", enumOrBase(typeName, d))
}

func colCtor(typeName string, d *schema.FieldDesc) string {
	if isTextual(d) {
		return fmt.Sprintf("sqlb.TextColumn[%s](%q)", base(d), d.Name)
	}
	return fmt.Sprintf("sqlb.Typed[%s](%q)", enumOrBase(typeName, d), d.Name)
}

// isTextual reports whether the pattern operators apply. An enum is a string in
// SQL but is compared by equality in practice, so it is excluded.
func isTextual(d *schema.FieldDesc) bool {
	return d.Type == schema.TypeText || d.Type == schema.TypeVarchar
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
