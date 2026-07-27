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
// # What Lock means here
//
// Most DDL is a catalog write that nobody notices. A few statements hold their
// lock for a time proportional to the number of rows — because they rewrite the
// table, scan it, or build an index over it — and those are the ones that turn
// a routine migration into an outage. A change that does one carries Lock and
// Hazard, naming the lock and the sequence to use instead on a table too large
// to hold it.
//
// It is a note rather than a refusal, and unlike a destructive change it is not
// commented out. Whether a full scan matters depends on how many rows the table
// holds, which is not in the schema and never will be. Commenting out every
// SET NOT NULL would make the generator useless for the ordinary case and train
// people to uncomment without reading — which is how the destructive guard
// would stop working too. Migration.Blocking is the hook for a project that
// does know which of its tables are big.
//
// For the changes whose remedy is a fixed rewrite — adding a CHECK or a
// FOREIGN KEY, requiring a column — Unblock performs it, moving the scan out
// from under the lock. It is called rather than applied automatically, for the
// same reason the hazard is a note: the sequence is longer, splits the
// migration across files, and buys nothing on a table small enough that the
// scan is instant.
//
// # What is not inferred
//
// A rename is indistinguishable from a drop and an add when only the before
// and after states are known, so it has to be declared: schema.RenamedFrom
// says a column or a table used to be called something else, and the diff
// emits ALTER TABLE … RENAME. Without the hint a rename is a drop and an add,
// which is correct, lossy, and never silently wrong. Inferring one from a
// similar name and type is the tempting alternative and is rejected on
// consequence asymmetry: a wrong inference destroys a column of production
// data, a missing one costs a hint (ADR-0014).
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
//  4. RENAME, of tables, columns, constraints and indexes
//  5. ADD COLUMN and ALTER COLUMN
//  6. DROP COLUMN
//  7. ADD CONSTRAINT, foreign keys last, once every table and column exists
//  8. CREATE INDEX
//  9. DROP TABLE
//
// Rendering reverses this list for the Down section, which is exactly the
// mirror of it, so reversibility falls out of the ordering rather than being
// arranged separately.
//
// The renames sit where they do because that is the only place both sides work
// out. Everything before them is expressed in the old names and everything
// after in the new ones — and because the Down runs the list backwards, each
// half is reversed while the names it was written against are the ones in
// effect. Putting the renames first instead would leave every drop's Down
// re-adding a constraint against a column that no longer answers to that name.
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

	// tableRenames maps an old storage name to its new one. A foreign key
	// names the table it points at, so a reference to a renamed table has to
	// be recognised as unchanged rather than dropped and re-added.
	tableRenames map[string]string

	createTables   []Change
	dropIndexes    []Change
	dropForeignKey []Change
	dropOther      []Change
	renames        []Change
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
		d.renames,
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
	d.tableRenames = map[string]string{}
	claimed := map[string]bool{}
	for _, t := range d.target.Tables() {
		cur := d.currentFor(t)
		if cur == nil {
			continue // created below, once the rename map is complete
		}
		claimed[cur.Name()] = true
		if cur.Name() != t.Name() {
			d.tableRenames[cur.Name()] = t.Name()
		}
	}

	for _, t := range d.target.Tables() {
		cur := d.currentFor(t)
		if cur == nil {
			if err := d.tableCreated(t); err != nil {
				return err
			}
			continue
		}
		if cur.Name() != t.Name() {
			d.renames = append(d.renames, Change{
				Comment: "rename table " + cur.Name() + " to " + t.Name(),
				Up:      renameTable(cur.Name(), t.Name()),
				Down:    renameTable(t.Name(), cur.Name()),
			})
		}
		if err := d.tableAltered(cur, t); err != nil {
			return err
		}
	}
	for _, t := range d.current.Tables() {
		if claimed[t.Name()] {
			continue
		}
		if err := d.tableDropped(t); err != nil {
			return err
		}
	}
	return nil
}

