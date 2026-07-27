package schema

import (
	"fmt"
	"strings"
)

// Field is a column under construction. Its methods are chainable setters, so
// the DSL reads as a declaration:
//
//	schema.Text("email").Unique().Searchable()
//
// Code generators read the result through Desc.
type Field struct {
	d FieldDesc
}

// FieldDesc is the resolved description of a column: everything a generator or
// the runtime needs to know about it.
type FieldDesc struct {
	Name    string
	Type    Type
	Size    int // varchar length; 0 means unbounded
	Comment string

	Nullable   bool
	PrimaryKey bool
	Unique     bool
	Default    *Default
	EnumValues []string

	// Capabilities. Each is opt-in and gates one specific REST affordance.
	Filterable bool // may be used in a REST filter expression
	Sortable   bool // may appear in ?sort
	Searchable bool // included in the ?search fan-out
	Expandable bool // relation may be pulled in via ?expand (references only)

	// Write protection, enforced by the REST layer. Go code going through the
	// query engine directly is trusted and bypasses these.
	ReadOnly  bool // never settable through REST
	Immutable bool // settable at create, rejected on update
	Hidden    bool // never serialised into a REST response

	// ConstraintName pins the name of the constraint this column declares —
	// its unique constraint, or its foreign key if it is a reference. Set it
	// when adopting an existing database, so that a generated migration
	// recognises the constraint already there instead of dropping and
	// recreating it under a name of its own choosing.
	ConstraintName string

	Ref *Reference
}

// Reference describes a foreign key.
type Reference struct {
	Name     string // relation name, e.g. "org" for column "org_id"
	Table    *TableDef
	Column   string // referenced column; defaults to the target primary key
	OnDelete Action
	OnUpdate Action
}

// Desc returns the column description. The pointer aliases the field's own
// state, so generators must treat it as read-only.
func (f *Field) Desc() *FieldDesc { return &f.d }

// Name is the column name.
func (f *Field) Name() string { return f.d.Name }

// FieldSpec is anything contributing columns to a table. Both *Field and the
// grouped helpers (Timestamps, SoftDelete) implement it, so they mix freely in
// a single Table call.
type FieldSpec interface {
	fields() []*Field
}

// Group is an ordered set of fields inserted into a table as a unit. Use it to
// factor recurring column sets out of a schema.
type Group []*Field

func (g Group) fields() []*Field { return g }

func (f *Field) fields() []*Field { return []*Field{f} }

func newField(name string, t Type) *Field {
	return &Field{d: FieldDesc{Name: name, Type: t}}
}

// Column type constructors.

func Text(name string) *Field      { return newField(name, TypeText) }
func Int(name string) *Field       { return newField(name, TypeInt) }
func BigInt(name string) *Field    { return newField(name, TypeBigInt) }
func Float(name string) *Field     { return newField(name, TypeFloat) }
func Numeric(name string) *Field   { return newField(name, TypeNumeric) }
func Bool(name string) *Field      { return newField(name, TypeBool) }
func UUID(name string) *Field      { return newField(name, TypeUUID) }
func Timestamp(name string) *Field { return newField(name, TypeTimestamp) }
func Date(name string) *Field      { return newField(name, TypeDate) }
func Time(name string) *Field      { return newField(name, TypeTime) }
func JSON(name string) *Field      { return newField(name, TypeJSON) }
func Bytes(name string) *Field     { return newField(name, TypeBytes) }

// Varchar is a length-bounded text column.
func Varchar(name string, size int) *Field {
	f := newField(name, TypeVarchar)
	f.d.Size = size
	return f
}

// Enum is a text column constrained to a fixed set of values. Codegen emits a
// Go string type with one constant per value.
func Enum(name string, values ...string) *Field {
	f := newField(name, TypeEnum)
	f.d.EnumValues = values
	return f
}

// UUIDv7 is the conventional primary key column: a UUID defaulting to a
// generated, time-ordered v7 value.
func UUIDv7(name string) *Field {
	f := newField(name, TypeUUID)
	f.d.Default = GenUUIDv7()
	return f
}

// Ref declares a foreign key to target. The column is named name+"_id" and the
// relation is named name, so Ref("org", Org) yields column "org_id" reachable
// as ?expand=org once marked Expandable.
func Ref(name string, target *TableDef) *Field {
	f := newField(name+"_id", TypeUUID)
	f.d.Ref = &Reference{Name: name, Table: target, OnDelete: NoAction, OnUpdate: NoAction}
	if target != nil {
		if pk := target.PrimaryKey(); pk != nil {
			f.d.Type = pk.Desc().Type
			f.d.Ref.Column = pk.Desc().Name
		}
	}
	return f
}

