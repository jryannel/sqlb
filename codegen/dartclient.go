package codegen

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/jryannel/sqlb/schema"
)

// This file emits the Dart client described by ADR-0031: row views, typed
// request parameters, a URL encoder for the filter grammar, transport
// functions and a cursor pager.
//
// It is the same argument ADR-0028 makes for TypeScript, in a language that
// forces three of its decisions the other way. Dart has no structural types, so
// a response cannot be narrowed by `select` at compile time; it has no implicit
// JSON deserialisation, so a row has to be decoded explicitly and snake_case
// property names would be a lint error in every consuming file; and it has no
// keyed query cache, so a key factory would be vocabulary with no consumer.
//
// One file, and it imports nothing — not even a pub package. The transport is
// injected, because Dio's interceptor chain, token refresh and what a 401 does
// are the application's and are not derivable from a schema.

// dartCoreNames are the identifiers a generated class must not take, because
// declaring one shadows it for the whole library and the generated code uses
// most of them itself. A table named "lists" wants the class name List, which
// would make every List<T> in this file mean the wrong thing.
//
// The emitted runtime's own type names are in here for the same reason.
var dartCoreNames = map[string]bool{
	// dart:core, the part a generated file is likely to collide with.
	"BigInt": true, "Comparable": true, "DateTime": true, "Duration": true,
	"Enum": true, "Error": true, "Exception": true, "Function": true,
	"Future": true, "Invocation": true, "Iterable": true, "Iterator": true,
	"List": true, "Map": true, "MapEntry": true, "Match": true, "Never": true,
	"Null": true, "Object": true, "Pattern": true, "Record": true,
	"RegExp": true, "Set": true, "Sink": true, "StackTrace": true,
	"Stream": true, "String": true, "StringBuffer": true, "Symbol": true,
	"Type": true, "Uri": true,

	// The runtime this file emits.
	"ApiRequest": true, "ArrayCond": true, "Collection": true, "Cond": true,
	"CursorPager":   true,
	"MissingColumn": true, "NullableArrayCond": true, "NullableCond": true,
	"NullableTextCond": true,
	"Page":             true, "Problem": true, "ProblemDetail": true, "Row": true,
	"SortTerm": true, "TableName": true, "TextCond": true, "Transport": true,
	"UnknownEnumValue": true, "WireValue": true,
}

// dartReservedMembers are the names already spoken for on a generated row,
// request body or enum. A column that wants one is escaped rather than allowed
// to shadow it, since a duplicate member is a compile error the consumer would
// find rather than this generator.
var dartReservedMembers = map[string]bool{
	"toJson": true, "toString": true, "hashCode": true, "runtimeType": true,
	"noSuchMethod": true, "has": true, "table": true, "index": true,
	"values": true, "wire": true, "byWire": true, "isEmpty": true,
	"isNotEmpty": true, "toQuery": true, "withCursor": true, "copyWith": true,
	// The two accessors a sort enum's members sit beside.
	"asc": true, "desc": true,
}

// dartKeywords are the words Dart reserves outright. A few are contextual and
// legal as identifiers, but escaping all of them keeps the rule one sentence
// long.
var dartKeywords = map[string]bool{
	"abstract": true, "as": true, "assert": true, "async": true, "await": true,
	"base": true, "break": true, "case": true, "catch": true, "class": true,
	"const": true, "continue": true, "covariant": true, "default": true,
	"deferred": true, "do": true, "dynamic": true, "else": true, "enum": true,
	"export": true, "extends": true, "extension": true, "external": true,
	"factory": true, "false": true, "final": true, "finally": true, "for": true,
	"function": true, "get": true, "hide": true, "if": true, "implements": true,
	"import": true, "in": true, "interface": true, "is": true, "late": true,
	"library": true, "mixin": true, "new": true, "null": true, "of": true,
	"on": true, "operator": true, "part": true, "required": true,
	"rethrow": true, "return": true, "sealed": true, "set": true, "show": true,
	"static": true, "super": true, "switch": true, "sync": true, "this": true,
	"throw": true, "true": true, "try": true, "typedef": true, "var": true,
	"void": true, "when": true, "while": true, "with": true, "yield": true,
}

// renderDartClient emits the whole client: runtime, row views, request
// vocabulary, transport functions and pagers.
func renderDartClient(opts Options) ([]byte, error) {
	resources, err := dartResources(opts.Registry)
	if err != nil {
		return nil, err
	}

	var b bytes.Buffer
	b.WriteString(dartHeader)
	b.WriteString(dartRuntime)

	// Row views for every table, not only the exposed ones: an expansion can
	// name a table that has no endpoint of its own, and the row still has to
	// have a type. This is the same call `.Expandable()` already makes on the
	// server.
	for _, t := range opts.Registry.Tables() {
		if err := dartRowSection(&b, opts.Registry, t); err != nil {
			return nil, err
		}
	}

	for _, r := range resources {
		dartResourceSection(&b, r)
	}

	dartTableEnum(&b, opts.Registry)
	return b.Bytes(), nil
}

// dartResource is everything about one exposed table the emitter needs,
// resolved once so the emitters below read as output rather than as lookups.
type dartResource struct {
	table  *schema.TableDef
	base   string // Task — the prefix every vocabulary type takes
	row    string // Task, or ListRow where the base collides with dart:core
	ident  string // task
	plural string // Tasks
	path   string
	ops    schema.Op

	filterable []*schema.FieldDesc
	sortable   []*schema.FieldDesc
	selectable []*schema.FieldDesc
	searchable bool
	relations  []dartRelation
	hasPK      bool
}

// dartRelation is one entry of a resource's ?expand vocabulary, in the
// direction it is served.
type dartRelation struct {
	name    string // wire name, e.g. "list"
	member  string // Dart getter, e.g. "list"
	target  string // Dart type of the expanded rows
	forward bool   // a reference on this table, rather than one pointing at it
}

func (r dartResource) hasExpand() bool { return len(r.relations) > 0 }

func dartResources(reg *schema.Registry) ([]dartResource, error) {
	var out []dartResource
	for _, t := range reg.Tables() {
		rest := t.Rest()
		if rest == nil {
			continue
		}
		base := dartTypeBase(t)
		r := dartResource{
			table:  t,
			base:   base,
			row:    dartRowType(t),
			ident:  dartLowerFirst(base),
			plural: dartPascal(t.LocalName()),
			path:   rest.Path,
			ops:    rest.Ops,
		}
		for _, f := range t.Fields() {
			d := f.Desc()
			if d.Hidden {
				continue
			}
			if d.PrimaryKey {
				r.hasPK = true
			}
			r.selectable = append(r.selectable, d)
			if d.Filterable {
				r.filterable = append(r.filterable, d)
			}
			if d.Sortable {
				r.sortable = append(r.sortable, d)
			}
			if d.Searchable {
				r.searchable = true
			}
		}
		// The expandable set comes from the columns, exactly as the generated
		// rest.Options does, so the client cannot offer a relation the server
		// would reject or miss one it would serve.
		for _, name := range expandableRelations(reg, t) {
			rel, err := dartRelationOf(reg, t, name)
			if err != nil {
				return nil, err
			}
			r.relations = append(r.relations, rel)
		}
		out = append(out, r)
	}
	return out, nil
}

func dartRelationOf(reg *schema.Registry, t *schema.TableDef, name string) (dartRelation, error) {
	for _, f := range t.Fields() {
		d := f.Desc()
		if d.Expandable && d.Ref != nil && !d.Ref.External && d.Ref.Name == name {
			if d.Ref.Table == nil {
				return dartRelation{}, fmt.Errorf("codegen: table %s: relation %q has no target table", t.Name(), name)
			}
			return dartRelation{
				name:    name,
				member:  dartMember(name),
				target:  dartRowType(d.Ref.Table),
				forward: true,
			}, nil
		}
	}
	for _, inv := range reg.Inverses(t) {
		if inv.Expandable && inv.Name == name {
			return dartRelation{
				name:   name,
				member: dartMember(name),
				target: dartRowType(inv.Table),
			}, nil
		}
	}
	return dartRelation{}, fmt.Errorf("codegen: table %s: no relation named %q", t.Name(), name)
}

// dartRowSection emits the enums, the row view and the request bodies for one
// table.
func dartRowSection(b *bytes.Buffer, reg *schema.Registry, t *schema.TableDef) error {
	base, row := dartTypeBase(t), dartRowType(t)
	fmt.Fprintf(b, "\n// %s\n", dartRule(t.Name()))

	for _, f := range t.Fields() {
		d := f.Desc()
		if d.Type != schema.TypeEnum || len(d.EnumValues) == 0 || d.Hidden {
			continue
		}
		if err := dartEnum(b, base, t, d); err != nil {
			return err
		}
	}

	members, err := dartRowMembers(reg, t)
	if err != nil {
		return err
	}

	fmt.Fprintln(b)
	if c := t.Comment(); c != "" {
		dartDoc(b, "", c)
	} else {
		dartDoc(b, "", "A row of "+t.Name()+".")
	}
	dartDoc(b, "", "")
	dartDoc(b, "", "Reading a column the request did not return throws MissingColumn rather")
	dartDoc(b, "", "than yielding null, so a projection that dropped a column is reported")
	dartDoc(b, "", "where it is used instead of somewhere further out.")
	fmt.Fprintf(b, "class %s extends Row {\n", row)
	dartDoc(b, "  ", "Wraps one decoded response object. Columns are read on access.")
	fmt.Fprintf(b, "  %s.fromJson(super.json) : super.fromJson();\n", row)

	dartDoc(b, "\n  ", "The table this row came from.")
	fmt.Fprintf(b, "  static const String table = %s;\n", dartString(t.Name()))

	for _, m := range members {
		fmt.Fprintln(b)
		if m.doc != "" {
			dartDoc(b, "  ", m.doc)
		}
		fmt.Fprintf(b, "  %s\n", m.getter)
	}

	// Only when the table is exposed: the column vocabulary is emitted per
	// resource, so a row of an unexposed table has no enum to take. Such a table
	// still gets a row view — an expansion can name a table that has no endpoint
	// of its own — but nothing can project it, so nothing needs to ask.
	if t.Rest() != nil && len(dartSelectable(t)) > 0 {
		fmt.Fprintln(b)
		dartDoc(b, "  ", "Whether the request that produced this row returned [column].")
		fmt.Fprintf(b, "  bool has(%sColumn column) => _present(column.wire);\n", base)
	}
	fmt.Fprintln(b, "}")

	dartBodyTypes(b, t, base)
	dartActionBodies(b, t, base)
	return nil
}

// dartRowMember is one emitted getter, with the doc comment that precedes it.
type dartRowMember struct {
	doc    string
	getter string
}

