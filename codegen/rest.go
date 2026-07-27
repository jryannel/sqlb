package codegen

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/jryannel/sqlb/schema"
)

// renderREST emits the request body types and the registration function for
// every table the schema exposes.
//
// The handlers themselves are not generated. rest.Resource is one generic
// function that serves any model, and the OpenAPI document it produces is
// already per-resource, because the parameters come from the model's
// capabilities rather than from a Go struct. What a generator is still needed
// for is the part generics cannot express: the *shape of a request body*, which
// differs from the row in three ways — read-only columns are absent, defaulted
// ones are optional, and a PATCH must distinguish an omitted field from one set
// to null.
//
// Returns nil when the schema exposes nothing, so a package that declares no
// REST surface does not acquire a dependency on huma.
func renderREST(opts Options) ([]byte, error) {
	var exposed []*schema.TableDef
	for _, t := range opts.Registry.Tables() {
		if t.Rest() != nil {
			exposed = append(exposed, t)
		}
	}
	if len(exposed) == 0 {
		return nil, nil
	}

	imports := map[string]bool{
		"github.com/danielgtaylor/huma/v2": true,
		"github.com/jryannel/sqlb":         true,
		"github.com/jryannel/sqlb/rest":    true,
	}
	for _, t := range exposed {
		for _, f := range bodyFields(t, forCreate) {
			if strings.Contains(f.Desc().GoType(), "time.Time") {
				imports["time"] = true
			}
		}
		if t.Rest().Ops.Has(schema.OpUpdate) && len(bodyFields(t, forUpdate)) > 0 {
			imports["encoding/json"] = true
			imports["errors"] = true
		}
	}

	b := header(opts.Package, sortedSet(imports))

	for _, t := range exposed {
		renderCreateBody(b, t)
		renderUpdateBody(b, t)
	}
	renderRegister(b, exposed)

	return gofmt(opts.restFile(), b.Bytes())
}

// bodyKind selects which columns a request body carries.
type bodyKind int

const (
	forCreate bodyKind = iota
	forUpdate
)

// bodyFields is the column set a request body may write.
//
// Read-only columns belong to the database or to a hook; hidden columns never
// cross the boundary in either direction; and an immutable column is settable
// once, at create, so it is absent from the update body rather than rejected by
// the handler.
func bodyFields(t *schema.TableDef, kind bodyKind) []*schema.Field {
	var out []*schema.Field
	for _, f := range t.Fields() {
		d := f.Desc()
		switch {
		case d.ReadOnly || d.Hidden || d.PrimaryKey:
		case kind == forUpdate && d.Immutable:
		default:
			out = append(out, f)
		}
	}
	return out
}

// optionalOnCreate reports whether a create body may omit the column: a
// nullable column is absent as NULL, and a defaulted one is absent so the
// database fills it.
func optionalOnCreate(d *schema.FieldDesc) bool {
	return d.Nullable || d.Default != nil
}

func renderCreateBody(b *bytes.Buffer, t *schema.TableDef) {
	if !t.Rest().Ops.Has(schema.OpCreate) {
		return
	}
	typeName := TypeName(t.LocalName())
	name := typeName + "Create"
	fields := bodyFields(t, forCreate)

	fmt.Fprintf(b, "\n// %s is the request body for creating a %s.\n", name, typeName)
	fmt.Fprintf(b, "//\n// Read-only columns are absent: the database or a BeforeCreate hook owns them.\n")
	fmt.Fprintf(b, "// A column with a default is optional, so leaving it out means the database\n")
	fmt.Fprintf(b, "// supplies the value rather than the zero value overwriting it.\n")
	fmt.Fprintf(b, "type %s struct {\n", name)
	for _, f := range fields {
		d := f.Desc()
		fmt.Fprintf(b, "\t%s %s `json:\"%s%s\"`", GoName(d.Name), bodyType(typeName, d, forCreate), d.Name, omitEmpty(optionalOnCreate(d)))
		if c := d.Comment; c != "" {
			fmt.Fprintf(b, " // %s", c)
		}
		fmt.Fprintln(b)
	}
	fmt.Fprintln(b, "}")

	fmt.Fprintf(b, "\n// Row builds the row to insert. It satisfies rest.CreateBody.\n")
	fmt.Fprintf(b, "func (c %s) Row() (*%s, error) {\n", name, typeName)
	fmt.Fprintf(b, "\trow := &%s{}\n", typeName)
	for _, f := range fields {
		d := f.Desc()
		field := GoName(d.Name)
		switch {
		case d.Nullable:
			// The model field is already a pointer, so absent and null are the
			// same thing and both mean NULL.
			fmt.Fprintf(b, "\trow.%s = c.%s\n", field, field)
		case optionalOnCreate(d):
			fmt.Fprintf(b, "\tif c.%s != nil {\n\t\trow.%s = *c.%s\n\t}\n", field, field, field)
		default:
			fmt.Fprintf(b, "\trow.%s = c.%s\n", field, field)
		}
	}
	fmt.Fprintln(b, "\treturn row, nil\n}")
}

