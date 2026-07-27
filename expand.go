package sqlb

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// Expansion: one query, one extra column per relation.
//
// `?expand=list` becomes a LEFT JOIN and a `json_build_object` over the target's
// columns, aliased into a result column the scanner recognises:
//
//	SELECT "tasks"."id", …,
//	       CASE WHEN "__ex_list"."id" IS NULL THEN NULL
//	            ELSE json_build_object('id', "__ex_list"."id", …) END AS "__expand_list"
//	FROM "tasks"
//	LEFT JOIN "lists" AS "__ex_list" ON "__ex_list"."id" = "tasks"."list_id"
//
// # Why one query rather than two
//
// The obvious alternative is to read the page, collect the foreign keys and
// issue a second `WHERE id IN (…)`. It avoids the join and it is what an ORM
// without a query builder usually does. It also cannot be made consistent: the
// second query runs at a later snapshot, so a row can vanish between them and a
// caller gets a null expansion for a reference the database still holds. Inside
// one statement that cannot happen.
//
// # Why json_build_object rather than the target's columns
//
// Joining the columns in directly would mean aliasing every one of them to
// avoid collisions — `tasks.id` and `lists.id` are both `id` — and then
// unaliasing them at scan time. Building an object instead means the target
// arrives as one value, and scanning it is a json.Unmarshal into a type that
// already knows its own shape.
//
// The columns are listed explicitly rather than using `row_to_json(t.*)`,
// because `Hidden` has to hold across a join. A hidden column on the target is
// hidden when the target is expanded, and `row_to_json` of the whole row would
// quietly carry a password hash into a response.
//
// # Why the base table is qualified
//
// Both arguments above are settled; a third was not knowable until Postgres saw
// the SQL. Once a second table is in the statement, an unqualified column is
// ambiguous rather than merely unclear — the compiler resolves bare names to the
// base table for a joined query, and only for a joined query. See compiler.column.
//
// ADR-0025 records all three, and the reason the third one is the useful one.

// expandPrefix marks a result column as an expanded relation. It is not a legal
// column name in any schema this generates, so it cannot collide with one.
const expandPrefix = "__expand_"

// expandAlias is the table alias the target is joined under.
func expandAlias(name string) string { return "__ex_" + name }

// Expand resolves the named relations inline, one LEFT JOIN each.
//
// Names are relation names, not column names: `Expand("list")`, not
// `Expand("list_id")`. An unknown name fails the builder rather than being
// ignored, because a silently dropped expansion answers the request with a 200
// and a missing field.
//
// Expanding is additive and idempotent: naming the same relation twice joins it
// once.
func (b *Builder[T]) Expand(names ...string) *Builder[T] {
	for _, name := range names {
		if name == "" {
			continue
		}
		if contains(b.expand, name) {
			continue
		}
		rel := b.model.Relation(name)
		if rel == nil {
			return b.fail("cannot expand %q: %s has no such relation%s",
				name, b.model.Type.Name(), didYouMean(b.model.RelationNames()))
		}
		if _, err := rel.Target(); err != nil {
			return b.Fail(err)
		}
		b.expand = append(b.expand, name)
	}
	return b
}

// Expanded reports the relations this query will resolve.
func (b *Builder[T]) Expanded() []string { return append([]string(nil), b.expand...) }

// compileExpansions writes the joins. Called while compiling FROM, so the
// aliases exist by the time the projection references them.
func (b *Builder[T]) compileExpansions(c *compiler) {
	for _, name := range b.expand {
		rel := b.model.Relation(name)
		target, err := rel.Target()
		if err != nil {
			c.fail("%s", err)
			return
		}
		if target.PK == nil {
			c.fail("cannot expand %q: %s has no primary key to join on",
				name, target.Type.Name())
			return
		}

		alias := expandAlias(name)
		c.write(" LEFT JOIN ")
		c.ident(target.Table)
		c.write(" AS ")
		c.ident(alias)
		c.write(" ON ")
		c.column(Column{Table: alias, Name: target.PK.Name})
		c.write(" = ")
		c.column(Column{Table: b.from(), Name: rel.FK.Name})
	}
}

// compileExpansionSelections writes one JSON column per expanded relation.
func (b *Builder[T]) compileExpansionSelections(c *compiler) {
	for _, name := range b.expand {
		rel := b.model.Relation(name)
		target, err := rel.Target()
		if err != nil {
			c.fail("%s", err)
			return
		}
		alias := expandAlias(name)

		c.write(", ")
		// A LEFT JOIN that matched nothing produces a row of NULLs, and
		// json_build_object over those yields an object full of nulls rather
		// than a null. The caller asked whether there is a related row; an
		// object of nulls answers "yes, and it is empty", which is wrong.
		c.write("CASE WHEN ")
		c.column(Column{Table: alias, Name: target.PK.Name})
		c.write(" IS NULL THEN NULL ELSE json_build_object(")
		first := true
		for _, col := range target.Columns {
			if col.Hidden {
				continue
			}
			if !first {
				c.write(", ")
			}
			first = false
			c.write("'" + col.Name + "', ")
			c.column(Column{Table: alias, Name: col.Name})
		}
		c.write(") END AS ")
		c.ident(expandPrefix + name)
	}
}

// scanExpansion decodes one expanded relation into the row being built.
//
// A NULL arrives as a nil pointer field, which is the honest representation of
// "the reference is null" and of "the row it pointed at is gone".
func scanExpansion(rv reflect.Value, rel *RelationInfo, raw []byte) error {
	field, ok := fieldByIndexAlloc(rv, rel.Index)
	if !ok {
		return fmt.Errorf("sqlb: cannot reach field %s to expand %q", rel.Field, rel.Name)
	}
	if len(raw) == 0 || string(raw) == "null" {
		field.SetZero()
		return nil
	}

	target := reflect.New(rel.Elem)
	if err := json.Unmarshal(raw, target.Interface()); err != nil {
		return fmt.Errorf("sqlb: decoding expanded %q: %w", rel.Name, err)
	}
	if field.Kind() == reflect.Pointer {
		field.Set(target)
		return nil
	}
	field.Set(target.Elem())
	return nil
}

// fieldByIndexAlloc walks to a field, allocating nil embedded pointers on the
// way, because the destination is being written rather than read.
func fieldByIndexAlloc(v reflect.Value, index []int) (reflect.Value, bool) {
	for i, x := range index {
		if i > 0 && v.Kind() == reflect.Pointer {
			if v.IsNil() {
				if !v.CanSet() {
					return reflect.Value{}, false
				}
				v.Set(reflect.New(v.Type().Elem()))
			}
			v = v.Elem()
		}
		if v.Kind() != reflect.Struct || x >= v.NumField() {
			return reflect.Value{}, false
		}
		v = v.Field(x)
	}
	if !v.CanSet() {
		return reflect.Value{}, false
	}
	return v, true
}

// didYouMean renders the available names for a rejection, following ADR-0011:
// a refusal should say what would have worked.
func didYouMean(names []string) string {
	if len(names) == 0 {
		return " (it declares none)"
	}
	return " (expandable: " + strings.Join(names, ", ") + ")"
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
