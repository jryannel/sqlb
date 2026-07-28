package schema

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Registry holds a set of table declarations.
//
// A registry is also the unit of module isolation. Independent modules — fx
// modules, or any other arrangement where one package must not import another —
// each declare into their own registry, so two modules may both own a table
// called "events" without colliding.
type Registry struct {
	mu     sync.RWMutex
	module string
	tables []*TableDef
	byName map[string]*TableDef
}

// NewRegistry returns an empty registry. Most schemas use the default one via
// the package-level functions.
func NewRegistry() *Registry {
	return &Registry{byName: make(map[string]*TableDef)}
}

// NewModule returns a registry whose tables are all prefixed with the module
// name, so that table ownership is visible in the database and cannot be
// forgotten:
//
//	var Billing = schema.NewModule("billing")
//	var Invoice = Billing.Table("invoices", …)   // → billing_invoices
//
// The prefix is applied by the registry rather than written into each
// declaration, which is the point: a convention that has to be repeated at
// every call site is a convention that drifts.
//
// Declarations still use the local name, so a table moving between modules
// changes one line.
func NewModule(name string) *Registry {
	if !isIdent(name) {
		panic(fmt.Sprintf("sqlb/schema: module name %q is not a valid SQL identifier prefix", name))
	}
	r := NewRegistry()
	r.module = name
	return r
}

// Module returns the module name, or "" for a registry that is not a module.
func (r *Registry) Module() string { return r.module }

// Qualify renders a local table name as this registry would store it.
func (r *Registry) Qualify(local string) string {
	if r.module == "" {
		return local
	}
	return r.module + "_" + local
}

var defaultRegistry = NewRegistry()

// DefaultRegistry returns the registry that Table populates.
func DefaultRegistry() *Registry { return defaultRegistry }

// Register adds a table to the default registry. Table calls this for you.
func Register(t *TableDef) { defaultRegistry.Add(t) }

// Add registers a table. A duplicate name panics: two tables with the same
// name is an authoring error that would otherwise surface as confusing DDL.
func (r *Registry) Add(t *TableDef) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.byName[t.name]; dup {
		panic(fmt.Sprintf("sqlb/schema: table %q declared twice", t.name))
	}
	r.byName[t.name] = t
	r.tables = append(r.tables, t)
}

// Tables returns every registered table, sorted by name so that generated
// output is deterministic across runs.
func (r *Registry) Tables() []*TableDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*TableDef, len(r.tables))
	copy(out, r.tables)
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// Get returns the named table, or nil.
func (r *Registry) Get(name string) *TableDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byName[name]
}

// Exposed returns the tables with a REST surface, sorted by name.
func (r *Registry) Exposed() []*TableDef {
	var out []*TableDef
	for _, t := range r.Tables() {
		if t.rest != nil {
			out = append(out, t)
		}
	}
	return out
}

// Error is a single schema validation failure, located at a table and
// optionally a column.
type Error struct {
	Table  string
	Column string
	Msg    string
}

func (e Error) Error() string {
	if e.Column != "" {
		return fmt.Sprintf("%s.%s: %s", e.Table, e.Column, e.Msg)
	}
	return fmt.Sprintf("%s: %s", e.Table, e.Msg)
}

