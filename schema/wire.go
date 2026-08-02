package schema

import "strings"

// WireCase is how a column's name is spelled on the wire.
//
// It is a property of the schema, not of a column. ADR-0036's decision is that
// there is exactly *one* spelling, so that the JSON body, the OpenAPI document,
// the filter grammar's parameter names and both generated clients cannot
// disagree — filter parameters are column names by construction, so a second
// spelling breaks one of the five surfaces. That decision is unchanged. What the
// 2026-08-02 amendment changed is that the one spelling is a declared *function*
// of the column name rather than the identity function (issue #116).
//
// There is deliberately no per-field override. A per-column mapping is the part
// with a reason to drift, and it is what makes a generated client's contents
// depend on configuration — every guarantee ADR-0028 makes is about the client
// having no contents that can be wrong.
type WireCase string

const (
	// Verbatim spells a column on the wire exactly as the database spells it.
	// The default, and what sqlb has always done.
	Verbatim WireCase = ""

	// Camel spells created_at as createdAt across every surface at once.
	//
	// For an application whose front end is camelCase throughout, the
	// alternative was renaming the columns — and camelCase identifiers in
	// Postgres are reachable only double-quoted, which breaks every
	// hand-written query, psql session and pg_dump a human reads, permanently.
	// Choosing the wire spelling is reversible; renaming the columns is not.
	Camel WireCase = "camel"
)

// WireName spells one column name in this case.
//
// Pure, total, and the only place the transformation lives: every surface calls
// this rather than reproducing it, which is what keeps "there is one spelling"
// true by construction rather than by five packages agreeing.
func (c WireCase) WireName(column string) string {
	switch c {
	case Camel:
		return toCamel(column)
	default:
		return column
	}
}

// ColumnName is WireName's inverse: the column a wire name refers to.
//
// It exists so that the round trip can be *checked* rather than assumed —
// see [Registry.Validate], which refuses a schema holding a column this cannot
// recover. Nothing at runtime calls it: the request path is handed each
// column's wire name as data and never computes one (ADR-0036's amendment).
func (c WireCase) ColumnName(wire string) string {
	switch c {
	case Camel:
		return toSnake(wire)
	default:
		return wire
	}
}

// toCamel turns created_at into createdAt. A leading underscore is preserved,
// because a column named _internal is one whose name starts that way and
// dropping it would collide two columns onto one wire name.
func toCamel(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	upper := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '_' {
			// Leading underscores are structural rather than separators.
			if b.Len() == 0 {
				b.WriteByte(c)
				continue
			}
			upper = true
			continue
		}
		if upper {
			b.WriteByte(upperByte(c))
			upper = false
			continue
		}
		b.WriteByte(c)
	}
	// A trailing underscore has nothing to capitalise and would vanish, which
	// the round-trip check below catches; written back out so it does not.
	if upper {
		b.WriteByte('_')
	}
	return b.String()
}

// toSnake turns createdAt into created_at.
func toSnake(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 4)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			if b.Len() > 0 {
				b.WriteByte('_')
			}
			b.WriteByte(c - 'A' + 'a')
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

func upperByte(c byte) byte {
	if c >= 'a' && c <= 'z' {
		return c - 'a' + 'A'
	}
	return c
}

// WireCase sets how this registry's columns are spelled on the wire, and
// returns the registry so a declaration can chain.
//
//	var Module = schema.NewModule("app").WireCase(schema.Camel)
//
// One setting for the whole schema, applied identically at every surface. See
// [WireCase] for why it is not per column, and [Registry.Validate] for what
// happens to a column the chosen case cannot round-trip.
func (r *Registry) WireCase(c WireCase) *Registry {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.wire = c
	return r
}

// Wire returns the registry's wire case.
func (r *Registry) Wire() WireCase {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.wire
}

// SetWireCase sets the wire case on the default registry, for a schema written
// with the package-level [Table].
//
// Call it before declaring tables — or after, since it is read when a surface is
// generated rather than when a table is declared. Either works; putting it at
// the top of the file is how a reader finds it.
func SetWireCase(c WireCase) { defaultRegistry.WireCase(c) }

// validateWireNames refuses a schema whose columns the chosen case cannot
// round-trip.
//
// snake → camel is not total-and-invertible over arbitrary names. It recovers
// created_at → createdAt → created_at and pos_x → posX → pos_x, and it does not
// recover a digit boundary: pos_x_2 → posX2 → pos_x2. A schema carrying one of
// those would ship a client whose parameter name does not name any column.
//
// So it is a build failure, on a schema nobody has deployed, naming the column
// and both spellings — rather than a wrong name in a client somebody has. That
// is the same bar ADR-0016 sets for a guard and the same one the round trip
// holds introspect to: the transformation is either provably safe for this
// schema or refused outright.
//
// Two columns that collide onto one wire name are the other half of the same
// failure and are reported the same way.
func (r *Registry) validateWireNames(report func(table, column, format string, args ...any)) {
	if r.wire == Verbatim {
		return
	}
	for _, t := range r.tables {
		seen := map[string]string{}
		for _, f := range t.fields {
			d := f.Desc()
			wire := r.wire.WireName(d.Name)
			if back := r.wire.ColumnName(wire); back != d.Name {
				report(t.name, d.Name,
					"WireCase(%q) spells this column %q, which reads back as %q — "+
						"the name does not survive the round trip, so a client would ask for a column "+
						"that does not exist. Rename the column, or leave the schema Verbatim",
					r.wire, wire, back)
				continue
			}
			if prev, dup := seen[wire]; dup {
				report(t.name, d.Name,
					"WireCase(%q) spells both %q and this column %q, so one wire name would name two columns",
					r.wire, prev, wire)
				continue
			}
			seen[wire] = d.Name
		}
	}
}
