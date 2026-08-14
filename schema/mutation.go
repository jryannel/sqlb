package schema

import "strings"

// A mutation is the row-scoped half of what schema.Action currently covers:
// a domain verb that always addresses one row, with a generated envelope —
// fetch under BeforeQuery, lock when it writes, persist the declared columns,
// answer with the row.
//
// It exists beside Action rather than replacing it because Action's other
// half — a collection verb with no row, no fetch, no lock — is a genuinely
// different shape: nothing obliges it, and ADR-0030's closure does not reach
// it. Splitting by name rather than by IsCollection() says that difference at
// the declaration instead of leaving a reader of a table's Actions() to check
// each one's path for "{id}". Mutation is always the row-scoped one; Action,
// used this way, is always the collection one.
//
// This is additive and experimental: nothing about schema.Action changes, an
// existing item-form Action keeps working exactly as declared, and codegen
// does not read Mutations() yet — see rest.Mutation for the runtime half.

// Mutation is a row-scoped domain verb exposed on a table.
//
// Declare one with [TableDef.AddMutation] — matching [TableDef.AddQuery]
// rather than the bare Action/Expose/AddIndex mix this schema package
// otherwise has, since Mutation is new enough that agreeing with its own
// sibling method mattered more than agreeing with precedent that is itself
// inconsistent (Expose has no Add, AddIndex does, for a reason that stopped
// applying the moment a verb had no shorthand sibling to be told apart from).
//
//	Task.AddMutation(schema.Mutation{
//	    Name:   "complete",
//	    Body:   schema.Body(schema.Text("note").Nullable()),
//	    Writes: []string{"status", "completed_at"},
//	})
//
// which serves POST /tasks/{id}/complete and asks the application, at
// registration, for a func(context.Context, *Task, CompleteTaskInput) error —
// exactly [Action]'s item-form signature.
type Mutation struct {
	// Name is the verb: "complete" gives POST /tasks/{id}/complete.
	Name string

	// Path is the sub-path under the collection. It defaults to
	// "/{id}/"+Name and must contain "{id}" — a mutation with nothing to
	// address by id is what [Action] is for.
	Path string

	// Body is the request body, declared in the field vocabulary. See
	// [Action.Body]; the vocabulary and the reasoning are identical.
	Body []*Field

	// Writes names the columns the envelope persists after the verb returns,
	// taken off the row the verb mutated. See [Action.Writes].
	//
	// A string rather than a *Field, and that was tried: [schema.Field.Name]
	// exists because of it. Typed would have meant pulling every referenced
	// column out of schema.Table(...)'s inline declaration into a named var —
	// real ceremony per column, for what Validate() already catches. Reads on
	// [Query] stayed typed because it only ever costs a *table* reference, and
	// every table already has a name; Writes would have cost one per column,
	// and the declaration is supposed to stay short.
	Writes []string

	// Touches names tables the verb writes through the transaction beyond the
	// row the envelope persists. See [Action.Touches].
	Touches []string

	// Summary and Description document the operation.
	Summary     string
	Description string
}

// AddMutation declares a row-scoped domain verb on the table and returns the
// table, so declarations chain the way Action, Query and Expose already do.
//
// The table must also be exposed, and its Path must address a row: a
// mutation with no {id} is refused at validation, on the grounds that a
// collection-scoped verb is what Action already is and giving the same shape
// two names would be a declaration deciding nothing.
func (t *TableDef) AddMutation(m Mutation) *TableDef {
	if m.Path == "" {
		m.Path = "/{id}/" + m.Name
	}
	t.mutations = append(t.mutations, m)
	return t
}

// Mutations returns the table's declared row-scoped verbs, in declaration
// order.
func (t *TableDef) Mutations() []Mutation { return t.mutations }

// FullPath is the mutation's route: the resource path with the mutation's own
// path appended.
func (m Mutation) FullPath(resource string) string { return resource + m.Path }

