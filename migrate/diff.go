package migrate

import (
	"fmt"
	"sort"

	"github.com/jryannel/sqlb/schema"
)

// Diff computes the changes that take current to target.
//
// It is a pure function over two registries rather than a comparison between a
// registry and a live database, which is the point: introspection produces the
// same *schema.Registry the DSL produces, so the same machinery generates a
// migration forwards and an import backwards, and the whole engine is testable
// without a database (ADR-0014).
//
// # What Destructive means here
//
// A change is marked Destructive when applying it can lose data that cannot be
// recovered by reversing it: dropping a table or column, or a type change that
// is not a widening. Adding NOT NULL to an existing column is included,
// because the fix for a failure is a backfill rather than a retry. Changes
// that merely fail loudly — adding a CHECK that existing rows violate,
// removing a value from an enum — are not destructive, since nothing is lost;
// they carry a Comment saying what to check instead.
//
// # What is not inferred
//
// A rename is indistinguishable from a drop and an add when only the before
// and after states are known, so it is emitted as a drop and an add: correct,
// lossy, and never silently wrong. Declaring renames explicitly is the
// unbuilt half of this (ADR-0014).
//
// # Ordering
//
// Changes are ordered so that each one's dependencies already exist and
// nothing is dropped out from under something that still refers to it:
//
//  1. CREATE TABLE for new tables
//  2. DROP INDEX for removed and changed indexes — before the columns they
//     cover can disappear
//  3. DROP CONSTRAINT, foreign keys first, since a foreign key depends on the
//     unique or primary key constraint it points at
//  4. ADD COLUMN and ALTER COLUMN
//  5. DROP COLUMN
//  6. ADD CONSTRAINT, foreign keys last, once every table and column exists
//  7. CREATE INDEX
//  8. DROP TABLE
//
// Rendering reverses this list for the Down section, which is exactly the
// mirror of it, so reversibility falls out of the ordering rather than being
// arranged separately.
func Diff(current, target *schema.Registry) ([]Change, error) {
	if current == nil {
		current = schema.NewRegistry()
	}
	if target == nil {
		target = schema.NewRegistry()
	}
	// A registry that does not validate would produce DDL for a schema that
	// cannot exist — a reference to an unregistered table, a duplicate column.
	// Failing here beats failing halfway through a migration.
	if err := current.Validate(); err != nil {
		return nil, fmt.Errorf("migrate: current schema is not valid: %w", err)
	}
	if err := target.Validate(); err != nil {
		return nil, fmt.Errorf("migrate: target schema is not valid: %w", err)
	}

	d := &differ{current: current, target: target}
	if err := d.run(); err != nil {
		return nil, err
	}
	return d.changes(), nil
}

// differ accumulates changes into ordered phases. Phases exist because the
// correct order is not the order the comparison discovers things in: a column
// added to one table and an index dropped from another are found together and
// must be applied apart.
type differ struct {
	current, target *schema.Registry

	createTables   []Change
	dropIndexes    []Change
	dropForeignKey []Change
	dropOther      []Change
	alterColumns   []Change
	dropColumns    []Change
	addOther       []Change
	addForeignKey  []Change
	createIndexes  []Change
	dropTables     []Change
}

func (d *differ) changes() []Change {
	var out []Change
	for _, phase := range [][]Change{
		d.createTables,
		d.dropIndexes,
		d.dropForeignKey,
		d.dropOther,
		d.alterColumns,
		d.dropColumns,
		d.addOther,
		d.addForeignKey,
		d.createIndexes,
		d.dropTables,
	} {
		out = append(out, phase...)
	}
	return out
}

func (d *differ) run() error {
	for _, t := range d.target.Tables() {
		if d.current.Get(t.Name()) != nil {
			continue // altered below, walking the current registry
		}
		if err := d.tableCreated(t); err != nil {
			return err
		}
	}
	for _, t := range d.current.Tables() {
		if d.target.Get(t.Name()) == nil {
			if err := d.tableDropped(t); err != nil {
				return err
			}
			continue
		}
		if err := d.tableAltered(t, d.target.Get(t.Name())); err != nil {
			return err
		}
	}
	return nil
}

