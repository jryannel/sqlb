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

// Reads is the read-only exposure: generated reads, hand-written writes.
//
// The peer of CRUD, and the shape an application adopting sqlb into an
// existing REST surface reaches for — it already has its writes, and the
// reasons they stay hand-written are domain reasons that do not expire. See
// [rest.Reads] for the worked version of why (issue #101).
const Reads = OpRead | OpList

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

	// Opclasses names the operator class each column is indexed under, keyed by
	// column name. An absent entry takes the type's default.
	//
	// For most indexes an operator class is a tuning decision. For some it is
	// the whole meaning: pgvector's `hnsw` has *no* default class, because the
	// class is what selects the distance function, so an index emitted without
	// one is rejected outright —
	//
	//	ERROR: data type vector has no default operator class for access method "hnsw"
	//
	// — and a schema that could not express it could not describe its own
	// database (issue #53).
	//
	//	AddIndex(schema.Index{
	//	    Name:      "idx_chunks_embedding",
	//	    Columns:   []string{"embedding"},
	//	    Method:    "hnsw",
	//	    Opclasses: map[string]string{"embedding": "vector_cosine_ops"},
	//	    With:      map[string]string{"m": "16", "ef_construction": "64"},
	//	})
	Opclasses map[string]string

	// With is the index's storage parameters — `WITH (m = 16)`. Rendered in
	// sorted key order, because a map has none and a migration that reorders
	// its own DDL between runs is a diff nobody can read.
	With map[string]string

	// Orders names the sort order each column is indexed under, keyed by column
	// name, in the same shape Opclasses uses and for the same reason: the DDL
	// layer renders it without knowing anything about index position.
	//
	// An absent entry is ascending with Postgres's default null placement,
	// which is what almost every index wants. It is here because for the
	// indexes that do not, the ordering *is* the index — an index backing
	// `ORDER BY position ASC NULLS FIRST, created_at DESC` is unusable in any
	// other order — and a declaration that could not say so proposed dropping
	// the live index and could not tell "missing" from "differently ordered"
	// (issue #64).
	//
	//	AddIndex(schema.Index{
	//	    Name:    "idx_tasks_project_position",
	//	    Columns: []string{"project_id", "position", "created_at"},
	//	    Orders: map[string]schema.IndexOrder{
	//	        "position":   {Nulls: schema.NullsFirst},
	//	        "created_at": {Desc: true},
	//	    },
	//	})
	Orders map[string]IndexOrder
}

// IndexOrder is one column's sort order within an index.
//
// Structured rather than written SQL, because a written suffix would have to
// reproduce Postgres's normalisation to compare equal — it omits ASC, and omits
// the null placement that follows from the direction — and that is the failure
// mode issue #63 is about. A zero IndexOrder means ascending with the default
// placement, so a map entry is only ever needed for a column that departs from
// it.
type IndexOrder struct {
	Desc  bool
	Nulls Nulls
}

// Nulls is where NULLs sort within one index column. The zero value follows
// Postgres's own default, which is not a single placement: NULLS LAST for
// ascending, NULLS FIRST for descending.
type Nulls string

const (
	NullsDefault Nulls = ""
	NullsFirst   Nulls = "first"
	NullsLast    Nulls = "last"
)

// Suffix renders the order as the DDL fragment that follows the column, empty
// when the order is the one Postgres assumes.
//
// Normalised the way Postgres normalises: an explicit ASC is dropped, and so is
// a null placement that already follows from the direction. That is what makes
// two spellings of the same order compare equal, and it is why Suffix is also
// what the diff fingerprints — a declaration written `{Desc: true, Nulls:
// NullsFirst}` and one written `{Desc: true}` are the same index, and reading
// the second back from the catalog must not propose replacing the first.
func (o IndexOrder) Suffix() string {
	var out string
	if o.Desc {
		out = " DESC"
	}
	switch {
	case o.Nulls == NullsFirst && !o.Desc:
		out += " NULLS FIRST"
	case o.Nulls == NullsLast && o.Desc:
		out += " NULLS LAST"
	}
	return out
}

// Check is a table-level check constraint.
type Check struct {
	Name string
	Expr string
}

// Unique is a table-level UNIQUE constraint over one or more columns — the
// table-level peer of Field.Unique().
//
// It is a different object from a unique index, and the difference is not
// cosmetic: only a constraint can be the target of
// FOREIGN KEY … REFERENCES t (a, b) or be named in ON CONFLICT ON CONSTRAINT.
// Declaring one where the database has the other produces a migration that
// drops and rebuilds, which on a live table is the expensive kind.
type Unique struct {
	Name    string
	Columns []string
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
	uniques []Unique
	rest    *REST
	actions []Action
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
	r.Add(t)
	return t
}