// dartRowMembers resolves every getter a row view carries — one per visible
// column, plus one per expandable relation in either direction — and refuses a
// schema whose names collide, since two members of the same name is a compile
// error the consumer would find rather than this generator.
func dartRowMembers(reg *schema.Registry, t *schema.TableDef) ([]dartRowMember, error) {
	base := dartTypeBase(t)
	taken := map[string]string{}
	claim := func(name, by string) error {
		if prev, dup := taken[name]; dup {
			return fmt.Errorf(
				"codegen: table %s: %s wants the Dart member %s, which %s already uses; "+
					"rename one of them",
				t.Name(), by, name, prev)
		}
		taken[name] = by
		return nil
	}

	forward := map[string]dartRelation{}
	for _, f := range t.Fields() {
		d := f.Desc()
		if !d.Expandable || d.Ref == nil || d.Ref.External || d.Ref.Table == nil {
			continue
		}
		forward[d.Name] = dartRelation{
			name:    d.Ref.Name,
			member:  dartMember(d.Ref.Name),
			target:  dartRowType(d.Ref.Table),
			forward: true,
		}
	}

	var out []dartRowMember
	for _, f := range t.Fields() {
		d := f.Desc()
		if d.Hidden {
			// Absent from the row view entirely, as it is from the response and
			// from the typed column facade. A hidden column has no spelling a
			// client could use.
			continue
		}
		name := dartMember(d.Name)
		if err := claim(name, "column "+d.Name); err != nil {
			return nil, err
		}
		out = append(out, dartRowMember{
			doc:    dartColumnDoc(d, fmt.Sprintf("The %s.%s column.", t.Name(), d.Name)),
			getter: dartGetter(base, name, d),
		})

		// The relation sits directly under the key it expands, because the two
		// are one declaration split across two members and reading them apart
		// is how they come to disagree.
		if rel, ok := forward[d.Name]; ok {
			if err := claim(rel.member, "relation "+rel.name); err != nil {
				return nil, err
			}
			out = append(out, dartRowMember{
				doc:    fmt.Sprintf("Filled in by expand: [%sExpand.%s], null otherwise.", base, rel.member),
				getter: dartForwardGetter(rel),
			})
		}
	}

	for _, inv := range reg.Inverses(t) {
		if !inv.Expandable {
			continue
		}
		member := dartMember(inv.Name)
		if err := claim(member, "inverse relation "+inv.Name); err != nil {
			return nil, err
		}
		out = append(out, dartRowMember{
			doc: fmt.Sprintf("Filled in by expand: [%sExpand.%s], null otherwise. Capped at %d rows;\nCollection.hasMore reports truncation.",
				base, member, inv.Cap()),
			getter: dartInverseGetter(inv.Name, member, dartRowType(inv.Table)),
		})
	}
	return out, nil
}

// dartColumnDoc is the doc comment above anything derived from a column:
// whatever the schema wrote, or lead when it wrote nothing, plus the caveats a
// Dart reader cannot see from the type.
//
// The caveats are all cases where the Dart type is honest and incomplete — an
// int that is 64-bit on a phone and 53-bit in a web build, a String that is a
// date — so they belong where the value is read rather than in a document
// nobody has open.
func dartColumnDoc(d *schema.FieldDesc, lead string) string {
	var parts []string
	switch {
	case d.Comment != "":
		parts = append(parts, d.Comment)
	case lead != "":
		parts = append(parts, lead)
	}
	switch d.Type {
	case schema.TypeBigInt:
		// Stated where the type is read, because the loss is silent and only on
		// one platform: a Dart int is 64-bit on a device and a double on the
		// web, so the same code loses precision in a Flutter web build and not
		// in the app that was tested.
		parts = append(parts, "bigint. Compiled to JavaScript this is a double, so values above 2^53 lose precision.")
	case schema.TypeDate:
		// The row struct types a date column as time.Time, so what arrives is a
		// timestamp at midnight rather than YYYY-MM-DD. Said here because the
		// Dart type cannot: a DateTime that names a day and one that names an
		// instant look identical, and only one of them survives a time zone.
		parts = append(parts, "A date. It arrives as a timestamp at midnight UTC, because the row carries it as one — so compare the calendar fields rather than the instant.")
	case schema.TypeTime:
		parts = append(parts, "A time of day, carried as a timestamp whose date part is unset.")
	case schema.TypeBytes:
		parts = append(parts, "Base64, as JSON carries bytes.")
	}
	return strings.Join(parts, "\n")
}

// dartGetter is the whole getter declaration for one column.
//
// The readers are inherited from Row rather than passed the map and the type
// name, which is what keeps a getter to one line the formatter will not break.
func dartGetter(base, member string, d *schema.FieldDesc) string {
	col := dartString(d.Name)
	isEnum := d.Type == schema.TypeEnum && len(d.EnumValues) > 0

	// An array column is a JSON array on the wire, and a NULL one is null —
	// two different absences, which is why the nullable form returns null
	// rather than an empty list.
	if d.Array {
		if isEnum {
			enum := base + dartPascal(d.Name)
			if d.Nullable {
				return dartLine(fmt.Sprintf("List<%s>? get %s => _enumListOrNull(%s, %s.byWire);", enum, member, col, enum))
			}
			return dartLine(fmt.Sprintf("List<%s> get %s => _enumList(%s, %s.byWire);", enum, member, col, enum))
		}
		decode, typ := dartElemReader(d.Type)
		if d.Nullable {
			return dartLine(fmt.Sprintf("List<%s>? get %s => _listOrNull(%s, %s);", typ, member, col, decode))
		}
		return dartLine(fmt.Sprintf("List<%s> get %s => _list(%s, %s);", typ, member, col, decode))
	}

	if isEnum {
		enum := base + dartPascal(d.Name)
		if d.Nullable {
			return dartLine(fmt.Sprintf("%s? get %s => _enumOrNull(%s, %s.byWire);", enum, member, col, enum))
		}
		return dartLine(fmt.Sprintf("%s get %s => _enum(%s, %s.byWire);", enum, member, col, enum))
	}

	fn, typ := dartReader(d.Type)
	if d.Nullable {
		return dartLine(fmt.Sprintf("%s? get %s => %sOrNull(%s);", typ, member, fn, col))
	}
	return dartLine(fmt.Sprintf("%s get %s => %s(%s);", typ, member, fn, col))
}

// dartReader maps a column type onto the Row reader that decodes it and the
// Dart type that reader returns. The nullable form is the same name with an
// OrNull suffix.
func dartReader(t schema.Type) (fn, typ string) {
	switch t {
	case schema.TypeInt, schema.TypeBigInt:
		return "_int", "int"
	case schema.TypeFloat, schema.TypeNumeric:
		return "_double", "double"
	case schema.TypeBool:
		return "_bool", "bool"
	case schema.TypeTimestamp, schema.TypeDate, schema.TypeTime:
		// All three, because all three are a time.Time in the row struct and
		// therefore RFC 3339 on the wire. A date column does not arrive as
		// YYYY-MM-DD, whatever the column type suggests — dartColumnDoc says so
		// where it is read.
		return "_time", "DateTime"
	case schema.TypeJSON:
		return "_any", "Object"
	default:
		// Text, varchar, uuid and bytea all arrive as strings.
		return "_str", "String"
	}
}

// dartElemReader maps a column type onto the top-level decoder that reads one
// element of an array of it, and the Dart type that decoder returns.
//
// It is separate from dartReader because the two take different things: a
// column reader is handed a column name and looks it up, an element decoder is
// handed the value already pulled out of the JSON list.
func dartElemReader(t schema.Type) (decode, typ string) {
	switch t {
	case schema.TypeInt, schema.TypeBigInt:
		return "_asInt", "int"
	case schema.TypeFloat, schema.TypeNumeric:
		return "_asDouble", "double"
	case schema.TypeBool:
		return "_asBool", "bool"
	case schema.TypeTimestamp, schema.TypeDate, schema.TypeTime:
		return "_asTime", "DateTime"
	default:
		return "_asStr", "String"
	}
}

func dartForwardGetter(rel dartRelation) string {
	return dartLine(fmt.Sprintf("%s? get %s => _one(%s, %s.fromJson);",
		rel.target, rel.member, dartString(rel.name), rel.target))
}

func dartInverseGetter(name, member, target string) string {
	return dartLine(fmt.Sprintf("Collection<%s>? get %s => _many(%s, %s.fromJson);",
		target, member, dartString(name), target))
}

// dartNamedCtor writes a constructor whose parameters are all named and
// optional, collapsing it onto one line when it fits — which is the decision
// dart format makes, and reproducing it here is what keeps the emitted file a
// no-op for the formatter.
//
// params are written as they appear inside the braces, e.g. "this.title" or
// "required this.id".
func dartNamedCtor(b *bytes.Buffer, name string, params []string) {
	if len(params) == 0 {
		fmt.Fprintf(b, "  const %s();\n", name)
		return
	}
	line := fmt.Sprintf("  const %s({%s});", name, strings.Join(params, ", "))
	if len(line) <= dartWidth {
		fmt.Fprintln(b, line)
		return
	}
	fmt.Fprintf(b, "  const %s({\n", name)
	for _, p := range params {
		fmt.Fprintf(b, "    %s,\n", p)
	}
	fmt.Fprintln(b, "  });")
}

// dartMapBody writes an arrow-bodied method returning a map literal, on one
// line when it fits and exploded when it does not. Same reason as
// dartNamedCtor.
func dartMapBody(b *bytes.Buffer, signature string, entries []string) {
	line := fmt.Sprintf("  %s => {%s};", signature, strings.Join(entries, ", "))
	if len(entries) > 0 && len(line) <= dartWidth {
		fmt.Fprintln(b, line)
		return
	}
	if len(entries) == 0 {
		fmt.Fprintf(b, "  %s => {};\n", signature)
		return
	}
	fmt.Fprintf(b, "  %s => {\n", signature)
	for _, e := range entries {
		fmt.Fprintf(b, "    %s,\n", e)
	}
	fmt.Fprintln(b, "  };")
}

// dartEnumBody writes the members of an enum: blank-line separated, because
// each carries a doc comment, and the last terminated with a semicolon rather
// than a comma followed by a bare one. Both are what dart format leaves behind.
func dartEnumBody(b *bytes.Buffer, docs, names, wires []string) {
	for i := range names {
		if i > 0 {
			fmt.Fprintln(b)
		}
		dartDoc(b, "  ", docs[i])
		end := ","
		if i == len(names)-1 {
			end = ";"
		}
		fmt.Fprintf(b, "  %s(%s)%s\n", names[i], dartString(wires[i]), end)
	}
}

