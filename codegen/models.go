package codegen

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jryannel/sqlb/schema"
)

// renderModels emits one struct per table, plus a named string type for each
// enum column.
//
// The struct tags are the contract with the runtime: `db` names the column and
// `sqlb` carries the capabilities the schema declared. Everything the engine
// knows about a model at runtime comes from here.
func renderModels(opts Options) ([]byte, error) {
	tables := opts.Registry.Tables()

	imports := map[string]bool{}
	for _, t := range tables {
		for _, f := range t.Fields() {
			switch f.Desc().GoType() {
			case "time.Time", "*time.Time":
				imports["time"] = true
			case "json.RawMessage":
				imports["encoding/json"] = true
			}
		}
	}

	b := header(opts.Package, sortedSet(imports))

	for _, t := range tables {
		typeName := TypeName(t.LocalName())

		// Enum types first, so the struct that uses them reads top-down.
		for _, f := range t.Fields() {
			d := f.Desc()
			if d.Type != schema.TypeEnum || len(d.EnumValues) == 0 {
				continue
			}
			enum := typeName + GoName(d.Name)
			fmt.Fprintf(b, "\n// %s is the %s.%s column's value set.\n", enum, t.Name(), d.Name)
			fmt.Fprintf(b, "type %s string\n\n", enum)
			fmt.Fprintln(b, "const (")
			for _, v := range d.EnumValues {
				fmt.Fprintf(b, "\t%s%s %s = %q\n", enum, GoName(v), enum, v)
			}
			fmt.Fprintln(b, ")")
		}

		fmt.Fprintln(b)
		if c := t.Comment(); c != "" {
			fmt.Fprintf(b, "// %s %s\n", typeName, lowerFirst(c))
		} else {
			fmt.Fprintf(b, "// %s is a row of %s.\n", typeName, t.Name())
		}
		fmt.Fprintf(b, "type %s struct {\n", typeName)
		for _, f := range t.Fields() {
			d := f.Desc()
			fmt.Fprintf(b, "\t%s %s `db:%q%s`", GoName(d.Name), goType(typeName, d), d.Name, capTag(d))
			if c := d.Comment; c != "" {
				fmt.Fprintf(b, " // %s", c)
			}
			fmt.Fprintln(b)
		}
		fmt.Fprintln(b, "}")

		// TableName is always emitted, so the mapping never depends on the
		// singulariser guessing the type name back into the table name.
		fmt.Fprintf(b, "\n// TableName is the table %s maps to.\n", typeName)
		fmt.Fprintf(b, "func (%s) TableName() string { return %q }\n", typeName, t.Name())
	}

	return gofmt(opts.modelsFile(), b.Bytes())
}

// goType is the Go type for a column, using the generated enum type where the
// schema declared one.
func goType(typeName string, d *schema.FieldDesc) string {
	if d.Type == schema.TypeEnum && len(d.EnumValues) > 0 {
		enum := typeName + GoName(d.Name)
		if d.Nullable {
			return "*" + enum
		}
		return enum
	}
	return d.GoType()
}

// capTag renders the `sqlb` struct tag, omitted entirely when a column has no
// capabilities so the common case stays readable.
func capTag(d *schema.FieldDesc) string {
	caps := d.Capabilities()
	if caps == "" {
		return ""
	}
	return fmt.Sprintf(` sqlb:%q`, caps)
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	// Only lower an ordinary capitalised word: an acronym or a proper noun
	// should stay as written.
	r := []rune(s)
	if len(r) > 1 && r[1] >= 'A' && r[1] <= 'Z' {
		return s
	}
	r[0] = []rune(strings.ToLower(string(r[0])))[0]
	return string(r)
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
