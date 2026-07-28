package schema

import (
	"fmt"
	"sort"
	"strings"
)

// Lint reports schemas that are correct but operationally unwise.
//
// Validate answers "is this schema well-formed?" and returns errors. Lint
// answers "will this schema behave badly in production?" and returns advice.
// The distinction matters: a table can pass validation completely and still
// expose an unindexed filter that sequential-scans a large table on every
// request, which is the kind of mistake that is invisible in review and obvious
// at three in the morning.
//
// Diagnostics are advisory. Nothing fails because of them, and a schema may
// have good reasons to keep one — a filterable column on a table of twenty rows
// does not need an index.
type Diagnostic struct {
	Rule     string
	Table    string
	Column   string
	Severity Severity
	Message  string
	// Fix is the concrete change that would resolve it, where there is one.
	Fix string
}

// Severity ranks how much a diagnostic should be believed.
type Severity string

const (
	// SeverityWarn is a problem that will very likely bite in production.
	SeverityWarn Severity = "warn"
	// SeverityInfo is worth a look but is often fine.
	SeverityInfo Severity = "info"
)

func (d Diagnostic) String() string {
	loc := d.Table
	if d.Column != "" {
		loc += "." + d.Column
	}
	s := fmt.Sprintf("[%s] %s: %s: %s", d.Severity, d.Rule, loc, d.Message)
	if d.Fix != "" {
		s += "\n    fix: " + d.Fix
	}
	return s
}

// Diagnostics is an ordered set of lint results.
type Diagnostics []Diagnostic

func (ds Diagnostics) String() string {
	parts := make([]string, len(ds))
	for i, d := range ds {
		parts[i] = d.String()
	}
	return strings.Join(parts, "\n")
}

// Warnings returns only the warn-level diagnostics, for callers that want to
// fail a build on those but tolerate info.
func (ds Diagnostics) Warnings() Diagnostics {
	var out Diagnostics
	for _, d := range ds {
		if d.Severity == SeverityWarn {
			out = append(out, d)
		}
	}
	return out
}

