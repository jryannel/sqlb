package schema

import "strings"

// Op is a bitmask of the REST operations a table exposes.
type Op uint8

const (
	OpCreate Op = 1 << iota
	OpRead      // GET /resource/{id}
	OpUpdate
	OpDelete
	OpList // GET /resource with filter, sort, search, pagination
)

// CRUD is the conventional single-row operation set. Combine it with OpList
// for a fully exposed collection.
const CRUD = OpCreate | OpRead | OpUpdate | OpDelete

// Has reports whether the mask contains op.
func (o Op) Has(op Op) bool { return o&op != 0 }

// String renders the mask for diagnostics.
func (o Op) String() string {
	var parts []string
	for _, e := range []struct {
		op   Op
		name string
	}{
		{OpCreate, "create"}, {OpRead, "read"}, {OpUpdate, "update"},
		{OpDelete, "delete"}, {OpList, "list"},
	} {
		if o.Has(e.op) {
			parts = append(parts, e.name)
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, "|")
}

// REST describes how a table is exposed over HTTP.
type REST struct {
	// Path is the collection path, e.g. "/users". Defaults to "/"+table name.
	Path string
	// Ops is the set of exposed operations.
	Ops Op
	// DefaultPageSize applies when the request omits a page size. Zero means
	// the package default.
	DefaultPageSize int
	// MaxPageSize caps the page size a client may request. Zero means the
	// package default. This is a hard ceiling, not a hint.
	MaxPageSize int
	// MaxFilters caps how many filter predicates one request may carry, which
	// bounds the cost of a single query. Zero means the package default.
	MaxFilters int
	// Tag groups the resource's operations in the OpenAPI document. Defaults
	// to the table name.
	Tag string
}

// Index is a secondary index.
type Index struct {
	Name    string
	Columns []string
	Unique  bool
	Method  string // "btree", "gin", ...; empty means the dialect default
	Where   string // optional partial-index predicate
}

// Check is a table-level check constraint.
type Check struct {
	Name string
	Expr string
}

// TableDef is a table declaration. Build one with Table, which also registers
// it in the default registry.
type TableDef struct {
	name    string // storage name, including any module prefix
	local   string // name as declared, without the prefix
	module  string
	comment string
	pkName  string
	fields  []*Field
	indexes []Index
	checks  []Check
	rest    *REST
}

// Table declares a table and registers it in the default registry. This is the
// form a schema file uses.
func Table(name string, specs ...FieldSpec) *TableDef {
	return defaultRegistry.Table(name, specs...)
}

// Table declares a table in a specific registry. Use it to keep a schema
// isolated from the default one, which is mainly what tests want.
func (r *Registry) Table(name string, specs ...FieldSpec) *TableDef {
	t := &TableDef{name: r.Qualify(name), local: name, module: r.module}
	for _, s := range specs {
		if s == nil {
			continue
		}
		t.fields = append(t.fields, s.fields()...)
	}
	// An external reference exists to be joined on, so it carries an index
	// whether or not the declaration named one. The index is added to the
	// table's own list rather than applied invisibly at render time, so it
	// shows up in Indexes, the manifest and the generated DDL like any other.
	for _, f := range t.fields {
		if f.d.indexWanted && !t.hasLeadingIndex(f.d.Name) {
			t.indexes = append(t.indexes, Index{
				Name:    indexName(t.name, []string{f.d.Name}, false),
				Columns: []string{f.d.Name},
			})
		}
	}
	r.Add(t)
	return t
}

// hasLeadingIndex reports whether a column already leads an index, is unique,
// or is the primary key — the cases Postgres can seek on directly.
func (t *TableDef) hasLeadingIndex(column string) bool {
	for _, f := range t.fields {
		if f.d.Name == column && (f.d.PrimaryKey || f.d.Unique) {
			return true
		}
	}
	for _, idx := range t.indexes {
		if len(idx.Columns) > 0 && idx.Columns[0] == column {
			return true
		}
	}
	return false
}

// Name is the table's storage name, including any module prefix. This is the
// name that reaches SQL.
func (t *TableDef) Name() string { return t.name }

// LocalName is the name as declared, without the module prefix.
func (t *TableDef) LocalName() string { return t.local }

// Module is the owning module name, or "" if the table is not in one.
func (t *TableDef) Module() string { return t.module }

// Fields returns the table's columns in declaration order.
func (t *TableDef) Fields() []*Field { return t.fields }

// Field returns the named column, or nil.
func (t *TableDef) Field(name string) *Field {
	for _, f := range t.fields {
		if f.d.Name == name {
			return f
		}
	}
	return nil
}

// PrimaryKey returns the primary key column, or nil if the table declares none.
func (t *TableDef) PrimaryKey() *Field {
	for _, f := range t.fields {
		if f.d.PrimaryKey {
			return f
		}
	}
	return nil
}

// Relations returns the table's reference columns.
func (t *TableDef) Relations() []*Field {
	var out []*Field
	for _, f := range t.fields {
		if f.d.Ref != nil {
			out = append(out, f)
		}
	}
	return out
}

// Indexes returns the declared secondary indexes.
func (t *TableDef) Indexes() []Index { return t.indexes }

// Checks returns the declared check constraints.
func (t *TableDef) Checks() []Check { return t.checks }

// Rest returns the REST exposure, or nil if the table is not exposed.
func (t *TableDef) Rest() *REST { return t.rest }

// Comment returns the table description.
func (t *TableDef) Comment() string { return t.comment }

// PrimaryKeyName returns the pinned primary key constraint name, if any.
func (t *TableDef) PrimaryKeyName() string { return t.pkName }

// PrimaryKeyNamed pins the primary key constraint name, for adopting an
// existing database whose constraint is not called <table>_pkey.
func (t *TableDef) PrimaryKeyNamed(name string) *TableDef {
	t.pkName = name
	return t
}

// Index adds a secondary index over the given columns.
func (t *TableDef) Index(columns ...string) *TableDef {
	t.indexes = append(t.indexes, Index{
		Name:    indexName(t.name, columns, false),
		Columns: columns,
	})
	return t
}

// UniqueIndex adds a composite unique index.
func (t *TableDef) UniqueIndex(columns ...string) *TableDef {
	t.indexes = append(t.indexes, Index{
		Name:    indexName(t.name, columns, true),
		Columns: columns,
		Unique:  true,
	})
	return t
}

// AddIndex adds a fully specified index, for cases the shorthands do not cover
// such as GIN indexes or partial indexes.
func (t *TableDef) AddIndex(idx Index) *TableDef {
	if idx.Name == "" {
		idx.Name = indexName(t.name, idx.Columns, idx.Unique)
	}
	t.indexes = append(t.indexes, idx)
	return t
}

// Check adds a table-level check constraint.
func (t *TableDef) Check(name, expr string) *TableDef {
	t.checks = append(t.checks, Check{Name: name, Expr: expr})
	return t
}

// Describe attaches a table description, emitted into DDL and OpenAPI.
func (t *TableDef) Describe(s string) *TableDef {
	t.comment = s
	return t
}

// Expose publishes the table over HTTP. Without this call the table is
// reachable from Go code but has no REST surface at all.
func (t *TableDef) Expose(r REST) *TableDef {
	if r.Path == "" {
		// The URL uses the local name: a module prefix is a storage concern,
		// and leaking it into the API would make moving a table between
		// modules a breaking API change.
		r.Path = "/" + t.local
	}
	if r.Tag == "" {
		r.Tag = t.local
	}
	t.rest = &r
	return t
}

func indexName(table string, columns []string, unique bool) string {
	kind := "idx"
	if unique {
		kind = "uniq"
	}
	return table + "_" + strings.Join(columns, "_") + "_" + kind
}
