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
// Call it during initialisation, before any query runs, and it panics if you do
// not. That is a diagnostic rather than a lock: describing late is wrong because
// a statement built before the description does not carry it and one built after
// does, which is a difference no call site asked for.
//
// It is not, however, a data race. Each call copies the model, writes the copy
// and publishes it, so the model a statement resolved is never written again.
// A query in flight, or a REST binding that captured the model when it mounted,
// keeps a consistent snapshot of the description that was in force when it
// started. Nothing on the read path locks, which is what this arrangement buys
// that a mutex would not: describing costs a copy, once, at startup, and reading
// costs nothing.
//
// Naming a column that does not exist panics, listing the ones that do — and it
// panics before anything is published, so a description that fails partway leaves
// the model as it was rather than half-applied.
func Describe[T any]() *Description[T] {
	m := ModelOf[T]()
	if m.InUse() {
		panic(fmt.Sprintf(
			"sqlb: Describe[%s] called after a statement was already built against it; "+
				"describe models during initialisation, before any query runs", m.Type))
	}
	return &Description[T]{m: m}
}

// Description is a set of metadata changes to a model.
//
// It holds the most recently published model rather than a pending edit: each
// call publishes, so a chain is a sequence of whole models and a reader always
// sees one of them.
type Description[T any] struct {
	m *Model
}

// Model returns the model as described so far, for inspection.
func (d *Description[T]) Model() *Model { return d.m }

// apply writes fn's changes to a copy of the model and publishes it.
//
// Copying per call rather than once per chain is what keeps every published
// model immutable: the one this call started from may already have been handed
// to a statement. The copies are small and a description happens at startup.
//
// fn panics on a name the model does not have, which is why publishing comes
// after it: a failed description leaves the previous model in place.
func (d *Description[T]) apply(fn func(*Model)) *Description[T] {
	next := d.m.clone()
	fn(next)
	next.publish()
	d.m = next
	return d
}

// Table overrides the table name, which is otherwise derived from the type
// name or taken from a TableName method.
func (d *Description[T]) Table(name string) *Description[T] {
	return d.apply(func(m *Model) { m.Table = name })
}

// Column overrides the column a Go field maps to, for when the derived
// snake_case name is not the real one and the struct cannot be given a tag.
func (d *Description[T]) Column(field, column string) *Description[T] {
	return d.apply(func(m *Model) {
		for _, col := range m.Columns {
			if col.Field != field {
				continue
			}
			if other, taken := m.byName[column]; taken && other != col {
				panic(fmt.Sprintf("sqlb: cannot map %s.%s to column %q: already mapped from field %s",
					m.Type, field, column, other.Field))
			}
			delete(m.byName, col.Name)
			col.Name = column
			m.byName[column] = col
			return
		}
		panic(fmt.Sprintf("sqlb: %s has no field %q (fields: %s)",
			m.Type, field, strings.Join(fieldNamesOf(m), ", ")))
	})
}

