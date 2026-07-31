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
	oldName string // previous storage name, from RenamedFrom
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

// RenamedFromName returns the table's previous storage name, or "".
func (t *TableDef) RenamedFromName() string { return t.oldName }

// RenamedFrom declares that this table used to be called local, so that a
// generated migration renames it rather than dropping it and creating a new
// one. See Field.RenamedFrom for why a rename is declared rather than inferred,
// and for how long the hint is needed.
//
// The old name is local, without the module prefix, and is qualified with the
// same prefix as the current one — so this renames a table within a module, not
// between modules. Moving a table between modules changes which registry
// declares it, and is a drop and a create until something asks for otherwise.
func (t *TableDef) RenamedFrom(local string) *TableDef {
	t.oldName = local
	if t.module != "" {
		t.oldName = t.module + "_" + local
	}
	return t
}

// Index adds a secondary index over the given columns, named by convention:
// posts_org_id_idx. Use [TableDef.IndexNamed] when the name matters.
func (t *TableDef) Index(columns ...string) *TableDef {
	t.indexes = append(t.indexes, Index{
		Name:    indexName(t.name, columns, false),
		Columns: columns,
	})
	return t
}

// UniqueIndex adds a composite unique index, named by convention:
// posts_org_id_slug_uniq. Use [TableDef.UniqueIndexNamed] when the name
// matters, which for a unique index it more often does — see below.
func (t *TableDef) UniqueIndex(columns ...string) *TableDef {
	t.indexes = append(t.indexes, Index{
		Name:    indexName(t.name, columns, true),
		Columns: columns,
		Unique:  true,
	})
	return t
}

// IndexNamed adds a secondary index under a name you choose, rather than the
// one the convention would derive.
//
//	t.IndexNamed("idx_projects_org_id", "org_id")
//
// It exists for adopting a database somebody else's tool built. A declared
// index whose name does not match the live one is a rename, and a schema of any
// size turns "declare the tables sqlb already agrees with" into "rename every
// index in the database" — which is a migration nobody asked for, on a database
// where it is the least welcome (issue #57).
//
// # An index name is not always inert
//
// Postgres reports a violated constraint by name, and matching that name is the
// standard way to tell one unique violation from another:
//
//	pgErr.Code == "23505" && pgErr.ConstraintName == "idx_projects_org_code"
//
// So renaming an index can turn a handled collision — retry with the next
// suffix — into an unhandled 500, without touching the code that handles it.
// That is the reason this is a declaration rather than a lint: the schema has
// to be able to say what the name *is*, not merely prefer it.
func (t *TableDef) IndexNamed(name string, columns ...string) *TableDef {
	t.indexes = append(t.indexes, Index{Name: name, Columns: columns})
	return t
}

// UniqueIndexNamed adds a composite unique index under a name you choose. See
// [TableDef.IndexNamed], and note that a unique index is the kind whose name an
// application is most likely to be matching on.
func (t *TableDef) UniqueIndexNamed(name string, columns ...string) *TableDef {
	t.indexes = append(t.indexes, Index{Name: name, Columns: columns, Unique: true})
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

// ReplaceCheckExpr rewrites the expression of an already-declared check, and
// reports whether there was one by that name.
//
// This exists for one caller and it is worth naming, because a setter on a
// declaration is otherwise a smell. Postgres does not store a CHECK expression
// as it was written: it stores a parse tree, and hands back a normalised
// spelling — fully parenthesised, with explicit casts on literals. So a
// registry read back by introspect and a registry declared here disagree about
// every check they have in common, and a diff between them proposes dropping
// and re-adding each one forever (issue #24).
//
// The only reliable way to compare them is to put the declared expression
// through the same normalisation, which means asking a Postgres. That is what
// shadow.NormalizeChecks does, and this is how it writes the answer back.
// Comparing the two spellings textually instead was rejected: stripping
// parentheses can make two genuinely different expressions look equal, and a
// diff that reports "unchanged" for a changed constraint is silently wrong,
// where churn is merely loud.
func (t *TableDef) ReplaceCheckExpr(name, expr string) bool {
	for i := range t.checks {
		if t.checks[i].Name == name {
			t.checks[i].Expr = expr
			return true
		}
	}
	return false
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
