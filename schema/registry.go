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
		}

		if pks > 1 {
			report(t.name, "", "%d primary keys declared, expected at most one (use UniqueIndex for composite keys)", pks)
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
func isIdent(s string) bool {
	if s == "" || len(s) > 63 {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r == '_':
		case i > 0 && (r >= '0' && r <= '9'):
		default:
			return false
		}
	}
	return true
}
