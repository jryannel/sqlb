package sqlb

import (
	"fmt"
	"strings"
)

// Describe attaches column metadata to a model at runtime, as an alternative
// to the `sqlb` struct tags that codegen writes.
//
// It exists for two cases. The first is using sqlb without any code generation
// at all. The second, and the more common one, is layering sqlb over structs
// that already exist and that you would rather not edit — the output of another
// generator, or a package you do not own:
//
//	func init() {
//	    sqlb.Describe[Invoice]().
//	        Table("invoices").
//	        PrimaryKey("id").
//	        Defaulted("id", "created_at").
//	        Filterable("customer_id", "paid", "amount_due").
//	        Sortable("created_at", "amount_due").
//	        Hidden("internal_memo")
//	}
//
// Without either tags or a description, the query builder still works — column
// names are derived from field names — but no column is filterable, sortable or
// searchable, so the REST layer rejects every request against it. That is the
// intended default: capabilities are opt-in, and an undescribed model exposes
// nothing.
//
// Descriptions merge onto whatever the tags already said, so a partly tagged
// model can be completed here.
//
// Call it during initialisation, before any query runs. It mutates the cached
// model in place and does not lock, because doing so would put a mutex on the
// read path of every query to pay for something that happens once at startup.
// Calling it after the first statement has been built against the model panics
// rather than racing. Naming a column that does not exist panics too, listing
// the ones that do.
func Describe[T any]() *Description[T] {
	m := ModelOf[T]()
	if m.InUse() {
		// Mutating the cached model now would race against every in-flight
		// query, and a half-applied description is a capability silently
		// missing rather than a visible failure.
		panic(fmt.Sprintf(
			"sqlb: Describe[%s] called after a statement was already built against it; "+
				"describe models during initialisation, before any query runs", m.Type))
	}
	return &Description[T]{m: m}
}

// Description is a set of pending metadata changes to a model.
type Description[T any] struct {
	m *Model
}

// Model returns the model being described, for inspection.
func (d *Description[T]) Model() *Model { return d.m }

// Table overrides the table name, which is otherwise derived from the type
// name or taken from a TableName method.
func (d *Description[T]) Table(name string) *Description[T] {
	d.m.Table = name
	return d
}

// Column overrides the column a Go field maps to, for when the derived
// snake_case name is not the real one and the struct cannot be given a tag.
func (d *Description[T]) Column(field, column string) *Description[T] {
	for _, col := range d.m.Columns {
		if col.Field != field {
			continue
		}
		if other, taken := d.m.byName[column]; taken && other != col {
			panic(fmt.Sprintf("sqlb: cannot map %s.%s to column %q: already mapped from field %s",
				d.m.Type, field, column, other.Field))
		}
		delete(d.m.byName, col.Name)
		col.Name = column
		d.m.byName[column] = col
		return d
	}
	panic(fmt.Sprintf("sqlb: %s has no field %q (fields: %s)",
		d.m.Type, field, strings.Join(d.fieldNames(), ", ")))
}

// PrimaryKey marks the key column. It implies ReadOnly and Filterable, and is
// what lets the REST layer address a single row.
func (d *Description[T]) PrimaryKey(column string) *Description[T] {
	col := d.column("PrimaryKey", column)
	if d.m.PK != nil && d.m.PK != col {
		panic(fmt.Sprintf("sqlb: %s already has primary key %q, cannot also use %q",
			d.m.Type, d.m.PK.Name, column))
	}
	col.PrimaryKey = true
	col.ReadOnly = true
	col.Filterable = true
	d.m.PK = col
	return d
}

// Defaulted marks columns that carry a database default. Inserts omit such a
// column when its Go value is the zero value, so the database fills it instead
// of being handed an empty string or a zero timestamp.
func (d *Description[T]) Defaulted(columns ...string) *Description[T] {
	return d.each("Defaulted", columns, func(c *ColumnInfo) { c.HasDefault = true })
}

// Filterable allows the columns to be used in REST filter expressions.
func (d *Description[T]) Filterable(columns ...string) *Description[T] {
	return d.each("Filterable", columns, func(c *ColumnInfo) { c.Filterable = true })
}

// Sortable allows the columns to appear in ?sort.
func (d *Description[T]) Sortable(columns ...string) *Description[T] {
	return d.each("Sortable", columns, func(c *ColumnInfo) { c.Sortable = true })
}

// Searchable includes the columns in the ?search fan-out. It implies
// Filterable, matching the `search` tag.
func (d *Description[T]) Searchable(columns ...string) *Description[T] {
	return d.each("Searchable", columns, func(c *ColumnInfo) {
		c.Searchable = true
		c.Filterable = true
	})
}

// ReadOnly makes the columns unwritable through REST.
func (d *Description[T]) ReadOnly(columns ...string) *Description[T] {
	return d.each("ReadOnly", columns, func(c *ColumnInfo) { c.ReadOnly = true })
}

// Immutable allows the columns to be set at create time only.
func (d *Description[T]) Immutable(columns ...string) *Description[T] {
	return d.each("Immutable", columns, func(c *ColumnInfo) { c.Immutable = true })
}

// Hidden omits the columns from every REST response, and makes them
// unreachable from a filter, a sort or a projection.
func (d *Description[T]) Hidden(columns ...string) *Description[T] {
	return d.each("Hidden", columns, func(c *ColumnInfo) { c.Hidden = true })
}