// validateMutations checks one table's declared mutations. It mirrors
// validateActions closely — the two are the same shape checked against a
// different Writes/Touches/Body carrier — with one addition: Path must
// address a row, which Action leaves optional and Mutation does not.
func (r *Registry) validateMutations(t *TableDef, report func(string, string, string, ...any)) {
	if len(t.mutations) == 0 {
		return
	}
	if t.rest == nil {
		report(t.name, "", "declares %d mutation(s) but is not exposed; a mutation is a route on the resource, so add Expose", len(t.mutations))
		return
	}

	seen := make(map[string]bool, len(t.mutations))
	paths := make(map[string]string, len(t.mutations))
	for _, m := range t.mutations {
		switch {
		case m.Name == "":
			report(t.name, "", "mutation has no Name")
			continue
		case !isActionName(m.Name):
			report(t.name, "", "mutation name %q must be a lower-case identifier, optionally hyphenated: complete, archive, mark-read", m.Name)
		}
		if seen[m.Name] {
			report(t.name, "", "mutation %q declared twice", m.Name)
		}
		seen[m.Name] = true

		for _, a := range t.actions {
			if a.Name == m.Name {
				report(t.name, "", "mutation %q has the same name as an action on this table; "+
					"the two share an operation id and a client method name", m.Name)
			}
		}
		for _, q := range t.queries {
			if q.Name == m.Name {
				report(t.name, "", "mutation %q has the same name as a query on this table; "+
					"the two share an operation id and a client method name", m.Name)
			}
		}

		if op, verb, dup := collidesWithOp(t.rest.Ops, m.Name); dup {
			report(t.name, "", "mutation %q collides with %s, which the resource already generates as its "+
				"%q operation: the two share an operation id in the OpenAPI document, which Huma refuses "+
				"at mount, and a function name in every generated client, which then does not compile. "+
				"Name the verb for the transition it performs — complete, archive, mark-read — or drop "+
				"%s from Expose, which leaves the mutation as the resource's only %s route",
				m.Name, op, verb, op, verb)
		}

		if !strings.HasPrefix(m.Path, "/") {
			report(t.name, "", "mutation %q has path %q, which must start with %q", m.Name, m.Path, "/")
		}
		if !strings.Contains(m.Path, "{id}") {
			report(t.name, "", "mutation %q has path %q, which names no row to address; "+
				"a verb with nothing to fetch by id is what schema.Action is for", m.Name, m.Path)
		}
		if prev, dup := paths[m.Path]; dup {
			report(t.name, "", "mutation %q uses the same path as %q, so routing would depend on declaration order", m.Name, prev)
		}
		paths[m.Path] = m.Name

		if t.PrimaryKey() == nil {
			if cols := t.CompositeKey(); len(cols) > 0 {
				report(t.name, "", "mutation %q addresses a row by id but the table's key is composite (%s), "+
					"and one column is what an id is", m.Name, strings.Join(cols, ", "))
			} else {
				report(t.name, "", "mutation %q addresses a row by id but the table declares no primary key", m.Name)
			}
		}

		r.validateMutationBody(t, m, report)
		r.validateMutationWrites(t, m, report)
		r.validateMutationTouches(t, m, report)
	}
}

func (r *Registry) validateMutationTouches(t *TableDef, m Mutation, report func(string, string, string, ...any)) {
	seen := make(map[string]bool, len(m.Touches))
	for _, name := range m.Touches {
		switch {
		case name == "":
			report(t.name, "", "mutation %q: Touches has an empty table name", m.Name)
			continue
		case seen[name]:
			report(t.name, "", "mutation %q: Touches names %q twice", m.Name, name)
			continue
		}
		seen[name] = true
	}
}

func (r *Registry) validateMutationBody(t *TableDef, m Mutation, report func(string, string, string, ...any)) {
	seen := make(map[string]bool, len(m.Body))
	for _, f := range m.Body {
		d := f.Desc()
		if !isIdent(d.Name) {
			report(t.name, d.Name, "mutation %q: body property name is not a valid identifier", m.Name)
		}
		if seen[d.Name] {
			report(t.name, d.Name, "mutation %q: body property declared twice", m.Name)
		}
		seen[d.Name] = true

		for _, c := range []struct {
			claimed bool
			what    string
		}{
			{d.PrimaryKey, "PrimaryKey"},
			{d.Unique, "Unique"},
			{d.Filterable, "Filterable"},
			{d.Sortable, "Sortable"},
			{d.Searchable, "Searchable"},
			{d.ReadOnly, "ReadOnly"},
			{d.Immutable, "Immutable"},
			{d.Hidden, "Hidden"},
			{d.Scoped, "Scoped"},
			{d.Ref != nil, "Ref"},
			{d.Computed(), "Computed"},
		} {
			if c.claimed {
				report(t.name, d.Name, "mutation %q: body property claims %s, which describes a column rather than a request body", m.Name, c.what)
			}
		}
	}
}

func (r *Registry) validateMutationWrites(t *TableDef, m Mutation, report func(string, string, string, ...any)) {
	seen := make(map[string]bool, len(m.Writes))
	for _, name := range m.Writes {
		if seen[name] {
			report(t.name, name, "mutation %q: Writes names the column twice", m.Name)
		}
		seen[name] = true

		f := t.Field(name)
		switch {
		case f == nil:
			report(t.name, name, "mutation %q: Writes names no column of this table", m.Name)
		case f.Desc().Computed():
			report(t.name, name, "mutation %q: Writes names a computed column, which has no storage to write to", m.Name)
		case f.Desc().PrimaryKey:
			report(t.name, name, "mutation %q: Writes names the primary key; a verb that re-keys a row is a delete and an insert", m.Name)
		}
	}
}