// currentFor returns the table in the current registry that t describes, or
// nil if t is new.
//
// A table matching by name wins over a rename hint, which is what makes a hint
// left behind after its migration was generated harmless: by then the table
// answers to its new name, the old one is gone, and the hint resolves to
// nothing. Deleting it is still the right thing to do — it is a claim about the
// schema that has stopped being true — but forgetting to does not produce a
// second rename.
func (d *differ) currentFor(t *schema.TableDef) *schema.TableDef {
	if cur := d.current.Get(t.Name()); cur != nil {
		return cur
	}
	if old := t.RenamedFromName(); old != "" {
		return d.current.Get(old)
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
	cols := columnRenames(cur, tgt)
	for _, old := range sortedKeys(cols) {
		d.renames = append(d.renames, Change{
			Comment: "rename " + tgt.Name() + "." + old + " to " + cols[old],
			Up:      renameColumn(tgt.Name(), old, cols[old]),
			Down:    renameColumn(tgt.Name(), cols[old], old),
		})
	}
	if err := d.columns(cur, tgt, cols); err != nil {
		return err
	}
	d.tableComment(cur, tgt)
	d.constraints(cur, tgt, cols)
	d.indexes(cur, tgt, cols)
	return nil
}

// columnRenames resolves a table's rename hints into old name → new name.
//
// A hint counts only when the old column is present and the new one is not:
// anything else means the rename has already been generated and applied, and
// the leftover hint describes a schema that no longer exists. See currentFor
// for why that is a no-op rather than an error.
func columnRenames(cur, tgt *schema.TableDef) map[string]string {
	out := map[string]string{}
	for _, f := range tgt.Fields() {
		td := f.Desc()
		old := td.RenamedFrom
		if old == "" || cur.Field(old) == nil || cur.Field(td.Name) != nil {
			continue
		}
		out[old] = td.Name
	}
	return out
}

func (d *differ) columns(cur, tgt *schema.TableDef, cols map[string]string) error {
	renamedFrom := make(map[string]string, len(cols))
	for old, name := range cols {
		renamedFrom[name] = old
	}

	for _, f := range tgt.Fields() {
		td := f.Desc()
		existing := cur.Field(td.Name)
		if existing == nil {
			// A renamed column is not new: the rename is already emitted, and
			// what remains is whatever else changed about it at the same time.
			if old, renamed := renamedFrom[td.Name]; renamed {
				existing = cur.Field(old)
			}
		}
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
		if tgt.Field(cd.Name) != nil || cols[cd.Name] != "" {
			continue
		}
		// The column drop runs after the renames, so it and its Down are both
		// written against the table's new name.
		d.dropColumns = append(d.dropColumns, Change{
			Comment:     "drop column " + tgt.Name() + "." + cd.Name,
			Up:          dropColumn(tgt.Name(), cd.Name),
			Down:        mustAddColumnDown(tgt.Name(), cd),
			Destructive: true,
			Reason:      "dropping " + tgt.Name() + "." + cd.Name + " deletes its contents. The Down restores the column but not the values",
		})
	}
	return nil
}

// mustAddColumnDown renders the ADD COLUMN that reverses a drop. The column
// came from a registry that already rendered, so a failure here is impossible;
// an empty Down would render as "not reversible", which is honest either way.
func mustAddColumnDown(table string, d *schema.FieldDesc) string {
	sql, err := addColumn(table, d)
	if err != nil {
		return ""
	}
	return sql
}

func (d *differ) columnAdded(t *schema.TableDef, td *schema.FieldDesc) error {
	up, err := addColumn(t.Name(), td)
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
		c.Lock, c.Hazard = typeChangeHazard(t.Name(), cd, td)
		if c.Lock == "" && rewrites(td, cd) {
			// Widening a varchar is free and narrowing it is not, so the Up
			// costs nothing and the rollback rewrites the table. Worth saying
			// here, since the place it would otherwise be discovered is
			// halfway through an incident.
			c.Comment += " (free in this direction; reversing it rewrites the table)"
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
		// Two separate problems, so two separate flags: rows holding NULL make
		// this fail, and proving that none do makes it slow.
		lock, hazard := setNotNullHazard(t.Name(), td.Name)
		c := Change{
			Comment:     "require " + t.Name() + "." + td.Name,
			Up:          alterColumn(t.Name(), td.Name, "SET NOT NULL"),
			Down:        alterColumn(t.Name(), td.Name, "DROP NOT NULL"),
			Destructive: true,
			Reason: "rows with NULL in " + t.Name() + "." + td.Name +
				" will fail this constraint. Backfill them before applying",
			Lock:   lock,
			Hazard: hazard,
		}
		c.unblocked = notNullSequence(t.Name(), td.Name, c)
		d.alterColumns = append(d.alterColumns, c)
	case !cd.Nullable && td.Nullable:
		// The Up is a catalog write, so this is not flagged — but reversing it
		// is the scan above, which is a surprising way to find that out during
		// a rollback.
		d.alterColumns = append(d.alterColumns, Change{
			Comment: "allow NULL in " + t.Name() + "." + td.Name +
				" (reversing this scans the table to prove no row took the opportunity)",
			Up:   alterColumn(t.Name(), td.Name, "DROP NOT NULL"),
			Down: alterColumn(t.Name(), td.Name, "SET NOT NULL"),
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
		Down:    commentOnTable(tgt.Name(), cur.Comment()),
	})
}

// constraints emits the changes between a table's current and target
// constraints.
//
// The current side is compared in its post-rename form, so that a constraint
// which merely follows a renamed column or table matches its target and
// produces nothing. It is *dropped* in its original form: a drop is ordered
// before the renames, so its Down runs after they have been reversed and the
// old names are the ones in effect.
func (d *differ) constraints(cur, tgt *schema.TableDef, cols map[string]string) {
	orig := byName(constraints(cur))
	curCons := make(map[string]constraint, len(orig))
	for name, c := range orig {
		curCons[name] = c.renamed(cols, d.tableRenames)
	}
	tgtCons := byName(constraints(tgt))
	usedCur, usedTgt := map[string]bool{}, map[string]bool{}

	// Same name: either unchanged, or replaced in place.
	for _, name := range sortedKeys(curCons) {
		t, kept := tgtCons[name]
		if !kept {
			continue
		}
		usedCur[name], usedTgt[name] = true, true
		if t.def == curCons[name].def {
			continue
		}
		prev := curCons[name]
		d.dropConstraintChange(cur.Name(), orig[name])
		d.addConstraintChange(tgt.Name(), t, &prev)
	}

	// Same definition, different name. Constraint names are derived from the
	// table and column they cover, so a rename changes them — and Postgres
	// does not rename a constraint when the thing it is named after is
	// renamed. Renaming it is a catalog write; dropping and re-adding it
	// revalidates every row in the table, and for a foreign key every row in
	// the table it points at as well.
	for _, name := range sortedKeys(curCons) {
		if usedCur[name] {
			continue
		}
		for _, want := range sortedKeys(tgtCons) {
			if usedTgt[want] || tgtCons[want].def != curCons[name].def {
				continue
			}
			usedCur[name], usedTgt[want] = true, true
			d.renames = append(d.renames, Change{
				Comment: "rename constraint " + name + " to " + want,
				Up:      renameConstraint(tgt.Name(), name, want),
				Down:    renameConstraint(tgt.Name(), want, name),
			})
			break
		}
	}

	// Whatever is left is genuinely gone, or genuinely new.
	for _, name := range sortedKeys(curCons) {
		if !usedCur[name] {
			d.dropConstraintChange(cur.Name(), orig[name])
		}
	}
	for _, name := range sortedKeys(tgtCons) {
		if !usedTgt[name] {
			d.addConstraintChange(tgt.Name(), tgtCons[name], nil)
		}
	}
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
	// Every caller of this is altering a table that already exists and may hold
	// rows. A constraint on a table created by the same migration is emitted by
	// tableCreated, which does not come through here, because a table nothing
	// has inserted into yet costs nothing to constrain.
	lock, hazard := constraintHazard(table, c)
	ch := Change{
		Comment: "add constraint " + c.name,
		Up:      addConstraint(table, c),
		Down:    dropConstraint(table, c.name),
		Lock:    lock,
		Hazard:  hazard,
	}
	switch removed := removedEnumValues(prev, c); {
	case len(removed) > 0:
		ch.Comment += " (no longer permits " + joinQuoted(removed) +
			"; rows still holding one will fail — migrate them first)"
	case prev == nil:
		ch.Comment += " (existing rows must already satisfy it or this fails)"
	}
	// A unique constraint is enforced by an index and has no NOT VALID form —
	// there is no way to build an index without reading every row — so it keeps
	// its hazard and no alternative.
	if lock != "" && !c.unique {
		ch.unblocked = notValidSequence(table, c, ch)
	}
	if c.fk {
		d.addForeignKey = append(d.addForeignKey, ch)
		return
	}
	d.addOther = append(d.addOther, ch)
}

// The lock-brief sequences.
//
// Each replaces one statement that scans a table under a lock nothing else can
// work through, with two or more that do the scanning under a lock everything
// can. They are built here, where the table and the constraint are still
// known, and substituted by Unblock — which is the caller's decision, since
// the reason to prefer them is a row count this package cannot see.
//
// The rule they all follow: the statement that scans must not share a
// transaction with one that took a strong lock, because a lock is held until
// the transaction commits and not until the statement ends. That is what
// StageValidate is for.

// notValidSequence is the two-part form of an ADD CONSTRAINT: add it without
// checking the rows already there, then prove them separately.
func notValidSequence(table string, c constraint, orig Change) []Change {
	return []Change{{
		Comment: orig.Comment + " — NOT VALID, so it binds new and updated rows " +
			"from here while the rows already there are proven separately",
		Up:   addConstraintNotValid(table, c),
		Down: dropConstraint(table, c.name),
	}, {
		Stage:   StageValidate,
		Comment: validateComment(table, c.name),
		Up:      validateConstraint(table, c.name),
		Down:    nothingToUndo(c.name),
	}}
}

// notNullSequence is the lock-brief form of SET NOT NULL. Postgres 12 and later
// accept a validated CHECK as proof that a column holds no NULLs, so the scan
// can be done under a weak lock and the statement that takes the strong one has
// nothing left to look at.
//
// The check is dropped again at the end, so the table finishes exactly as the
// single statement would have left it. Anything else would be a constraint of
// this package's invention sitting in a schema that does not declare it, which
// the next diff would propose dropping.
//
// On a Postgres older than 12 every statement here still does the right thing;
// the SET NOT NULL simply scans again, and the sequence is a slower way to
// reach the same place rather than a broken one.
func notNullSequence(table, column string, orig Change) []Change {
	c := notNullCheck(table, column)
	out := []Change{{
		Comment: "prove " + table + "." + column + " holds no NULL before requiring it, " +
			"so that the requirement itself takes no scan — NOT VALID here, validated next",
		Up:   addConstraintNotValid(table, c),
		Down: dropConstraint(table, c.name),
	}, {
		Stage:   StageValidate,
		Comment: validateComment(table, c.name),
		Up:      validateConstraint(table, c.name),
		Down:    nothingToUndo(c.name),
	}, {
		// StageFinish, not StageValidate: this takes ACCESS EXCLUSIVE and holds
		// it until the transaction commits, so anything still scanning would
		// end up doing it underneath.
		Stage: StageFinish,
		Comment: "require " + table + "." + column +
			" (instant: Postgres finds the validated check and skips its own scan)",
		Up:   alterColumn(table, column, "SET NOT NULL"),
		Down: alterColumn(table, column, "DROP NOT NULL"),
	}, {
		Stage: StageFinish,
		Comment: "drop the check now the column carries the requirement itself, " +
			"leaving the table exactly as a plain SET NOT NULL would have",
		Up: dropConstraint(table, c.name),
		// Re-added NOT VALID rather than validated: on the way back the check
		// exists only so the migration that created it has something to drop,
		// and proving it again would be the scan this sequence avoided.
		Down: addConstraintNotValid(table, c),
	}}

	// Every step inherits the reason the original needed review. A sequence
	// with half its statements commented out is worse than either whole.
	for i := range out {
		out[i].Destructive = orig.Destructive
		out[i].Reason = orig.Reason
	}
	return out
}

func validateComment(table, name string) string {
	return "prove the rows already in " + table + " satisfy " + name +
		" — scans the table, under a lock readers and writers pass through"
}

// nothingToUndo is the Down of a validation. There is no statement that
// un-proves a constraint, and none is needed: the migration that added it drops
// it, and this says so rather than rendering as an unexplained gap.
func nothingToUndo(name string) string {
	return "-- a validation cannot be undone, and needs no undoing: " + name +
		" is dropped by the migration that added it"
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

// indexes emits the changes between a table's current and target indexes. The
// current side is compared post-rename and dropped in its original form, for
// the reasons given on constraints.
func (d *differ) indexes(cur, tgt *schema.TableDef, cols map[string]string) {
	orig := indexesByName(cur)
	curIdx := make(map[string]schema.Index, len(orig))
	for name, idx := range orig {
		curIdx[name] = renamedIndex(idx, cols)
	}
	tgtIdx := indexesByName(tgt)
	usedCur, usedTgt := map[string]bool{}, map[string]bool{}

	for _, name := range sortedKeys(curIdx) {
		t, kept := tgtIdx[name]
		if !kept {
			continue
		}
		usedCur[name], usedTgt[name] = true, true
		if indexDef(t) == indexDef(curIdx[name]) {
			continue
		}
		d.indexDropped(cur, tgt, orig[name], curIdx[name])
		d.indexCreated(tgt, t)
	}

	// Same definition, different name: rebuilding an index to change its name
	// is the most expensive way to do nothing. ALTER INDEX … RENAME touches
	// the catalog and takes no lock worth naming.
	for _, name := range sortedKeys(curIdx) {
		if usedCur[name] {
			continue
		}
		for _, want := range sortedKeys(tgtIdx) {
			if usedTgt[want] || indexDef(tgtIdx[want]) != indexDef(curIdx[name]) {
				continue
			}
			usedCur[name], usedTgt[want] = true, true
			d.renames = append(d.renames, Change{
				Comment: "rename index " + name + " to " + want,
				Up:      renameIndex(name, want),
				Down:    renameIndex(want, name),
			})
			break
		}
	}

	for _, name := range sortedKeys(curIdx) {
		if !usedCur[name] {
			d.indexDropped(cur, tgt, orig[name], curIdx[name])
		}
	}
	for _, name := range sortedKeys(tgtIdx) {
		if !usedTgt[name] {
			d.indexCreated(tgt, tgtIdx[name])
		}
	}
}

// indexDropped emits the drop of an index. orig is the index as it exists now,
// which is what the Down recreates; renamed is the same index with any column
// rename applied, which is what decides whether it is about to lose a column.
func (d *differ) indexDropped(cur, tgt *schema.TableDef, orig, renamed schema.Index) {
	// An index over a column that is going away is dropped without
	// CONCURRENTLY, which is what keeps it in the same file as the column
	// drop and therefore ordered before it — a concurrent one is split
	// into a file that runs afterwards, by which time Postgres has already
	// dropped the index along with the column and the statement fails.
	//
	// Nothing is lost by giving up CONCURRENTLY here: DROP COLUMN takes an
	// ACCESS EXCLUSIVE lock on the same table moments later, so the brief
	// lock this takes is one the migration was going to take anyway.
	concurrent := !coversDroppedColumn(renamed, tgt)
	d.dropIndexes = append(d.dropIndexes, Change{
		Comment: "drop index " + orig.Name,
		Up:      dropIndex(orig.Name, concurrent),
		Down:    createIndex(cur, orig, concurrent),
		Stage:   concurrentStage(concurrent),
	})
}

func (d *differ) indexCreated(tgt *schema.TableDef, idx schema.Index) {
	// The table already holds rows, so building the index without
	// CONCURRENTLY would lock it against writes for the duration.
	d.createIndexes = append(d.createIndexes, Change{
		Comment: "index " + idx.Name,
		Up:      createIndex(tgt, idx, true),
		Down:    dropIndex(idx.Name, true),
		Stage:   StageConcurrent,
	})
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

// concurrentStage maps the decision an index change already made — whether to
// use CONCURRENTLY — onto the file it therefore belongs in.
func concurrentStage(concurrent bool) Stage {
	if concurrent {
		return StageConcurrent
	}
	return StageMain
}
