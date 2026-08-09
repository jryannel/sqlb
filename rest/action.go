package rest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jryannel/sqlb"
)

// A declared action is a domain verb with a generated envelope (ADR-0043).
//
// The envelope is the part every hand-written verb handler repeats: parse the
// id, fetch the row under whatever a BeforeQuery hook confines it to, 404,
// decode the body, run the domain func inside a transaction, persist the
// declared write set, answer with the row. The adoption review measured that at
// roughly thirty lines before any domain logic, times about forty-six routes,
// written again in the OpenAPI document, the TypeScript client and the CLI.
//
// What is *not* generated is the verb. That is a plain Go func the application
// wrote, bound at registration, and the reason it stays plain is the failure
// mode docs/vision.md names: domain logic is where a DSL's expressiveness runs
// out first, and generated handlers that get copied out and edited by hand mean
// the seams were in the wrong place. So the seam here is the one BeforeCreate
// already uses.
//
// Two things the envelope buys that are easy to miss. The fetch goes through
// BeforeQuery, so an action on a Scoped model inherits ADR-0030's obligation
// machinery — which matters because hand-written verb handlers are exactly
// where the tenant predicate is remembered by hand. And a verb that declares a
// write set takes the row lock, because every one of these is a
// read-modify-write across a round trip and nobody should have to remember
// FOR UPDATE per route.

// ActionSpec describes one action to the runtime.
//
// It restates what schema.Action declared, and codegen writes it from that
// declaration — the same arrangement Options has with schema.REST, and for the
// same reason: nothing on the request path imports the schema package.
type ActionSpec struct {
	// Name is the verb, used in the operation ID: "complete" gives
	// complete-task.
	Name string

	// Path is the full route, resource path included: "/tasks/{id}/complete".
	// A path with no "{id}" is a collection action.
	Path string

	// Field is the name of this action's field on the generated Actions
	// struct, so that a nil func can be reported as the thing the author has
	// to go and set.
	Field string

	// Summary and Description document the operation.
	Summary     string
	Description string

	// Writes names the columns the envelope persists after the verb returns.
	// Empty means the verb writes nothing through the envelope — it may still
	// write through the transaction, which it has.
	Writes []string

	// Touches names the tables the verb writes through that transaction, as
	// the schema declared them. Nothing here enforces it and nothing here
	// could; it is carried so the operation's description can state a reach
	// the write set understates (#149).
	Touches []string

	// HasBody reports whether the action declared any body properties.
	//
	// The input type is generated either way, so that adding the first property
	// later does not change the shape of the func the application wrote. This
	// is what decides whether the *operation* reads a request body, because an
	// empty struct registered as a required body would make
	// POST /tasks/{id}/complete refuse a request that carries nothing — which
	// is the commonest verb there is.
	HasBody bool
}

// ErrNoTransaction is what a verb returns when it needs the unit of work and
// the resource does not open one.
//
// It is reachable only under Options.DisableTransactions, and it is worth a
// named error rather than a local one because the alternative is worse than an
// error: a verb that shrugs and writes its side effect anyway leaves half of a
// transition durable, which is the failure the transaction exists to prevent.
var ErrNoTransaction = errors.New(
	"rest: this action needs the transaction, and the resource runs its writes under autocommit (Options.DisableTransactions)")

// describe is the operation's description: what the schema wrote, plus what the
// route reaches.
//
// The reach is appended here rather than baked into Description by codegen so
// that a hand-written mount gets it from the same field a generated one does —
// and so that the sentence stays one sentence, in one place, when it needs
// rewording.
//
// Only Touches is appended. Writes is in the response schema and in the CLI's
// help already, and repeating it here would put the understated number in front
// of a reader twice for every once that the correction appears.
func (s ActionSpec) describe() string {
	if len(s.Touches) == 0 {
		return s.Description
	}
	reach := fmt.Sprintf(
		"Beyond the row in the response, this operation writes: %s. "+
			"That set is declared rather than enforced — see the schema for what it claims.",
		strings.Join(s.Touches, ", "))
	if s.Description == "" {
		return reach
	}
	return s.Description + "\n\n" + reach
}

