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
			if d.Sortable && !indexed[d.Name] {
				add(Diagnostic{
					Rule: "unindexed-sort", Table: t.name, Column: d.Name,
					Severity: SeverityInfo,
					Message:  "column is sortable but not indexed, so each page re-sorts the matching rows",
					Fix:      fmt.Sprintf("add .Index(%q) if this table will grow", d.Name),
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
		}

		if t.rest == nil {
			continue
		}

		// Keyset-stable pagination needs a deterministic order. Without a
		// sortable column, page 2 may repeat or skip rows from page 1.
		if t.rest.Ops.Has(OpList) && len(capableColumns(t, capSortable)) == 0 {
			add(Diagnostic{
				Rule: "list-without-sort", Table: t.name,
				Severity: SeverityWarn,
				Message:  "list endpoint has no sortable column, so paging has no deterministic order and may repeat or skip rows",
				Fix:      "mark at least one column .Sortable(), conventionally created_at or the primary key",
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