// dartEnum emits the value set of one enum column.
func dartEnum(b *bytes.Buffer, base string, t *schema.TableDef, d *schema.FieldDesc) error {
	name := base + dartPascal(d.Name)
	taken := map[string]bool{}
	members := make([]string, len(d.EnumValues))
	for i, v := range d.EnumValues {
		m := dartMember(v)
		if taken[m] {
			return fmt.Errorf(
				"codegen: table %s: column %s has two values that both spell the Dart name %s; rename one",
				t.Name(), d.Name, m)
		}
		taken[m] = true
		members[i] = m
	}

	fmt.Fprintln(b)
	dartDoc(b, "", fmt.Sprintf("The %s.%s column's value set.", t.Name(), d.Name))
	fmt.Fprintf(b, "enum %s implements WireValue {\n", name)
	docs := make([]string, len(d.EnumValues))
	for i, v := range d.EnumValues {
		docs[i] = fmt.Sprintf("The value %s.", v)
	}
	dartEnumBody(b, docs, members, d.EnumValues)
	fmt.Fprintf(b, "\n  const %s(this.wire);\n", name)
	fmt.Fprintln(b, "\n  @override\n  final String wire;")
	fmt.Fprintln(b)
	dartDoc(b, "  ", "The member [wire] names, or null when the server sent a value this\nclient does not have — a value set that grew since it was generated.")
	fmt.Fprintf(b, "  static %s? byWire(String wire) {\n", name)
	fmt.Fprintln(b, "    for (final value in values) {")
	fmt.Fprintln(b, "      if (value.wire == wire) return value;")
	fmt.Fprintln(b, "    }")
	fmt.Fprintln(b, "    return null;")
	fmt.Fprintln(b, "  }")
	fmt.Fprintln(b, "}")
	return nil
}

// dartBodyTypes emits the create and patch bodies, over the same column sets
// the Go bodies use, so the two cannot disagree about what a request may write.
func dartBodyTypes(b *bytes.Buffer, t *schema.TableDef, base string) {
	rest := t.Rest()
	if rest == nil {
		return
	}

	if rest.Ops.Has(schema.OpCreate) {
		fields := bodyFields(t, forCreate)
		fmt.Fprintln(b)
		dartDoc(b, "", fmt.Sprintf("The request body for creating a %s.", base))
		dartDoc(b, "", "")
		dartDoc(b, "", "Read-only columns are absent: the database or a BeforeCreate hook owns\nthem. A column with a default is optional, and leaving one out means the\ndatabase supplies the value.")
		fmt.Fprintf(b, "class %sCreate {\n", base)
		dartDoc(b, "  ", "Builds a request body. A property with no default here is one the\ndatabase has none for.")
		var params []string
		for _, f := range fields {
			d := f.Desc()
			required := ""
			if !optionalOnCreate(d) {
				required = "required "
			}
			params = append(params, required+"this."+dartMember(d.Name))
		}
		dartNamedCtor(b, base+"Create", params)
		for _, f := range fields {
			d := f.Desc()
			fmt.Fprintln(b)
			dartDoc(b, "  ", dartColumnDoc(d, fmt.Sprintf("The %s.%s column.", t.Name(), d.Name)))
			fmt.Fprintf(b, "  final %s %s;\n", dartBodyType(base, d, true), dartMember(d.Name))
		}
		fmt.Fprintln(b)
		dartDoc(b, "  ", "The JSON body. Absent properties are the ones left unset.")
		var entries []string
		for _, f := range fields {
			d := f.Desc()
			member := dartMember(d.Name)
			entry := fmt.Sprintf("%s: _wire(%s)", dartString(d.Name), member)
			if optionalOnCreate(d) {
				entry = fmt.Sprintf("if (%s != null) %s", member, entry)
			}
			entries = append(entries, entry)
		}
		dartMapBody(b, "Map<String, dynamic> toJson()", entries)
		fmt.Fprintln(b, "}")
	}

	if rest.Ops.Has(schema.OpUpdate) {
		fields := bodyFields(t, forUpdate)
		if len(fields) == 0 {
			return
		}
		fmt.Fprintln(b)
		dartDoc(b, "", fmt.Sprintf("The request body for patching a %s.", base))
		dartDoc(b, "", "")
		dartDoc(b, "", "One method per writable column, so a patch carries exactly the columns it\nnamed:")
		dartDoc(b, "", "")
		dartDoc(b, "", "```dart")
		dartDoc(b, "", fmt.Sprintf("final patch = %sPatch()..%s(value);", base, dartMember(fields[0].Desc().Name)))
		dartDoc(b, "", "```")
		dartDoc(b, "", "")
		dartDoc(b, "", "A method taking a nullable value writes NULL when passed null; a column\nleft unmentioned is not written at all. That distinction cannot be read off\na field, which is why these are methods and not a constructor.")
		fmt.Fprintf(b, "class %sPatch {\n", base)
		fmt.Fprintln(b, "  final Map<String, dynamic> _changes = {};")
		for _, f := range fields {
			d := f.Desc()
			fmt.Fprintln(b)
			doc := fmt.Sprintf("Writes %s.%s.", t.Name(), d.Name)
			if d.Nullable {
				doc += " Passing null writes NULL."
			}
			if c := dartColumnDoc(d, ""); c != "" {
				doc = c + "\n\n" + doc
			}
			dartDoc(b, "  ", doc)
			fmt.Fprintf(b, "  void %s(%s value) => _changes[%s] = _wire(value);\n",
				dartMember(d.Name), dartBodyType(base, d, false), dartString(d.Name))
		}
		fmt.Fprintln(b)
		dartDoc(b, "  ", "Whether the patch would write nothing, which the server answers 400 to.")
		fmt.Fprintln(b, "  bool get isEmpty => _changes.isEmpty;")
		fmt.Fprintln(b)
		dartDoc(b, "  ", "The JSON body: the columns this patch named, and no others.")
		fmt.Fprintln(b, "  Map<String, dynamic> toJson() => Map<String, dynamic>.of(_changes);")
		fmt.Fprintln(b, "}")
	}
}

// dartBodyType is the Dart type of a request-body property. On a create body an
// optional column is nullable, since absent and null mean the same thing there;
// on a patch the nullability is the column's own, because a patch method that
// accepts null is exactly the one that may write NULL.
func dartBodyType(base string, d *schema.FieldDesc, create bool) string {
	typ := dartValueType(base, d)
	if d.Nullable || (create && optionalOnCreate(d)) {
		return typ + "?"
	}
	return typ
}

// dartValueType is the non-null Dart type of a column's value, as a filter
// condition or a request body carries it.
func dartValueType(base string, d *schema.FieldDesc) string {
	if d.Array {
		return "List<" + dartElemType(base, d) + ">"
	}
	return dartElemType(base, d)
}

// dartElemType is the type of a single value of the column's declared type,
// ignoring the array flag — which is what an array's containment operators take
// one of.
func dartElemType(base string, d *schema.FieldDesc) string {
	if d.Type == schema.TypeEnum && len(d.EnumValues) > 0 {
		return base + dartPascal(d.Name)
	}
	_, typ := dartReader(d.Type)
	return typ
}

// dartResourceSection emits the query vocabulary, the transport functions and
// the pager for one exposed resource.
func dartResourceSection(b *bytes.Buffer, r dartResource) {
	fmt.Fprintf(b, "\n// %s\n", dartRule(r.path))

	dartWireEnum(b, r.base+"Column", r.table.Name(),
		"Columns [select] may name. The primary key is always returned.", r.selectable)

	if len(r.sortable) > 0 {
		fmt.Fprintln(b)
		dartDoc(b, "", "Sortable columns. Take [asc] or [desc] to get a term [sort] accepts.")
		fmt.Fprintf(b, "enum %sSort implements WireValue {\n", r.base)
		docs, names, wires := dartColumnMembers(r.table.Name(), "Order by %s.%s.", r.sortable)
		dartEnumBody(b, docs, names, wires)
		fmt.Fprintf(b, "\n  const %sSort(this.wire);\n", r.base)
		fmt.Fprintln(b, "\n  @override\n  final String wire;")
		fmt.Fprintln(b)
		dartDoc(b, "  ", "Ascending.")
		fmt.Fprintln(b, "  SortTerm get asc => SortTerm(wire);")
		fmt.Fprintln(b)
		dartDoc(b, "  ", "Descending.")
		fmt.Fprintln(b, "  SortTerm get desc => SortTerm('-$wire');")
		fmt.Fprintln(b, "}")
	}

	if r.hasExpand() {
		fmt.Fprintln(b)
		dartDoc(b, "", "Relations [expand] may name.")
		fmt.Fprintf(b, "enum %sExpand implements WireValue {\n", r.base)
		var docs, names, wires []string
		for _, rel := range r.relations {
			kind := "The %s relation, one row."
			if !rel.forward {
				kind = "The %s relation, a capped collection."
			}
			docs = append(docs, fmt.Sprintf(kind, rel.name))
			names = append(names, rel.member)
			wires = append(wires, rel.name)
		}
		dartEnumBody(b, docs, names, wires)
		fmt.Fprintf(b, "\n  const %sExpand(this.wire);\n", r.base)
		fmt.Fprintln(b, "\n  @override\n  final String wire;")
		fmt.Fprintln(b, "}")
	}

	dartWhere(b, r)
	dartParams(b, r)
	dartTransport(b, r)
	dartPager(b, r)
}

// dartWireEnum emits a plain vocabulary enum: members with a wire spelling and
// nothing else.
func dartWireEnum(b *bytes.Buffer, name, table, doc string, columns []*schema.FieldDesc) {
	if len(columns) == 0 {
		return
	}
	fmt.Fprintln(b)
	dartDoc(b, "", doc)
	fmt.Fprintf(b, "enum %s implements WireValue {\n", name)
	docs, names, wires := dartColumnMembers(table, "The %s.%s column.", columns)
	dartEnumBody(b, docs, names, wires)
	fmt.Fprintf(b, "\n  const %s(this.wire);\n", name)
	fmt.Fprintln(b, "\n  @override\n  final String wire;")
	fmt.Fprintln(b, "}")
}

// dartColumnMembers is the doc, member name and wire spelling of one enum
// member per column, for the vocabulary enums built straight off a column set.
func dartColumnMembers(table, format string, columns []*schema.FieldDesc) (docs, names, wires []string) {
	for _, d := range columns {
		docs = append(docs, fmt.Sprintf(format, table, d.Name))
		names = append(names, dartMember(d.Name))
		wires = append(wires, d.Name)
	}
	return docs, names, wires
}