func (s ActionSpec) validate(resource string) error {
	switch {
	case s.Name == "":
		return fmt.Errorf("rest: %s declares an action with no Name", resource)
	case s.Path == "":
		return fmt.Errorf("rest: %s action %q has no Path", resource, s.Name)
	}
	return nil
}

// operationID is the action's id in the document, which is also the one thing
// about it that has to be unique across the whole API.
func (s ActionSpec) operationID(opts Options) string { return s.Name + "-" + opts.name() }

// refuseDuplicateID answers the collision Huma would otherwise panic on.
//
// An action named for an operation the resource already serves — "create" on a
// resource exposing OpCreate — produces two operations with one id, and
// huma.AddOperation panics on the second. A panic at mount is not the wrong
// *time* to fail; it is the wrong *shape*, because every other refusal on this
// path is a returned error naming the declaration to change, and because the
// panic names the id without naming either operation that wants it.
//
// The check is Huma's own scan rather than a table of the verbs each Op is
// generated under. A table here would be that table's second copy — schema has
// one, and rest does not import schema (ADR-0040 keeps this package's
// dependencies to huma) — and it would answer a narrower question: this scan
// also catches two resources that share a Name, and an action colliding with
// another action mounted from somewhere else entirely.
//
// It sees what is already registered, so it depends on mount order: an action
// registered *before* the resource still panics from inside Resource. That is
// the order codegen emits and the order the documented example uses, and
// closing the other one would mean holding a registry of intent that nothing
// else here needs.
func refuseDuplicateID(api huma.API, resource, name, id string) error {
	for path, item := range api.OpenAPI().Paths {
		for _, op := range []*huma.Operation{
			item.Get, item.Post, item.Put, item.Patch,
			item.Delete, item.Head, item.Options, item.Trace,
		} {
			if op == nil || op.OperationID != id {
				continue
			}
			return fmt.Errorf("rest: %s action %q is already the operation id %q, held by %s %s: "+
				"an action does not replace an operation the resource serves, it adds a second one "+
				"with the same name, and the generated clients declare that name twice. Rename the "+
				"verb for the transition it performs, or drop the operation from Options.Ops so the "+
				"action is the only %s route",
				resource, name, id, op.Method, path, name)
		}
	}
	return nil
}

// missingDo is the mount-time refusal of an action nobody supplied a func for.
//
// The compiler gets the first word — an action added to the schema is a build
// error at the call site, because Actions grows a field with an exact
// signature — and it cannot get the last one, since Actions{} compiles. So the
// nil lands here, at startup, which is ADR-0030's shape: the failure that
// remains after the type system has done what it can is refused before the
// first request rather than answered by it.
func missingDo(resource string, spec ActionSpec) error {
	field := spec.Field
	if field == "" {
		field = spec.Name
	}
	return fmt.Errorf(
		"rest: %s declares the action %q, and Actions.%s is nil;\n"+
			"  pass it to Register, e.g. Register(api, db, Actions{%s: %s})\n"+
			"  or drop the action from the schema, which is the honest way to say the verb does not exist",
		resource, spec.Name, field, field, lowerFirst(field))
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	b := []byte(s)
	if b[0] >= 'A' && b[0] <= 'Z' {
		b[0] += 'a' - 'A'
	}
	return string(b)
}

// actionInput addresses one row and carries a body.
type actionInput[In any] struct {
	ID   string `path:"id" doc:"Primary key of the row"`
	Body In
}

// collectionInput carries a body and addresses no row.
type collectionInput[In any] struct {
	Body In
}

