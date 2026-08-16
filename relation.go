package sqlb

import (
	"fmt"
	"reflect"
	"strconv"
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

// The reverse direction is the same two words with the column on the other
// side, and the cardinality of the field is what says so:
//
//	Tasks *sqlb.Collection[Task] `db:"-" json:"tasks,omitempty" sqlb:"expands=list_id,order=-created_at,limit=50"`
//
// `expands=` names a column either way; whose column it is follows from the
// type. A struct means a column of mine, a Collection means a column of theirs.
// A second keyword — `collects=`, `hasmany=` — would restate what the type
// already says, and two statements of one fact can disagree.
//
// One asymmetry is deliberate. A forward relation requires the `expand`
// capability on its own column as well as the field, because that capability is
// what puts the relation in this resource's vocabulary. A reverse relation
// requires nothing of the column it joins on, because that column belongs to
// another table whose capabilities describe another endpoint. The field's
// existence is the whole opt-in, and codegen only emits it from an explicit
// `.InverseExpandable()`. ADR-0022 records why.

// defaultExpandLimit caps a reverse expansion that does not declare one. Past
// it the caller follows the child's own endpoint, filtered by the foreign key,
// which is paging and filtering that already exist.
const defaultExpandLimit = 50

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
	// Elem is the struct type behind the field, with any pointer removed. For
	// a Collection it is the child type, not the Collection itself.
	Elem reflect.Type
	// FK is the column joined on: a column of this model for a forward
	// relation, and a column of the target for a collection. It is resolved
	// with the target for a collection, so it is nil until Target has run.
	FK *ColumnInfo

	// Collection reports that this relation is the reverse direction — many
	// rows of the target pointing back at one row of this model.
	Collection bool
	// Reverse reports that this relation's foreign key lives on the target,
	// not on this row — true for both a capped Collection and a one-to-one
	// reverse relation. Collection narrows it further to "and there may be
	// more than one". FK resolution and the query compiler's join direction
	// both key off Reverse; only the capped-envelope machinery keys off
	// Collection specifically.
	Reverse bool
	// Order is the child column a collection is ordered by, with the target's
	// primary key appended as a tiebreaker. Empty means the primary key alone.
	// Under a cap, a non-total order does not reshuffle the result, it decides
	// which children the caller never sees — see ADR-0027 and ADR-0022.
	Order     string
	OrderDesc bool
	// Limit caps a collection. Zero means defaultExpandLimit.
	Limit int

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
		if r.err != nil || !r.Reverse {
			return
		}
		// A reverse relation — a collection or a one-to-one reverse — joins on
		// a column of the *target*, so it cannot be resolved while this model
		// is being built — the target may expand back, and building it then
		// would recurse. It resolves here instead, with the target, the first
		// time anything asks to expand.
		col := r.target.Column(r.fkName)
		if col == nil {
			r.err = fmt.Errorf(
				"sqlb: field %s expands %q, which is not a column of %s (have: %s)",
				r.Field, r.fkName, r.target.Type.Name(),
				strings.Join(r.target.ColumnNames(), ", "))
			return
		}
		r.FK = col

		// The order column is checked here for the same reason the foreign key
		// is: it names a column of the target, which only exists once the
		// target is resolved. Left unchecked, `order=craeted_at` builds fine
		// and fails at the database on the first expansion — which is the one
		// thing this package tries never to do with a name it could have read.
		if r.Order != "" && r.target.Column(strings.TrimPrefix(r.Order, "-")) == nil {
			r.err = fmt.Errorf(
				"sqlb: field %s orders its expansion by %q, which is not a column of %s (have: %s)",
				r.Field, r.Order, r.target.Type.Name(),
				strings.Join(r.target.ColumnNames(), ", "))
			return
		}
	})
	return r.target, r.err
}

// clone copies the declaration and drops the resolution, for Model.clone.
//
// The lazily resolved target is deliberately not carried across. A sync.Once
// cannot be copied, and there is nothing to gain by trying: a relation belongs
// to exactly one Model, so the copy re-resolves on first use and the original
// keeps serving whoever still holds it. That is also what keeps the two from
// sharing the mutable half of this struct, which is the whole point of copying
// the model rather than writing through it.
//
// A forward relation's foreign key is rebased through remap rather than looked
// up by name, so it follows its column through a Describe rename. A reverse
// relation's — a collection's or a one-to-one reverse's — belongs to the
// *target* model and is resolved with the target inside Target, so there is
// nothing to rebase and leaving it nil is the unresolved state the copy
// should start in.
func (r *RelationInfo) clone(remap map[*ColumnInfo]*ColumnInfo) *RelationInfo {
	c := &RelationInfo{
		Name:       r.Name,
		Field:      r.Field,
		Index:      r.Index,
		Elem:       r.Elem,
		Collection: r.Collection,
		Reverse:    r.Reverse,
		Order:      r.Order,
		OrderDesc:  r.OrderDesc,
		Limit:      r.Limit,
		fkName:     r.fkName,
	}
	if !r.Reverse {
		c.FK = remap[r.FK]
	}
	return c
}