func renderUpdateBody(b *bytes.Buffer, t *schema.TableDef) {
	if !t.Rest().Ops.Has(schema.OpUpdate) {
		return
	}
	typeName := TypeName(t.LocalName())
	name := typeName + "Patch"
	fields := bodyFields(t, forUpdate)
	if len(fields) == 0 {
		// Every column is read-only, hidden or immutable, so there is nothing a
		// patch could write. Registration reports this rather than emitting a
		// body type with no fields.
		return
	}

	fmt.Fprintf(b, "\n// %s is the request body for patching a %s.\n", name, typeName)
	fmt.Fprintf(b, "//\n// Every field is a pointer and every field is optional, so a request writes\n")
	fmt.Fprintf(b, "// only the columns it names. Immutable columns are absent: they are settable\n")
	fmt.Fprintf(b, "// once, at create.\n")
	fmt.Fprintf(b, "type %s struct {\n", name)
	for _, f := range fields {
		d := f.Desc()
		fmt.Fprintf(b, "\t%s %s `json:\"%s,omitempty\"`", GoName(d.Name), bodyType(typeName, d, forUpdate), d.Name)
		if c := d.Comment; c != "" {
			fmt.Fprintf(b, " // %s", c)
		}
		fmt.Fprintln(b)
	}
	// Presence cannot be read off the decoded struct: a nil pointer means both
	// "absent" and "explicitly null", and those must write different SQL.
	fmt.Fprintf(b, "\n\t// present records which properties the request body actually carried.\n")
	fmt.Fprintln(b, "\tpresent map[string]bool")
	fmt.Fprintln(b, "}")

	fmt.Fprintf(b, "\n// UnmarshalJSON decodes the body and remembers which properties were present.\n")
	fmt.Fprintf(b, "//\n// Without this a nil pointer would be ambiguous: `{}` and `{\"%s\": null}` decode\n", fields[0].Desc().Name)
	fmt.Fprintf(b, "// identically, but the first must change nothing and the second must write NULL.\n")
	fmt.Fprintf(b, "func (u *%s) UnmarshalJSON(data []byte) error {\n", name)
	fmt.Fprintf(b, "\ttype plain %s\n", name)
	fmt.Fprintf(b, "\tif err := json.Unmarshal(data, (*plain)(u)); err != nil {\n\t\treturn err\n\t}\n")
	fmt.Fprintln(b, "\tvar keys map[string]json.RawMessage")
	fmt.Fprintf(b, "\tif err := json.Unmarshal(data, &keys); err != nil {\n\t\treturn err\n\t}\n")
	fmt.Fprintln(b, "\tu.present = make(map[string]bool, len(keys))")
	fmt.Fprintln(b, "\tfor k := range keys {\n\t\tu.present[k] = true\n\t}")
	fmt.Fprintln(b, "\treturn nil\n}")

	fmt.Fprintf(b, "\n// Changes reports the columns the request named. It satisfies rest.UpdateBody.\n")
	fmt.Fprintf(b, "func (u %s) Changes() (map[string]any, error) {\n", name)
	fmt.Fprintf(b, "\tout := make(map[string]any, len(u.present))\n")
	for _, f := range fields {
		d := f.Desc()
		field := GoName(d.Name)
		fmt.Fprintf(b, "\tif u.present[%q] {\n", d.Name)
		if d.Nullable {
			// A nil pointer that was present is an explicit null.
			fmt.Fprintf(b, "\t\tout[%q] = u.%s\n", d.Name, field)
		} else {
			fmt.Fprintf(b, "\t\tif u.%s == nil {\n", field)
			fmt.Fprintf(b, "\t\t\treturn nil, errors.New(%q)\n", d.Name+" is not nullable and cannot be set to null")
			fmt.Fprintf(b, "\t\t}\n")
			fmt.Fprintf(b, "\t\tout[%q] = *u.%s\n", d.Name, field)
		}
		fmt.Fprintln(b, "\t}")
	}
	fmt.Fprintln(b, "\treturn out, nil\n}")
}

