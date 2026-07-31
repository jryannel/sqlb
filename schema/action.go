package schema

import "strings"

// An action is a domain verb the table exposes: POST /tasks/{id}/complete.
//
// What is declared here is the *envelope* — the route, the request body, and
// the columns the verb is allowed to leave changed. What runs inside it is a
// plain Go func, bound at registration rather than here. That split is the
// whole of ADR-0043: domain logic is where a DSL's expressiveness runs out
// first, so this one does not try, and the seam is the one BeforeCreate
// already uses.
//
// The func cannot live in this struct even if it wanted to. A registry is a
// value five emitters read and sqlb.json serialises, and it is linked into the
// sqlb command — a func is neither serialisable nor readable by a generator,
// and putting the application's domain code here would make the driver depend
// on the application.

// Action is a domain verb exposed on a table.
//
// Declare one with [TableDef.Action]:
//
//	Task.Action(schema.Action{
//	    Name:   "complete",
//	    Body:   schema.Body(schema.Text("note").Nullable()),
//	    Writes: []string{"status", "closed_at"},
//	})
//
// which serves POST /tasks/{id}/complete and asks the application, at
// registration, for a func(context.Context, *Task, CompleteTaskInput) error.
type Action struct {
	// Name is the verb. It appears in the URL, in the operation ID, and in the
	// generated identifiers — "complete" gives POST /tasks/{id}/complete and an
	// Actions.CompleteTask field.
	Name string

	// Path is the sub-path under the collection. It defaults to
	// "/{id}/"+Name, which is the item form.
	//
	// A path that does not contain "{id}" is a *collection* action: there is no
	// row to fetch, so the verb receives only the body and answers 204. Note
	// what that costs — with no generated fetch there is no BeforeQuery for a
	// declared scope to hang off, so a collection action inherits none of
	// ADR-0030's closure and is in the same position as a sqlb.Query in
	// application code.
	Path string

	// Body is the request body, declared in the field vocabulary. Build it with
	// [Body].
	//
	// It is declared rather than reflected from an application type for two
	// reasons. The value of an action is that the verb reaches the TypeScript,
	// Dart, CLI and OpenAPI emitters, and those read this declaration — a body
	// sqlb cannot see produces a client method typed `unknown`, which is the
	// drift this feature exists to remove. And reflecting an application struct
	// would invert the dependency, since models are generated *from* the schema.
	//
	// Leaving it empty is normal: most verbs carry nothing. The generated input
	// type is still emitted, empty, so that adding the first property later
	// does not change the shape of the func the application wrote.
	Body []*Field

	// Writes names the columns the envelope persists after the verb returns,
	// and it is enforced rather than documented: exactly these columns are
	// written, from the row the verb mutated.
	//
	// A verb that has to touch anything else has the transaction and can issue
	// the statement itself. What this buys is that the blast radius of a route
	// is something the OpenAPI document and `sqlb impact` can state — and that
	// the envelope knows to take the row lock, since a declared write set is
	// exactly the case where a read-modify-write can be lost.
	//
	// It must be empty on a collection action, which has no row.
	Writes []string

	// Summary is the one-line description in the OpenAPI document.
	//
	// Left empty it is filled in downstream, as "Complete a task", rather than
	// here: writing that sentence needs the singular of the table name, and a
	// singulariser is a thing codegen has and this package deliberately does
	// not — a wrong guess in a Go type name is cosmetic, and one baked into a
	// declaration is not.
	Summary string

	// Description documents the operation at length.
	Description string
}

// Body builds an action's request body from field declarations.
//
//	Body: schema.Body(
//	    schema.Text("note").Nullable(),
//	    schema.Timestamp("completed_at"),
//	)
//
// The vocabulary is the column vocabulary, deliberately: it is the one the
// emitters already know how to turn into a TypeScript type, a Dart class, a CLI
// flag and an OpenAPI schema. Only what describes a *value* applies here —
// name, type, nullability, enum values, default and comment. The capabilities
// that describe a column's place in a table (Filterable, PrimaryKey, Ref,
// Computed, and the rest) have no meaning in a request body and are refused by
// Validate rather than ignored.
func Body(specs ...FieldSpec) []*Field {
	var out []*Field
	for _, s := range specs {
		if s == nil {
			continue
		}
		out = append(out, s.fields()...)
	}
	return out
}

// Action declares a domain verb on the table and returns the table, so that
// declarations chain the way Expose and AddIndex already do.
//
// The table must also be exposed: an action is a route on the resource, and a
// table with no resource has nowhere to put one.
func (t *TableDef) Action(a Action) *TableDef {
	if a.Path == "" {
		a.Path = "/{id}/" + a.Name
	}
	t.actions = append(t.actions, a)
	return t
}