// tableCreated emits the table and everything that comes with it.
func (d *differ) tableCreated(t *schema.TableDef) error {
	up, err := createTable(t)
	if err != nil {
		return err
	}
	d.createTables = append(d.createTables, Change{
		Comment: "create table " + t.Name(),
		Up:      up,
		Down:    dropTable(t),
	})

	for _, c := range constraints(t) {
		if !c.fk {
			continue // already inline in CREATE TABLE
		}
		d.addForeignKey = append(d.addForeignKey, Change{
			Comment: "add foreign key " + c.name,
			Up:      addConstraint(t.Name(), c),
			Down:    dropConstraint(t.Name(), c.name),
		})
	}

	// The table is empty by construction, so its indexes cannot contend with
	// anything and do not need CONCURRENTLY — which keeps them in the same
	// file as the table instead of forcing a split.
	for _, idx := range sortedIndexes(t) {
		d.createIndexes = append(d.createIndexes, Change{
			Comment: "index " + idx.Name,
			Up:      createIndex(t, idx, false),
			Down:    dropIndex(idx.Name, false),
		})
	}
	return nil
}

func (d *differ) tableDropped(t *schema.TableDef) error {
	// DROP TABLE takes the table's own indexes and constraints with it, so
	// none of them need emitting separately.
	//
	// The Down recreates the table and its inline constraints, and stops
	// there. Its indexes and references are not restored: a reference may
	// point at another table dropped in the same migration, and getting that
	// order right in reverse is work in service of a rollback that cannot
	// bring the rows back anyway. The Reason says so rather than the Down
	// implying otherwise.
	down, err := createTable(t)
	if err != nil {
		return err
	}
	d.dropTables = append(d.dropTables, Change{
		Comment:     "drop table " + t.Name(),
		Up:          dropTable(t),
		Down:        down,
		Destructive: true,
		Reason: "dropping table " + t.Name() + " deletes every row in it. " +
			"The Down recreates the table and its constraints, but not the data, the indexes or the references to it",
	})
	return nil
}

func (d *differ) tableAltered(cur, tgt *schema.TableDef) error {
	if err := d.columns(cur, tgt); err != nil {
		return err
	}
	d.tableComment(cur, tgt)
	d.constraints(cur, tgt)
	d.indexes(cur, tgt)
	return nil
}

func (d *differ) columns(cur, tgt *schema.TableDef) error {
	for _, f := range tgt.Fields() {
		td := f.Desc()
		existing := cur.Field(td.Name)
		if existing == nil {
			if err := d.columnAdded(tgt, td); err != nil {
				return err
			}
			continue
		}
		if err := d.columnAltered(tgt, existing.Desc(), td); err != nil {
			return err
		}
	}
	for _, f := range cur.Fields() {
		cd := f.Desc()
		if tgt.Field(cd.Name) != nil {
			continue
		}
		d.dropColumns = append(d.dropColumns, Change{
			Comment:     "drop column " + cur.Name() + "." + cd.Name,
			Up:          dropColumn(cur.Name(), cd.Name),
			Down:        mustAddColumnDown(cur, cd),
			Destructive: true,
			Reason:      "dropping " + cur.Name() + "." + cd.Name + " deletes its contents. The Down restores the column but not the values",
		})
	}
	return nil
}

// mustAddColumnDown renders the ADD COLUMN that reverses a drop. The column
// came from a registry that already rendered, so a failure here is impossible;
// an empty Down would render as "not reversible", which is honest either way.
func mustAddColumnDown(t *schema.TableDef, d *schema.FieldDesc) string {
	sql, err := addColumn(t, d)
	if err != nil {
		return ""
	}
	return sql
}

func (d *differ) columnAdded(t *schema.TableDef, td *schema.FieldDesc) error {
	up, err := addColumn(t, td)
	if err != nil {
		return err
	}
	if td.Comment != "" {
		up += "\n" + commentOnColumn(t.Name(), td.Name, td.Comment)
	}
	c := Change{
		Comment: "add column " + t.Name() + "." + td.Name,
		Up:      up,
		Down:    dropColumn(t.Name(), td.Name),
	}
	// Postgres can add a NOT NULL column with a default without rewriting the
	// table, but a NOT NULL column with no default is simply rejected on any
	// table that has rows in it.
	if !td.Nullable && td.Default == nil {
		c.Destructive = true
		c.Reason = "adding NOT NULL column " + t.Name() + "." + td.Name +
			" with no default fails on a table that already has rows. Give it a default or backfill it"
	}
	d.alterColumns = append(d.alterColumns, c)
	return nil
}