// bodyType is the Go type of a body field: a pointer wherever the field is
// optional, so that absent is distinguishable from zero.
func bodyType(typeName string, d *schema.FieldDesc, kind bodyKind) string {
	base := goType(typeName, d)
	if strings.HasPrefix(base, "*") {
		return base // nullable columns are already pointers
	}
	if kind == forUpdate || optionalOnCreate(d) {
		return "*" + base
	}
	return base
}

func omitEmpty(optional bool) string {
	if optional {
		return ",omitempty"
	}
	return ""
}

func renderRegister(b *bytes.Buffer, exposed []*schema.TableDef) {
	fmt.Fprintf(b, "\n// Register mounts every exposed resource on api.\n")
	fmt.Fprintf(b, "//\n// The handlers are rest.Resource, instantiated per model. Registration is\n")
	fmt.Fprintf(b, "// generic rather than reflective because query hooks are keyed by type: a\n")
	fmt.Fprintf(b, "// BeforeQuery hook registered on a model applies to its REST reads too, which\n")
	fmt.Fprintf(b, "// is how tenant scoping stops being something each handler must remember.\n")
	fmt.Fprintln(b, "func Register(api huma.API, db sqlb.Executor) error {")
	for _, t := range exposed {
		typeName := TypeName(t.LocalName())
		r := t.Rest()
		create, update := "rest.None["+typeName+"]", "rest.None["+typeName+"]"
		if r.Ops.Has(schema.OpCreate) {
			create = typeName + "Create"
		}
		if r.Ops.Has(schema.OpUpdate) && len(bodyFields(t, forUpdate)) > 0 {
			update = typeName + "Patch"
		}

		fmt.Fprintf(b, "\tif err := rest.Resource[%s, %s, %s](api, db, rest.Options{\n", typeName, create, update)
		fmt.Fprintf(b, "\t\tPath: %q,\n", r.Path)
		fmt.Fprintf(b, "\t\tName: %q,\n", Singular(t.LocalName()))
		fmt.Fprintf(b, "\t\tTag:  %q,\n", r.Tag)
		fmt.Fprintf(b, "\t\tOps:  %s,\n", opsExpr(r.Ops))
		if c := t.Comment(); c != "" {
			fmt.Fprintf(b, "\t\tDescription: %q,\n", c)
		}
		if r.DefaultPageSize > 0 {
			fmt.Fprintf(b, "\t\tDefaultPageSize: %d,\n", r.DefaultPageSize)
		}
		if r.MaxPageSize > 0 {
			fmt.Fprintf(b, "\t\tMaxPageSize: %d,\n", r.MaxPageSize)
		}
		if r.MaxFilters > 0 {
			fmt.Fprintf(b, "\t\tMaxFilters: %d,\n", r.MaxFilters)
		}
		// Expandable comes from the columns rather than from REST, because
		// .Expandable() is already the opt-in and a second one on the resource
		// would only be a way to disagree with the first.
		if rel := expandableRelations(t); len(rel) > 0 {
			quoted := make([]string, len(rel))
			for i, name := range rel {
				quoted[i] = fmt.Sprintf("%q", name)
			}
			fmt.Fprintf(b, "\t\tExpandable: []string{%s},\n", strings.Join(quoted, ", "))
		}
		fmt.Fprintf(b, "\t}); err != nil {\n\t\treturn err\n\t}\n")
	}
	fmt.Fprintln(b, "\treturn nil\n}")
}

// opsExpr renders an operation mask as the rest constants that make it up, so
// the generated file reads like the schema declaration it came from.
func opsExpr(ops schema.Op) string {
	var parts []string
	for _, e := range []struct {
		op   schema.Op
		name string
	}{
		{schema.OpCreate, "rest.OpCreate"}, {schema.OpRead, "rest.OpRead"},
		{schema.OpUpdate, "rest.OpUpdate"}, {schema.OpDelete, "rest.OpDelete"},
		{schema.OpList, "rest.OpList"},
	} {
		if ops.Has(e.op) {
			parts = append(parts, e.name)
		}
	}
	if len(parts) == 0 {
		return "0"
	}
	return strings.Join(parts, " | ")
}