func dartWhere(b *bytes.Buffer, r dartResource) {
	fmt.Fprintln(b)
	dartDoc(b, "", "Filter conditions, one property per filterable column.")
	dartDoc(b, "", "")
	dartDoc(b, "", "The condition type is narrowed by the column: pattern operators exist only\non text, null tests only on a nullable column, and the value type is the\ncolumn's own — so a filter the server would answer 400 to does not compile.")
	fmt.Fprintf(b, "class %sWhere {\n", r.base)
	dartDoc(b, "  ", "Builds a filter. Every column is optional; naming two conjoins them.")
	params := make([]string, 0, len(r.filterable))
	for _, d := range r.filterable {
		params = append(params, "this."+dartMember(d.Name))
	}
	dartNamedCtor(b, r.base+"Where", params)
	for _, d := range r.filterable {
		fmt.Fprintln(b)
		doc := fmt.Sprintf("Conditions on %s.%s.", r.table.Name(), d.Name)
		if c := dartColumnDoc(d, ""); c != "" {
			doc = c + "\n\n" + doc
		}
		dartDoc(b, "  ", doc)
		fmt.Fprintf(b, "  final %s? %s;\n", dartCondType(r.base, d), dartMember(d.Name))
	}
	fmt.Fprintln(b)
	fmt.Fprintln(b, "  void _encode(_Query out) {")
	for _, d := range r.filterable {
		fmt.Fprintf(b, "    %s?._encode(out, %s);\n", dartMember(d.Name), dartString(d.Name))
	}
	fmt.Fprintln(b, "  }")
	fmt.Fprintln(b, "}")
}

// dartCondType is the filter condition a column accepts: the operator set
// narrowed by type, which is the part an OpenAPI document cannot say.
func dartCondType(base string, d *schema.FieldDesc) string {
	// Pattern operators need a text column: the server refuses them on anything
	// else, and an enum is a string in SQL but compared by equality in
	// practice, so it is excluded here as it is in the typed facade.
	// An array column takes containment and whole-array equality, and none of
	// the ordering or pattern operators — the same set the server accepts.
	if d.Array {
		if d.Nullable {
			return "NullableArrayCond<" + dartElemType(base, d) + ">"
		}
		return "ArrayCond<" + dartElemType(base, d) + ">"
	}
	text := d.Type == schema.TypeText || d.Type == schema.TypeVarchar
	switch {
	case text && d.Nullable:
		return "NullableTextCond"
	case text:
		return "TextCond"
	case d.Nullable:
		return "NullableCond<" + dartValueType(base, d) + ">"
	default:
		return "Cond<" + dartValueType(base, d) + ">"
	}
}

func dartParams(b *bytes.Buffer, r dartResource) {
	if r.ops.Has(schema.OpList) {
		fields := dartListFields(r)

		fmt.Fprintln(b)
		dartDoc(b, "", fmt.Sprintf("Parameters for GET %s.", r.path))
		fmt.Fprintf(b, "class %sListParams {\n", r.base)
		dartDoc(b, "  ", "Builds a request. Every parameter is optional; the defaults are the\nserver's.")
		params := make([]string, 0, len(fields))
		for _, f := range fields {
			if f.def != "" {
				params = append(params, "this."+f.name+" = "+f.def)
				continue
			}
			params = append(params, "this."+f.name)
		}
		dartNamedCtor(b, r.base+"ListParams", params)
		for _, f := range fields {
			fmt.Fprintln(b)
			if f.doc != "" {
				dartDoc(b, "  ", f.doc)
			}
			fmt.Fprintf(b, "  final %s %s;\n", f.typ, f.name)
		}

		fmt.Fprintln(b)
		dartDoc(b, "  ", "The same parameters, resuming after [cursor].")
		dartDoc(b, "  ", "")
		dartDoc(b, "  ", "[page] is dropped: a page number and a cursor are two answers to where a\npage starts, and the server accepts only one of them.")
		fmt.Fprintf(b, "  %sListParams withCursor(String? cursor) => %sListParams(\n", r.base, r.base)
		for _, f := range fields {
			switch f.name {
			case "cursor":
				fmt.Fprintln(b, "    cursor: cursor,")
			case "page":
			default:
				fmt.Fprintf(b, "    %s: %s,\n", f.name, f.name)
			}
		}
		fmt.Fprintln(b, "  );")

		fmt.Fprintln(b)
		dartDoc(b, "  ", "Encodes these parameters into the server's query grammar.")
		fmt.Fprintln(b, "  String toQuery() {")
		fmt.Fprintln(b, "    final out = _Query();")
		fmt.Fprintln(b, "    where?._encode(out);")
		if r.searchable {
			fmt.Fprintln(b, "    if (search != null) out.set('search', search!);")
		}
		if len(r.sortable) > 0 {
			fmt.Fprintln(b, "    if (sort != null && sort!.isNotEmpty) {")
			fmt.Fprintln(b, "      out.set('sort', sort!.map((term) => term.wire).join(','));")
			fmt.Fprintln(b, "    }")
		}
		fmt.Fprintln(b, "    if (select != null && select!.isNotEmpty) {")
		fmt.Fprintln(b, "      out.set('select', select!.map((column) => column.wire).join(','));")
		fmt.Fprintln(b, "    }")
		if r.hasExpand() {
			fmt.Fprintln(b, "    if (expand != null && expand!.isNotEmpty) {")
			fmt.Fprintln(b, "      out.set('expand', expand!.map((relation) => relation.wire).join(','));")
			fmt.Fprintln(b, "    }")
		}
		fmt.Fprintln(b, "    if (page != null) out.set('page', '$page');")
		fmt.Fprintln(b, "    if (perPage != null) out.set('per_page', '$perPage');")
		if r.hasPK {
			fmt.Fprintln(b, "    if (cursor != null) out.set('cursor', cursor!);")
		}
		fmt.Fprintln(b, "    if (countExact) out.set('count', 'exact');")
		fmt.Fprintln(b, "    params?.forEach((key, values) {")
		fmt.Fprintln(b, "      for (final value in values) {")
		fmt.Fprintln(b, "        out.add(key, value);")
		fmt.Fprintln(b, "      }")
		fmt.Fprintln(b, "    });")
		fmt.Fprintln(b, "    return out.build();")
		fmt.Fprintln(b, "  }")
		fmt.Fprintln(b, "}")
	}

	if r.ops.Has(schema.OpRead) {
		fmt.Fprintln(b)
		dartDoc(b, "", fmt.Sprintf("Parameters for GET %s/{id}.", r.path))
		dartDoc(b, "", "")
		dartDoc(b, "", "There is no [select] here: the item endpoint rejects unknown query\nparameters and does not declare one.")
		fmt.Fprintf(b, "class %sGetParams {\n", r.base)
		if !r.hasExpand() {
			dartDoc(b, "  ", "Builds a request.")
			fmt.Fprintf(b, "  const %sGetParams();\n", r.base)
			fmt.Fprintln(b)
			dartDoc(b, "  ", "Always empty: this endpoint takes no parameters.")
			fmt.Fprintln(b, "  String toQuery() => '';")
			fmt.Fprintln(b, "}")
			return
		}
		dartDoc(b, "  ", "Builds a request.")
		fmt.Fprintf(b, "  const %sGetParams({this.expand});\n", r.base)
		fmt.Fprintln(b)
		dartDoc(b, "  ", "Relations to pull in alongside the row.")
		fmt.Fprintf(b, "  final List<%sExpand>? expand;\n", r.base)
		fmt.Fprintln(b)
		dartDoc(b, "  ", "Encodes these parameters into the server's query grammar.")
		fmt.Fprintln(b, "  String toQuery() {")
		fmt.Fprintln(b, "    final out = _Query();")
		fmt.Fprintln(b, "    if (expand != null && expand!.isNotEmpty) {")
		fmt.Fprintln(b, "      out.set('expand', expand!.map((relation) => relation.wire).join(','));")
		fmt.Fprintln(b, "    }")
		fmt.Fprintln(b, "    return out.build();")
		fmt.Fprintln(b, "  }")
		fmt.Fprintln(b, "}")
	}
}

// dartField is one property of a params class.
type dartField struct {
	name string
	typ  string
	def  string // constructor default, for the ones that are not nullable
	doc  string
}

// dartListFields is the property list of a list-params class, in the order it
// is emitted. A capability the resource did not declare contributes nothing, so
// a column that never opted in has no spelling here either.
func dartListFields(r dartResource) []dartField {
	out := []dartField{{
		name: "where", typ: r.base + "Where?",
		doc: "Filter conditions. Repeating a column conjoins its conditions.",
	}}
	if r.searchable {
		out = append(out, dartField{
			name: "search", typ: "String?",
			doc: "Case-insensitive substring match, fanned out over the searchable\ncolumns.",
		})
	}
	if len(r.sortable) > 0 {
		out = append(out, dartField{
			name: "sort", typ: "List<SortTerm>?",
			doc: "Ordering, most significant first.",
		})
	}
	out = append(out, dartField{
		name: "select", typ: "List<" + r.base + "Column>?",
		doc: "Columns to return. Omitted columns are absent from the response, and\nreading one off the row throws MissingColumn. The primary key comes back\nwhether or not it was asked for.",
	})
	if r.hasExpand() {
		out = append(out, dartField{
			name: "expand", typ: "List<" + r.base + "Expand>?",
			doc: "Relations to pull in alongside each row.",
		})
	}
	out = append(out,
		dartField{name: "page", typ: "int?", doc: "One-based page number. Prefer [cursor] for a deep walk."},
		dartField{name: "perPage", typ: "int?", doc: "Rows per page. The server caps this."},
	)
	if r.hasPK {
		out = append(out, dartField{
			name: "cursor", typ: "String?",
			doc: "Resume after a nextCursor from a previous response. Cannot be combined\nwith [page], and is only valid for the [sort] it was issued under.",
		})
	}
	out = append(out,
		dartField{
			name: "countExact", typ: "bool", def: "false",
			doc: "Ask for a total row count, which costs the server a second query.",
		},
		dartField{
			name: "params", typ: "Map<String, List<String>>?",
			doc: "Parameters this vocabulary cannot express, appended verbatim. Reaching\nfor it often means the typed layer is in the wrong place — ADR-0028 says\nso and names it as the signal to watch.",
		},
	)
	return out
}