// Validate checks the registry for authoring mistakes and returns every
// problem it finds, joined into a single error. Reporting all of them at once
// rather than stopping at the first keeps the edit-generate loop short.
func (r *Registry) Validate() error {
	var errs []error
	report := func(table, column, format string, args ...any) {
		errs = append(errs, Error{Table: table, Column: column, Msg: fmt.Sprintf(format, args...)})
	}

	// A rename hint claims that a name is gone, so two tables cannot claim the
	// same one and no table may claim a name that is still declared.
	renamedTables := make(map[string]string)

	// Inverse names are claimed on the *target's* endpoint, so the collision
	// this catches is between two references declared in different tables.
	// Keyed by target table and name; the value is where it was claimed from.
	inverses := make(map[string]string)

	for _, t := range r.Tables() {
		if !isIdent(t.name) {
			report(t.name, "", "table name is not a valid SQL identifier")
		}
		if old := t.oldName; old != "" {
			switch {
			case !isIdent(old):
				report(t.name, "", "RenamedFrom %q is not a valid SQL identifier", old)
			case old == t.name:
				report(t.name, "", "RenamedFrom names the table itself")
			case r.Get(old) != nil:
				report(t.name, "", "RenamedFrom %q is still declared as a table of its own; a rename means the old name is gone", old)
			}
			if prev, dup := renamedTables[old]; dup {
				report(t.name, "", "RenamedFrom %q is also claimed by table %q", old, prev)
			}
			renamedTables[old] = t.name
		}

		seen := make(map[string]bool, len(t.fields))
		renamedCols := make(map[string]string)
		pks := 0
		scoped := 0
		for _, f := range t.fields {
			d := f.Desc()
			if !isIdent(d.Name) {
				report(t.name, d.Name, "column name is not a valid SQL identifier")
			}
			if seen[d.Name] {
				report(t.name, d.Name, "column declared twice")
			}
			seen[d.Name] = true

			if old := d.RenamedFrom; old != "" {
				switch {
				case !isIdent(old):
					report(t.name, d.Name, "RenamedFrom %q is not a valid SQL identifier", old)
				case old == d.Name:
					report(t.name, d.Name, "RenamedFrom names the column itself")
				case t.Field(old) != nil:
					// Either the hint is wrong, or the two columns are being
					// swapped — which Postgres cannot do in one statement
					// either, and which a generator should not attempt.
					report(t.name, d.Name, "RenamedFrom %q is still declared as a column of its own; a rename means the old name is gone", old)
				}
				if prev, dup := renamedCols[old]; dup {
					report(t.name, d.Name, "RenamedFrom %q is also claimed by column %q", old, prev)
				}
				renamedCols[old] = d.Name
			}

			if d.PrimaryKey {
				pks++
				if d.Nullable {
					report(t.name, d.Name, "primary key cannot be Nullable")
				}
				if d.Hidden {
					report(t.name, d.Name, "primary key cannot be Hidden: REST responses need it to address the row")
				}
			}
			if d.Scoped {
				scoped++
				// A tenant column a request may write is not a tenant column:
				// the create body would carry it, and the caller would choose
				// which tenant to write into. ReadOnly keeps it out of the
				// generated bodies entirely, which leaves the BeforeCreate
				// hook as the only thing that can supply it. Immutable is not
				// enough — it closes the update and leaves the create open.
				if !d.ReadOnly {
					report(t.name, d.Name, "Scoped column must be ReadOnly, or a create request gets to name the tenant it writes into")
				}
				// A tenant column that may be NULL is scoped by a predicate
				// that cannot match it, so those rows are visible to nobody
				// and, on the day someone writes IS NULL OR = $1, to everybody.
				if d.Nullable {
					report(t.name, d.Name, "Scoped column cannot be Nullable: a row whose tenant is NULL is outside every tenant's predicate")
				}
			}
			if d.Expandable && d.Ref == nil {
				report(t.name, d.Name, "Expandable is only meaningful on a Ref column")
			}
			if d.Searchable && !isTextual(d.Type) {
				report(t.name, d.Name, "Searchable requires a text column, got %s", d.Type)
			}
			if d.Type == TypeEnum && len(d.EnumValues) == 0 {
				report(t.name, d.Name, "Enum declares no values")
			}
			if d.Hidden && d.Filterable {
				report(t.name, d.Name, "column is both Hidden and Filterable, which leaks its contents through filter probing")
			}
			if d.Ref != nil && d.Ref.External {
				if d.Expandable {
					report(t.name, d.Name, "a reference across a module boundary cannot be Expandable: expanding it would join a table this module does not own")
				}
				if d.Ref.Inverse != "" {
					report(t.name, d.Name, "a reference across a module boundary cannot declare an Inverse: nothing about the other side is resolvable, in either direction")
				}
				if d.Ref.Target == "" {
					report(t.name, d.Name, "ExternalRef declares no target")
				}
			}
			if d.Ref != nil && !d.Ref.External {
				switch {
				case d.Ref.Table == nil:
					report(t.name, d.Name, "Ref target is nil (declaration order: the target table var must be initialised first)")
				case r.Get(d.Ref.Table.name) == nil:
					report(t.name, d.Name, "Ref target %q is not registered", d.Ref.Table.name)
				case d.Ref.Table.PrimaryKey() == nil:
					report(t.name, d.Name, "Ref target %q has no primary key", d.Ref.Table.name)
				}
			}
			if d.Ref != nil {
				r.validateInverse(t, d, inverses, report)
			}
		}

		if pks > 1 {
			report(t.name, "", "%d primary keys declared, expected at most one (use UniqueIndex for composite keys)", pks)
		}
		// Two scope columns would name one hook twice and say nothing more.
		// There is no matching check for soft delete: the group always
		// declares deleted_at, so a second one is already a duplicate column.
		if scoped > 1 {
			report(t.name, "", "%d Scoped columns declared, expected at most one", scoped)
		}

		for _, idx := range t.indexes {
			if len(idx.Columns) == 0 {
				report(t.name, "", "index %q covers no columns", idx.Name)
			}
			for _, c := range idx.Columns {
				if !seen[c] {
					report(t.name, "", "index %q references unknown column %q", idx.Name, c)
				}
			}
			// A derived index name concatenates the table and every column it
			// covers, so a prefixed table with a composite index passes 63
			// bytes without anything looking long. Postgres then truncates
			// silently — even quoted — so the name in the schema and the name
			// in the database differ, and every later diff proposes renaming
			// one to the other forever.
			if len(idx.Name) > maxIdentBytes {
				report(t.name, "", "index name %q is %d bytes; Postgres truncates at %d, "+
					"so give it a shorter Name explicitly", idx.Name, len(idx.Name), maxIdentBytes)
			}
		}

		if t.rest != nil {
			needsPK := t.rest.Ops.Has(OpRead) || t.rest.Ops.Has(OpUpdate) || t.rest.Ops.Has(OpDelete)
			if needsPK && pks == 0 {
				report(t.name, "", "exposed for %s but has no primary key to address rows by", t.rest.Ops)
			}
			if t.rest.Ops == 0 {
				report(t.name, "", "Expose declares no operations")
			}
			if !strings.HasPrefix(t.rest.Path, "/") {
				report(t.name, "", "REST path %q must start with %q", t.rest.Path, "/")
			}
			if t.rest.MaxPageSize < 0 || t.rest.DefaultPageSize < 0 {
				report(t.name, "", "page sizes must not be negative")
			}
			if t.rest.MaxPageSize > 0 && t.rest.DefaultPageSize > t.rest.MaxPageSize {
				report(t.name, "", "DefaultPageSize %d exceeds MaxPageSize %d", t.rest.DefaultPageSize, t.rest.MaxPageSize)
			}
			// A declared soft delete and a generated hard DELETE are a
			// contradiction the runtime cannot resolve: nothing reads
			// deleted_at, so the generated handler removes the row and the
			// column that was supposed to record its removal stays NULL
			// forever. Every other disagreement between a schema and its
			// behaviour in this package is loud; this one silently did the
			// opposite of what the table declares.
			if seen["deleted_at"] && t.rest.Ops.Has(OpDelete) {
				report(t.name, "deleted_at",
					"declares a soft delete but exposes OpDelete, which hard-deletes the row; "+
						"drop OpDelete from Expose and route DELETE to an update of deleted_at")
			}
		}
	}

	// Duplicate REST paths would make routing order-dependent.
	paths := make(map[string]string)
	for _, t := range r.Exposed() {
		if prev, dup := paths[t.rest.Path]; dup {
			report(t.name, "", "REST path %q already used by table %q", t.rest.Path, prev)
			continue
		}
		paths[t.rest.Path] = t.name
	}

	return errors.Join(errs...)
}