func (d *differ) columnAltered(t *schema.TableDef, cd, td *schema.FieldDesc) error {
	curType, err := sqlType(cd)
	if err != nil {
		return err
	}
	tgtType, err := sqlType(td)
	if err != nil {
		return err
	}

	if curType != tgtType {
		c := Change{
			Comment: "change type of " + t.Name() + "." + td.Name + " from " + curType + " to " + tgtType,
			Up:      alterColumn(t.Name(), td.Name, "TYPE "+tgtType),
			Down:    alterColumn(t.Name(), td.Name, "TYPE "+curType),
		}
		if !widens(cd, td) {
			c.Destructive = true
			c.Reason = "converting " + t.Name() + "." + td.Name + " from " + curType +
				" to " + tgtType + " is not a widening: values that do not fit are truncated or rejected"
			// No USING clause is generated. Postgres refuses a conversion it
			// cannot make implicitly, and refusing is what should happen: a
			// generated USING would pick a cast nobody reviewed, and casting
			// to a narrower type truncates silently.
			c.Comment += " (add a USING clause by hand if Postgres refuses the cast)"
		}
		d.alterColumns = append(d.alterColumns, c)
	}

	switch {
	case cd.Nullable && !td.Nullable:
		d.alterColumns = append(d.alterColumns, Change{
			Comment:     "require " + t.Name() + "." + td.Name,
			Up:          alterColumn(t.Name(), td.Name, "SET NOT NULL"),
			Down:        alterColumn(t.Name(), td.Name, "DROP NOT NULL"),
			Destructive: true,
			Reason: "rows with NULL in " + t.Name() + "." + td.Name +
				" will fail this constraint. Backfill them before applying",
		})
	case !cd.Nullable && td.Nullable:
		d.alterColumns = append(d.alterColumns, Change{
			Comment: "allow NULL in " + t.Name() + "." + td.Name,
			Up:      alterColumn(t.Name(), td.Name, "DROP NOT NULL"),
			Down:    alterColumn(t.Name(), td.Name, "SET NOT NULL"),
		})
	}

	curDefault, err := renderDefault(cd.Default)
	if err != nil {
		return err
	}
	tgtDefault, err := renderDefault(td.Default)
	if err != nil {
		return err
	}
	if curDefault != tgtDefault {
		up, down := "DROP DEFAULT", "DROP DEFAULT"
		if tgtDefault != "" {
			up = "SET DEFAULT " + tgtDefault
		}
		if curDefault != "" {
			down = "SET DEFAULT " + curDefault
		}
		d.alterColumns = append(d.alterColumns, Change{
			Comment: "default of " + t.Name() + "." + td.Name +
				" (existing rows are not backfilled; a default applies to new rows only)",
			Up:   alterColumn(t.Name(), td.Name, up),
			Down: alterColumn(t.Name(), td.Name, down),
		})
	}

	if cd.Comment != td.Comment {
		d.alterColumns = append(d.alterColumns, Change{
			Comment: "describe " + t.Name() + "." + td.Name,
			Up:      commentOnColumn(t.Name(), td.Name, td.Comment),
			Down:    commentOnColumn(t.Name(), td.Name, cd.Comment),
		})
	}
	return nil
}

func (d *differ) tableComment(cur, tgt *schema.TableDef) {
	if cur.Comment() == tgt.Comment() {
		return
	}
	d.alterColumns = append(d.alterColumns, Change{
		Comment: "describe " + tgt.Name(),
		Up:      commentOnTable(tgt.Name(), tgt.Comment()),
		Down:    commentOnTable(cur.Name(), cur.Comment()),
	})
}

func (d *differ) constraints(cur, tgt *schema.TableDef) {
	curCons := byName(constraints(cur))
	tgtCons := byName(constraints(tgt))

	for _, name := range sortedKeys(curCons) {
		c := curCons[name]
		t, kept := tgtCons[name]
		if kept && t.def == c.def {
			continue
		}
		d.dropConstraintChange(tgt.Name(), c)
	}
	for _, name := range sortedKeys(tgtCons) {
		t := tgtCons[name]
		c, existed := curCons[name]
		if existed && c.def == t.def {
			continue
		}
		d.addConstraintChange(tgt.Name(), t, existing(curCons, name))
	}
}

// existing returns the constraint previously held under this name, or nil.
func existing(m map[string]constraint, name string) *constraint {
	if c, ok := m[name]; ok {
		return &c
	}
	return nil
}

func (d *differ) dropConstraintChange(table string, c constraint) {
	ch := Change{
		Comment: "drop constraint " + c.name,
		Up:      dropConstraint(table, c.name),
		Down:    addConstraint(table, c),
	}
	if c.fk {
		d.dropForeignKey = append(d.dropForeignKey, ch)
		return
	}
	d.dropOther = append(d.dropOther, ch)
}