// implicitIndexes are the indexes a column asked for without naming one — today
// only an external reference, which exists to be joined on and scans the table
// without one.
//
// They are resolved when the index set is read rather than appended here, and
// that ordering is the whole of it: everything a declaration says about a table
// after Table() returns — .Index("org_id"), .UniqueIndex("org_id", "code"), an
// index introspect read back out of the database — arrives later. Deciding
// earlier meant deciding against an empty list, so a table that went on to
// declare an index on the same column ended up with two indexes of the same
// name, and a registry introspect built carried an index the database does not
// have (issues #54 and #55, found by the drift gate).
func (t *TableDef) implicitIndexes() []Index {
	var out []Index
	for _, f := range t.fields {
		if f.d.indexWanted && !t.hasLeadingIndex(f.d.Name) {
			out = append(out, Index{
				Name:    indexName(t.name, []string{f.d.Name}, false),
				Columns: []string{f.d.Name},
			})
		}
	}
	return out
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

// Fields returns the table's columns in declaration order, computed ones
// included: they are columns to every consumer that describes the row — the
// model, the clients, the CLI, the OpenAPI document.
func (t *TableDef) Fields() []*Field { return t.fields }

// StoredFields returns the columns the database actually holds.
//
// It is what the DDL and the diff read, and the only distinction either of them
// has to make about a computed column: an expression has no type to declare, no
// default to write and no ALTER to propose, so a migration that saw one would
// propose creating a column that must not exist and then propose dropping it
// again on the next run (ADR-0041).
func (t *TableDef) StoredFields() []*Field {
	out := make([]*Field, 0, len(t.fields))
	for _, f := range t.fields {
		if f.d.Computed() {
			continue
		}
		out = append(out, f)
	}
	return out
}

// Field returns the named column, or nil.
func (t *TableDef) Field(name string) *Field {
	for _, f := range t.fields {
		if f.d.Name == name {
			return f
		}
	}
	return nil
}

// StoredField returns the named column if the database holds it, and nil for a
// computed one — which is what a migration wants: turning a stored column into
// a computed one means the storage goes away, and a diff that saw the
// declaration would leave the old column behind forever.
func (t *TableDef) StoredField(name string) *Field {
	if f := t.Field(name); f != nil && !f.d.Computed() {
		return f
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

// Indexes returns the table's secondary indexes: the declared ones, and the
// implicit index an external reference asks for when nothing else already
// covers its column.
func (t *TableDef) Indexes() []Index {
	implicit := t.implicitIndexes()
	if len(implicit) == 0 {
		return t.indexes
	}
	// Implicit first, which is where they were when Table added them, so the
	// order of a generated migration does not depend on when this moved.
	return append(implicit, t.indexes...)
}

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

// Unique adds a composite UNIQUE constraint, named the way Postgres names one
// itself: secrets_tenant_kind_tenant_id_name_key.
//
//	t.Unique("tenant_kind", "tenant_id", "name")
//
// # Why this is not UniqueIndex
//
// [TableDef.UniqueIndex] renders CREATE UNIQUE INDEX, which enforces the same
// rule through a different object. Two of those differences are load-bearing:
// a unique index cannot be the target of FOREIGN KEY … REFERENCES t (a, b),
// and it cannot be named in ON CONFLICT ON CONSTRAINT. `UNIQUE (a, b)` written
// inline in CREATE TABLE is also what a hand-written migration reaches for by
// default, so a database being adopted usually has the constraint.
//
// Declaring the index where the database has the constraint is therefore not a
// near-miss that diffs to nothing. It diffs to a drop and a rebuild, which is a
// real migration on live data forced by the declaration language rather than by
// anything the schema needs (issue #108).
//
// Use [TableDef.UniqueNamed] when the live name does not follow the
// convention — which for a constraint an application may be matching on by
// name, the same way [TableDef.IndexNamed] describes.
func (t *TableDef) Unique(columns ...string) *TableDef {
	t.uniques = append(t.uniques, Unique{
		Name:    uniqueConstraintName(t.name, columns),
		Columns: columns,
	})
	return t
}

// UniqueNamed adds a composite UNIQUE constraint under a name you choose,
// rather than the one the convention would derive. See [TableDef.Unique].
func (t *TableDef) UniqueNamed(name string, columns ...string) *TableDef {
	t.uniques = append(t.uniques, Unique{Name: name, Columns: columns})
	return t
}

// Uniques returns the table-level unique constraints.
func (t *TableDef) Uniques() []Unique { return t.uniques }

// uniqueConstraintName derives the name Postgres would have given the
// constraint, which is what makes an adopted database diff to nothing.
func uniqueConstraintName(table string, columns []string) string {
	return table + "_" + strings.Join(columns, "_") + "_key"
}

// ReplaceIndexWhere rewrites a partial index's predicate, for the same reason
// and by the same caller as ReplaceCheckExpr below.
//
// A partial-index predicate is stored the way a CHECK is — as a parse tree,
// rendered back by pg_get_expr — so `latitude IS NOT NULL` comes back as
// `(latitude IS NOT NULL)` and a declaration written the obvious way never
// matches the live index. The diff then proposes creating an index that is
// already there, with DDL that looks identical to what the database holds
// (issue #63).
//
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
// shadow.Normalize does, and this is how it writes the answer back.
// Comparing the two spellings textually instead was rejected: stripping
// parentheses can make two genuinely different expressions look equal, and a
// diff that reports "unchanged" for a changed constraint is silently wrong,
// where churn is merely loud.
func (t *TableDef) ReplaceIndexWhere(name, expr string) bool {
	for i := range t.indexes {
		if t.indexes[i].Name == name {
			t.indexes[i].Where = expr
			return true
		}
	}
	return false
}

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