func dartTransport(b *bytes.Buffer, r dartResource) {
	path := dartString(r.path)

	if r.ops.Has(schema.OpList) {
		fmt.Fprintln(b)
		dartDoc(b, "", fmt.Sprintf("GET %s — the filtered, sorted, paged collection.", r.path))
		fmt.Fprintf(b, "Future<Page<%s>> list%s(\n", r.row, r.plural)
		fmt.Fprintln(b, "  Transport request, {")
		fmt.Fprintf(b, "  %sListParams params = const %sListParams(),\n", r.base, r.base)
		fmt.Fprintln(b, "  Object? cancel,")
		fmt.Fprintln(b, "}) async {")
		fmt.Fprintf(b, "  const path = %s;\n", path)
		fmt.Fprintln(b, "  final json = await request(_get(path, params.toQuery(), cancel));")
		fmt.Fprintf(b, "  return _page(json, %s.fromJson);\n}\n", r.row)
	}

	if r.ops.Has(schema.OpRead) {
		fmt.Fprintln(b)
		dartDoc(b, "", fmt.Sprintf("GET %s/{id} — one row by primary key.", r.path))
		fmt.Fprintf(b, "Future<%s> get%s(\n", r.row, r.base)
		fmt.Fprintln(b, "  Transport request,")
		fmt.Fprintln(b, "  Object id, {")
		fmt.Fprintf(b, "  %sGetParams params = const %sGetParams(),\n", r.base, r.base)
		fmt.Fprintln(b, "  Object? cancel,")
		fmt.Fprintln(b, "}) async {")
		fmt.Fprintf(b, "  final path = _itemPath(%s, id);\n", path)
		fmt.Fprintln(b, "  final json = await request(_get(path, params.toQuery(), cancel));")
		fmt.Fprintf(b, "  return _row(json, %s.fromJson);\n}\n", r.row)
	}

	if r.ops.Has(schema.OpCreate) {
		fmt.Fprintln(b)
		dartDoc(b, "", fmt.Sprintf("POST %s — create a row.", r.path))
		fmt.Fprintf(b, "Future<%s> create%s(\n", r.row, r.base)
		fmt.Fprintln(b, "  Transport request,")
		fmt.Fprintf(b, "  %sCreate body, {\n", r.base)
		fmt.Fprintln(b, "  Object? cancel,")
		fmt.Fprintln(b, "}) async {")
		fmt.Fprintf(b, "  const path = %s;\n", path)
		fmt.Fprintln(b, "  final json = await request(_post(path, body.toJson(), cancel));")
		fmt.Fprintf(b, "  return _row(json, %s.fromJson);\n}\n", r.row)
	}

	if r.ops.Has(schema.OpUpdate) && len(bodyFields(r.table, forUpdate)) > 0 {
		fmt.Fprintln(b)
		dartDoc(b, "", fmt.Sprintf("PATCH %s/{id} — write the columns the body named, and no others.", r.path))
		fmt.Fprintf(b, "Future<%s> update%s(\n", r.row, r.base)
		fmt.Fprintln(b, "  Transport request,")
		fmt.Fprintln(b, "  Object id,")
		fmt.Fprintf(b, "  %sPatch body, {\n", r.base)
		fmt.Fprintln(b, "  Object? cancel,")
		fmt.Fprintln(b, "}) async {")
		fmt.Fprintf(b, "  final path = _itemPath(%s, id);\n", path)
		fmt.Fprintln(b, "  final json = await request(_patch(path, body.toJson(), cancel));")
		fmt.Fprintf(b, "  return _row(json, %s.fromJson);\n}\n", r.row)
	}

	if r.ops.Has(schema.OpDelete) {
		fmt.Fprintln(b)
		dartDoc(b, "", fmt.Sprintf("DELETE %s/{id}.", r.path))
		fmt.Fprintf(b, "Future<void> delete%s(\n", r.base)
		fmt.Fprintln(b, "  Transport request,")
		fmt.Fprintln(b, "  Object id, {")
		fmt.Fprintln(b, "  Object? cancel,")
		fmt.Fprintln(b, "}) async {")
		fmt.Fprintf(b, "  final path = _itemPath(%s, id);\n", path)
		fmt.Fprintln(b, "  await request(_delete(path, cancel));")
		fmt.Fprintln(b, "}")
	}

	dartActionMethods(b, r)
}

// dartPager emits the cursor-walking constructor for one listable resource.
//
// This is the layer a mobile client needs and a schema can supply: an
// infinite-scrolling list is the default shape of a phone screen, and paging it
// by offset is the arithmetic keyset cursors exist to replace (ADR-0027).
func dartPager(b *bytes.Buffer, r dartResource) {
	if !r.ops.Has(schema.OpList) || !r.hasPK {
		return
	}
	fmt.Fprintln(b)
	dartDoc(b, "", fmt.Sprintf("A cursor walk over %s, for a list that loads as it is scrolled.", r.path))
	dartDoc(b, "", "")
	dartDoc(b, "", "[params] is the filter and ordering the whole walk runs under. Its [page]\nand [cursor] are ignored: the pager owns where a page starts.\n\nThere is no cancel token, unlike the single-request functions. A walk is\nmany requests and a Dio CancelToken is one-shot, so cancelling to abandon\none page would poison every page after it.")
	fmt.Fprintf(b, "CursorPager<%s> %sPager(\n", r.row, r.ident)
	fmt.Fprintln(b, "  Transport request, {")
	fmt.Fprintf(b, "  %sListParams params = const %sListParams(),\n", r.base, r.base)
	fmt.Fprintln(b, "}) {")
	fmt.Fprintf(b, "  return CursorPager<%s>(\n", r.row)
	// No cancel token here, unlike the single-request functions. A walk is many
	// requests and Dio's CancelToken is one-shot: cancelling it to abandon one
	// page would poison every page after it, so per-request cancellation is the
	// caller's to do through the transport.
	call := fmt.Sprintf("(cursor) => list%s(request, params: params.withCursor(cursor)),", r.plural)
	if 4+len(call) <= dartWidth {
		fmt.Fprintf(b, "    %s\n", call)
	} else {
		body := strings.TrimPrefix(call, "(cursor) => ")
		fmt.Fprintf(b, "    (cursor) =>\n        %s\n", body)
	}
	fmt.Fprintln(b, "  );")
	fmt.Fprintln(b, "}")
}

// dartTableEnum names the tables, so that a change-feed subscriber receiving a
// table and a row key (ADR-0012) can switch on something the compiler checks
// rather than on a string literal.
//
// It is deliberately not a cache-key factory. The TypeScript client emits one
// because TanStack Query has a keyed cache to invalidate; Riverpod, BLoC and
// the rest have no such registry, so keys would be vocabulary with no consumer.
func dartTableEnum(b *bytes.Buffer, reg *schema.Registry) {
	var exposed []*schema.TableDef
	for _, t := range reg.Tables() {
		if t.Rest() != nil {
			exposed = append(exposed, t)
		}
	}
	if len(exposed) == 0 {
		return
	}
	fmt.Fprintf(b, "\n// %s\n", dartRule("change feed"))
	fmt.Fprintln(b)
	dartDoc(b, "", "The tables this client serves, for a subscriber that receives a table name\nand a row key and has to decide what to refetch.")
	fmt.Fprintln(b, "enum TableName implements WireValue {")
	var docs, names, wires []string
	for _, t := range exposed {
		docs = append(docs, fmt.Sprintf("The %s table, served at %s.", t.Name(), t.Rest().Path))
		names = append(names, dartMember(t.LocalName()))
		wires = append(wires, t.Name())
	}
	dartEnumBody(b, docs, names, wires)
	fmt.Fprintln(b, "\n  const TableName(this.wire);")
	fmt.Fprintln(b, "\n  @override\n  final String wire;")
	fmt.Fprintln(b)
	dartDoc(b, "  ", "The member [wire] names, or null for a table this client does not serve.")
	fmt.Fprintln(b, "  static TableName? byWire(String wire) {")
	fmt.Fprintln(b, "    for (final value in values) {")
	fmt.Fprintln(b, "      if (value.wire == wire) return value;")
	fmt.Fprintln(b, "    }")
	fmt.Fprintln(b, "    return null;")
	fmt.Fprintln(b, "  }")
	fmt.Fprintln(b, "}")
}

// dartSelectable is the visible columns of a table, which is what `select` may
// name.
func dartSelectable(t *schema.TableDef) []*schema.FieldDesc {
	var out []*schema.FieldDesc
	for _, f := range t.Fields() {
		if d := f.Desc(); !d.Hidden {
			out = append(out, d)
		}
	}
	return out
}

// ------------------------------------------------------------------- naming

// dartTypeBase is the name every generated type for a table is built from: the
// singular of its local name in PascalCase.
//
// Unlike GoName it does not upper-case initialisms, because Dart's convention
// is the opposite one — HttpRequest, not HTTPRequest — so org_id gives OrgId.
func dartTypeBase(t *schema.TableDef) string {
	return dartPascal(Singular(t.LocalName()))
}

// dartRowType is the class name of a table's row view: dartTypeBase, unless
// that collides with something the emitted file already means, in which case it
// takes a Row suffix.
//
// A table named "lists" is the case this exists for. Declaring class List makes
// every List<T> in the generated library refer to it, and the failure would be
// a wall of type errors rather than a name clash.
func dartRowType(t *schema.TableDef) string {
	base := dartTypeBase(t)
	if dartCoreNames[base] {
		return base + "Row"
	}
	return base
}

// dartPascal converts a snake_case SQL identifier to PascalCase.
func dartPascal(s string) string {
	var b strings.Builder
	for _, part := range strings.Split(s, "_") {
		if part == "" {
			continue
		}
		r := []rune(part)
		b.WriteString(strings.ToUpper(string(r[0])))
		b.WriteString(string(r[1:]))
	}
	return b.String()
}

// dartMember converts a snake_case SQL identifier to the lowerCamelCase Dart
// naming convention wants, escaping a name that is already spoken for.
//
// The escape is a "Value" suffix rather than a trailing underscore or a dollar
// sign, because those trip the lowerCamelCase lint every consuming project
// inherits from package:lints, and generated code that a project cannot
// analyse cleanly is generated code it will exclude.
func dartMember(s string) string {
	name := dartLowerFirst(dartPascal(s))
	if name == "" {
		return "value"
	}
	if dartKeywords[name] || dartReservedMembers[name] {
		return name + "Value"
	}
	// A name that starts with a digit is not an identifier. This reaches enum
	// values, which are arbitrary strings, rather than column names.
	if r := []rune(name)[0]; r >= '0' && r <= '9' {
		return "v" + name
	}
	return name
}

func dartLowerFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = []rune(strings.ToLower(string(r[0])))[0]
	return string(r)
}

// dartString renders a Dart single-quoted string literal. Single quotes,
// because prefer_single_quotes is on in every project that takes
// package:lints, and generated code that fails the consumer's lint is generated
// code they will exclude from analysis.
func dartString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `'`, `\'`, "$", `\$`, "\n", `\n`)
	return "'" + r.Replace(s) + "'"
}

// dartDoc emits a doc comment, one /// line per line of text.
func dartDoc(b *bytes.Buffer, indent, text string) {
	// A leading newline in the indent lets a caller ask for a blank line before
	// the comment without a second call.
	prefix := indent
	if strings.HasPrefix(indent, "\n") {
		fmt.Fprintln(b)
		prefix = indent[1:]
	}
	if text == "" {
		fmt.Fprintf(b, "%s///\n", prefix)
		return
	}
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			fmt.Fprintf(b, "%s///\n", prefix)
			continue
		}
		fmt.Fprintf(b, "%s/// %s\n", prefix, line)
	}
}

// dartWidth is the column dart format wraps at. The emitter reproduces its
// decisions rather than deferring to it, because this module has no Dart
// toolchain and a generated file the formatter would rewrite puts a project's
// format gate and its staleness gate in direct conflict.
const dartWidth = 80