// PrimaryKey marks the key column. It implies ReadOnly and Filterable, and is
// what lets the REST layer address a single row.
func (d *Description[T]) PrimaryKey(column string) *Description[T] {
	return d.apply(func(m *Model) {
		col := columnOf(m, "PrimaryKey", column)
		if m.PK != nil && m.PK != col {
			panic(fmt.Sprintf("sqlb: %s already has primary key %q, cannot also use %q",
				m.Type, m.PK.Name, column))
		}
		col.PrimaryKey = true
		col.ReadOnly = true
		col.Filterable = true
		m.PK = col
	})
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

// SortNullsFirst makes the columns sort NULLs before real values, in either
// direction, and marks them sortable.
//
// SortNullsLast is the same the other way. Both exist because Postgres's
// default placement is not one placement but two — NULLS LAST ascending, NULLS
// FIRST descending — so a column whose NULLs mean something ("not published
// yet") reverses its intent when the direction flips. Declaring it here is the
// hand-written half of what `Sortable(schema.NullsLast)` says in a schema.
func (d *Description[T]) SortNullsFirst(columns ...string) *Description[T] {
	return d.each("SortNullsFirst", columns, func(c *ColumnInfo) {
		c.Sortable, c.SortNulls = true, NullsFirst
	})
}

// SortNullsLast makes the columns sort NULLs after real values, in either
// direction, and marks them sortable. See [Description.SortNullsFirst].
func (d *Description[T]) SortNullsLast(columns ...string) *Description[T] {
	return d.each("SortNullsLast", columns, func(c *ColumnInfo) {
		c.Sortable, c.SortNulls = true, NullsLast
	})
}

// SQLType names the columns' Postgres type — "date", "timestamptz", "time" —
// for the cases where the Go type does not determine it.
//
//	d.SQLType("date", "due_on", "invoiced_on")
//
// A generated model carries this in its struct tag and needs no call. A
// hand-written one does, because those three types are all time.Time in Go and
// an expanded row serialises each of them differently: without it, expanding a
// relation whose target has a date column answers 500 (#84).
//
// The name is the schema package's logical type, which is also the Postgres
// one for every type where the two differ only in spelling.
func (d *Description[T]) SQLType(name string, columns ...string) *Description[T] {
	return d.each("SQLType", columns, func(c *ColumnInfo) { c.PGType = name })
}

// Searchable includes the columns in the ?search fan-out. It implies
// Filterable, matching the `search` tag.
//
// A computed column cannot be searchable, and saying so panics rather than
// being ignored — for the reason Computed gives.
func (d *Description[T]) Searchable(columns ...string) *Description[T] {
	// Read before the copy rather than inside it because the callback each
	// hands to apply sees only a column. The type is the one thing a copy
	// cannot change.
	t := d.m.Type
	return d.each("Searchable", columns, func(c *ColumnInfo) {
		if c.Computed() {
			panic(fmt.Sprintf("sqlb: Searchable(%q): %s computes that column; ?search fans out over text columns, not expressions",
				c.Name, t))
		}
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

// Scoped declares that the column confines the model's rows to one tenant, and
// so that every operation a resource exposes over it must be constrained by a
// hook. It is the runtime form of schema.Field.Scoped, for models sqlb did not
// generate, and it writes no predicate: [rest.Resource] refuses to mount a
// resource whose obligations no hook satisfies, and that is all it does.
func (d *Description[T]) Scoped(column string) *Description[T] {
	return d.apply(func(m *Model) {
		col := columnOf(m, "Scoped", column)
		if m.Scope != nil && m.Scope != col {
			panic(fmt.Sprintf("sqlb: %s already scopes on %q, cannot also use %q",
				m.Type, m.Scope.Name, column))
		}
		col.Scoped = true
		m.Scope = col
	})
}

// SoftDeleted declares the column a soft-delete predicate is expected to
// filter — the runtime form of schema.SoftDelete's half that is not a column
// definition. Like Scoped it obliges a BeforeQuery hook and nothing more.
func (d *Description[T]) SoftDeleted(column string) *Description[T] {
	return d.apply(func(m *Model) {
		col := columnOf(m, "SoftDeleted", column)
		if m.Soft != nil && m.Soft != col {
			panic(fmt.Sprintf("sqlb: %s already soft-deletes on %q, cannot also use %q",
				m.Type, m.Soft.Name, column))
		}
		col.SoftDelete = true
		m.Soft = col
	})
}

// Computed declares that a column is a SQL expression rather than storage: the
// compiler renders expr wherever the column is named, so the value lands in the
// projection, and a Filterable or Sortable one reaches WHERE and ORDER BY too.
//
//	sqlb.Describe[Task]().
//	    Table("tasks").
//	    PrimaryKey("id").
//	    Computed("is_overdue", "due_date < current_date AND status <> 'done'").
//	    Filterable("is_overdue")
//
// It is the runtime form of schema.Computed, for models sqlb did not generate —
// the generated ones say the same thing through a ComputedColumns method, which
// is where the expression goes because a struct tag is a comma-separated list
// and SQL is not (ADR-0041).
//
// The column must already be mapped: a computed value needs a field to scan
// into, and the field is what puts it in the JSON and in the Go type.
//
// Each `?` in expr takes the bind named at the matching position of needs, and
// `??` is a literal question mark. A bind is supplied per query with
// [Builder.Bind], which is how a per-viewer expression gets the viewer:
//
//	Computed("is_starred",
//	    "EXISTS (SELECT 1 FROM stars s WHERE s.task_id = tasks.id AND s.member_id = ?)",
//	    "viewer")
//
// Declaring the column writes no value. A query that renders it without the
// bind fails rather than sending NULL, and [rest.Resource] refuses to mount a
// resource whose binds no BeforeQuery hook supplies — the same obligation shape
// Scoped uses, for the same reason: an unbound expression is false for every row
// forever and looks exactly like a working feature (ADR-0030, ADR-0041).
func (d *Description[T]) Computed(column, expr string, needs ...string) *Description[T] {
	return d.apply(func(m *Model) {
		col := columnOf(m, "Computed", column)
		if err := setComputed(m, col, expr, needs); err != nil {
			panic(err.Error())
		}
	})
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
	return d.apply(func(m *Model) {
		sf, ok := m.Type.FieldByName(field)
		if !ok {
			panic(fmt.Sprintf("sqlb: Relation(%q, %q): %s has no such field (fields: %s)",
				field, fkColumn, m.Type, strings.Join(fieldNamesOf(m), ", ")))
		}
		for _, col := range m.Columns {
			if col.Field == field {
				panic(fmt.Sprintf(
					"sqlb: Relation(%q, %q): %s.%s is mapped to column %q; "+
						"a relation field holds an expanded row, not a value of its own — tag it `db:\"-\"`",
					field, fkColumn, m.Type, field, col.Name))
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
		if prev := m.Relation(rel.Name); prev != nil {
			panic(fmt.Sprintf("sqlb: Relation(%q, %q): %s already expands %q, from field %s",
				field, fkColumn, m.Type, rel.Name, prev.Field))
		}

		// A collection joins on a column of the target, which this description
		// cannot see and must not mark expandable: that column's capabilities
		// describe the target's own endpoint. It resolves with the target instead,
		// on first expansion.
		if !rel.Collection {
			rel.FK = columnOf(m, "Relation", fkColumn)
			rel.FK.Expandable = true
		}
		// Appended to a slice of its own rather than relying on clone having
		// left no spare capacity. It has not — clone sizes exactly — but an
		// append that grew into spare capacity would write into the model this
		// one was copied from, and that is too quiet a thing to leave resting
		// on how a function two files away allocates.
		m.Relations = append(append([]*RelationInfo(nil), m.Relations...), rel)
	})
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

func (d *Description[T]) each(what string, columns []string, set func(*ColumnInfo)) *Description[T] {
	return d.apply(func(m *Model) {
		for _, name := range columns {
			set(columnOf(m, what, name))
		}
	})
}

// columnOf resolves a name, panicking with the available columns if it is not
// one. Failing loudly at startup is the point: a mistyped column here would
// otherwise silently leave a capability off and surface as a 400 much later.
//
// It takes the model rather than reading the Description's, because a
// description resolves against the copy it is about to publish and not against
// the one already in the cache.
func columnOf(m *Model, what, name string) *ColumnInfo {
	if col := m.Column(name); col != nil {
		return col
	}
	panic(fmt.Sprintf("sqlb: %s(%q): %s has no such column (columns: %s)",
		what, name, m.Type, strings.Join(m.ColumnNames(), ", ")))
}

func fieldNamesOf(m *Model) []string {
	out := make([]string, len(m.Columns))
	for i, c := range m.Columns {
		out[i] = c.Field
	}
	return out
}