// Action registers a verb on one row of T.
//
// The envelope fetches the row, hands it to do inside a transaction, persists
// spec.Writes, and answers 200 with the row. do reports failure by returning an
// error; a *Problem is answered with its own status, which is how a verb says
// "cannot complete an archived task" is a 409 rather than a 500.
func Action[T, In any](api huma.API, db sqlb.Executor, opts Options, spec ActionSpec, do func(context.Context, *T, In) error) error {
	if err := opts.validate(); err != nil {
		return err
	}
	if err := spec.validate(opts.Path); err != nil {
		return err
	}
	if db == nil {
		return fmt.Errorf("rest: %s has no Executor", spec.Path)
	}
	if do == nil {
		return missingDo(opts.Path, spec)
	}

	b, err := bind[T](opts)
	if err != nil {
		return err
	}
	if b.model.PK == nil {
		return fmt.Errorf("rest: %s addresses a row by id but %s declares no primary key",
			spec.Path, b.model.Type)
	}

	writes, err := b.writeSet(spec)
	if err != nil {
		return err
	}

	// The envelope's fetch is a read, so a confined table's obligations are the
	// read ones. Only the read ones: the row this writes is one the envelope
	// itself fetched under that predicate, unlike a PATCH, whose id comes from
	// the request and which therefore needs its own BeforeUpdate.
	readOnly := opts
	readOnly.Path = spec.Path
	readOnly.Ops = OpRead
	if err := checkObligations[T](b.model, db, readOnly); err != nil {
		return err
	}

	w, err := newWriter(db, opts)
	if err != nil {
		return err
	}

	fetch := b.actionSelection(writes)

	run := func(ctx context.Context, id string, body In) (*itemOutput[T], error) {
		key, err := b.key(id)
		if err != nil {
			return nil, err
		}
		out, err := write(ctx, w, func(ctx context.Context, db sqlb.Executor) (T, error) {
			q := sqlb.Query[T]().
				Select(fetch...).
				Where(sqlb.F(b.model.PK.Name).Eq(key))
			if len(writes) > 0 {
				// A verb that changes the row is a read-modify-write across a
				// round trip, so without this two concurrent completions read
				// the same row and the second overwrites the first. The
				// declaration of a write set is exactly the signal that this
				// is that shape.
				q = q.ForUpdate()
			}
			found, err := q.One(ctx, db)
			if err != nil {
				return found, err
			}
			if err := do(ctx, &found, body); err != nil {
				var zero T
				return zero, err
			}
			if len(writes) == 0 {
				return found, nil
			}
			// Exactly the declared columns, taken off the row the verb
			// mutated. A column it changed and did not declare stays
			// unwritten, which is what makes Writes a statement about the
			// route rather than a comment on it.
			stmt := sqlb.UpdateRows[T]().Where(sqlb.F(b.model.PK.Name).Eq(key))
			rv := reflect.ValueOf(found)
			for _, col := range writes {
				fv, ok := valueByIndex(rv, col.Index)
				if !ok {
					continue
				}
				stmt.Set(col.Name, fv.Interface())
			}
			return stmt.One(ctx, db)
		})
		if err != nil {
			return nil, asHumaError(ctx, err, opts.name())
		}
		return &itemOutput[T]{Body: row[T]{value: out, cols: b.selectable, keys: b.jsonKey}}, nil
	}

	id := spec.operationID(opts)
	if err := refuseDuplicateID(api, opts.Path, spec.Name, id); err != nil {
		return err
	}

	op := huma.Operation{
		OperationID:                  id,
		Method:                       http.MethodPost,
		Path:                         spec.Path,
		Summary:                      spec.Summary,
		Description:                  spec.describe(),
		Tags:                         []string{opts.tag()},
		Security:                     opts.Security,
		RejectUnknownQueryParameters: true,
		Responses: errorResponses(api.OpenAPI().Components.Schemas,
			http.StatusBadRequest, http.StatusNotFound, http.StatusConflict,
			http.StatusUnprocessableEntity, http.StatusInternalServerError),
	}

	if spec.HasBody {
		huma.Register(api, op, func(ctx context.Context, in *actionInput[In]) (*itemOutput[T], error) {
			return run(ctx, in.ID, in.Body)
		})
		return nil
	}
	huma.Register(api, op, func(ctx context.Context, in *itemInput) (*itemOutput[T], error) {
		var body In
		return run(ctx, in.ID, body)
	})
	return nil
}