// dartLine renders a one-line class member, splitting the arrow body onto its
// own line when the declaration would not fit. That is what dart format does
// with one, so emitting it this way makes the formatter a no-op.
func dartLine(decl string) string {
	const indent = 2 // every member this is used for sits inside a class
	if indent+len(decl) <= dartWidth {
		return decl
	}
	if i := strings.Index(decl, " => "); i >= 0 {
		return decl[:i] + " =>\n      " + decl[i+len(" => "):]
	}
	return decl
}

// dartRule is a section divider, padded so the generated file has visible
// seams.
func dartRule(label string) string {
	const width = 76
	rule := strings.Repeat("-", max(3, width-len(label)-1))
	return rule + " " + label
}

const dartHeader = `// Code generated by github.com/jryannel/sqlb. DO NOT EDIT.
//
// The typed client for this schema: row views, request bodies, the typed
// filter vocabulary, the URL encoder, one function per exposed operation, and
// a cursor pager for lists that load as they are scrolled.
//
// It imports nothing — not dart:io, not a pub package. The transport is
// injected, because the base URL, the auth header, token refresh and what a
// 401 does are the application's and are not derivable from a schema. That is
// the same seam the server takes by mounting onto a router the application
// built.
//
// Property names are lowerCamelCase and the wire spelling is carried beside
// them, because snake_case members would fail the lowerCamelCase lint in every
// file that touched them. Nothing is lost: Dart has to decode a response
// explicitly either way, so the mapping costs a string constant rather than a
// runtime layer.
//
// ADR-0031.

// ignore_for_file: unused_element
`