// Validate checks the default registry.
func Validate() error { return defaultRegistry.Validate() }

// Tables returns every table in the default registry.
func Tables() []*TableDef { return defaultRegistry.Tables() }

// Get returns the named table from the default registry.
func Get(name string) *TableDef { return defaultRegistry.Get(name) }

func isTextual(t Type) bool {
	switch t {
	case TypeText, TypeVarchar, TypeEnum:
		return true
	}
	return false
}

// isIdent reports whether s is a safe unquoted SQL identifier. The generator
// and the filter parser both rely on this: an identifier that passes here can
// be interpolated into SQL without further escaping.
func isIdent(s string) bool { return CheckIdent(s) == nil }

// CheckIdent reports why the DSL cannot declare a table or column called name,
// or nil when it can.
//
// It is exported for introspection, which reads names a database already has
// rather than names an author chose. Those two are not the same set — a
// camelCase column is legal in Postgres and undeclarable here — and an importer
// needs to say which construct it had to skip, not fail the whole import with a
// message about what the DSL considers impossible.
func CheckIdent(name string) error {
	if name == "" {
		return fmt.Errorf("name is empty")
	}
	if len(name) > maxIdentBytes {
		return fmt.Errorf("name is %d bytes; Postgres truncates identifiers at %d, "+
			"so the declared name and the real one would differ", len(name), maxIdentBytes)
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r == '_':
		case i > 0 && (r >= '0' && r <= '9'):
		case r >= 'A' && r <= 'Z':
			return fmt.Errorf("name contains an upper-case letter, which Postgres only " +
				"preserves for a quoted identifier; the DSL declares unquoted names only")
		default:
			return fmt.Errorf("name contains %q, which is not allowed in an unquoted "+
				"identifier", r)
		}
	}
	return nil
}