// Cap reports how many children this relation returns at most.
func (r *RelationInfo) Cap() int {
	if r.Limit > 0 {
		return r.Limit
	}
	return defaultExpandLimit
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

// relationTag is the `sqlb` tag of a relation field: the column it expands,
// for a collection the order and cap that decide which children are
// returned, and for a one-to-one reverse relation the `reverse` token that
// says the column belongs to the target, not to this row.
type relationTag struct {
	fk      string
	order   string
	desc    bool
	limit   int
	reverse bool
}

// expansionOf reads the `expands=<column>` capability, which marks a field as
// holding an expanded row rather than a value of its own, along with the
// options only a collection uses.
func expansionOf(tag string) (relationTag, bool, error) {
	var rt relationTag
	found := false
	for _, part := range strings.Split(tag, ",") {
		part = strings.TrimSpace(part)
		switch {
		case strings.HasPrefix(part, "expands="):
			rt.fk = strings.TrimSpace(strings.TrimPrefix(part, "expands="))
			found = true
		case strings.HasPrefix(part, "order="):
			col := strings.TrimSpace(strings.TrimPrefix(part, "order="))
			// The `-` prefix is the descending marker the sort grammar
			// already uses, so a schema and a URL spell it the same way.
			rt.order, rt.desc = strings.TrimPrefix(col, "-"), strings.HasPrefix(col, "-")
		case strings.HasPrefix(part, "limit="):
			n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(part, "limit=")))
			if err != nil || n <= 0 {
				return rt, false, fmt.Errorf(
					"sqlb: %q is not a usable expansion limit: want a positive whole number", part)
			}
			rt.limit = n
		case part == "reverse":
			// A one-to-one reverse relation: the field is a plain struct (not
			// a Collection), but the foreign key still lives on the target,
			// not on this row — the same join direction a collection uses,
			// without the cap or the has_more envelope.
			rt.reverse = true
		}
	}
	return rt, found, nil
}

// newRelation builds a relation from the struct field carrying it. The foreign
// key is not resolved here; see RelationInfo.fkName.
func newRelation(sf reflect.StructField, index []int, rt relationTag) (*RelationInfo, error) {
	if rt.fk == "" {
		return nil, fmt.Errorf("sqlb: field %s declares `expands=` with no column name", sf.Name)
	}

	r := &RelationInfo{
		Name:      relationName(sf),
		Field:     sf.Name,
		Index:     index,
		Order:     rt.order,
		OrderDesc: rt.desc,
		Limit:     rt.limit,
		fkName:    rt.fk,
	}

	// The field's cardinality is the declaration of which side the column is
	// on, so it is read before anything else about the type.
	if elem, isCollection := collectionElem(sf.Type); isCollection {
		r.Collection = true
		r.Reverse = true
		r.Elem = elem
		return r, nil
	}

	if rt.reverse {
		if rt.order != "" || rt.limit != 0 {
			return nil, fmt.Errorf(
				"sqlb: field %s expands %q with reverse and declares order or limit, which only a "+
					"capped collection uses; a one-to-one reverse relation has no order to break ties "+
					"in and nothing to cap",
				sf.Name, rt.fk)
		}
		r.Reverse = true
		elem := sf.Type
		if elem.Kind() == reflect.Pointer {
			elem = elem.Elem()
		}
		if elem.Kind() != reflect.Struct {
			return nil, fmt.Errorf(
				"sqlb: field %s expands %q with reverse but is %s, want a struct or a pointer to one",
				sf.Name, rt.fk, sf.Type.Kind())
		}
		r.Elem = elem
		return r, nil
	}

	if rt.order != "" || rt.limit != 0 {
		return nil, fmt.Errorf(
			"sqlb: field %s expands %q and declares order or limit, which only a collection uses; "+
				"make it a *sqlb.Collection[T] to expand the reverse direction, or drop the options",
			sf.Name, rt.fk)
	}

	elem := sf.Type
	if elem.Kind() == reflect.Pointer {
		elem = elem.Elem()
	}
	if elem.Kind() != reflect.Struct {
		return nil, fmt.Errorf(
			"sqlb: field %s expands %q but is %s, want a struct, a pointer to one, or a *sqlb.Collection[T]",
			sf.Name, rt.fk, sf.Type.Kind())
	}
	r.Elem = elem
	return r, nil
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
		// A reverse relation's column — a collection's or a one-to-one
		// reverse's — belongs to the target, and resolving it here would mean
		// building the target's model in the middle of building this one.
		// RelationInfo.Target does it instead, lazily and once.
		if r.Reverse {
			continue
		}
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
