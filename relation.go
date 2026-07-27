package sqlb

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// Relations: the model side of `?expand`.
//
// An expandable reference is two struct fields working together. The foreign
// key is an ordinary column carrying the `expand` capability:
//
//	ListID string `db:"list_id" json:"list_id" sqlb:"filter,expand"`
//
// and beside it sits the field the expanded row lands in, which is not a column
// at all:
//
//	List *List `db:"-" json:"list,omitempty" sqlb:"expands=list_id"`
//
// Splitting it that way is what keeps expansion from leaking into everything
// else. The relation field is `db:"-"`, so no projection selects it, no insert
// writes it and no update sets it; it exists only for a query that asked for it
// to fill in. And because the target is a Go type rather than a table name, the
// target's own model — its columns, its hidden ones, its primary key — is
// reachable by reflection rather than by a second declaration that could
// disagree with the first.

// RelationInfo describes one expandable reference.
type RelationInfo struct {
	// Name is what `?expand` names it, taken from the field's json tag and
	// falling back to the snake-cased field name. It is deliberately the JSON
	// name: the parameter is part of the wire format, and a client should not
	// have to know the Go field is called `List` when the payload says `list`.
	Name string
	// Field is the Go struct field the expanded row is written to.
	Field string
	// Index is the reflect path to that field.
	Index []int
	// Elem is the struct type behind the field, with any pointer removed.
	Elem reflect.Type
	// FK is the local column joined on.
	FK *ColumnInfo

	// fkName holds the declared foreign key column until it can be resolved,
	// which cannot happen until every column of the model has been collected —
	// the relation field may be declared above the column it names.
	fkName string

	// target is resolved on first use rather than at build time, because a
	// model that expands to a model that expands back would otherwise recurse
	// forever. Nothing needs the target until a query asks to expand.
	once   sync.Once
	target *Model
	err    error
}

// Target returns the model of the expanded type.
//
// Resolved lazily and once. A cycle — two models expandable to each other — is
// fine as long as nothing expands both at the same moment, which the SQL could
// not express anyway.
func (r *RelationInfo) Target() (*Model, error) {
	r.once.Do(func() {
		r.target, r.err = modelOfType(r.Elem)
	})
	return r.target, r.err
}

// Relation returns the named relation, or nil.
func (m *Model) Relation(name string) *RelationInfo {
	for _, r := range m.Relations {
		if r.Name == name {
			return r
		}
	}
	return nil
}

// RelationNames returns every expandable relation name, in declaration order.
func (m *Model) RelationNames() []string {
	out := make([]string, len(m.Relations))
	for i, r := range m.Relations {
		out[i] = r.Name
	}
	return out
}

// expansionOf reads the `expands=<column>` capability, which marks a field as
// holding an expanded row rather than a value of its own.
func expansionOf(tag string) (string, bool) {
	for _, part := range strings.Split(tag, ",") {
		part = strings.TrimSpace(part)
		if rest, found := strings.CutPrefix(part, "expands="); found {
			return strings.TrimSpace(rest), true
		}
	}
	return "", false
}

// newRelation builds a relation from the struct field carrying it. The foreign
// key is not resolved here; see RelationInfo.fkName.
func newRelation(sf reflect.StructField, index []int, fk string) (*RelationInfo, error) {
	if fk == "" {
		return nil, fmt.Errorf("sqlb: field %s declares `expands=` with no column name", sf.Name)
	}

	elem := sf.Type
	if elem.Kind() == reflect.Pointer {
		elem = elem.Elem()
	}
	if elem.Kind() != reflect.Struct {
		return nil, fmt.Errorf(
			"sqlb: field %s expands %q but is %s, want a struct or a pointer to one",
			sf.Name, fk, sf.Type.Kind())
	}

	return &RelationInfo{
		Name:   relationName(sf),
		Field:  sf.Name,
		Index:  index,
		Elem:   elem,
		fkName: fk,
	}, nil
}

// relationName prefers the json tag, so the expand parameter and the response
// property are spelled the same way.
func relationName(sf reflect.StructField) string {
	tag := sf.Tag.Get("json")
	if name, _, _ := strings.Cut(tag, ","); name != "" && name != "-" {
		return name
	}
	return snake(sf.Name)
}

// resolveRelations links each relation to the column it joins on, once every
// column is known.
func resolveRelations(m *Model) error {
	for _, r := range m.Relations {
		col := m.Column(r.fkName)
		if col == nil {
			return fmt.Errorf(
				"sqlb: field %s.%s expands %q, which is not a column of %s (have: %s)",
				m.Type.Name(), r.Field, r.fkName, m.Type.Name(),
				strings.Join(m.ColumnNames(), ", "))
		}
		// The capability and the field have to agree, or the two halves of the
		// declaration describe different intentions and one of them is wrong.
		// Catching it here beats a request being refused for a relation the
		// model plainly has.
		if !col.Expandable {
			return fmt.Errorf(
				"sqlb: field %s.%s expands column %q, but %[3]q does not declare the `expand` capability; "+
					"add it to the column's sqlb tag, or drop the relation field",
				m.Type.Name(), r.Field, r.fkName)
		}
		r.FK = col
	}
	return nil
}