// dartRuntime is the part of the emitted file that does not depend on the
// schema: the row base class, the response envelopes, the operator vocabulary,
// the transport interface, the URL encoder and the pager.
//
// It is inlined rather than imported from a published package, for the reason
// the models are: a client generated against the server it talks to cannot be a
// version behind it.
//
// It carries no backticks, because Dart doc comments reference identifiers with
// [square brackets] and this is a Go raw string.
const dartRuntime = `
// -------------------------------------------------------------------- runtime

/// A value with a spelling on the wire.
///
/// Every generated enum implements it, so the encoder can write a filter
/// carrying a typed value without knowing which enum it is.
abstract interface class WireValue {
  /// The value as the server spells it.
  String get wire;
}

/// Thrown when a row is asked for a column the response did not carry.
///
/// Dart cannot narrow a type by a runtime projection, so this is where a
/// [select] that dropped a column is reported: at the read, naming the column
/// and the fix, rather than as a null that travels somewhere else first.
class MissingColumn implements Exception {
  /// Names the row type and the column that was absent.
  const MissingColumn(this.type, this.column);

  /// The row view that was read, e.g. Task.
  final String type;

  /// The column, in its wire spelling.
  final String column;

  @override
  String toString() =>
      'MissingColumn: $type.$column was not in the response. Add it to '
      'select, or drop select to get every column.';
}

/// Thrown when a column carries a value its generated enum does not have.
///
/// The value set grew on the server and this client predates it, so the fix is
/// to regenerate rather than to handle the value.
class UnknownEnumValue implements Exception {
  /// Names the enum and the value that was not in it.
  const UnknownEnumValue(this.type, this.value);

  /// The generated enum, e.g. TaskStatus.
  final String type;

  /// The value the server sent.
  final String value;

  @override
  String toString() =>
      'UnknownEnumValue: $type has no value $value. The schema this client '
      'was generated from is older than the server; regenerate it.';
}

/// The shared behaviour of every generated row: the decoded response object it
/// wraps, and equality over it.
///
/// A row is a view rather than a copy. Columns are decoded on access, so a
/// list of two hundred rows whose screen reads three columns decodes three
/// columns, and a projection is representable at all — which it would not be
/// if the constructor required every field.
abstract class Row {
  /// Wraps one decoded response object.
  Row.fromJson(this._json);

  final Map<String, dynamic> _json;
  final Map<String, Object?> _cache = {};

  /// The decoded response object, exactly as it arrived.
  ///
  /// Useful for a local cache: what came back can be stored and handed to
  /// fromJson later without a second encoding step.
  Map<String, dynamic> toJson() => _json;

  /// Whether the response carried [column] at all, which is a different
  /// question from whether its value was null.
  bool _present(String column) => _json.containsKey(column);

  Object? _memo(String key, Object? Function() build) {
    if (_cache.containsKey(key)) return _cache[key];
    final value = build();
    _cache[key] = value;
    return value;
  }

  /// Reads a column, or reports that the request did not return it.
  ///
  /// Absence is checked with containsKey rather than by testing for null,
  /// because a nullable column returned as null and one that was never
  /// selected are different facts and only one of them is a mistake.
  Object? _read(String column, {required bool nullable}) {
    if (!_json.containsKey(column)) {
      throw MissingColumn('$runtimeType', column);
    }
    final value = _json[column];
    if (value == null && !nullable) {
      throw MissingColumn('$runtimeType', column);
    }
    return value;
  }

  String _str(String column) => _read(column, nullable: false)! as String;

  String? _strOrNull(String column) => _read(column, nullable: true) as String?;

  int _int(String column) => (_read(column, nullable: false)! as num).toInt();

  int? _intOrNull(String column) =>
      (_read(column, nullable: true) as num?)?.toInt();

  double _double(String column) =>
      (_read(column, nullable: false)! as num).toDouble();

  double? _doubleOrNull(String column) =>
      (_read(column, nullable: true) as num?)?.toDouble();

  bool _bool(String column) => _read(column, nullable: false)! as bool;

  bool? _boolOrNull(String column) => _read(column, nullable: true) as bool?;

  DateTime _time(String column) => DateTime.parse(_str(column));

  DateTime? _timeOrNull(String column) {
    final value = _strOrNull(column);
    return value == null ? null : DateTime.parse(value);
  }

  /// Reads an array column, decoding each element with the decoder its scalar
  /// form uses. A NULL column and an empty array are different values, which is
  /// why the nullable form returns null rather than an empty list.
  List<T> _list<T>(String column, T Function(Object) decode) {
    final value = _read(column, nullable: false)! as List;
    return value.map((e) => decode(e as Object)).toList(growable: false);
  }

  List<T>? _listOrNull<T>(String column, T Function(Object) decode) {
    final value = _read(column, nullable: true) as List?;
    if (value == null) return null;
    return value.map((e) => decode(e as Object)).toList(growable: false);
  }

  List<T> _enumList<T>(String column, T? Function(String) byWire) =>
      _list(column, (e) => _asEnum(byWire, e));

  List<T>? _enumListOrNull<T>(String column, T? Function(String) byWire) =>
      _listOrNull(column, (e) => _asEnum(byWire, e));

  Object _any(String column) => _read(column, nullable: false)!;

  Object? _anyOrNull(String column) => _read(column, nullable: true);

  /// Reads an enum column. The type argument names itself in the error, so a
  /// value the schema has since grown is reported as what it is.
  T _enum<T>(String column, T? Function(String) byWire) {
    final value = _str(column);
    return byWire(value) ?? (throw UnknownEnumValue('$T', value));
  }

  T? _enumOrNull<T>(String column, T? Function(String) byWire) {
    final value = _strOrNull(column);
    if (value == null) return null;
    return byWire(value) ?? (throw UnknownEnumValue('$T', value));
  }

  /// Reads a forward expansion: one row, or null when it was not expanded.
  T? _one<T>(String relation, T Function(Map<String, dynamic>) make) =>
      _memo(relation, () {
            final value = _json[relation];
            return value == null ? null : make(value as Map<String, dynamic>);
          })
          as T?;

  /// Reads a reverse expansion: a capped collection, or null when it was not
  /// expanded.
  Collection<T>? _many<T>(
    String relation,
    T Function(Map<String, dynamic>) make,
  ) =>
      _memo(relation, () {
            final value = _json[relation];
            if (value == null) return null;
            return Collection<T>.fromJson(value as Map<String, dynamic>, make);
          })
          as Collection<T>?;

  @override
  bool operator ==(Object other) =>
      identical(this, other) ||
      other is Row &&
          other.runtimeType == runtimeType &&
          _jsonEquals(_json, other._json);

  @override
  int get hashCode => Object.hash(runtimeType, _jsonHash(_json));

  @override
  String toString() => '$runtimeType($_json)';
}

/// A capped set of expanded child rows.
///
/// [hasMore] reports truncation, which a bare list could not, so a caller
/// showing twenty of two hundred can tell.
class Collection<T> {
  /// Wraps rows that are already decoded.
  const Collection({required this.items, required this.hasMore});

  /// Decodes an envelope, mapping each element with [item].
  factory Collection.fromJson(
    Map<String, dynamic> json,
    T Function(Map<String, dynamic>) item,
  ) {
    return Collection<T>(
      items: _rows(json, item),
      hasMore: json['has_more'] as bool? ?? false,
    );
  }

  /// The rows, in the order the server returned them.
  final List<T> items;

  /// Whether the server had more rows than it returned.
  final bool hasMore;
}

/// The body of every list response: a collection, plus where in the walk it is.
class Page<T> extends Collection<T> {
  /// Wraps rows that are already decoded.
  const Page({
    required super.items,
    required super.hasMore,
    required this.page,
    required this.perPage,
    this.nextCursor,
    this.total,
  });

  /// Decodes a list response, mapping each element with [item].
  factory Page.fromJson(
    Map<String, dynamic> json,
    T Function(Map<String, dynamic>) item,
  ) {
    return Page<T>(
      items: _rows(json, item),
      hasMore: json['has_more'] as bool? ?? false,
      page: (json['page'] as num?)?.toInt() ?? 1,
      perPage: (json['per_page'] as num?)?.toInt() ?? 0,
      nextCursor: json['next_cursor'] as String?,
      total: (json['total'] as num?)?.toInt(),
    );
  }

  /// The one-based page number this response is.
  final int page;

  /// The page size the server used, which may be smaller than the one asked
  /// for.
  final int perPage;

  /// Where to resume, present whenever a next page exists. Prefer it to
  /// [page]: it costs the same at any depth and cannot skip or repeat a row
  /// when the table is written to mid-walk.
  final String? nextCursor;

  /// Total matching rows, present only when countExact was asked for.
  final int? total;
}

/// One rejected parameter or field.
class ProblemDetail {
  /// Builds a detail. Decoded from a response rather than constructed, mostly.
  const ProblemDetail({
    required this.message,
    this.location,
    this.value,
    this.allowed = const [],
  });

  /// Decodes one entry of a problem document's errors array.
  factory ProblemDetail.fromJson(Map<String, dynamic> json) => ProblemDetail(
    message: json['message'] as String? ?? '',
    location: json['location'] as String?,
    value: json['value'],
    allowed: ((json['allowed'] as List<dynamic>?) ?? const [])
        .map((value) => '$value')
        .toList(growable: false),
  );

  /// What was wrong.
  final String message;

  /// Where the problem is, e.g. query.sort.
  final String? location;

  /// The value that was rejected.
  final Object? value;

  /// What would have been accepted instead, where the set is finite. This is
  /// the half of an error that turns a dead end into a fix, and it is why this
  /// type exists at all rather than a message string.
  final List<String> allowed;
}

/// The RFC 9457 problem document every rejection returns.
class Problem {
  /// Builds a problem document.
  const Problem({
    this.type,
    this.title,
    this.status,
    this.detail,
    this.errors = const [],
  });

  /// Decodes a problem document.
  factory Problem.fromJson(Map<String, dynamic> json) => Problem(
    type: json['type'] as String?,
    title: json['title'] as String?,
    status: (json['status'] as num?)?.toInt(),
    detail: json['detail'] as String?,
    errors: ((json['errors'] as List<dynamic>?) ?? const [])
        .map((value) => ProblemDetail.fromJson(value as Map<String, dynamic>))
        .toList(growable: false),
  );

  /// Narrows a decoded error body to a problem document, or null if it is not
  /// one. A transport uses this to decide whether the body it got is worth
  /// decoding.
  static Problem? tryParse(Object? body) {
    if (body is! Map<String, dynamic>) return null;
    if (body['status'] is num || body['errors'] is List) {
      return Problem.fromJson(body);
    }
    return null;
  }

  /// A URI naming the problem kind.
  final String? type;

  /// A short summary.
  final String? title;

  /// The HTTP status.
  final int? status;

  /// A longer explanation.
  final String? detail;

  /// One entry per rejected parameter or field.
  final List<ProblemDetail> errors;

  /// The values a rejection named for one parameter, e.g.
  /// allowedFor('query.sort'). Empty when the rejection named none.
  List<String> allowedFor(String location) {
    for (final error in errors) {
      if (error.location == location) return error.allowed;
    }
    return const [];
  }
}

/// One request, as the generated functions describe it.
class ApiRequest {
  /// Describes a request. Built by the generated functions, not by hand.
  const ApiRequest({
    required this.method,
    required this.path,
    this.query,
    this.body,
    this.cancel,
  });

  /// GET, POST, PATCH or DELETE.
  final String method;

  /// Path from the API root, already encoded, e.g. /tasks/1.
  final String path;

  /// Encoded query string without the leading question mark.
  final String? query;

  /// The JSON body, already reduced to maps and lists.
  final Object? body;

  /// Whatever the transport's HTTP client cancels with, passed through
  /// untouched. Dio takes a CancelToken here; a client that has no such notion
  /// ignores it. It is [Object] rather than a package type so that this file
  /// keeps its promise to import nothing.
  final Object? cancel;
}

/// The application's request function.
///
/// Everything not derivable from the schema lives behind this: the base URL,
/// the auth header, refresh, retry, offline behaviour and what a 401 does. It
/// returns the decoded JSON body, or null for a 204.
///
/// A minimal one over Dio:
///
///     final Transport transport = (request) async {
///       final response = await dio.request<Object?>(
///         request.query == null || request.query!.isEmpty
///             ? request.path
///             : '${request.path}?${request.query}',
///         options: Options(method: request.method),
///         data: request.body,
///         cancelToken: request.cancel as CancelToken?,
///       );
///       return response.data;
///     };
typedef Transport = Future<Object?> Function(ApiRequest request);

/// One term of an ordering: a sortable column, ascending or descending.
class SortTerm implements WireValue {
  /// Wraps a term the server will accept. Reach for a generated Sort enum's
  /// asc or desc rather than building one of these by hand.
  const SortTerm(this.wire);

  @override
  final String wire;
}

/// Operators every column type accepts.
///
/// Setting more than one conjoins them: the server reads a repeated parameter
/// as AND, so a condition object is a conjunction over one column.
class Cond<T extends Object> {
  /// Builds a condition. Every operator is optional; the unset ones are not
  /// sent.
  const Cond({
    this.eq,
    this.ne,
    this.gt,
    this.gte,
    this.lt,
    this.lte,
    this.isIn,
    this.notIn,
    this.between,
  });

  /// Equal to.
  final T? eq;

  /// Not equal to.
  final T? ne;

  /// Greater than.
  final T? gt;

  /// Greater than or equal to.
  final T? gte;

  /// Less than.
  final T? lt;

  /// Less than or equal to.
  final T? lte;

  /// One of. Named isIn because in is a Dart keyword.
  final List<T>? isIn;

  /// None of.
  final List<T>? notIn;

  /// Between two bounds, inclusive.
  final (T, T)? between;

  void _encode(_Query out, String column) => _comparison(
    out,
    column,
    eq: eq,
    ne: ne,
    gt: gt,
    gte: gte,
    lt: lt,
    lte: lte,
    isIn: isIn,
    notIn: notIn,
    between: between,
  );
}

/// The operators an array column accepts: containment, and whole-array
/// equality.
///
/// The ordering operators and the substring one are absent, because the server
/// refuses them on an array — array ordering is not a thing an API should
/// offer, and a substring is a text operation. There is no [contains] here for
/// the same reason: the name belongs to text, and one name meaning two things
/// depending on the column is the ambiguity this client exists to remove.
class ArrayCond<T extends Object> {
  /// Builds a condition. Every operator is optional; the unset ones are not
  /// sent.
  const ArrayCond({this.eq, this.ne, this.has, this.hasAny, this.hasAll});

  /// The whole array, compared element by element.
  final List<T>? eq;

  /// Not equal to the whole array.
  final List<T>? ne;

  /// The array contains this element.
  final T? has;

  /// The array shares at least one element with these.
  final List<T>? hasAny;

  /// The array contains all of these.
  final List<T>? hasAll;

  void _encode(_Query out, String column) => _containment(
    out,
    column,
    eq: eq,
    ne: ne,
    has: has,
    hasAny: hasAny,
    hasAll: hasAll,
  );
}

/// An array column that may be NULL: the containment operators, plus the null
/// tests. A NULL array and an empty one are different values, so both tests
/// mean something here.
class NullableArrayCond<T extends Object> {
  /// Builds a condition. Every operator is optional.
  const NullableArrayCond({
    this.eq,
    this.ne,
    this.has,
    this.hasAny,
    this.hasAll,
    this.isNull,
    this.notNull,
  });

  /// The whole array, compared element by element.
  final List<T>? eq;

  /// Not equal to the whole array.
  final List<T>? ne;

  /// The array contains this element.
  final T? has;

  /// The array shares at least one element with these.
  final List<T>? hasAny;

  /// The array contains all of these.
  final List<T>? hasAll;

  /// The column is NULL, which is not the same as holding no elements.
  final bool? isNull;

  /// The column is not NULL.
  final bool? notNull;

  void _encode(_Query out, String column) {
    _containment(
      out,
      column,
      eq: eq,
      ne: ne,
      has: has,
      hasAny: hasAny,
      hasAll: hasAll,
    );
    _nullChecks(out, column, isNull: isNull, notNull: notNull);
  }
}

/// The operators a nullable column accepts: the comparisons, plus the null
/// tests that only mean something when a column can be null.
class NullableCond<T extends Object> {
  /// Builds a condition. Every operator is optional.
  const NullableCond({
    this.eq,
    this.ne,
    this.gt,
    this.gte,
    this.lt,
    this.lte,
    this.isIn,
    this.notIn,
    this.between,
    this.isNull,
    this.notNull,
  });

  /// Equal to.
  final T? eq;

  /// Not equal to.
  final T? ne;

  /// Greater than.
  final T? gt;

  /// Greater than or equal to.
  final T? gte;

  /// Less than.
  final T? lt;

  /// Less than or equal to.
  final T? lte;

  /// One of.
  final List<T>? isIn;

  /// None of.
  final List<T>? notIn;

  /// Between two bounds, inclusive.
  final (T, T)? between;

  /// IS NULL, when true.
  final bool? isNull;

  /// IS NOT NULL, when true.
  final bool? notNull;

  void _encode(_Query out, String column) {
    _comparison(
      out,
      column,
      eq: eq,
      ne: ne,
      gt: gt,
      gte: gte,
      lt: lt,
      lte: lte,
      isIn: isIn,
      notIn: notIn,
      between: between,
    );
    _nullChecks(out, column, isNull: isNull, notNull: notNull);
  }
}

/// The operators a text column accepts: the comparisons, plus the pattern
/// matches the server refuses on anything else.
class TextCond {
  /// Builds a condition. Every operator is optional.
  const TextCond({
    this.eq,
    this.ne,
    this.gt,
    this.gte,
    this.lt,
    this.lte,
    this.isIn,
    this.notIn,
    this.between,
    this.like,
    this.ilike,
    this.contains,
    this.startsWith,
    this.endsWith,
  });

  /// Equal to.
  final String? eq;

  /// Not equal to.
  final String? ne;

  /// Greater than.
  final String? gt;

  /// Greater than or equal to.
  final String? gte;

  /// Less than.
  final String? lt;

  /// Less than or equal to.
  final String? lte;

  /// One of.
  final List<String>? isIn;

  /// None of.
  final List<String>? notIn;

  /// Between two bounds, inclusive.
  final (String, String)? between;

  /// SQL LIKE, with the caller's own wildcards.
  final String? like;

  /// Case-insensitive LIKE.
  final String? ilike;

  /// Case-insensitive substring. The value is escaped, so a search for 50%
  /// matches that literal string rather than everything.
  final String? contains;

  /// Case-insensitive prefix.
  final String? startsWith;

  /// Case-insensitive suffix.
  final String? endsWith;

  void _encode(_Query out, String column) {
    _comparison(
      out,
      column,
      eq: eq,
      ne: ne,
      gt: gt,
      gte: gte,
      lt: lt,
      lte: lte,
      isIn: isIn,
      notIn: notIn,
      between: between,
    );
    _patterns(
      out,
      column,
      like: like,
      ilike: ilike,
      contains: contains,
      startsWith: startsWith,
      endsWith: endsWith,
    );
  }
}

/// The operators a nullable text column accepts: everything.
class NullableTextCond {
  /// Builds a condition. Every operator is optional.
  const NullableTextCond({
    this.eq,
    this.ne,
    this.gt,
    this.gte,
    this.lt,
    this.lte,
    this.isIn,
    this.notIn,
    this.between,
    this.like,
    this.ilike,
    this.contains,
    this.startsWith,
    this.endsWith,
    this.isNull,
    this.notNull,
  });

  /// Equal to.
  final String? eq;

  /// Not equal to.
  final String? ne;

  /// Greater than.
  final String? gt;

  /// Greater than or equal to.
  final String? gte;

  /// Less than.
  final String? lt;

  /// Less than or equal to.
  final String? lte;

  /// One of.
  final List<String>? isIn;

  /// None of.
  final List<String>? notIn;

  /// Between two bounds, inclusive.
  final (String, String)? between;

  /// SQL LIKE, with the caller's own wildcards.
  final String? like;

  /// Case-insensitive LIKE.
  final String? ilike;

  /// Case-insensitive substring, escaped.
  final String? contains;

  /// Case-insensitive prefix.
  final String? startsWith;

  /// Case-insensitive suffix.
  final String? endsWith;

  /// IS NULL, when true.
  final bool? isNull;

  /// IS NOT NULL, when true.
  final bool? notNull;

  void _encode(_Query out, String column) {
    _comparison(
      out,
      column,
      eq: eq,
      ne: ne,
      gt: gt,
      gte: gte,
      lt: lt,
      lte: lte,
      isIn: isIn,
      notIn: notIn,
      between: between,
    );
    _patterns(
      out,
      column,
      like: like,
      ilike: ilike,
      contains: contains,
      startsWith: startsWith,
      endsWith: endsWith,
    );
    _nullChecks(out, column, isNull: isNull, notNull: notNull);
  }
}

/// Walks a list endpoint by cursor: the shape an infinite-scrolling list needs,
/// and the arithmetic keyset pagination exists to replace.
///
/// It holds rows and a position, and nothing about how they are displayed, so
/// it drops into a Riverpod notifier, a BLoC or a StatefulWidget without
/// preferring any of them.
class CursorPager<T> {
  /// Wraps a fetch that takes the cursor to resume from, or null for the first
  /// page. The generated per-resource pagers supply one.
  CursorPager(this._fetch);

  final Future<Page<T>> Function(String? cursor) _fetch;

  /// The rows loaded so far, in order. Owned by the pager: read it, do not
  /// mutate it.
  final List<T> items = [];

  String? _cursor;
  bool _exhausted = false;
  int? _total;
  Future<void>? _inFlight;

  /// Whether another page exists. False before the first load only because
  /// nothing has been fetched yet.
  bool get hasMore => !_exhausted;

  /// Whether a fetch is in flight.
  bool get isLoading => _inFlight != null;

  /// Total matching rows, if a load asked for the count.
  int? get total => _total;

  /// Fetches the next page and appends it.
  ///
  /// Concurrent calls collapse onto the one already running, which is what a
  /// scroll listener needs: it fires on every frame near the end of the list
  /// and only the first call should reach the network.
  Future<void> loadMore() {
    final pending = _inFlight;
    if (pending != null) return pending;
    if (_exhausted) return Future<void>.value();
    final started = _load().whenComplete(() => _inFlight = null);
    _inFlight = started;
    return started;
  }

  Future<void> _load() async {
    final page = await _fetch(_cursor);
    items.addAll(page.items);
    _total = page.total ?? _total;
    _cursor = page.nextCursor;
    if (_cursor == null) _exhausted = true;
  }

  /// Discards everything loaded and starts the walk over, which is what a
  /// pull-to-refresh does.
  ///
  /// A fetch already in flight is not cancelled and its rows are still
  /// appended. Cancel it through the transport if that matters.
  void reset() {
    items.clear();
    _cursor = null;
    _exhausted = false;
    _total = null;
  }
}

// --------------------------------------------------------------------- decoding

List<T> _rows<T>(
  Map<String, dynamic> json,
  T Function(Map<String, dynamic>) item,
) {
  return ((json['items'] as List<dynamic>?) ?? const [])
      .map((value) => item(value as Map<String, dynamic>))
      .toList(growable: false);
}

bool _jsonEquals(Object? a, Object? b) {
  if (identical(a, b)) return true;
  if (a is Map && b is Map) {
    if (a.length != b.length) return false;
    for (final key in a.keys) {
      if (!b.containsKey(key) || !_jsonEquals(a[key], b[key])) return false;
    }
    return true;
  }
  if (a is List && b is List) {
    if (a.length != b.length) return false;
    for (var i = 0; i < a.length; i++) {
      if (!_jsonEquals(a[i], b[i])) return false;
    }
    return true;
  }
  return a == b;
}

int _jsonHash(Object? value) {
  if (value is Map) {
    // Exclusive-or, so that the hash does not depend on key order — which a
    // decoded JSON object does not promise.
    var hash = 0;
    value.forEach((key, item) {
      hash ^= Object.hash(key, _jsonHash(item));
    });
    return hash;
  }
  if (value is List) return Object.hashAll(value.map(_jsonHash));
  return value.hashCode;
}

// --------------------------------------------------------------------- encoding

/// The JSON form of a value a request body carries.
Object? _wire(Object? value) {
  if (value is WireValue) return value.wire;
  if (value is DateTime) return value.toUtc().toIso8601String();
  // An array column carries a list, whose elements need the same treatment —
  // an enum array is a list of WireValue, and JSON has no spelling for one.
  if (value is List) return value.map(_wire).toList(growable: false);
  return value;
}

/// One value in the filter grammar: an enum by its wire spelling, a timestamp
/// as RFC 3339 UTC, everything else as it prints.
String _scalar(Object? value) {
  if (value is WireValue) return value.wire;
  if (value is DateTime) return value.toUtc().toIso8601String();
  return '$value';
}

/// A member of a comma-separated list is quoted when it carries a comma or a
/// quote, which is how the server's parser reads it back whole.
String _member(Object? value) {
  final text = _scalar(value);
  if (!text.contains(',') && !text.contains('"')) return text;
  return '"${text.replaceAll('"', '\\"')}"';
}

void _comparison(
  _Query out,
  String column, {
  Object? eq,
  Object? ne,
  Object? gt,
  Object? gte,
  Object? lt,
  Object? lte,
  List<Object?>? isIn,
  List<Object?>? notIn,
  (Object?, Object?)? between,
}) {
  if (eq != null) out.add(column, 'eq.${_scalar(eq)}');
  if (ne != null) out.add(column, 'ne.${_scalar(ne)}');
  if (gt != null) out.add(column, 'gt.${_scalar(gt)}');
  if (gte != null) out.add(column, 'gte.${_scalar(gte)}');
  if (lt != null) out.add(column, 'lt.${_scalar(lt)}');
  if (lte != null) out.add(column, 'lte.${_scalar(lte)}');
  if (isIn != null) out.add(column, 'in.${isIn.map(_member).join(',')}');
  if (notIn != null) out.add(column, 'nin.${notIn.map(_member).join(',')}');
  if (between != null) {
    out.add(column, 'between.${_member(between.$1)},${_member(between.$2)}');
  }
}

/// Decoders for one element of an array column. They take the value already
/// pulled out of the JSON list, which is what makes them shareable between the
/// nullable and non-nullable readers.
String _asStr(Object v) => v as String;

int _asInt(Object v) => (v as num).toInt();

double _asDouble(Object v) => (v as num).toDouble();

bool _asBool(Object v) => v as bool;

DateTime _asTime(Object v) => DateTime.parse(v as String);

/// Decodes one element of an enum array, naming the type in the error so a
/// value the schema has since grown is reported as what it is.
T _asEnum<T>(T? Function(String) byWire, Object v) {
  final value = v as String;
  return byWire(value) ?? (throw UnknownEnumValue('$T', value));
}

void _containment(
  _Query out,
  String column, {
  List<Object?>? eq,
  List<Object?>? ne,
  Object? has,
  List<Object?>? hasAny,
  List<Object?>? hasAll,
}) {
  if (eq != null) out.add(column, 'eq.${eq.map(_member).join(',')}');
  if (ne != null) out.add(column, 'ne.${ne.map(_member).join(',')}');
  if (has != null) out.add(column, 'has.${_scalar(has)}');
  if (hasAny != null) {
    out.add(column, 'hasany.${hasAny.map(_member).join(',')}');
  }
  if (hasAll != null) {
    out.add(column, 'hasall.${hasAll.map(_member).join(',')}');
  }
}

void _patterns(
  _Query out,
  String column, {
  String? like,
  String? ilike,
  String? contains,
  String? startsWith,
  String? endsWith,
}) {
  if (like != null) out.add(column, 'like.$like');
  if (ilike != null) out.add(column, 'ilike.$ilike');
  if (contains != null) out.add(column, 'contains.$contains');
  if (startsWith != null) out.add(column, 'startswith.$startsWith');
  if (endsWith != null) out.add(column, 'endswith.$endsWith');
}

void _nullChecks(_Query out, String column, {bool? isNull, bool? notNull}) {
  if (isNull ?? false) out.add(column, 'isnull');
  if (notNull ?? false) out.add(column, 'notnull');
}

/// Accumulates query parameters, keeping repeats — which is how the grammar
/// conjoins two conditions on one column.
class _Query {
  final List<MapEntry<String, String>> _pairs = [];

  void add(String key, String value) => _pairs.add(MapEntry(key, value));

  void set(String key, String value) {
    _pairs.removeWhere((pair) => pair.key == key);
    add(key, value);
  }

  /// The encoded query string, sorted so that the same parameters always
  /// produce the same string — which is what makes a URL comparable in a test
  /// and cacheable by a proxy.
  ///
  /// The sort is stable, since List.sort is not: repeats of one key keep the
  /// order they were added in.
  String build() {
    final indexed = _pairs.indexed.toList()
      ..sort((a, b) {
        final byKey = a.$2.key.compareTo(b.$2.key);
        return byKey != 0 ? byKey : a.$1.compareTo(b.$1);
      });
    return indexed
        .map(
          (entry) =>
              '${Uri.encodeQueryComponent(entry.$2.key)}='
              '${Uri.encodeQueryComponent(entry.$2.value)}',
        )
        .join('&');
  }
}

String _itemPath(String collection, Object id) =>
    '$collection/${Uri.encodeComponent('$id')}';

// ------------------------------------------------------------------ transport

// One constructor per verb, so that a generated call is a single short line
// rather than a five-line argument list. They exist for the reader, not for the
// compiler.

ApiRequest _get(String path, String query, Object? cancel) =>
    ApiRequest(method: 'GET', path: path, query: query, cancel: cancel);

ApiRequest _post(String path, Object? body, Object? cancel) =>
    ApiRequest(method: 'POST', path: path, body: body, cancel: cancel);

ApiRequest _patch(String path, Object? body, Object? cancel) =>
    ApiRequest(method: 'PATCH', path: path, body: body, cancel: cancel);

ApiRequest _delete(String path, Object? cancel) =>
    ApiRequest(method: 'DELETE', path: path, cancel: cancel);

Page<T> _page<T>(Object? json, T Function(Map<String, dynamic>) make) =>
    Page<T>.fromJson(json! as Map<String, dynamic>, make);

T _row<T>(Object? json, T Function(Map<String, dynamic>) make) =>
    make(json! as Map<String, dynamic>);
`
