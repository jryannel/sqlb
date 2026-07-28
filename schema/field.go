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

	// Obligations. Neither of these changes a query. They are read once, at
	// startup, where rest refuses to mount a resource whose declarations have
	// no hook behind them; nothing on the request path reads either one.
	Scoped     bool // every exposed operation must be constrained by a hook
	SoftDelete bool // the column a soft-delete predicate is expected to filter

	// indexWanted marks a column that should carry an index even though the
	// declaration does not name one — currently only external references,
	// which exist to be joined on.
	indexWanted bool

	// ConstraintName pins the name of the constraint this column declares —
	// its unique constraint, or its foreign key if it is a reference. Set it
	// when adopting an existing database, so that a generated migration
	// recognises the constraint already there instead of dropping and
	// recreating it under a name of its own choosing.
	ConstraintName string

	// RenamedFrom is the column's previous name, declared for one release so
	// that a migration renames the column instead of dropping and re-adding
	// it. Nothing else reads it.
	RenamedFrom string

	Ref *Reference
}

// Reference describes a relationship to another table.
//
// A reference is either internal — a real foreign key to a table in the same
// registry — or external, which is a column holding another module's identifier
// with no database-level constraint behind it.
type Reference struct {
	Name     string // relation name, e.g. "org" for column "org_id"
	Table    *TableDef
	Column   string // referenced column; defaults to the target primary key
	OnDelete Action
	OnUpdate Action

	// External marks a reference across a module boundary. No FOREIGN KEY is
	// emitted for one, so the modules stay independently deployable and
	// independently migratable. Referential integrity becomes the
	// application's responsibility, which is the trade a module architecture
	// is already making everywhere else.
	External bool
	// Target names what is referenced, for documentation and for the
	// manifest: "tenants.id", or "platform/users.users.id". It is free text,
	// deliberately — resolving it would require the dependency this is
	// designed to avoid.
	Target string

	// Inverse is the name the target knows this relation by, and declaring it
	// is what makes the reverse relation exist at all.
	//
	// It cannot be derived. Two references from posts to authors — the writer
	// and the reviewer — would both derive to "posts" on the far side, and an
	// author's posts are not the posts an author reviewed. The distinction
	// exists only in the head of whoever wrote the schema, so the schema is
	// where it has to be written. ADR-0022.
	Inverse string
	// InverseExpandable exposes the reverse relation through ?expand on the
	// target's endpoint. It is a separate decision from Expandable, about a
	// different endpoint, and neither implies the other — ADR-0006.
	InverseExpandable bool
	// InverseOrder is the column an expanded collection is ordered by, with a
	// leading "-" for descending. It names a column of *this* table, since
	// these are the rows being collected. Empty means the primary key.
	InverseOrder string
	// InverseLimit caps an expanded collection. Zero takes sqlb's default.
	InverseLimit int
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

// ExternalRef declares a reference to a table this module does not own.
//
// It produces a column named relation+"_id" holding the other side's
// identifier, and an index to join on — but no FOREIGN KEY, so the two modules
// can be migrated and deployed independently, and either can be moved to its
// own database without dropping a constraint:
//
//	// in the billing module, with no import of the tenants module
//	schema.ExternalRef("tenant", "tenants.id").Filterable()
//
// The target is free text. Resolving it to a real table would require exactly
// the dependency this exists to avoid, so it is recorded for the manifest and
// for whoever reads the schema, and not checked.
//
// Such a reference cannot be Expandable: expanding it would join a table this
// module does not own. Fetch the other side through that module's own API.
func ExternalRef(relation, target string) *Field {
	f := newField(relation+"_id", TypeUUID)
	f.d.Ref = &Reference{Name: relation, Target: target, External: true}
	f.d.indexWanted = true
	return f
}

// OfType overrides the column type, for an external reference whose target is
// not the conventional UUID.
func (f *Field) OfType(t Type) *Field {
	f.d.Type = t
	return f
}

// Timestamps is the created_at / updated_at pair, both defaulting to now().
func Timestamps() Group {
	return Group{
		Timestamp("created_at").Default(Now()).ReadOnly().Sortable(),
		Timestamp("updated_at").Default(Now()).ReadOnly().Sortable(),
	}
}

// SoftDelete adds a nullable deleted_at column, and nothing else. Nothing on
// the request path reads the column: the name is not load-bearing anywhere
// below this line, and declaring the group changes no query.
//
// What it does do is oblige the table to have a BeforeQuery hook. A table that
// declares a soft delete and filters nothing returns deleted rows from every
// list endpoint, so [rest.Resource] refuses to mount one whose reads no hook
// constrains ([ADR-0030]). The refusal is at startup and it checks only that a
// hook exists — writing the predicate is still the caller's, exactly as below.
//
// Filtering the deleted rows out is a BeforeQuery registration, which is the
// seam that reaches generated REST handlers as well as queries written by hand
// ([ADR-0008]):
//
//	sqlb.On[Post]().BeforeQuery(func(_ context.Context, q *sqlb.Builder[Post]) error {
//	    q.Where(sqlb.F("deleted_at").IsNull())
//	    return nil
//	})
//
// Serving DELETE as an update to the column is the caller's too, and BeforeDelete
// cannot do it — that hook receives a *Delete and can abort or amend the
// statement, not turn it into an UPDATE. A table that means deletes to be soft
// should leave OpDelete out of its Expose and route the endpoint itself.
//
// [ADR-0008]: https://github.com/jryannel/sqlb/blob/main/docs/adr/0008-hooks-as-domain-seam.md
// [ADR-0030]: https://github.com/jryannel/sqlb/blob/main/docs/adr/0030-declared-scope-is-required.md
func SoftDelete() Group {
	f := Timestamp("deleted_at").Nullable().ReadOnly()
	f.d.SoftDelete = true
	return Group{f}
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

// RenamedFrom declares that this column used to be called old, so that a
// generated migration renames it rather than dropping it and adding a new one:
//
//	schema.Text("email_address").RenamedFrom("email")
//
// A rename is indistinguishable from a drop and an add when only the before and
// after states are known, and inferring one from a similar name and type would
// destroy data whenever the inference was wrong. So it is declared, never
// inferred (ADR-0014).
//
// The hint is needed for exactly one release: the migration it produces is
// generated once, and after that the old name is gone from every database the
// migration has been applied to. A hint whose old column no longer exists is
// ignored, so leaving one behind is harmless — but delete it at the next edit,
// because a stale hint reads as a claim about the current schema that is no
// longer true.
func (f *Field) RenamedFrom(old string) *Field {
	f.d.RenamedFrom = old
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

// Inverse names the relation from the target's side: the name an author knows
// its posts by. Declaring it is what makes the reverse relation exist.
//
//	schema.Ref("list", List).Expandable().Inverse("tasks").InverseExpandable()
//
// Read as: a task has a list; a list has tasks; both directions may be
// expanded. Absent Inverse there is no reverse relation, which is not an error
// — most references never need one.
//
// One side declares, as it already does for the column, the constraint and the
// delete action. What the target does gain is a field on its generated struct,
// because the expanded rows need somewhere to land.
func (f *Field) Inverse(name string) *Field {
	if f.d.Ref != nil {
		f.d.Ref.Inverse = name
	}
	return f
}

// InverseExpandable exposes the reverse relation through ?expand on the
// target's endpoint, and takes the options that decide which children a capped
// expansion returns:
//
//	schema.Ref("list", List).
//	    Expandable().
//	    Inverse("tasks").
//	    InverseExpandable(schema.ExpandOrder("-created_at"), schema.ExpandLimit(20))
//
// It requires Inverse: a relation with no name cannot be asked for. Exposure is
// a separate decision from Expandable in the forward direction, because the two
// are about different endpoints.
func (f *Field) InverseExpandable(opts ...InverseOption) *Field {
	if f.d.Ref != nil {
		f.d.Ref.InverseExpandable = true
		for _, opt := range opts {
			opt(f.d.Ref)
		}
	}
	return f
}

// InverseOption adjusts an expanded collection.
type InverseOption func(*Reference)

// ExpandOrder orders an expanded collection by a column of the referencing
// table, with a leading "-" for descending — the spelling ?sort already uses.
// The primary key is appended as a tiebreaker, because under a cap a non-total
// order decides which children the caller never sees.
func ExpandOrder(column string) InverseOption {
	return func(r *Reference) { r.InverseOrder = column }
}

// ExpandLimit caps how many children an expansion returns; the default is 50.
// Past the cap the response reports has_more and the caller follows the child's
// own endpoint, filtered by this foreign key — which is why that column wants
// to be Filterable, and why Lint says so when it is not.
func ExpandLimit(n int) InverseOption {
	return func(r *Reference) { r.InverseLimit = n }
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

// Scoped declares that this column confines the table's rows to one tenant,
// and that every operation the table exposes must be constrained by a hook.
//
//	schema.Ref("workspace", Workspace).Filterable().ReadOnly().Scoped()
//
// Like [SoftDelete], it writes no predicate and changes no query. What it
// changes is what happens when the predicate is missing: [rest.Resource]
// refuses to mount the resource at startup rather than serving every tenant's
// rows with a 200 next to them ([ADR-0030]).
//
// The obligation follows the operations the table exposes, because a
// BeforeQuery hook constrains what a request can see and says nothing about
// what it can overwrite by id — a list needs BeforeQuery, an update needs
// BeforeUpdate, a delete needs BeforeDelete, and a create needs BeforeCreate
// when the column is ReadOnly and so has no other source than the hook.
//
// The row itself is the tenant on the table the others point at, so there the
// declaration goes on the primary key:
//
//	schema.UUIDv7("id").PrimaryKey().Scoped()
//
// A table may declare one scope column. Where the confinement cannot be
// written as a column of this table at all — a membership join, say — declare
// it on the column the hook does constrain, which is the key it narrows.
//
// [ADR-0030]: https://github.com/jryannel/sqlb/blob/main/docs/adr/0030-declared-scope-is-required.md
func (f *Field) Scoped() *Field {
	f.d.Scoped = true
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
	add(d.Expandable, "expand")
	add(d.ReadOnly, "readonly")
	add(d.Immutable, "immutable")
	add(d.Hidden, "hidden")
	add(d.Scoped, "scope")
	add(d.SoftDelete, "softdelete")
	return strings.Join(out, ",")
}

// IndexWanted reports whether this column implicitly asked for an index. An
// external reference does, since a soft foreign key exists to be joined on and
// one without an index scans the table.
func (d *FieldDesc) IndexWanted() bool { return d.indexWanted }
