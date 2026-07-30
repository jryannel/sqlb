package codegen

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/jryannel/sqlb/schema"
)

// renderModels emits one struct per table, plus a named string type for each
// enum column.
//
// The struct tags are the contract with the runtime: `db` names the column,
// `sqlb` carries the capabilities the schema declared, and `json` names the
// property the REST layer serialises it as. Everything the engine knows about a
// model at runtime comes from here.
func renderModels(opts Options) ([]byte, error) {
	tables := opts.Registry.Tables()

	ov, err := newOverrides(opts.Types, opts.Registry)
	if err != nil {
		return nil, err
	}

	imports := map[string]bool{}
	for _, path := range ov.imports(opts.Registry) {
		imports[path] = true
	}
	for _, t := range tables {
		for _, f := range t.Fields() {
			// The default mapping decides which stdlib import a column needs;
			// an overridden column brings its own, above.
			if _, replaced := ov.base(t.Name(), f.Desc()); replaced {
				continue
			}
			switch f.Desc().GoType() {
			case "time.Time", "*time.Time", "[]time.Time":
				imports["time"] = true
			case "json.RawMessage":
				imports["encoding/json"] = true
			case "sqlb.Vector":
				// The second thing in this file that is not a plain Go type,
				// and the first that is a *column*. An embedding needs the
				// codec that moves it in binary, so the model cannot be
				// importable without sqlb the way the rest of them are.
				imports["github.com/jryannel/sqlb"] = true
			}
		}
		// An expanded collection lands in a sqlb.Collection. Models are
		// otherwise importable without sqlb — a table with neither a reverse
		// relation nor a vector column stays that way.
		for _, inv := range opts.Registry.Inverses(t) {
			if inv.Expandable {
				imports["github.com/jryannel/sqlb"] = true
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
		rels, err := relationsOf(t)
		if err != nil {
			return nil, err
		}

		fmt.Fprintf(b, "type %s struct {\n", typeName)
		for _, f := range t.Fields() {
			d := f.Desc()
			fmt.Fprintf(b, "\t%s %s `db:%q %s%s`",
				GoName(d.Name), goType(typeName, t.Name(), d, ov), d.Name, jsonTag(d), capTag(d))
			if c := d.Comment; c != "" {
				fmt.Fprintf(b, " // %s", c)
			}
			fmt.Fprintln(b)

			// The relation sits directly under the key it expands, because the
			// two are one declaration split across two fields and reading them
			// apart is how they come to disagree.
			if rel, ok := rels[d.Name]; ok {
				fmt.Fprintf(b, "\t%s *%s `db:\"-\" json:%q sqlb:%q` // filled in by ?expand=%s\n",
					rel.field, rel.target, rel.relation+",omitempty", "expands="+d.Name, rel.relation)
			}
		}

		// The reverse relations come after every column, because they belong to
		// no column of this table: they are declared on the far side, by the
		// reference that points here.
		inverses, err := inversesOf(opts.Registry, t)
		if err != nil {
			return nil, err
		}
		for _, inv := range inverses {
			fmt.Fprintf(b, "\t%s *sqlb.Collection[%s] `db:\"-\" json:%q sqlb:%q` // filled in by ?expand=%s\n",
				inv.field, inv.target, inv.relation+",omitempty", inv.tag, inv.relation)
		}
		fmt.Fprintln(b, "}")

		// TableName is always emitted, so the mapping never depends on the
		// singulariser guessing the type name back into the table name.
		fmt.Fprintf(b, "\n// TableName is the table %s maps to.\n", typeName)
		fmt.Fprintf(b, "func (%s) TableName() string { return %q }\n", typeName, t.Name())
	}

	return gofmt(opts.modelsFile(), b.Bytes())
}

// relation is the second half of an expandable reference: the typed field an
// expanded row lands in.
type relation struct {
	field    string // Go field name, e.g. "List"
	target   string // Go type of the expanded model, e.g. "List"
	relation string // name on the wire, e.g. "list"
}

// relationsOf returns the relation field to emit after each expandable
// reference column, keyed by that column's name.
//
// An expandable reference is two struct fields working together. The foreign
// key stays an ordinary column carrying the `expand` capability, and beside it
// sits a `db:"-"` field the joined row is scanned into — no projection selects
// it, no insert writes it, no update sets it. The runtime links the two through
// the `expands=` tag; sqlb's relation.go is where that is read.
//
// Only internal references qualify. An ExternalRef names a table this module
// does not own, and the schema already refuses to mark one Expandable.
//
// Note what this does *not* check: whether the target table is itself exposed
// over REST. Expanding a relation into an unexposed table is a legitimate
// design — the row is reachable inline without the table acquiring a collection
// endpoint of its own — and `.Expandable()` is the explicit opt-in that says so.
// Hidden columns of the target are stripped by the join either way.
func relationsOf(t *schema.TableDef) (map[string]relation, error) {
	// Every column already owns its Go name, and a relation that collided with
	// one would emit a struct with two identical fields — which go/format
	// accepts, because it parses without type-checking, so the break would
	// surface at the consumer's next build instead of here.
	taken := map[string]string{}
	for _, f := range t.Fields() {
		taken[GoName(f.Desc().Name)] = "column " + f.Desc().Name
	}

	out := map[string]relation{}
	for _, f := range t.Fields() {
		d := f.Desc()
		// A nil Ref.Table on an internal reference is already refused by
		// Registry.Validate, which render runs first.
		if !d.Expandable || d.Ref == nil || d.Ref.External || d.Ref.Table == nil {
			continue
		}
		name := GoName(d.Ref.Name)
		if by, dup := taken[name]; dup {
			return nil, fmt.Errorf(
				"codegen: table %s: relation %q wants the Go field %s, which %s already uses; "+
					"rename one of them, or drop .Expandable()",
				t.Name(), d.Ref.Name, name, by)
		}
		taken[name] = "relation " + d.Ref.Name

		out[d.Name] = relation{
			field:    name,
			target:   TypeName(d.Ref.Table.LocalName()),
			relation: d.Ref.Name,
		}
	}
	return out, nil
}

// inverse is the field a reverse relation contributes to the *target's*
// struct: the collection the children land in.
type inverse struct {
	field    string // Go field name, e.g. "Tasks"
	target   string // Go type of the child model, e.g. "Task"
	relation string // name on the wire, e.g. "tasks"
	tag      string // the sqlb tag, e.g. "expands=list_id,order=-created_at"
}

// inversesOf returns the collection fields to emit on t, one per reference
// elsewhere in the registry that named an inverse and exposed it.
//
// Only exposed ones produce a field. A declared-but-unexposed inverse names the
// relationship for the manifest and stops there — the field exists to be filled
// in by ?expand, so emitting one nothing can ask for would be vocabulary with
// no consumer. ADR-0022, and ADR-0006 for why the two are separate decisions.
func inversesOf(reg *schema.Registry, t *schema.TableDef) ([]inverse, error) {
	taken := map[string]string{}
	for _, f := range t.Fields() {
		taken[GoName(f.Desc().Name)] = "column " + f.Desc().Name
	}
	for _, rel := range relationsIn(t) {
		taken[rel.field] = "relation " + rel.relation
	}

	var out []inverse
	for _, inv := range reg.Inverses(t) {
		if !inv.Expandable {
			continue
		}
		name := GoName(inv.Name)
		if by, dup := taken[name]; dup {
			return nil, fmt.Errorf(
				"codegen: table %s: inverse relation %q wants the Go field %s, which %s already uses; "+
					"rename the Inverse on %s.%s",
				t.Name(), inv.Name, name, by, inv.Table.Name(), inv.Column)
		}
		taken[name] = "inverse relation " + inv.Name

		// The cap is always written out, even when it is the default. A
		// generated model that stated no limit would be relying on the
		// engine's, and the number would then live in two places with only one
		// of them readable from the file.
		tag := "expands=" + inv.Column
		if inv.Order != "" {
			tag += ",order=" + inv.Order
		}
		tag += ",limit=" + strconv.Itoa(inv.Cap())
		out = append(out, inverse{
			field:    name,
			target:   TypeName(inv.Table.LocalName()),
			relation: inv.Name,
			tag:      tag,
		})
	}
	return out, nil
}

// relationsIn is relationsOf without the collision report, for callers that
// only need the field names already spoken for.
func relationsIn(t *schema.TableDef) []relation {
	var out []relation
	for _, f := range t.Fields() {
		d := f.Desc()
		if !d.Expandable || d.Ref == nil || d.Ref.External || d.Ref.Table == nil {
			continue
		}
		out = append(out, relation{
			field:    GoName(d.Ref.Name),
			target:   TypeName(d.Ref.Table.LocalName()),
			relation: d.Ref.Name,
		})
	}
	return out
}

// expandableRelations names the relations a resource may expand, in
// declaration order and forward direction first. It drives both the generated
// rest.Options and the manifest, so the two cannot drift apart.
func expandableRelations(reg *schema.Registry, t *schema.TableDef) []string {
	var out []string
	for _, f := range t.Fields() {
		if d := f.Desc(); d.Expandable && d.Ref != nil && !d.Ref.External {
			out = append(out, d.Ref.Name)
		}
	}
	for _, inv := range reg.Inverses(t) {
		if inv.Expandable {
			out = append(out, inv.Name)
		}
	}
	return out
}

// goType is the Go type for a column, using the generated enum type where the
// schema declared one.
func goType(typeName, table string, d *schema.FieldDesc, ov *overrides) string {
	// An override replaces the base type only. Nullable and Array wrap it
	// afterwards, in the same place they always did, which is why an override
	// never has to know about either (ADR-0035).
	if base, replaced := ov.base(table, d); replaced {
		switch {
		case d.Array:
			return "[]" + base
		case d.Nullable:
			return "*" + base
		}
		return base
	}
	if d.Type == schema.TypeEnum && len(d.EnumValues) > 0 {
		enum := typeName + GoName(d.Name)
		switch {
		case d.Array:
			// A nil slice already says NULL, so an array is the plain slice
			// whether or not the column is nullable.
			return "[]" + enum
		case d.Nullable:
			return "*" + enum
		}
		return enum
	}
	return d.GoType()
}

// jsonTag renders the `json` struct tag.
//
// A hidden column gets `json:"-"`. The REST layer already excludes it from
// every projection, but a hidden column that could still be marshalled is one
// stray json.Marshal away from leaking — a debug log, a hand-written handler —
// and the tag closes that off at the type.
func jsonTag(d *schema.FieldDesc) string {
	if d.Hidden {
		return `json:"-"`
	}
	return fmt.Sprintf("json:%q", d.Name)
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