// Actions returns the table's declared verbs, in declaration order.
func (t *TableDef) Actions() []Action { return t.actions }

// IsCollection reports whether the action addresses the collection rather than
// one row — which is to say, whether its path names no id.
func (a Action) IsCollection() bool { return !strings.Contains(a.Path, "{id}") }

// FullPath is the action's route: the resource path with the action's own path
// appended.
func (a Action) FullPath(resource string) string { return resource + a.Path }

// validateActions checks one table's verbs.
//
// Every rule here closes something that is otherwise silent at runtime: a verb
// whose Writes names a column that does not exist writes nothing and answers
// 200, and a body field claiming Filterable looks like it did something.
func (r *Registry) validateActions(t *TableDef, report func(string, string, string, ...any)) {
	if len(t.actions) == 0 {
		return
	}
	if t.rest == nil {
		report(t.name, "", "declares %d action(s) but is not exposed; an action is a route on the resource, so add Expose", len(t.actions))
		return
	}

	seen := make(map[string]bool, len(t.actions))
	paths := make(map[string]string, len(t.actions))
	for _, a := range t.actions {
		switch {
		case a.Name == "":
			report(t.name, "", "action has no Name")
			continue
		case !isActionName(a.Name):
			report(t.name, "", "action name %q must be a lower-case identifier, optionally hyphenated: complete, archive, mark-read", a.Name)
		}
		if seen[a.Name] {
			report(t.name, "", "action %q declared twice", a.Name)
		}
		seen[a.Name] = true

		if !strings.HasPrefix(a.Path, "/") {
			report(t.name, "", "action %q has path %q, which must start with %q", a.Name, a.Path, "/")
		}
		if prev, dup := paths[a.Path]; dup {
			report(t.name, "", "action %q uses the same path as %q, so routing would depend on declaration order", a.Name, prev)
		}
		paths[a.Path] = a.Name

		// The item form addresses a row, so it needs something to address it
		// by. Reported here rather than at mount because the declaration is
		// where the mistake is.
		if !a.IsCollection() && t.PrimaryKey() == nil {
			report(t.name, "", "action %q addresses a row by id but the table declares no primary key", a.Name)
		}

		r.validateActionBody(t, a, report)
		r.validateActionWrites(t, a, report)
	}
}

// validateActionBody refuses the claims a request body cannot make.
func (r *Registry) validateActionBody(t *TableDef, a Action, report func(string, string, string, ...any)) {
	seen := make(map[string]bool, len(a.Body))
	for _, f := range a.Body {
		d := f.Desc()
		if !isIdent(d.Name) {
			report(t.name, d.Name, "action %q: body property name is not a valid identifier", a.Name)
		}
		if seen[d.Name] {
			report(t.name, d.Name, "action %q: body property declared twice", a.Name)
		}
		seen[d.Name] = true

		// A body property is a value, not a column. Every capability below
		// describes a column's place in a table, and silently ignoring one
		// would leave a declaration that reads as though it did something.
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
				report(t.name, d.Name, "action %q: body property claims %s, which describes a column rather than a request body", a.Name, c.what)
			}
		}
	}
}

// validateActionWrites checks that the declared write set is writable.
func (r *Registry) validateActionWrites(t *TableDef, a Action, report func(string, string, string, ...any)) {
	if a.IsCollection() && len(a.Writes) > 0 {
		report(t.name, "", "action %q is a collection action and has no row to write; drop Writes, or give the path an {id}", a.Name)
		return
	}
	seen := make(map[string]bool, len(a.Writes))
	for _, name := range a.Writes {
		if seen[name] {
			report(t.name, name, "action %q: Writes names the column twice", a.Name)
		}
		seen[name] = true

		f := t.Field(name)
		switch {
		case f == nil:
			report(t.name, name, "action %q: Writes names no column of this table", a.Name)
		case f.Desc().Computed():
			report(t.name, name, "action %q: Writes names a computed column, which has no storage to write to", a.Name)
		case f.Desc().PrimaryKey:
			report(t.name, name, "action %q: Writes names the primary key; a verb that re-keys a row is a delete and an insert", a.Name)
		}
	}
}

// isActionName reports whether s is a legal verb: lower-case, starting with a
// letter, and hyphen-separated. It is a URL segment and a Go identifier at
// once, so it has to survive both.
func isActionName(s string) bool {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-' && i > 0 && i < len(s)-1:
		default:
			return false
		}
	}
	return true
}