// Timestamps is the created_at / updated_at pair, both defaulting to now().
func Timestamps() Group {
	return Group{
		Timestamp("created_at").Default(Now()).ReadOnly().Sortable(),
		Timestamp("updated_at").Default(Now()).ReadOnly().Sortable(),
	}
}

// SoftDelete adds a nullable deleted_at column. The REST layer filters rows
// with a non-null value out of list responses by default.
func SoftDelete() Group {
	return Group{Timestamp("deleted_at").Nullable().ReadOnly()}
}

// Chainable configuration.

// PrimaryKey marks the column as the table's primary key. Primary keys are
// implicitly read-only and filterable.
func (f *Field) PrimaryKey() *Field {
	f.d.PrimaryKey = true
	f.d.ReadOnly = true
	f.d.Filterable = true
	return f
}

// Named overrides the column name.
//
// Most columns are named where they are declared, so this is only needed where
// the name was derived: Ref("org", Org) produces "org_id", and a database that
// calls it "organisation_uuid" needs
//
//	schema.Ref("org", Org).Named("organisation_uuid")
//
// The relation keeps its own name, so ?expand=org still works.
func (f *Field) Named(column string) *Field {
	f.d.Name = column
	return f
}

// ConstraintNamed pins the name of the constraint this column declares. Use it
// when adopting an existing database whose constraint names do not match the
// ones this package would generate.
func (f *Field) ConstraintNamed(name string) *Field {
	f.d.ConstraintName = name
	return f
}

// Nullable allows SQL NULL. Codegen emits the Go field as a pointer.
func (f *Field) Nullable() *Field {
	f.d.Nullable = true
	return f
}

// Unique adds a single-column unique constraint.
func (f *Field) Unique() *Field {
	f.d.Unique = true
	return f
}

// Default sets the column default.
func (f *Field) Default(d *Default) *Field {
	f.d.Default = d
	return f
}

// Comment attaches a description, emitted into DDL and the OpenAPI document.
func (f *Field) Comment(s string) *Field {
	f.d.Comment = s
	return f
}

// Filterable allows the column to be used in REST filter expressions.
func (f *Field) Filterable() *Field {
	f.d.Filterable = true
	return f
}

// Sortable allows the column to appear in ?sort.
func (f *Field) Sortable() *Field {
	f.d.Sortable = true
	return f
}

// Searchable includes the column in the ?search fan-out. Implies Filterable,
// since search is a filter over the same column.
func (f *Field) Searchable() *Field {
	f.d.Searchable = true
	f.d.Filterable = true
	return f
}

// Expandable allows a reference to be resolved inline via ?expand.
func (f *Field) Expandable() *Field {
	f.d.Expandable = true
	return f
}

// ReadOnly makes the column unwritable through REST.
func (f *Field) ReadOnly() *Field {
	f.d.ReadOnly = true
	return f
}

// Immutable allows the column to be set at create time only.
func (f *Field) Immutable() *Field {
	f.d.Immutable = true
	return f
}

// Hidden omits the column from every REST response. Use it for password
// hashes and similar values that must never leave the process.
func (f *Field) Hidden() *Field {
	f.d.Hidden = true
	return f
}

// OnDelete sets the foreign key delete action. It panics if the field is not a
// reference: that is a schema authoring bug, and failing at init is more useful
// than failing at request time.
func (f *Field) OnDelete(a Action) *Field {
	if f.d.Ref == nil {
		panic(fmt.Sprintf("sqlb/schema: OnDelete on non-reference column %q", f.d.Name))
	}
	f.d.Ref.OnDelete = a
	return f
}

// OnUpdate sets the foreign key update action.
func (f *Field) OnUpdate(a Action) *Field {
	if f.d.Ref == nil {
		panic(fmt.Sprintf("sqlb/schema: OnUpdate on non-reference column %q", f.d.Name))
	}
	f.d.Ref.OnUpdate = a
	return f
}

// GoType is the Go type codegen emits for this column.
func (d *FieldDesc) GoType() string {
	base := d.Type.GoType()
	if d.Nullable && base != "[]byte" && base != "json.RawMessage" {
		return "*" + base
	}
	return base
}

// Capabilities renders the column's capabilities as the comma-separated body
// of the `sqlb` struct tag that the runtime engine reads back.
func (d *FieldDesc) Capabilities() string {
	var out []string
	add := func(cond bool, s string) {
		if cond {
			out = append(out, s)
		}
	}
	add(d.PrimaryKey, "pk")
	add(d.Default != nil, "default")
	add(d.Filterable, "filter")
	add(d.Sortable, "sort")
	add(d.Searchable, "search")
	add(d.ReadOnly, "readonly")
	add(d.Immutable, "immutable")
	add(d.Hidden, "hidden")
	return strings.Join(out, ",")
}