// Lint checks the registry.
func (r *Registry) Lint() Diagnostics {
	var out Diagnostics
	add := func(d Diagnostic) { out = append(out, d) }

	for _, t := range r.Tables() {
		indexed := t.indexedColumns()

		for _, f := range t.fields {
			d := f.Desc()

			// A filterable column with no index leading a btree means every
			// request that uses it scans the table.
			if d.Filterable && !d.Hidden && !indexed[d.Name] && !isLowCardinality(d) {
				add(Diagnostic{
					Rule: "unindexed-filter", Table: t.name, Column: d.Name,
					Severity: SeverityWarn,
					Message:  "column is filterable but is not the leading column of any index, so filtering on it scans the table",
					Fix:      fmt.Sprintf("add .Index(%q) to the table, or drop .Filterable() from the column", d.Name),
				})
			}

			// Sorting an unindexed column forces a sort of the whole result
			// set, which pagination makes worse by repeating it per page.
			//
			// The suggested index carries the primary key as a trailing column,
			// because that is the ordering a paged list actually runs: every
			// list ends with the key so the page boundary is unambiguous
			// (ADR-0027), and an index on the sort column alone cannot serve
			// the cursor's seek.
			if d.Sortable && !indexed[d.Name] {
				fix := fmt.Sprintf("add .Index(%q) if this table will grow", d.Name)
				if pk := pkColumn(t); pk != "" && pk != d.Name {
					fix = fmt.Sprintf(
						"add .AddIndex(schema.Index{Columns: []string{%q, %q}}) if this table will grow; "+
							"the key is the second column because a paged list orders by it to break ties",
						d.Name, pk)
				}
				add(Diagnostic{
					Rule: "unindexed-sort", Table: t.name, Column: d.Name,
					Severity: SeverityInfo,
					Message:  "column is sortable but not indexed, so each page re-sorts the matching rows",
					Fix:      fix,
				})
			}

			// Search compiles to ILIKE '%term%', which no btree index can
			// serve. A trigram GIN index is the usual answer.
			if d.Searchable {
				if !hasIndexMethod(t, d.Name, "gin") {
					add(Diagnostic{
						Rule: "search-without-trigram", Table: t.name, Column: d.Name,
						Severity: SeverityWarn,
						Message:  "searchable columns compile to ILIKE '%…%', which no btree index can serve",
						Fix: fmt.Sprintf(
							`add a trigram index: .AddIndex(schema.Index{Columns: []string{%q}, Method: "gin"}) with pg_trgm installed`,
							d.Name),
					})
				}
			}

			// Expanding a relation joins on the foreign key column.
			if d.Expandable && d.Ref != nil && !indexed[d.Name] {
				add(Diagnostic{
					Rule: "unindexed-expand", Table: t.name, Column: d.Name,
					Severity: SeverityWarn,
					Message:  "relation is expandable but its foreign key is not indexed, so expansion joins without one",
					Fix:      fmt.Sprintf("add .Index(%q)", d.Name),
				})
			}

			if d.Ref != nil && d.Ref.InverseExpandable {
				// The reverse direction runs one correlated subquery per base
				// row, so an unindexed foreign key is a sequential scan per row
				// of the page rather than one extra scan for the statement.
				// Same rule as above, a worse consequence.
				if !indexed[d.Name] {
					add(Diagnostic{
						Rule: "unindexed-inverse-expand", Table: t.name, Column: d.Name,
						Severity: SeverityWarn,
						Message: fmt.Sprintf(
							"%q collects these rows, and an expansion runs one subquery per row of the page; without an index on this column each of those scans the table",
							d.Ref.Inverse),
						Fix: fmt.Sprintf("add .Index(%q)", d.Name),
					})
				}
				// An expanded collection is capped, and past the cap the caller
				// is expected to follow this table's own endpoint filtered by
				// this column. If it cannot filter by it, the overflow has
				// nowhere to go and the truncation is a dead end.
				if !d.Filterable {
					add(Diagnostic{
						Rule: "uncapped-inverse-overflow", Table: t.name, Column: d.Name,
						Severity: SeverityWarn,
						Message: fmt.Sprintf(
							"an expansion of %q is capped and reports has_more, but this column is not filterable, so a caller has no way to read the rest",
							d.Ref.Inverse),
						Fix: "add .Filterable() to this column",
					})
				}
			}
		}

		// A table outside any module in a codebase that uses them is usually
		// an oversight, and it is the one that will collide later.
		if t.module == "" && r.module != "" {
			add(Diagnostic{
				Rule: "unnamespaced-table", Table: t.name,
				Severity: SeverityInfo,
				Message:  "table is not in a module, so its name is not namespaced",
			})
		}

		if t.rest == nil {
			continue
		}

		// This used to warn that paging could repeat or skip rows. It cannot
		// any more: filter.Apply appends the primary key to every list, so the
		// ordering is total whether or not the schema offers a way to choose
		// it (ADR-0027). What is left is a usability point rather than a
		// correctness one, so it is an info — a list whose order no client can
		// influence is usually an oversight, not a decision.
		if t.rest.Ops.Has(OpList) && len(capableColumns(t, capSortable)) == 0 {
			add(Diagnostic{
				Rule: "list-without-sort", Table: t.name,
				Severity: SeverityInfo,
				Message: "list endpoint has no sortable column, so every client gets the same " +
					"primary-key order and none can ask for another",
				Fix: "mark at least one column .Sortable(), conventionally created_at",
			})
		}

		// A list endpoint with no filters can only be paged through, which is
		// usually an oversight rather than an intention.
		if t.rest.Ops.Has(OpList) && len(capableColumns(t, capFilterable)) == 0 {
			add(Diagnostic{
				Rule: "list-without-filters", Table: t.name,
				Severity: SeverityInfo,
				Message:  "list endpoint exposes no filterable column, so clients can only page through everything",
			})
		}

		// Without an explicit ceiling the package default applies, which may
		// not be what this table can afford.
		if t.rest.Ops.Has(OpList) && t.rest.MaxPageSize == 0 {
			add(Diagnostic{
				Rule: "no-max-page-size", Table: t.name,
				Severity: SeverityInfo,
				Message:  "no MaxPageSize, so the package default applies as the hard ceiling",
				Fix:      "set MaxPageSize on the REST exposure to a value this table can serve",
			})
		}

		// Writable endpoints on a table with no key cannot address a row.
		if t.rest.Ops.Has(OpCreate) && t.PrimaryKey() == nil {
			add(Diagnostic{
				Rule: "create-without-key", Table: t.name,
				Severity: SeverityWarn,
				Message:  "table accepts creates but has no primary key, so a created row cannot be addressed afterwards",
			})
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			return out[i].Severity == SeverityWarn
		}
		if out[i].Table != out[j].Table {
			return out[i].Table < out[j].Table
		}
		return out[i].Rule < out[j].Rule
	})
	return out
}

// Lint checks the default registry.
func Lint() Diagnostics { return defaultRegistry.Lint() }

// indexedColumns returns the columns that lead an index, are unique, or are the
// primary key — the ones Postgres can seek on directly.
func (t *TableDef) indexedColumns() map[string]bool {
	out := map[string]bool{}
	for _, f := range t.fields {
		d := f.Desc()
		if d.PrimaryKey || d.Unique {
			out[d.Name] = true
		}
	}
	for _, idx := range t.indexes {
		if len(idx.Columns) > 0 {
			// Only the leading column of a btree is seekable on its own.
			out[idx.Columns[0]] = true
		}
	}
	return out
}

func hasIndexMethod(t *TableDef, column, method string) bool {
	for _, idx := range t.indexes {
		if !strings.EqualFold(idx.Method, method) {
			continue
		}
		for _, c := range idx.Columns {
			if c == column {
				return true
			}
		}
	}
	return false
}

type capKind int

const (
	capFilterable capKind = iota
	capSortable
)

func capableColumns(t *TableDef, k capKind) []string {
	var out []string
	for _, f := range t.fields {
		d := f.Desc()
		if d.Hidden {
			continue
		}
		if (k == capFilterable && d.Filterable) || (k == capSortable && d.Sortable) {
			out = append(out, d.Name)
		}
	}
	return out
}

// isLowCardinality reports whether an index would probably not help. A boolean
// or a short enum has too few distinct values for a btree to beat a scan, so
// flagging it as unindexed would be noise.
func isLowCardinality(d *FieldDesc) bool {
	if d.Type == TypeBool {
		return true
	}
	return d.Type == TypeEnum && len(d.EnumValues) > 0 && len(d.EnumValues) <= 4
}

// pkColumn is the primary key's column name, or empty when the table declares
// none. Diagnostics use it to suggest the composite index a paged list wants.
func pkColumn(t *TableDef) string {
	pk := t.PrimaryKey()
	if pk == nil {
		return ""
	}
	return pk.Desc().Name
}