func (d *differ) addConstraintChange(table string, c constraint, prev *constraint) {
	// The Down is just a drop. When this add replaces an earlier definition,
	// the drop of that definition was emitted in an earlier phase and its own
	// Down restores it, so reversing the whole list restores the old
	// constraint without this change having to know about it.
	ch := Change{
		Comment: "add constraint " + c.name,
		Up:      addConstraint(table, c),
		Down:    dropConstraint(table, c.name),
	}
	switch removed := removedEnumValues(prev, c); {
	case len(removed) > 0:
		ch.Comment += " (no longer permits " + joinQuoted(removed) +
			"; rows still holding one will fail — migrate them first)"
	case prev == nil:
		ch.Comment += " (existing rows must already satisfy it or this fails)"
	}
	if c.fk {
		d.addForeignKey = append(d.addForeignKey, ch)
		return
	}
	d.addOther = append(d.addOther, ch)
}

// removedEnumValues reports which permitted values a replaced enum CHECK no
// longer allows. Removing one cannot lose data — Postgres rejects the whole
// statement — but it is the case worth naming, because the fix is in the data
// rather than in the schema.
func removedEnumValues(prev *constraint, next constraint) []string {
	if prev == nil || prev.enum == nil || next.enum == nil {
		return nil
	}
	keep := make(map[string]bool, len(next.enum))
	for _, v := range next.enum {
		keep[v] = true
	}
	var removed []string
	for _, v := range prev.enum {
		if !keep[v] {
			removed = append(removed, v)
		}
	}
	return removed
}

func (d *differ) indexes(cur, tgt *schema.TableDef) {
	curIdx := indexesByName(cur)
	tgtIdx := indexesByName(tgt)

	for _, name := range sortedKeys(curIdx) {
		c := curIdx[name]
		t, kept := tgtIdx[name]
		if kept && indexDef(t) == indexDef(c) {
			continue
		}
		// An index over a column that is going away is dropped without
		// CONCURRENTLY, which is what keeps it in the same file as the column
		// drop and therefore ordered before it — a concurrent one is split
		// into a file that runs afterwards, by which time Postgres has already
		// dropped the index along with the column and the statement fails.
		//
		// Nothing is lost by giving up CONCURRENTLY here: DROP COLUMN takes an
		// ACCESS EXCLUSIVE lock on the same table moments later, so the brief
		// lock this takes is one the migration was going to take anyway.
		concurrent := !coversDroppedColumn(c, tgt)
		d.dropIndexes = append(d.dropIndexes, Change{
			Comment:    "drop index " + name,
			Up:         dropIndex(name, concurrent),
			Down:       createIndex(cur, c, concurrent),
			Concurrent: concurrent,
		})
	}
	for _, name := range sortedKeys(tgtIdx) {
		t := tgtIdx[name]
		c, existed := curIdx[name]
		if existed && indexDef(c) == indexDef(t) {
			continue
		}
		// The table already holds rows, so building the index without
		// CONCURRENTLY would lock it against writes for the duration.
		d.createIndexes = append(d.createIndexes, Change{
			Comment:    "index " + name,
			Up:         createIndex(tgt, t, true),
			Down:       dropIndex(name, true),
			Concurrent: true,
		})
	}
}

// coversDroppedColumn reports whether the index spans a column the target no
// longer declares. Postgres drops the whole index when any indexed column
// goes, even a trailing one.
func coversDroppedColumn(idx schema.Index, tgt *schema.TableDef) bool {
	for _, col := range idx.Columns {
		if tgt.Field(col) == nil {
			return true
		}
	}
	return false
}

func byName(cs []constraint) map[string]constraint {
	m := make(map[string]constraint, len(cs))
	for _, c := range cs {
		m[c.name] = c
	}
	return m
}

func indexesByName(t *schema.TableDef) map[string]schema.Index {
	m := make(map[string]schema.Index, len(t.Indexes()))
	for _, idx := range t.Indexes() {
		m[idx.Name] = idx
	}
	return m
}

func sortedIndexes(t *schema.TableDef) []schema.Index {
	out := append([]schema.Index(nil), t.Indexes()...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// sortedKeys keeps output deterministic across runs, which matters more here
// than usual: a migration that reorders itself between runs is a diff nobody
// can review.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func joinQuoted(vs []string) string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = sqlString(v)
	}
	return joinWithAnd(out)
}

func joinWithAnd(vs []string) string {
	switch len(vs) {
	case 0:
		return ""
	case 1:
		return vs[0]
	}
	s := ""
	for i, v := range vs[:len(vs)-1] {
		if i > 0 {
			s += ", "
		}
		s += v
	}
	return s + " and " + vs[len(vs)-1]
}