// CollectionAction registers a verb on the collection rather than on a row.
//
// There is no row to fetch, so do receives only the body and the response is a
// 204. Note what is absent along with the fetch: no BeforeQuery runs, so a
// declared scope obliges nothing here and confining the statements this verb
// issues is the verb's own job — the position sqlb.Query in application code is
// already in (ADR-0030).
func CollectionAction[In any](api huma.API, db sqlb.Executor, opts Options, spec ActionSpec, do func(context.Context, In) error) error {
	if err := opts.validate(); err != nil {
		return err
	}
	if err := spec.validate(opts.Path); err != nil {
		return err
	}
	if db == nil {
		return fmt.Errorf("rest: %s has no Executor", spec.Path)
	}
	if do == nil {
		return missingDo(opts.Path, spec)
	}

	w, err := newWriter(db, opts)
	if err != nil {
		return err
	}

	run := func(ctx context.Context, body In) (*struct{}, error) {
		_, err := write(ctx, w, func(ctx context.Context, _ sqlb.Executor) (struct{}, error) {
			return struct{}{}, do(ctx, body)
		})
		if err != nil {
			return nil, asHumaError(ctx, err, opts.name())
		}
		return nil, nil
	}

	id := spec.operationID(opts)
	if err := refuseDuplicateID(api, opts.Path, spec.Name, id); err != nil {
		return err
	}

	op := huma.Operation{
		OperationID:                  id,
		Method:                       http.MethodPost,
		Path:                         spec.Path,
		Summary:                      spec.Summary,
		Description:                  spec.describe(),
		Tags:                         []string{opts.tag()},
		Security:                     opts.Security,
		DefaultStatus:                statusNoBody,
		RejectUnknownQueryParameters: true,
		Responses: errorResponses(api.OpenAPI().Components.Schemas,
			http.StatusBadRequest, http.StatusConflict,
			http.StatusUnprocessableEntity, http.StatusInternalServerError),
	}

	if spec.HasBody {
		huma.Register(api, op, func(ctx context.Context, in *collectionInput[In]) (*struct{}, error) {
			return run(ctx, in.Body)
		})
		return nil
	}
	huma.Register(api, op, func(ctx context.Context, _ *struct{}) (*struct{}, error) {
		var body In
		return run(ctx, body)
	})
	return nil
}

// writeSet resolves an action's declared write set against the model.
//
// A name that is not a column, or is one the database does not store, is a
// startup error rather than a silently skipped column: the schema validator
// catches it for a declared schema, and a hand-written model has nothing else
// standing between a typo and a route that answers 200 having written nothing.
func (b *binding[T]) writeSet(spec ActionSpec) ([]*sqlb.ColumnInfo, error) {
	out := make([]*sqlb.ColumnInfo, 0, len(spec.Writes))
	for _, name := range spec.Writes {
		col := b.model.Column(name)
		switch {
		case col == nil:
			return nil, fmt.Errorf("rest: %s writes %q, which is not a column of %s",
				spec.Path, name, b.model.Type)
		case col.Expr != "":
			return nil, fmt.Errorf("rest: %s writes %q, which is computed and has no storage",
				spec.Path, name)
		}
		out = append(out, col)
	}
	return out, nil
}

// actionSelection is the projection the envelope's fetch uses: the default one,
// plus any column the action declared it writes that the default leaves out.
//
// The default projection is every *non-hidden* column, and the write-back takes
// its values off the struct the verb mutated. So a Hidden column in Writes —
// a secret, an internal counter, exactly what Hidden exists for — reached the
// verb as its zero value, and a read-modify-write on it (`p.Counter++`, rotating
// a suffix onto a secret) persisted a value derived from zero over the stored
// one. Under the FOR UPDATE lock whose entire purpose is correct
// read-modify-write (#67).
//
// Projecting them is strictly better than refusing them: they are about to be
// written, so the verb has to see what they currently hold. The response
// projection is unchanged — it is b.selectable, so a Hidden column is fetched
// and written but still never serialised.
func (b *binding[T]) actionSelection(writes []*sqlb.ColumnInfo) []sqlb.Selectable {
	items := b.selection()
	for _, col := range writes {
		if b.isSelectable(col.Name) {
			continue
		}
		items = append(items, sqlb.F(col.Name))
	}
	return items
}

func (b *binding[T]) isSelectable(name string) bool {
	for _, col := range b.selectable {
		if col.Name == name {
			return true
		}
	}
	return false
}