// maxIdentBytes is Postgres's NAMEDATALEN-1. An identifier longer than this is
// silently truncated, even when quoted, so a longer declared name would not be
// the name the database ends up holding.
const maxIdentBytes = 63

// validateInverse checks a declared reverse relation.
//
// The collision case is the one this exists for, and it is the reason the name
// cannot be derived: posts.author_id and posts.reviewer_id both point at
// authors, so a derived reverse would call both of them "posts" and an author's
// posts are not the posts an author reviewed. Two references claiming one name
// on one target is therefore an error rather than a last-writer-wins. ADR-0022.
func (r *Registry) validateInverse(t *TableDef, d *FieldDesc, claimed map[string]string, report func(string, string, string, ...any)) {
	ref := d.Ref
	if ref.Inverse == "" {
		if ref.InverseExpandable {
			report(t.name, d.Name, "InverseExpandable without Inverse: a relation with no name on the target cannot be asked for")
		}
		if ref.InverseOrder != "" || ref.InverseLimit != 0 {
			report(t.name, d.Name, "an expansion order or limit was declared without an Inverse to apply it to")
		}
		return
	}
	if ref.External {
		return // already reported, and nothing below can be checked
	}
	if !isIdent(ref.Inverse) {
		report(t.name, d.Name, "Inverse %q is not a valid identifier", ref.Inverse)
	}
	if ref.Table == nil {
		return // already reported
	}

	key := ref.Table.name + "." + ref.Inverse
	if prev, dup := claimed[key]; dup {
		report(t.name, d.Name,
			"Inverse %q is already claimed on %q by %s; two references to one table need two names, since the rows they collect are different sets",
			ref.Inverse, ref.Table.name, prev)
	}
	claimed[key] = t.name + "." + d.Name

	// The name lands as a field on the target, beside its columns.
	if ref.Table.Field(ref.Inverse) != nil {
		report(t.name, d.Name, "Inverse %q collides with a column of %q", ref.Inverse, ref.Table.name)
	}

	if ref.InverseLimit < 0 {
		report(t.name, d.Name, "ExpandLimit is %d, want a positive number", ref.InverseLimit)
	}
	// The order names a column of this table, because these are the rows being
	// collected — not of the target, which is the easy mistake to make.
	if col := strings.TrimPrefix(ref.InverseOrder, "-"); col != "" && t.Field(col) == nil {
		report(t.name, d.Name,
			"ExpandOrder %q is not a column of %q — an expanded collection is ordered by the rows it collects, which are this table's",
			col, t.name)
	}
}

// DefaultExpandLimit is the cap an expanded collection takes when it declares
// none. It mirrors the engine's own default, and sqlb's model test asserts the
// two agree — a schema package that disagreed with the runtime would publish a
// number the responses do not honour.
const DefaultExpandLimit = 50

// InverseRelation is a reverse relation seen from the target's side: the rows
// of another table that point at this one, and the name this table knows them
// by.
//
// It is derived rather than declared here — the declaration lives on the
// referencing column, which is the side that already owns the constraint. What
// the target gains is a field on its generated struct and, if the reference
// exposed it, a name in its ?expand vocabulary. ADR-0022.
type InverseRelation struct {
	Name       string    // the name ?expand uses on the target
	Table      *TableDef // the table whose rows are collected
	Column     string    // that table's foreign key column
	Order      string    // ordering column, with a leading "-" for descending
	Limit      int       // cap as declared; zero means DefaultExpandLimit
	Expandable bool      // reachable through ?expand on the target
}

// Cap is how many rows one expansion returns at most, with the default
// resolved. Anything published — the manifest, a generated tag — uses this
// rather than Limit, so a caller is never left to guess the number.
func (i InverseRelation) Cap() int {
	if i.Limit > 0 {
		return i.Limit
	}
	return DefaultExpandLimit
}

// Inverses returns the reverse relations pointing at t, in a deterministic
// order: by referencing table, then by declaration order within it.
func (r *Registry) Inverses(t *TableDef) []InverseRelation {
	if t == nil {
		return nil
	}
	var out []InverseRelation
	for _, src := range r.Tables() {
		for _, f := range src.Fields() {
			d := f.Desc()
			if d.Ref == nil || d.Ref.Inverse == "" || d.Ref.External || d.Ref.Table != t {
				continue
			}
			out = append(out, InverseRelation{
				Name:       d.Ref.Inverse,
				Table:      src,
				Column:     d.Name,
				Order:      d.Ref.InverseOrder,
				Limit:      d.Ref.InverseLimit,
				Expandable: d.Ref.InverseExpandable,
			})
		}
	}
	return out
}