// Relation declares an expandable reference: field is the Go field an expanded
// row lands in, and fkColumn is the local column joined on.
//
//	sqlb.Describe[Task]().
//	    Table("tasks").
//	    PrimaryKey("id").
//	    Relation("List", "list_id")
//
// It is the runtime form of the two-field declaration codegen writes, and it
// says in one call what the tags say in two:
//
//	ListID string `db:"list_id" sqlb:"expand"`
//	List   *List  `db:"-"       sqlb:"expands=list_id"`
//
// Which is the reason it needs no agreement check. Split across two tags the
// halves can disagree — a field expanding a column that never declared the
// capability — and the model build refuses that. Here there is one statement of
// one fact, so declaring the relation is what makes the column expandable.
//
// The relation is named by field's json tag, falling back to the snake-cased
// field name, because `?expand` names the relation the way the response spells
// it. The field itself must not be a mapped column: an expanded row is not a
// value of the row it hangs off, and a field cannot be both.
//
// The target's own model — its columns, and which of them are Hidden — comes
// from the Go type, and is resolved on first expansion rather than here, so two
// models expandable to each other do not recurse at startup.
//
// # The reverse direction
//
// A field of type *sqlb.Collection[T] declares the other direction, and then
// fkColumn is a column of T rather than of this model:
//
//	sqlb.Describe[List]().
//	    Table("lists").
//	    PrimaryKey("id").
//	    Relation("Tasks", "list_id", sqlb.ExpandOrder("-created_at"), sqlb.ExpandLimit(20))
//
// The options apply to a collection only, because only a collection is capped
// and only a capped result has to decide which rows it keeps. Passing them to a
// forward relation is refused rather than ignored.
func (d *Description[T]) Relation(field, fkColumn string, opts ...RelationOption) *Description[T] {
	sf, ok := d.m.Type.FieldByName(field)
	if !ok {
		panic(fmt.Sprintf("sqlb: Relation(%q, %q): %s has no such field (fields: %s)",
			field, fkColumn, d.m.Type, strings.Join(d.fieldNames(), ", ")))
	}
	for _, col := range d.m.Columns {
		if col.Field == field {
			panic(fmt.Sprintf(
				"sqlb: Relation(%q, %q): %s.%s is mapped to column %q; "+
					"a relation field holds an expanded row, not a value of its own — tag it `db:\"-\"`",
				field, fkColumn, d.m.Type, field, col.Name))
		}
	}

	rt := relationTag{fk: fkColumn}
	for _, opt := range opts {
		opt(&rt)
	}
	rel, err := newRelation(sf, sf.Index, rt)
	if err != nil {
		panic(err.Error())
	}
	if prev := d.m.Relation(rel.Name); prev != nil {
		panic(fmt.Sprintf("sqlb: Relation(%q, %q): %s already expands %q, from field %s",
			field, fkColumn, d.m.Type, rel.Name, prev.Field))
	}

	// A collection joins on a column of the target, which this description
	// cannot see and must not mark expandable: that column's capabilities
	// describe the target's own endpoint. It resolves with the target instead,
	// on first expansion.
	if !rel.Collection {
		rel.FK = d.column("Relation", fkColumn)
		rel.FK.Expandable = true
	}
	d.m.Relations = append(d.m.Relations, rel)
	return d
}

// RelationOption adjusts a collection expansion declared through Describe. The
// generated form spells the same two things in the struct tag:
// `sqlb:"expands=list_id,order=-created_at,limit=20"`.
type RelationOption func(*relationTag)

// ExpandOrder orders a collection's children, most significant first, with a
// leading "-" for descending — the spelling ?sort already uses. The target's
// primary key is appended as a tiebreaker either way, because under a cap a
// non-total order decides which children the caller never sees.
func ExpandOrder(column string) RelationOption {
	return func(rt *relationTag) {
		rt.order, rt.desc = strings.TrimPrefix(column, "-"), strings.HasPrefix(column, "-")
	}
}

// ExpandLimit caps how many children an expansion returns. The default is 50.
// Past the cap the collection reports HasMore, and the caller follows the
// child's own endpoint filtered by the foreign key.
func ExpandLimit(n int) RelationOption {
	return func(rt *relationTag) { rt.limit = n }
}

// Timestamps is shorthand for the common created_at / updated_at pair:
// database-defaulted, read-only and sortable.
func (d *Description[T]) Timestamps(columns ...string) *Description[T] {
	if len(columns) == 0 {
		columns = []string{"created_at", "updated_at"}
	}
	return d.each("Timestamps", columns, func(c *ColumnInfo) {
		c.HasDefault = true
		c.ReadOnly = true
		c.Sortable = true
	})
}

func (d *Description[T]) each(what string, columns []string, apply func(*ColumnInfo)) *Description[T] {
	for _, name := range columns {
		apply(d.column(what, name))
	}
	return d
}

// column resolves a name, panicking with the available columns if it is not
// one. Failing loudly at startup is the point: a mistyped column here would
// otherwise silently leave a capability off and surface as a 400 much later.
func (d *Description[T]) column(what, name string) *ColumnInfo {
	if col := d.m.Column(name); col != nil {
		return col
	}
	panic(fmt.Sprintf("sqlb: %s(%q): %s has no such column (columns: %s)",
		what, name, d.m.Type, strings.Join(d.m.ColumnNames(), ", ")))
}

func (d *Description[T]) fieldNames() []string {
	out := make([]string, len(d.m.Columns))
	for i, c := range d.m.Columns {
		out[i] = c.Field
	}
	return out
}
